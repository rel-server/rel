package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"sync"

	"github.com/fatih/color"
	"github.com/go-chi/chi/middleware"
	"github.com/gorilla/websocket"
	"sales-way.com/server/utils"
)

var green = color.New(color.FgGreen).SprintFunc()
var red = color.New(color.FgRed).SprintFunc()
var yellow = color.New(color.FgYellow).SprintFunc()

// ///////////////////////////////////////////////
type JSONCommand struct {
	Id       int      `json:"id"`
	Command  string   `json:"command"`
	Channels []string `json:"channels"`
	Payload  string   `json:"payload"`
}

func (c *JSONCommand) ok() []byte {
	res := JSONAnswer{
		Ok:      true,
		Message: "Ok",
		Id:      c.Id,
	}
	bytes, _ := json.Marshal(res)
	return bytes
}

func (c *JSONCommand) error(err string) []byte {
	res := JSONAnswer{
		Ok:      false,
		Message: err,
		Id:      c.Id,
	}
	bytes, _ := json.Marshal(res)
	return bytes
}

type JSONAnswer struct {
	Id      int    `json:"id"`
	Ok      bool   `json:"ok"`
	Message string `json:"message"`
}

type JSONNotification struct {
	Channel string          `json:"channel"`
	Payload json.RawMessage `json:"payload"`
}

type JSONNotificationString struct {
	Channel string `json:"channel"`
	Payload string `json:"payload"`
}

var anon = getenvOrDefault("PGRST_DB_ANON_ROLE", "@unauthenticated")

/*
	Package Websocket-Session contains everything to setup the websocket connection to the PG server
*/
// Upgrade the connection to use a websocket
func wsHandlerFn(notifier *SwWebsocketPostgresNotifier) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {

		session := SwWebsocketSession{}
		session.Id = middleware.GetReqID(r.Context())
		session.Notifier = notifier
		session.Req = r

		role, err := jwtGetRoleFromRequest(r)
		if err != nil {
			session.Role = anon
		} else {
			session.Role = role
		}

		if err := session.doInit(); err != nil {
			// End the connection if we couldn't init
			session.log("can't create websocket", err)
			return
		}

		// upgrade the connection to a websocket
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			session.log(err)
			return
		}
		defer conn.Close()
		session.Conn = conn

		defer func() {
			// cleanup the channel
			for listening := range session.Channels.Map {
				notifier.unsubscribe(listening, &session)
			}
		}()

		// Then read all messages coming from the client
		for {
			message_type, message, err := conn.ReadMessage()
			if err != nil {
				session.log("read error: ", err)
				break
			}

			switch message_type {
			case websocket.PingMessage:
				// Reply to ping
				session.Conn.WriteMessage(websocket.PongMessage, message)
				session.log("ping")
			case websocket.CloseMessage:
				// We are done, just finish and close the connection

			case websocket.TextMessage:

				session.handleMessage(message)
			}
		}
	}
}

// ////////////////////////////////////////////////////////////////////////////////////
// Individual user session
type SwWebsocketSession struct {
	Id   string
	Role string

	Conn     *websocket.Conn
	Notifier *SwWebsocketPostgresNotifier
	Req      *http.Request

	sendLock sync.Mutex

	// What canals is that user subscribed to
	Channels utils.Set[string]
}

// If there is a ws.init() function, perform it
func (session *SwWebsocketSession) doInit() error {
	if session.Notifier.HasOnConnect {
		var supl_channels []string
		row, err := session.Notifier.srv.PgSimpleQueryRow(session.context(), `SELECT ws.onconnect($1, $2)`, session.Role, session.Id)
		if err != nil {
			return err
		}
		if err := row.Scan(&supl_channels); err != nil {
			return err
		}
	}

	return nil
}

var session_should_log = false

func (session *SwWebsocketSession) log(msg ...interface{}) {
	if session_should_log {
		args := append([]interface{}{yellow("☢"), "[" + session.Id + "]", cyan(session.Role)}, msg...)
		log.Println(args...)
	}
	// session.Notifier.srv.LogInfo(args...)

}

func (session *SwWebsocketSession) context() context.Context {
	return session.Req.Context()
}

func (session *SwWebsocketSession) Send(message []byte) error {
	// Acquire a lock, because Send may be called from several threads
	session.sendLock.Lock()
	defer session.sendLock.Unlock()

	session.log(cyan("⇧ "), string(message))
	return session.Conn.WriteMessage(websocket.TextMessage, message)
}

func (session *SwWebsocketSession) Notify(channel string, message string) error {

	if !json.Valid([]byte(message)) {
		var notif = JSONNotificationString{
			Channel: channel,
			Payload: message,
		}

		bytes, _ := json.Marshal(notif)
		return session.Send(bytes)
	}

	// Acquire a lock, because this method may be called from the pg notify thread
	var notif = JSONNotification{
		Channel: channel,
		Payload: []byte(message),
	}

	bytes, _ := json.Marshal(notif)
	return session.Send(bytes)
}

func (session *SwWebsocketSession) handleMessage(message []byte) error {
	session.log(green("⇩ "), string(message))
	// We only deal with text messages and their JSON payload to do anything

	var cmd JSONCommand
	var err error
	var ok bool

	if err = json.Unmarshal(message, &cmd); err != nil {
		session.log("msg: ", err)
		return err
	}

	// Sending back to the client the status result of the command
	defer func() {
		if err != nil {
			session.Send(cmd.error(err.Error()))
		} else {
			session.Send(cmd.ok())
		}
	}()

	if len(cmd.Channels) == 0 {
		err = fmt.Errorf("channels are empty")
		return err
	}

	switch cmd.Command {
	case "S":
		for _, ch := range cmd.Channels {
			if ok, err = session.Notifier.subscribe(ch, session); ok {
				session.log(green("⎆ "), "subscribing to ", ch)
			}
		}
	case "U":
		for _, ch := range cmd.Channels {
			if ok, err = session.Notifier.unsubscribe(ch, session); ok {
				session.log(red("⏻ "), "unsubscribing from ", ch)
			}
		}
	case "N":
		// simple notify !
		for _, ch := range cmd.Channels {
			if !session.Channels.Has(ch) {
				err = fmt.Errorf("can't post on %s", ch)
				return err
			}
			session.Notifier.srv.PgSimpleExec(
				session.context(),
				`select pg_notify($1, $2)`,
				ch,
				cmd.Payload,
			)
		}
	default:
		err = fmt.Errorf("unknown command %s", cmd.Command)
	}

	return err
}

// Cleanup makes sure this session disappears from all the subscribers
func (session *SwWebsocketSession) Cleanup() error {
	return nil
}
