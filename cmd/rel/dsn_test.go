package main

import (
	"net"
	"net/url"
	"strings"
	"testing"

	"github.com/rel-server/rel/config"
)

func TestPostgresURI(t *testing.T) {
	got := postgresURI("db.internal", 5432, "rel_db", "rel_user", "p@ss w/ord")
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

// TestPostgresURI_IPv6HostIsBracketed : an unbracketed IPv6 host produces
// "::1:5432", which net.SplitHostPort rejects — confirmed empirically.
func TestPostgresURI_IPv6HostIsBracketed(t *testing.T) {
	got := postgresURI("::1", 5432, "rel_db", "rel_user", "pw")
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

func TestRedactedTarget_NeverIncludesCredentials(t *testing.T) {
	got := redactedTarget("postgres://secret_user:secret_pass@db.internal:5432/mydb")
	if got != "db.internal:5432/mydb" {
		t.Errorf("redactedTarget = %q, want %q", got, "db.internal:5432/mydb")
	}
	if strings.Contains(got, "secret_user") || strings.Contains(got, "secret_pass") {
		t.Errorf("redactedTarget leaked a credential: %q", got)
	}
}

func TestResolveConnectionURI_GranularFields(t *testing.T) {
	got := resolveConnectionURI(config.Pg{
		User: "pg_user", Password: "pg_pass", Host: "db.internal", Port: 5432, Database: "rel_db",
	})
	if want := "postgres://pg_user:pg_pass@db.internal:5432/rel_db"; got != want {
		t.Errorf("resolveConnectionURI = %q, want %q", got, want)
	}
}

// TestResolveConnectionURI_PgURI_Authoritative : pg.uri wins outright over
// the granular fields, unchanged.
func TestResolveConnectionURI_PgURI_Authoritative(t *testing.T) {
	got := resolveConnectionURI(config.Pg{
		URI: "postgres://u:p@db.internal:5432/mydb", Host: "should-be-ignored", User: "should-be-ignored",
	})
	if got != "postgres://u:p@db.internal:5432/mydb" {
		t.Errorf("resolveConnectionURI = %q, want the raw pg.uri unchanged", got)
	}
}
