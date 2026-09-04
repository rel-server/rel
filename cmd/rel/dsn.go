package main

import (
	"fmt"
	"net"
	"net/url"
	"strconv"

	"github.com/ceymard/rel/config"
)

// postgresURI builds a pgx connection URI via net/url.URL (QueryEscape
// mis-escapes userinfo) and net.JoinHostPort (IPv6 needs bracketing).
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
// logging, never the credentials ; unparsed input yields "" rather than an error.
func redactedTarget(uri string) string {
	u, err := url.Parse(uri)
	if err != nil {
		return ""
	}
	return u.Host + u.Path
}

// resolveConnectionURIs builds primaryURI (introspection/dmut) and
// queryURI (serving pool) ; queryURI falls back to primaryURI when unset.
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

	// pg.uri AND pg.query.user set : swap just the userinfo, keep the rest.
	u, perr := url.Parse(primaryURI)
	if perr != nil {
		return "", "", fmt.Errorf("parsing pg.uri to apply pg.query.user: %w", perr)
	}
	u.User = url.UserPassword(cfg.Query.User, cfg.Query.Password)
	return primaryURI, u.String(), nil
}
