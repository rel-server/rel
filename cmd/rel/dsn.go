package main

import (
	"net"
	"net/url"
	"strconv"

	"github.com/rel-server/rel/config"
)

// postgresURI builds a pgx connection URI via net/url.URL (QueryEscape
// mis-escapes userinfo) and net.JoinHostPort (IPv6 needs bracketing).
func postgresURI(host string, port int, database, user, password string) string {
	u := &url.URL{
		Scheme: "postgres",
		User:   url.UserPassword(user, password),
		Host:   net.JoinHostPort(host, strconv.Itoa(port)),
		Path:   "/" + database,
	}
	return u.String()
}

// redactedTarget extracts "host:port/database" from a connection URI for
// logging, never the credentials ; unparsed input yields "" rather than an error.
func redactedTarget(uri string) string {
	u, err := url.Parse(uri)
	if err != nil {
		return ""
	}
	return u.Host + u.Path
}

// resolveConnectionURI builds the single Postgres connection string used
// for introspection, reload.cmd, AND serving requests alike (config.PgQuery's
// own doc comment : there is no separate, narrower-scoped login) — pg.uri
// as-is when set (preserving any query parameters, e.g. sslmode), otherwise
// built from pg.host/pg.port/pg.database/pg.user/pg.password.
func resolveConnectionURI(cfg config.Pg) string {
	if cfg.URI != "" {
		return cfg.URI
	}
	return postgresURI(cfg.Host, cfg.Port, cfg.Database, cfg.User, cfg.Password)
}
