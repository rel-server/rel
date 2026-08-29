package main

import (
	"net"
	"net/url"
	"testing"

	"github.com/ceymard/rel/config"
)

func TestPostgresURI(t *testing.T) {
	got := postgresURI(config.Pg{
		Querier:  config.Login{User: "rel_user", Password: "p@ss w/ord"},
		Host:     "db.internal",
		Port:     5432,
		Database: "rel_db",
	})
	want := "postgres://rel_user:p%40ss%20w%2Ford@db.internal:5432/rel_db"
	if got != want {
		t.Errorf("postgresURI = %q, want %q", got, want)
	}

	u, err := url.Parse(got)
	if err != nil {
		t.Fatalf("built URI doesn't parse: %v", err)
	}
	if pass, _ := u.User.Password(); pass != "p@ss w/ord" {
		t.Errorf("round-tripped password = %q, want the original %q", pass, "p@ss w/ord")
	}
}

// TestPostgresURI_IPv6HostIsBracketed covers a bug an adversarial review
// caught : fmt.Sprintf("%s:%d", pg.Host, pg.Port) on an IPv6 host produces
// an unbracketed "::1:5432", which net.SplitHostPort (used internally by
// pgconn.ParseConfig when pg.NewInfos actually connects) rejects outright
// with "too many colons in address" — confirmed empirically. The built URI
// must parse back to the exact host/port pgx will see.
func TestPostgresURI_IPv6HostIsBracketed(t *testing.T) {
	got := postgresURI(config.Pg{
		Querier:  config.Login{User: "rel_user", Password: "pw"},
		Host:     "::1",
		Port:     5432,
		Database: "rel_db",
	})
	u, err := url.Parse(got)
	if err != nil {
		t.Fatalf("built URI doesn't parse: %v", err)
	}
	if host, port := u.Hostname(), u.Port(); host != "::1" || port != "5432" {
		t.Errorf("expected host=::1 port=5432, got host=%q port=%q (uri: %q)", host, port, got)
	}
	if _, _, err := net.SplitHostPort(u.Host); err != nil {
		t.Errorf("u.Host %q doesn't survive net.SplitHostPort (what pgx actually calls): %v", u.Host, err)
	}
}
