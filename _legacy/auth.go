package main

// Auth expects a schema called 'auth' in postgres with these functions ;
//
// 		* login(username text, password text) returns text # for classical login
//					# Returns a role name
//		* logout(rolname text) # Just to tell the database that we logged out, the database does not perform
//		* ext_login(uid text, email text, data json) returns text
//          returns a role name if found, null other wise, and the login fails.
//

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"log"
	"math/big"
	"net/http"
	"os"
	"time"

	"github.com/dgrijalva/jwt-go"
	"github.com/go-chi/chi"
	"github.com/jackc/pgtype"
	"github.com/jackc/pgx/v4"
	"github.com/markbates/goth/gothic"
	"sales-way.com/server/sw"
)

type AuthEndpoint struct {
	Name        string `json:"name"`
	DisplayName string `json:"displayname"`
	Endpoint    string `json:"endpoint"`
	Hostname    string
}

var authEndpoints = make([]AuthEndpoint, 0)

// doExternalLogin tries to ask the db what role the user is mapped to.
func doExternalLogin(srv *sw.SwServer, rw http.ResponseWriter, req *http.Request, user string) {

	if !srv.AvailableLoginFunctions.ExternalLogin {
		ReplyError(rw, 403, "cannot use external login", "External login is disabled")
		return
	}

	conn, err := srv.Pool.Acquire(context.Background())
	if err != nil {
		_, _ = fmt.Fprintln(rw, err)
		return
	}
	defer conn.Release()

	row := conn.QueryRow(context.Background(), `SELECT auth.external_login($1) --?`, user)

	// var role string
	var role pgtype.Text
	if err := row.Scan(&role); err != nil || role.Status == pgtype.Null {
		var msg = "User not found"
		if err != nil {
			msg = err.Error()
		}
		ReplyError(rw, 401, msg, " can't log '"+user+"' in from SSO endpoint.")
		log.Print("!!! user ", user, " failed to log in")
		return
	}
	log.Print("*** user ", user, " logged in from an SSO endpoint")
	if err := jwtSetCookieForRole(srv, req, rw, role.String, false); err != nil {
		ReplyError(rw, 500, "internal server error", "Failed to set cookie: "+err.Error())
		return
	}
	rw.Header().Set("Location", fmt.Sprintf("https://%s", req.Host))
	rw.WriteHeader(http.StatusTemporaryRedirect)
}

func UnsetCookie(w http.ResponseWriter, cookie string) {
	c := &http.Cookie{
		Name:     cookie,
		Value:    "",
		Path:     "/",
		Expires:  time.Unix(0, 0),
		HttpOnly: true,
	}
	http.SetCookie(w, c)
}

var SQL_CHECK_FUNCTIONS = `SELECT
  count(1) FILTER (WHERE routine_name = 'login' AND routine_schema = 'auth' AND parameters = 'text,text:text') > 0 AS login,
  count(1) FILTER (WHERE routine_name = 'external_login' AND routine_schema = 'auth' AND parameters = 'text:text') > 0 AS external_login,
  count(1) FILTER (WHERE routine_name = 'session_check' AND routine_schema = 'auth' AND (parameters = 'text,text,json:text' or parameters = 'text,jsonb:text')) > 0 AS session_check
FROM (
SELECT
    routine_schema,
    routine_name,
    routine_type,
    string_agg(
        p.data_type,
        ','
        ORDER BY ordinal_position
    ) || ':' || r.data_type AS parameters
FROM information_schema.routines r
LEFT JOIN information_schema.parameters p
    ON r.specific_name = p.specific_name
GROUP BY routine_schema, routine_name, routine_type, r.specific_name, r.data_type
)
`

func ReloadLoginFunctions(srv *sw.SwServer) {
	rows, err := srv.PgSimpleQueryRow(context.Background(), SQL_CHECK_FUNCTIONS)
	if err != nil {
		srv.LogError("auth: could not check functions: ", err)
	}
	if err = rows.Scan(&srv.AvailableLoginFunctions.Login, &srv.AvailableLoginFunctions.ExternalLogin, &srv.AvailableLoginFunctions.SessionCheck); err != nil {
		srv.LogError("auth: could not scan functions: ", err)
	}

	if srv.AvailableLoginFunctions.SessionCheck {
		srv.LogInfo("auth: session check function found, session checking enabled")
	} else {
		srv.LogWarn("auth: session check function not found, session checking disabled")
	}
}

const charset = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789-_"

func generateSecret(length int) (string, error) {
	result := make([]byte, length)
	for i := range result {
		n, err := rand.Int(rand.Reader, big.NewInt(int64(len(charset))))
		if err != nil {
			return "", err
		}
		result[i] = charset[n.Int64()]
	}
	return string(result), nil
}

func jwtSecretFromPath(srv *sw.SwServer) string {
	secretPath := "/app/secrets/JWT_SECRET"
	if _, err := os.Stat(secretPath); os.IsNotExist(err) {
		srv.LogInfo("auth: JWT_SECRET not found, generating a new one")

		// Generate a new secret
		secret, err := generateSecret(64)
		if err != nil {
			srv.LogError("auth: could not generate JWT secret: ", err)
			return ""
		}

		if err := os.WriteFile(secretPath, []byte(secret), 0600); err != nil {
			srv.LogError("auth: could not write JWT_SECRET: ", err)
		}
		return secret
	}
	secret, err := os.ReadFile(secretPath)
	if err != nil {
		srv.LogError("auth: could not read JWT_SECRET: ", err)
		return ""
	}
	srv.LogInfo("auth: read JWT_SECRET from file")
	return string(secret)
}

// Need two functions : auth.login(uid text, pass text) -> text and auth.oauth(uid text, email text) -> text
// Both of these return a role name if it worked or return null or raise an error if it didn't.
// Their result will be included in the returned JWT.

// SetupAuth met en place l'authentification
func SetupAuth(srv *sw.SwServer) {

	ReloadLoginFunctions(srv)

	var disabledPasswords = false
	if DISABLE_PASSWD != "" {
		disabledPasswords = true
		srv.LogWarn(`basic auth: username/password login method is deactivated`)
	} else if !srv.AvailableLoginFunctions.Login {
		disabledPasswords = true
		srv.LogError("auth: login function not found")
	} else {
		authEndpoints = append(authEndpoints, AuthEndpoint{
			Name:        "_basic",
			DisplayName: "basic",
			Endpoint:    "/auth/login",
		})
		srv.LogInfo(`basic auth: username/password login is allowed`)
	}

	if JWT_SECRET == "" {
		JWT_SECRET = jwtSecretFromPath(srv)
	}

	srv.Router.Get("/auth/endpoints", func(rw http.ResponseWriter, r *http.Request) {
		rw.WriteHeader(200)
		enc := json.NewEncoder(rw)
		var current_hostname = r.Host
		var endpoints = make([]AuthEndpoint, 0)

		for _, end := range authEndpoints {
			if end.Hostname == current_hostname || end.Hostname == "" {
				endpoints = append(endpoints, end)
			}
		}

		enc.Encode(endpoints)
	})

	//
	// Login via User/Pass
	//
	srv.Router.Handle("/auth/login", http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		// Try to log in the user into the database with good old user and password.
		var (
			err  error
			role string
			row  pgx.Row
		)

		if disabledPasswords {
			ReplyError(rw, 403, "cannot use password", "Password authentication is disabled")
			return
		}

		decoder := json.NewDecoder(r.Body)

		type Credentials struct {
			Username string `json:"username"`
			Password string `json:"password"`
		}
		var creds = &Credentials{}

		switch r.Method {
		case "POST":
			if err = decoder.Decode(&creds); err != nil {
				ReplyError(rw, 400, "malformed request", "Cannot use credentials")
				return
			}
		case "GET":
			creds.Username = r.URL.Query().Get("username")
			creds.Password = r.URL.Query().Get("password")
		default:
			ReplyError(rw, 405, "method not allowed", "Only POST and GET are allowed")
			return
		}

		// We have a login and a password, we're going to try to query the database
		conn, err := srv.Pool.Acquire(context.Background())
		if err != nil {
			goto erred
		}
		defer conn.Release()

		if _, err := conn.Exec(context.Background(), `RESET ROLE`); err != nil {
			goto erred
		}

		row = conn.QueryRow(context.Background(), `SELECT auth.login($1::text, $2::text)`, creds.Username, creds.Password)
		if err = row.Scan(&role); err != nil {
			goto erred
		}

		log.Print("*** user ", creds.Username, " logged in with user/pass")
		// It all worked, send back the user to the home page
		if err = jwtSetCookieForRole(srv, r, rw, role, false); err != nil {
			goto erred
		}
		rw.Header().Set("Location", fmt.Sprintf("https://%s", r.Host))
		rw.WriteHeader(http.StatusTemporaryRedirect)

		return
	erred:
		ReplyError(rw, 401, err.Error(), "")
	}))

	srv.Router.Get("/auth/am-i-impersonator", func(w http.ResponseWriter, r *http.Request) {
		imper, err := jwtGetImpersonatorFromRequest(r)
		if err != nil {
			goto erred
		}

		if imper {
			w.Write([]byte("true"))
		} else {
			w.Write([]byte("false"))
		}
		return
	erred:
		ReplyError(w, 401, err.Error(), "")
	})

	// Allow a user that is tagged as an impersonator to impersonate others.
	srv.Router.Get("/auth/impersonate/{user}", func(w http.ResponseWriter, r *http.Request) {
		var (
			err           error
			role          string
			original_role string
			impersonator  bool
			ok            bool
			claims        jwt.MapClaims
		)

		jwtCookie, err := r.Cookie(JWT_COOKIE)
		if err != nil {
			goto erred
		}

		claims, err = jwtGetMapClaims(jwtCookie.Value)
		if err != nil {
			goto erred
		}

		if original_role, ok = claims["role"].(string); !ok {
			err = fmt.Errorf("could not fetch role claim")
			goto erred
		}

		if impersonator, ok = claims["impersonator"].(bool); !ok {
			err = fmt.Errorf("could not fetch impersonator claim")
			goto erred
		}

		if !impersonator {
			err = fmt.Errorf("user is not an impersonator")
			goto erred
		}

		role = chi.URLParam(r, "user")

		log.Print("*** user ", original_role, " impersonated ", role)
		if err = jwtSetCookieForRole(srv, r, w, role, true); err != nil {
			goto erred
		}
		w.Header().Set("Location", fmt.Sprintf("https://%s", r.Host))
		w.WriteHeader(http.StatusTemporaryRedirect)

		return
	erred:
		ReplyError(w, 401, err.Error(), "")
	})

	//
	// Logout user
	//
	srv.Router.Get("/auth/logout", func(rw http.ResponseWriter, req *http.Request) {
		gothic.Logout(rw, req)

		if role, err := jwtGetRoleFromRequest(req); err == nil {
			log.Print(role, `logged out`)

			conn, err := srv.Pool.Acquire(context.Background())
			if err != nil {
				fmt.Fprintln(rw, err)
				return
			}
			defer conn.Release()
			// Give a chance to the database to know that the user was logged out.
			_ = conn.QueryRow(context.Background(), `SELECT auth.logout($1)`, role)
		}

		// FIXME perform SAML logout.

		// res.Header().Set("Location", "/")
		// res.WriteHeader(http.StatusTemporaryRedirect)
		UnsetCookie(rw, JWT_COOKIE)
		fmt.Fprint(rw, `{"result":"ok"}`)
	})

	srv.Router.Get("/test", func(rw http.ResponseWriter, r *http.Request) {

		rol, err := jwtGetRoleFromRequest(r)
		if err != nil {
			ReplyError(rw, 401, "Unauthorized", err.Error())
			return
		}

		rw.WriteHeader(200)
		// On a désormais un jwt validé !
		_, _ = fmt.Fprint(rw, rol)
	})

}
