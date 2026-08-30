package main

import (
	"fmt"
	"net"
	"net/url"
	"strconv"

	"github.com/ceymard/rel/config"
)

// postgresURI builds a pgx connection URI for login against host:port/
// database — no existing helper builds this anywhere in the repo (pg.
// NewInfos takes a ready-made URI string). Takes the pieces directly (not
// a config.Pg) since the primary and query logins are two different
// values sharing the same host/port/database. Built via net/url.URL
// (User: url.UserPassword(...)) rather than hand-escaping with
// url.QueryEscape + fmt.Sprintf : QueryEscape encodes a space as "+",
// which is only meaningful in a query string, not the userinfo component
// — a literal "+" in a password would round-trip wrong. url.URL.String()
// escapes userinfo correctly for that component. net.JoinHostPort (not
// fmt.Sprintf("%s:%d", ...)) for the host:port pair — an IPv6 host needs
// bracketing ("::1" -> "[::1]:5432") or pgconn.ParseConfig's own
// net.SplitHostPort call fails outright on the unbracketed form ;
// confirmed empirically.
func postgresURI(host string, port int, database string, login config.Login) string {
	u := &url.URL{
		Scheme: "postgres",
		User:   url.UserPassword(login.User, login.Password),
		Host:   net.JoinHostPort(host, strconv.Itoa(port)),
		Path:   "/" + database,
	}
	return u.String()
}

// redactedTarget extracts "host:port/database" from a connection URI for
// logging — never the credentials, regardless of whether the URI came
// from pg.uri directly or was built from granular fields. Empty/unparsed
// input yields "" rather than an error : this is diagnostic-only, never
// worth failing startup over.
func redactedTarget(uri string) string {
	u, err := url.Parse(uri)
	if err != nil {
		return ""
	}
	return u.Host + u.Path
}

// resolveConnectionURIs builds the two connection strings pg.
// NewInfosAdminQuery needs from cfg.Pg : primaryURI (used for
// introspection and dmut migrations, always) and queryURI (used for the
// pool that actually serves requests). pg.query.user is OPTIONAL — see
// config.PgQuery's own doc comment — so queryURI is simply primaryURI
// again whenever it's unset ; NewInfosAdminQuery(primaryURI, primaryURI)
// then degenerates to one shared pool, same as the simplest possible
// pg.uri-only setup implies.
func resolveConnectionURIs(cfg config.Pg) (primaryURI, queryURI string, err error) {
	if cfg.URI != "" {
		primaryURI = cfg.URI
	} else {
		primaryURI = postgresURI(cfg.Host, cfg.Port, cfg.Database, config.Login{User: cfg.User, Password: cfg.Password})
	}

	if cfg.Query.User == "" {
		return primaryURI, primaryURI, nil
	}

	if cfg.URI == "" {
		return primaryURI, postgresURI(cfg.Host, cfg.Port, cfg.Database, cfg.Query.Login), nil
	}

	// pg.uri set AND pg.query.user set : swap just the userinfo on the
	// otherwise-opaque URI, keeping whatever host/port/database/query
	// params it already specifies.
	u, perr := url.Parse(primaryURI)
	if perr != nil {
		return "", "", fmt.Errorf("parsing pg.uri to apply pg.query.user: %w", perr)
	}
	u.User = url.UserPassword(cfg.Query.User, cfg.Query.Password)
	return primaryURI, u.String(), nil
}
