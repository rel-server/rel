package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"time"

	"github.com/dgrijalva/jwt-go"
	"sales-way.com/server/sw"
)

func init() {
	var swtkDuration = JWT_ACCESSTOKEN_EXPIRY
	if swtkDuration != "" {
		if dur, err := strconv.Atoi(swtkDuration); err == nil {
			// log.Print("Cookie sessions expire after ", dur, " hours")
			JWT_DURATION = time.Second * time.Duration(dur)
		} else if dur, err := time.ParseDuration(swtkDuration); err == nil {
			JWT_DURATION = dur
		} else {
			log.Print("Invalid JWT duration: ", swtkDuration)
			JWT_DURATION = time.Hour * 24 * 365
		}
	} else {
		JWT_DURATION = time.Hour * 24 * 365
	}
	log.Print("Session JWT expires after: ", JWT_DURATION.String())
}

var JWT_DURATION time.Duration

type contextKey string

const jwtRoleContextKey = contextKey("jwt-role")

type PgJwtClaims struct {
	jwt.StandardClaims
	Impersonator bool   `json:"impersonator"`
	Role         string `json:"role"`
	SessionId    string `json:"s"`
}

// jwtCreateToken creates a new string token, ready to be returned and put into a cookie.
func jwtCreateToken(srv *sw.SwServer, role string, impersonator bool, sessionId string) (string, error) {
	now := time.Now()
	exp := now.Add(JWT_DURATION)

	if DB_IMPERSONATOR_ROLE != "" && !impersonator {
		conn, err := srv.Pool.Acquire(context.Background())
		if err != nil {
			return "", err
		}
		defer conn.Release()

		row := conn.QueryRow(
			context.Background(),
			`SELECT pg_has_role($1::text, $2::text, 'member')`,
			role,
			DB_IMPERSONATOR_ROLE,
		)
		if err = row.Scan(&impersonator); err != nil {
			return "", err
		}
	}

	claims := PgJwtClaims{
		Role:         role,
		SessionId:    sessionId,
		Impersonator: impersonator,
		StandardClaims: jwt.StandardClaims{
			ExpiresAt: exp.Unix(),
			IssuedAt:  now.Unix(),
		},
	}

	token := jwt.NewWithClaims(jwt.SigningMethodHS512, claims)
	return token.SignedString([]byte(JWT_SECRET))
}

func jwtGetMapClaims(token string) (jwt.MapClaims, error) {
	var (
		tok    *jwt.Token
		claims jwt.MapClaims
		ok     bool
		err    error
	)

	if tok, err = jwt.Parse(token, func(t *jwt.Token) (interface{}, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", t.Header["alg"])
		}
		// hmacSampleSecret is a []byte containing your secret, e.g. []byte("my_secret_key")
		return []byte(JWT_SECRET), nil
	}); err != nil {
		log.Print("did not validate jwt")
		return nil, fmt.Errorf(`jwt: did not sign properly`)
	}

	if err := tok.Claims.Valid(); err != nil {
		// Error condition
		return nil, fmt.Errorf(`jwt: claims are invalid %w`, err)
	}

	if claims, ok = tok.Claims.(jwt.MapClaims); !ok {
		return nil, fmt.Errorf(`jwt: claims are invalid`)
	}

	return claims, nil
}

// jwtGetRole extracts the role inside the JWT token provided
func jwtGetRole(token string) (string, error) {
	claims, err := jwtGetMapClaims(token)
	if err != nil {
		return "", err
	}

	if r, ok := claims["role"]; ok {
		if sres, ok := r.(string); ok {
			// log.Print(claims)
			return sres, nil
		}
	}
	return "", fmt.Errorf(`jwt: role is inexistant or not a string`)
}

func jwtGetImpersonatorFromRequest(r *http.Request) (bool, error) {
	c, err := r.Cookie(JWT_COOKIE)
	if err != nil {
		return false, fmt.Errorf(`no '%s' cookie`, JWT_COOKIE)
	}

	claims, err := jwtGetMapClaims(c.Value)
	if err != nil {
		return false, fmt.Errorf(`can't get claims from token: %w`, err)
	}

	if r, ok := claims["impersonator"]; ok {
		if sres, ok := r.(bool); ok {
			// log.Print(claims)
			return sres, nil
		}
	}
	return false, nil
}

// jwtGetRoleFromRequest returns the role this request comes from
func jwtGetRoleFromRequest(r *http.Request) (string, error) {
	c, err := r.Cookie(JWT_COOKIE)
	if err != nil {
		if DB_ANON_ROLE != "" {
			return DB_ANON_ROLE, nil
		}
		return "", fmt.Errorf(`no '%s' cookie`, JWT_COOKIE)
	}

	rol, err := jwtGetRole(c.Value)
	if err != nil {
		if DB_ANON_ROLE != "" {
			return DB_ANON_ROLE, nil
		}
		return "", fmt.Errorf(`can't get role from token: %w`, err)
	}
	return rol, nil
}

func jwtSetCookieAttrs(cook *http.Cookie) {
	cook.Path = "/"
	cook.HttpOnly = true
	cook.Secure = true
	cook.SameSite = http.SameSiteLaxMode
	if JWT_DURATION > 0 {
		cook.Expires = time.Now().Local().Add(JWT_DURATION)
	}
}

func jwtRefreshCookieMiddleware(next http.Handler) http.Handler {
	if JWT_DURATION > 0 {
		return http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
			var cook, err = r.Cookie(JWT_COOKIE)
			if err == nil {
				// log.Print("refreshing !")
				// Update the expiry
				jwtSetCookieAttrs(cook)
				http.SetCookie(rw, cook)
			}

			next.ServeHTTP(rw, r)
		})
	}
	return next
}

// jwtMiddleware is a middleware that protects a route from an unauthorized client.
func jwtMiddleware(srv *sw.SwServer) func(next http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {

			cookie, err := r.Cookie(JWT_COOKIE)
			if err != nil {
				// pp.Println("Error getting cookie: ", err)
				ReplyError(w, 401, "Unauthorized", "You may not access this resource no cookie")
				return
			}

			claims, err := jwtGetMapClaims(cookie.Value)
			if err != nil {
				// pp.Println("Error getting claims: ", err)
				ReplyError(w, 401, "Unauthorized", "You may not access this resource")
				return
			}

			role, ok := claims["role"].(string)
			if !ok {
				// pp.Println("Unknown role")
				ReplyError(w, 401, "Unauthorized", "Unknown role")
				return
			}

			if srv.AvailableLoginFunctions.SessionCheck {
				sess, ok := claims["s"].(string)
				if !ok {
					// pp.Println("Unknown session")
					ReplyError(w, 401, "Unauthorized", "Unknown session")
					return
				}

				headers, err := json.Marshal(r.Header)
				if err != nil {
					// pp.Println(err)
					ReplyError(w, 401, "Unauthorized", "Failed to marshal headers: "+err.Error())
					return
				}

				if err := srv.PgSimpleExec(context.Background(), `SELECT auth.session_check($1, $2::text, $3::json)`, sess, role, headers); err != nil {
					// pp.Println(err)
					ReplyError(w, 401, "Unauthorized", "Failed to check session: "+err.Error())
					return
				}
			}

			newctx := context.WithValue(r.Context(), jwtRoleContextKey, role)
			next.ServeHTTP(w, r.WithContext(newctx))
		})
	}
}

// jwtSetCookieForRole sets the corresponding access token
func jwtSetCookieForRole(srv *sw.SwServer, r *http.Request, rw http.ResponseWriter, role string, impersonator bool) error {

	var sessionId string

	if srv.AvailableLoginFunctions.SessionCheck {
		// make a json from the headers
		headers, err := json.Marshal(r.Header)
		if err != nil {
			return err
		}

		// inform the database that `role` is logging in. We expect the function to return a number that will identify the session.
		row, err := srv.PgSimpleQueryRow(context.Background(), `SELECT auth.session_check(null, $1::text, $2::json)`, role, headers)
		if err != nil {
			log.Print(`!! jwt: `, err)
			return err
		}

		if err = row.Scan(&sessionId); err != nil {
			log.Print(`!! jwt: `, err)
			return err
		}
	}

	jwt, err := jwtCreateToken(srv, role, impersonator, sessionId)
	if err != nil {
		log.Print(`!! jwt: `, err)
		return err
	}

	c := &http.Cookie{
		Name:  JWT_COOKIE,
		Value: jwt,
	}

	jwtSetCookieAttrs(c)
	http.SetCookie(rw, c)
	return nil
}
