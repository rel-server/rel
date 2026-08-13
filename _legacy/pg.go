package main

import (
	"github.com/NYTimes/gziphandler"
	"sales-way.com/server/sw"
)

func SetupPG(srv *sw.SwServer) error {

	var jwt = jwtMiddleware(srv)

	var routes = srv.Router.With(jwt, gziphandler.GzipHandler)

	SetupPG2Routes(srv, routes)
	setupPg3Routes(srv)
	setupPgTypescript(srv)

	return nil
}
