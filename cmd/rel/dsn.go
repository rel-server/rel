package main

import (
	"net"
	"net/url"
	"strconv"

	"github.com/ceymard/rel/config"
)

// postgresURI builds a pgx connection URI from cfg.Pg — no existing helper
// builds this anywhere in the repo (pg.NewInfos takes a ready-made URI
// string ; config.Pg previously had no Database field at all, see this
// task's plan). Built via net/url.URL (User: url.UserPassword(...)) rather
// than hand-escaping with url.QueryEscape + fmt.Sprintf : QueryEscape
// encodes a space as "+", which is only meaningful in a query string, not
// the userinfo component — a literal "+" in a password would round-trip
// wrong. url.URL.String() escapes userinfo correctly for that component.
// net.JoinHostPort (not fmt.Sprintf("%s:%d", ...)) for the host:port pair —
// an IPv6 pg.Host needs bracketing ("::1" -> "[::1]:5432") or
// pgconn.ParseConfig's own net.SplitHostPort call fails outright on the
// unbracketed form ; confirmed empirically.
func postgresURI(pg config.Pg) string {
	u := &url.URL{
		Scheme: "postgres",
		User:   url.UserPassword(pg.Querier.User, pg.Querier.Password),
		Host:   net.JoinHostPort(pg.Host, strconv.Itoa(pg.Port)),
		Path:   "/" + pg.Database,
	}
	return u.String()
}
