package main

import (
	"fmt"
	"net/url"

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
func postgresURI(pg config.Pg) string {
	u := &url.URL{
		Scheme: "postgres",
		User:   url.UserPassword(pg.Querier.User, pg.Querier.Password),
		Host:   fmt.Sprintf("%s:%d", pg.Host, pg.Port),
		Path:   "/" + pg.Database,
	}
	return u.String()
}
