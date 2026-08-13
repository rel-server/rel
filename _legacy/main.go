/// The main package for our simple server.
/// The server is only here to handle simple things, like
///  - OAuth with third party providers
///  - File uploads and file sends (through X-Accel-Redirect with NGinx)

// Stuff that would have to be handled ;
//  - setting headers from pg (via GUC and set_config and a string)
//  - having all request headers exposed
//  - GET (...) / INSERT (POST) / UPDATE (PATCH) / UPSERT (PUT) / DELETE (...)
//  - function calling / RPC
//  - return types (json / csv / xlsx / odt ?)
//	- handling blobs (base64 in json view ?)
//  - Websocket support for queries which allows for several in parallel (and works with a custom client)
//     * this would only produce json and consume json, no support for anything else.
//  - Websocket NOTIFY and LISTEN with filters

//  - bulk insert over several collections with json
// Super bulk insert using excel !

package main

import (
	"context"
	"crypto/rand"
	"embed"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/go-chi/chi"
	"github.com/go-chi/chi/middleware"
	"github.com/jackc/pgx/v5/pgxpool"
	"sales-way.com/server/sw"
)

func generateNonce() string {
	b := make([]byte, 16) // 128-bit nonce
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return base64.RawStdEncoding.EncodeToString(b)
}

func init() {
	head_csp = "default-src 'self' 'strict-dynamic' data: https://*.sales-way.com https://fonts.googleapis.com https://fonts.gstatic.com " + getenvOrDefault("SW_CSP_DEFAULT_SRC", "") + " blob:; script-src 'self' 'strict-dynamic' https://www.google-analytics.com " + getenvOrDefault("SW_CSP_SCRIPT_SRC", "") + " ; worker-src 'self' " + getenvOrDefault("SW_CSP_WORKER_SRC", "") + " blob: ; img-src 'self' https://*.sales-way.com " + getenvOrDefault("SW_CSP_IMG_SRC", "") + " data: ; font-src 'self' https://fonts.gstatic.com https://*.sales-way.com data: " + getenvOrDefault("SW_CSP_FONT_SRC", "") + "; style-src " + getenvOrDefault("SW_CSP_STYLE_SRC", "'self' 'strict-dynamic'") + ";" + getenvOrDefault("SW_CSP_ADDITIONAL", "")
	head_frame_options = getenvOrDefault("SW_X_FRAME_OPTIONS", "SAMEORIGIN")
	head_referrer_policy = getenvOrDefault("SW_REFERRER_POLICY", "same-origin")
	head_content_type_options = getenvOrDefault("SW_X_CONTENT_TYPE_OPTIONS", "nosniff")
	head_permissions_policy = getenvOrDefault("SW_PERMISSIONS_POLICY", "geolocation=(self), midi=(self), sync-xhr=(self), microphone=(self), camera=(self), magnetometer=(self), gyroscope=(self), fullscreen=(self), payment=(self)")
	head_cross_origin_embedder_policy = getenvOrDefault("SW_CROSS_ORIGIN_EMBEDDER_POLICY", "require-corp")
	head_cross_origin_opener_policy = getenvOrDefault("SW_CROSS_ORIGIN_OPENER_POLICY", "same-origin")
}

var head_csp = ""
var head_frame_options = ""
var head_referrer_policy = ""
var head_content_type_options = ""
var head_permissions_policy = ""
var head_cross_origin_embedder_policy = ""
var head_cross_origin_opener_policy = ""

//go:embed web/static/*
var staticFiles embed.FS

func main() {

	var err error
	var db_url = getenvOrDefault("DATABASE_URL", "PGRST_DB_URI", "")
	var srv sw.SwServer

	var schems_def = getenvOrDefault("PGRST_DB_SCHEMA", "public")
	srv.Schemas = strings.Split(schems_def, ",")

	srv.Router = chi.NewRouter()

	getClientVersion(&srv)

	var pool *pgxpool.Pool

	if db_url != "" {
		pool, err = pgxpool.New(context.Background(), db_url)
		for err != nil {
			log.Printf("[!] Failed to connect to database : %s, retrying in 1 second", err)
			time.Sleep(1 * time.Second)
			pool, err = pgxpool.New(context.Background(), db_url)
		}
	}

	srv.Pool = pool

	var hostname = getenvOrDefault("VIRTUAL_HOST", "localhost")
	var splitter = regexp.MustCompilePOSIX("[ \\t]*,[ \\t]*")
	srv.Hostnames = splitter.Split(hostname, -1)
	srv.Port = "3001"
	// var all_hostnames = strings.Split(hostname, ",")

	if hostname == "localhost" {
		// bye bye
		srv.LogWarn("Application *MUST* provide VIRTUAL_HOST")
	}

	srv.LogInfo("goserver version ", VERSION)
	if srv.Pool != nil {
		srv.LogInfo("using database (", db_url, ")")

		retries := 5
		for retries > 0 {
			srv.LogInfo("acquiring database connection")
			conn, err := srv.Pool.Acquire(context.Background())
			if err != nil {
				srv.LogWarn("Cannot aquire database connection", err)
				retries--
				srv.LogWarn("Waiting 5 seconds to retry, ", retries, " remaining")
				time.Sleep(5 * time.Second)
			} else {
				conn.Release()
				break
			}
		}
		if retries == 0 {
			srv.LogError("could not acquire database at all, failing.")
			return
		}

	} else {
		srv.LogWarn("no Postgres database")
	}
	srv.LogInfo("virtual host is ", hostname)

	srv.Router.Use(middleware.RequestID)
	srv.Router.Use(middleware.RealIP)
	srv.Router.Use(middleware.Logger)
	srv.Router.Use(RecovererColored)
	srv.Router.Use(jwtRefreshCookieMiddleware)

	// Rajout pour une question de sécurité pour éviter les attaques de "Cache Poisoning", tel
	// que le rapport de sécurité de BMS le suggère.
	// On rajoute assez brutalement le header "Vary: Origin" à toutes les réponses de notre serveur
	// pour éviter qu'une requête qui n'était pas CORS vers un endroit de notre serveur se fasse cacher
	// par le browser en tant que non CORS et se fasse ensuite jeter parce que subitement le browser
	// a voulu passer par le CORS.
	//
	// Il se peut que ce header soit problématique le jour où on décide d'autoriser des requêtes
	// cross domain (ce qui n'est pas le cas dans nos applications) et où on se retrouve potentiellement
	// avec un Allow-Origin: * (encore une fois, ce n'est pas le cas).
	srv.Router.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
			var origin = r.Header.Get("Origin")
			if origin == "" || origin == "null" {
				origin = "https://" + r.Host
			}
			rw.Header().Add("Access-Control-Allow-Origin", origin)
			rw.Header().Add("Vary", "Origin")
			next.ServeHTTP(rw, r)
		})
	})

	if getenvOrDefault("SW_WEBSOCKETS_DISABLE") != "" {
		session_should_log = getenvOrDefault("SW_WEBSOCKETS_LOG") != ""
		if err = setupWebsockets(&srv); err != nil {
			srv.LogError("when setting up websockets: ", err)
		}
	}

	if err = tryRunDmut(&srv, db_url); err != nil {
		srv.LogError("when running mutations: ", err)
	}

	if srv.HasPg() {
		if err := SetupPG(&srv); err != nil {
			sw.LogError(err)
		}
		if err := setupPg3Routes(&srv); err != nil {
			srv.LogError("when setting up pg3 routes: ", err)
		}
		SetupAuth(&srv)
	}

	if err = SetupOAuth(&srv); err != nil {
		srv.LogError(fmt.Errorf(`did not setup oauth: %w`, err))
	}

	if err = setupSaml(&srv); err != nil {
		srv.LogError(fmt.Errorf(`did not setup saml: %w`, err))
	}

	if err = srv.PgReloadTables(); err != nil {
		srv.LogError("when reloading tables: ", err)
	}

	registerPlugins(&srv)

	// Strip the "web/static" prefix so it's served at root
	embeddedStatic, err := fs.Sub(staticFiles, "web/static")
	if err != nil {
		log.Fatal("embedded static failure:", err)
	}

	embeddedServer := http.FileServer(http.FS(embeddedStatic))

	var static = getenvOrDefault("SW_STATIC_PATH", "/static")

	srv.Router.NotFound(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {

		// Join internally call path.Clean to prevent directory traversal
		path := filepath.Join(static, r.URL.Path)

		// check whether a file exists or is a directory at the given path
		fi, err := os.Stat(path)
		if err != nil {
			if os.IsNotExist(err) {
				embeddedServer.ServeHTTP(w, r)
				// ReplyError(w, 404, "path not found", r.URL.Path+" does not exist")
				return
			}

			ReplyError(w, http.StatusInternalServerError, "Internal server error", err.Error())
			return
		}

		// File is a directory, serve index.html
		if fi.IsDir() {
			path = filepath.Join(path, "index.html")
			if fi, err := os.Stat(path); err != nil || fi.IsDir() {
				ReplyError(w, 404, "path not found", r.URL.Path+" does not exist")
				return
			}
		}

		//
		if strings.HasSuffix(path, ".html") || strings.HasSuffix(path, ".htm") {
			var head = w.Header()
			var origin = r.Header.Get("Origin")
			if origin == "" || origin == "null" {
				origin = "https://" + r.Host
			}

			var nonce = generateNonce()

			head.Add("Access-Control-Allow-Origin", origin)
			head.Add("Vary", "Origin")

			var head_csp_nonced = strings.Replace(head_csp, "'strict-dynamic'", "'strict-dynamic' 'nonce-"+nonce+"'", -1)
			head.Add("Content-Security-Policy", head_csp_nonced)
			srv.LogInfo("head_csp_nonced: ", head_csp_nonced)

			head.Add("X-Frame-Options", head_frame_options)
			head.Add("X-Content-Type-Options", head_content_type_options)
			head.Add("Referrer-Policy", head_referrer_policy)
			head.Add("Permissions-Policy", head_permissions_policy)
			head.Add("Cross-Origin-Embedder-Policy", head_cross_origin_embedder_policy)
			head.Add("Cross-Origin-Opener-Policy", head_cross_origin_opener_policy)

			// set cache control header to prevent caching
			// this is to prevent the browser from caching the index.html
			// and serving old build of SPA App
			w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")

			contents, err := os.ReadFile(path)
			if err != nil {
				ReplyError(w, http.StatusInternalServerError, "Internal server error", err.Error())
				return
			}

			str_contents := strings.Replace(string(contents), "{{nonce}}", nonce, -1)

			w.Write([]byte(str_contents))
			// file does not exist or path is a directory, serve index.html
			// http.ServeFile(w, r, indexpath)

			return
		}

		// set cache control header to serve file for a year
		// static files in this case need to be cache busted
		// (usualy by appending a hash to the filename)
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")

		// otherwise, use http.FileServer to serve the static file
		http.FileServer(http.Dir(static)).ServeHTTP(w, r)

	}))

	go func() {

		signalChannel := make(chan os.Signal, 2)
		signal.Notify(signalChannel, syscall.SIGUSR1)
		go func() {
			for {
				sig := <-signalChannel
				switch sig {
				case syscall.SIGUSR1:
					srv.LogWarn("Received, SIGUSR1, relaunching dmut")
					if err = tryRunDmut(&srv, db_url); err != nil {
						srv.LogError("when running mutations: ", err)
					} else {
						srv.PgReloadTables()
						ReloadLoginFunctions(&srv)
					}
				}
			}
		}()
	}()

	if getenvOrDefault("SW_WATCH_SERVER", "") != "" || getenvOrDefault("SW_ENABLE_DEBUG", "") == "true" {
		go watchAndRelaunch(&srv)
	}

	srv.Router.Get("/heartbeat", func(w http.ResponseWriter, r *http.Request) {

		status := struct {
			Ok   bool   `json:"ok"`
			Db   string `json:"db"`
			Dmut string `json:"dmut"`
		}{}
		status.Ok = true

		if srv.Pool == nil {
			status.Db = "no database"
		} else {
			status.Db = "ok"
		}

		status.Dmut = srv.DmutStatus

		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(status)
	})

	srv.LogInfo("*** Go server to listen on port ", srv.Port)
	srv.LogInfo("*** running as UID ", os.Getuid())
	log.Fatal(http.ListenAndServe(":"+srv.Port, srv.Router))
}

func watchAndRelaunch(_ *sw.SwServer) {
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		log.Fatal(err)
	}
	defer watcher.Close()

	done := make(chan bool)

	go func() {
		for {
			select {
			case event, ok := <-watcher.Events:
				if !ok {
					continue
				}

				log.Println("event:", event)

				argv0, err := lookPath()
				if nil != err {
					log.Println("error:", err)
					continue
				}
				// Doing stuff

				// Doing code.
				// Restart the server
				log.Println("restarting server")
				time.Sleep(200 * time.Millisecond)

				err = syscall.Exec(argv0, os.Args, os.Environ())
				if err != nil {
					log.Println("error:", err)
					continue
				}

			case err, ok := <-watcher.Errors:
				if !ok {
					continue
				}
				log.Println("error:", err)
			}
		}
	}()

	ex, err := os.Executable()
	if err != nil {
		log.Fatal(err)
	}

	log.Println("Watching", ex)
	err = watcher.Add(ex)
	if err != nil {
		log.Fatal(err)
	}
	<-done
}

func lookPath() (string, error) {
	if len(os.Args) == 0 {
		return "", errors.New("os.Args is empty")
	}
	argv0, err := exec.LookPath(os.Args[0])
	if nil != err {
		return "", err
	}
	if _, err = os.Stat(argv0); nil != err {
		return "", err
	}
	return argv0, nil
}
