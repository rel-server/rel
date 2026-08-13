package main

import (
	"context"
	"encoding/json"
	"log"
	"regexp"
	"strings"
	"sync"

	"github.com/dchest/uniuri"
	"github.com/fatih/color"
	"github.com/gorilla/websocket"
	"sales-way.com/server/sw"
	"sales-way.com/server/utils"
)

var upgrader = websocket.Upgrader{}

var cyan = color.New(color.FgHiCyan).SprintFunc()

//////////////////////////////////////////////////////

// setupWebsockets initializes the websocket backend, communicating with postgres to know which procedures it has defined to handle websockets the goserver way.
func setupWebsockets(srv *sw.SwServer) error {

	var err error
	var notif SwWebsocketPostgresNotifier
	// Give a unique control channel name
	notif.controlChannel = "__gs_ctrl_" + uniuri.New()
	// Initialize
	notif.srv = srv
	notif.initFromPg()

	srv.Router.Get("/ws", wsHandlerFn(&notif))

	// Launch the notify listen loop
	go notif.listenPg()

	return err
}

/*
Getting the functions ?

select
  row_to_json(R) as json
from (
select
  count(*) filter (where routine_name = 'onconnect') > 0 as has_on_connect,
  array_agg(pattern) filter (where start = 'onsubscribe') as subscribe_patterns,
  array_agg(pattern) filter (where start = 'onunsubscribe') as unsubscribe_patterns,
  count(*) filter (where routine_name = 'ondisconnect') > 0 as has_on_disconnect
from (select
  substring(routine_name, '^(.*?)_') as start,
  substring(routine_name, '^.*?_(.*)$') as pattern,
  *
from information_schema.routines
where specific_schema = 'ws'
  and routine_type = 'FUNCTION'
order by routine_name) S
) R*/

// ////////////////////////////////////////////////////////////////////////////////////
type SwWebsocketPostgresNotifier struct {
	srv            *sw.SwServer
	controlChannel string

	HasOnConnect     bool     `json:"has_on_connect"`
	HasOnDisconnect  bool     `json:"has_on_disconnect"`
	OnSubscribeFns   []string `json:"subscribe_patterns"`
	OnUnsubscribeFns []string `json:"unsubscribe_patterns"`

	onSubscribeReg   []*regexp.Regexp
	onUnsubscribeReg []*regexp.Regexp

	// The channels that are being listened on, with the client count
	ListeningTo         *utils.MapSet[string, *SwWebsocketSession]
	listeningLock       sync.RWMutex
	channelSubscribeFns map[string]*websocketChannelSubscriptionFns
}

// struct that holds the matches between the channel name and the subscribe / unsubscribe
// function regexps that are defined in the database.
type websocketChannelSubscriptionFns struct {
	fnsubscribe   string
	subscribe     []string
	fnunsubscribe string
	unsubscribe   []string
}

// Query the postgres server to know which methods it has defined for websocket handling
func (notifier *SwWebsocketPostgresNotifier) initFromPg() {

	var err error
	var srv = notifier.srv
	notifier.ListeningTo = utils.NewMapSet[string, *SwWebsocketSession]()
	notifier.channelSubscribeFns = make(map[string]*websocketChannelSubscriptionFns)

	// If there is no postgres, no need to try to initialize the websockets from it
	if srv.Pool == nil {
		return
	}

	row, err := srv.PgSimpleQueryRow(context.Background(), `
		select
			row_to_json(R) as json
		from (
		select
			count(*) filter (where routine_name = 'onconnect') > 0 as has_on_connect,
			array_agg(pattern) filter (where start = 'onsubscribe') as subscribe_patterns,
			array_agg(pattern) filter (where start = 'onunsubscribe') as unsubscribe_patterns,
			count(*) filter (where routine_name = 'ondisconnect') > 0 as has_on_disconnect
		from (select
			substring(routine_name, '^(.*?)_') as start,
			substring(routine_name, '^.*?_(.*)$') as pattern,
			*
		from information_schema.routines
		where specific_schema = 'ws'
			and routine_type = 'FUNCTION'
		order by routine_name) S
		) R
	`)
	if err != nil {
		srv.LogError(err)
		return
	}

	var js []byte
	if err = row.Scan(&js); err != nil {
		srv.LogError(err)
		return
	}

	if err = json.Unmarshal(js, &notifier); err != nil {
		srv.LogError(err)
		return
	}

	// Parse the subscriber regexp
	for _, pattern := range notifier.OnSubscribeFns {
		if reg, err := regexp.Compile("^" + pattern + "$"); err != nil {
			srv.LogWarn(err)
		} else {
			notifier.onSubscribeReg = append(notifier.onSubscribeReg, reg)
		}
	}

	// Parse the unsubscribe regexps
	for _, pattern := range notifier.OnUnsubscribeFns {
		if reg, err := regexp.Compile("^" + pattern + "$"); err != nil {
			srv.LogWarn(err)
		} else {
			notifier.onUnsubscribeReg = append(notifier.onUnsubscribeReg, reg)
		}
	}
}

// for a given channel name, run it against the regular expressions for subscribe / unsubscribe functions
func (notifier *SwWebsocketPostgresNotifier) getChannelSubFn(channel string) *websocketChannelSubscriptionFns {
	var res = &websocketChannelSubscriptionFns{}
	if res, ok := notifier.channelSubscribeFns[channel]; ok {
		return res
	}

	for _, reg := range notifier.onSubscribeReg {
		var matches = reg.FindStringSubmatch(channel)
		if len(matches) > 0 {
			res.fnsubscribe = "onsubscribe_" + reg.String()
			res.subscribe = matches
			break
		}
	}

	for _, reg := range notifier.onUnsubscribeReg {
		var matches = reg.FindStringSubmatch(channel)
		if len(matches) > 0 {
			res.fnunsubscribe = "onunsubscribe_" + reg.String()
			res.unsubscribe = matches
			break
		}
	}

	notifier.channelSubscribeFns[channel] = res
	return res
}

func (notifier *SwWebsocketPostgresNotifier) subscribe(channel string, session *SwWebsocketSession) (bool, error) {

	var conn, err = notifier.srv.Pool.Acquire(session.context())
	if err != nil {
		return false, err
	}
	defer conn.Release()

	notifier.listeningLock.Lock()
	defer notifier.listeningLock.Unlock()

	var fns = notifier.getChannelSubFn(channel)

	if fns.fnsubscribe != "" {
		// Try to run the onsubscribe function
		if _, err = conn.Exec(session.context(), `SELECT ws."`+fns.fnsubscribe+`"($1, $2, $3)`, fns.subscribe, session.Role, session.Id); err != nil {
			// if the onsubscribe returned an error, just fail.
			return false, err
		}
	}

	if notifier.ListeningTo.Add(channel, session) {

		// call on_subscribe

		if notifier.ListeningTo.SizeForKey(channel) == 1 {
			// This is the first time the channel is registered, so we tell the control to listen to it !
			if _, err = conn.Exec(session.context(), `SELECT pg_notify($1, $2)`, notifier.controlChannel, "S:"+channel); err != nil {
				return false, err
			}
		}

		session.Channels.Add(channel)

		return true, nil
	}

	return false, nil
}

func (notifier *SwWebsocketPostgresNotifier) unsubscribe(channel string, session *SwWebsocketSession) (bool, error) {

	notifier.listeningLock.Lock()
	defer notifier.listeningLock.Unlock()

	var conn, err = notifier.srv.Pool.Acquire(session.context())
	if err != nil {
		return false, err
	}
	defer conn.Release()

	if notifier.ListeningTo.RemoveValue(channel, session) {

		if notifier.ListeningTo.SizeForKey(channel) == 0 {

			// The channel has no one left, no point in listening to its updates anymore
			if _, err = conn.Exec(session.context(), `SELECT pg_notify($1, $2)`, notifier.controlChannel, "U:"+channel); err != nil {
				return false, err
			}

			delete(notifier.channelSubscribeFns, channel)
		}

		session.Channels.Remove(channel)
		return true, nil
	}
	return false, nil
}

// The main loop that listens to notify
func (notifier *SwWebsocketPostgresNotifier) listenPg() {

	if notifier.srv.Pool == nil {
		notifier.srv.LogError("no PG database configured, not listening to websockets")
		return
	}

	// The control channel acquires a connection and keeps it
	conn, err := notifier.srv.Pool.Acquire(context.Background())
	if err != nil {
		notifier.srv.LogError("Error acquiring connection:", err)
		return
	}
	// This should only be released when the program ends.
	defer conn.Release()

	// Start listening to the main channel
	notifier.srv.LogInfo("websockets: listening to channel '" + notifier.controlChannel + "'")
	_, err = conn.Exec(context.Background(), `listen "`+notifier.controlChannel+`"`)
	if err != nil {
		notifier.srv.LogError("Error listening to control channel:", err)
		return
	}

	// Main loop for the control channel
	for {
		notification, err := conn.Conn().WaitForNotification(context.Background())
		if err != nil {
			notifier.srv.LogError("websocket: error waiting for notification:", err)
			return
		}

		// nilaway says that err could be non-nil with a nil notification, so we're guarding against that here.
		if notification == nil {
			continue
		}

		// The notification
		if notification.Channel == notifier.controlChannel {

			if strings.HasPrefix(notification.Payload, "S:") {
				var channel = notification.Payload[2:]
				// subscribe
				_, err = conn.Exec(context.Background(), `listen "`+channel+`"`)
				if err != nil {
					notifier.srv.LogError("websocket: error: when listening to channel '", channel, "': ", err)
				} else {
					log.Println(green("✓ "), channel)
				}

			} else if strings.HasPrefix(notification.Payload, "U:") {
				// unsubscribe
				var channel = notification.Payload[2:]

				_, err = conn.Exec(context.Background(), `unlisten "`+channel+`"`)
				if err != nil {
					notifier.srv.LogError("websocket: error: when unlistening channel '", channel, "': ", err)
				} else {
					log.Println(red("𐄂 "), channel)
				}

			} else {
				// This is not supposed to happen !
				notifier.srv.LogWarn("websockets: warning: unknown command '" + notification.Payload + "'")
			}

		} else {

			// The notification came from somewhere else, so we dispatch it to our currently connected clients, if they are registered as listeners.
			notifier.listeningLock.RLock()
			if sessions, ok := notifier.ListeningTo.Map[notification.Channel]; ok {
				for session := range sessions.Map {
					session.Notify(notification.Channel, notification.Payload)
				}
			}
			notifier.listeningLock.RUnlock()

		}

		// fmt.Println("PID:", notification.PID, "Channel:", notification.Channel, "Payload:", notification.Payload)
	}
}
