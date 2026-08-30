package main

import (
	"net"
	"net/url"
	"strings"
	"testing"

	"github.com/ceymard/rel/config"
)

func TestPostgresURI(t *testing.T) {
	got := postgresURI("db.internal", 5432, "rel_db", config.Login{User: "rel_user", Password: "p@ss w/ord"})
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
	got := postgresURI("::1", 5432, "rel_db", config.Login{User: "rel_user", Password: "pw"})
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

func TestResolveConnectionURIs_GranularFields_NoQueryLoginSet(t *testing.T) {
	primary, query, err := resolveConnectionURIs(config.Pg{
		User: "pg_user", Password: "pg_pass", Host: "db.internal", Port: 5432, Database: "rel_db",
	})
	if err != nil {
		t.Fatalf("resolveConnectionURIs: %v", err)
	}
	if primary != query {
		t.Errorf("expected the SAME uri for both when pg.query.user is unset, got primary=%q query=%q", primary, query)
	}
	if want := "postgres://pg_user:pg_pass@db.internal:5432/rel_db"; primary != want {
		t.Errorf("primary = %q, want %q", primary, want)
	}
}

func TestResolveConnectionURIs_GranularFields_QueryLoginNarrower(t *testing.T) {
	primary, query, err := resolveConnectionURIs(config.Pg{
		User: "pg_user", Password: "pg_pass", Host: "db.internal", Port: 5432, Database: "rel_db",
		Query: config.PgQuery{Login: config.Login{User: "query_user", Password: "query_pass"}},
	})
	if err != nil {
		t.Fatalf("resolveConnectionURIs: %v", err)
	}
	if want := "postgres://pg_user:pg_pass@db.internal:5432/rel_db"; primary != want {
		t.Errorf("primary = %q, want %q", primary, want)
	}
	if want := "postgres://query_user:query_pass@db.internal:5432/rel_db"; query != want {
		t.Errorf("query = %q, want %q", query, want)
	}
}

// TestResolveConnectionURIs_PgURI_Authoritative covers pg.uri's own
// all-or-nothing rule end to end : the granular Host/User fields here are
// deliberately wrong ("should-be-ignored") and must NOT leak into either
// built URI.
func TestResolveConnectionURIs_PgURI_Authoritative(t *testing.T) {
	primary, query, err := resolveConnectionURIs(config.Pg{
		URI: "postgres://u:p@db.internal:5432/mydb", Host: "should-be-ignored", User: "should-be-ignored",
	})
	if err != nil {
		t.Fatalf("resolveConnectionURIs: %v", err)
	}
	if primary != "postgres://u:p@db.internal:5432/mydb" {
		t.Errorf("primary = %q, want the raw pg.uri unchanged", primary)
	}
	if query != primary {
		t.Errorf("expected query == primary when pg.query.user is unset, got %q", query)
	}
}

// TestResolveConnectionURIs_PgURI_WithQueryLogin covers the one case
// dsn.go's own logic has to actually manipulate a URI rather than just
// pass it through or build one from scratch : pg.uri set AND pg.query.user
// set, which must swap only the userinfo, keeping host/port/database from
// the original URI intact.
func TestResolveConnectionURIs_PgURI_WithQueryLogin(t *testing.T) {
	primary, query, err := resolveConnectionURIs(config.Pg{
		URI:   "postgres://u:p@db.internal:5432/mydb",
		Query: config.PgQuery{Login: config.Login{User: "query_user", Password: "query_pass"}},
	})
	if err != nil {
		t.Fatalf("resolveConnectionURIs: %v", err)
	}
	if primary != "postgres://u:p@db.internal:5432/mydb" {
		t.Errorf("primary = %q, want the raw pg.uri unchanged", primary)
	}
	u, perr := url.Parse(query)
	if perr != nil {
		t.Fatalf("built query uri doesn't parse: %v", perr)
	}
	if u.User.Username() != "query_user" {
		t.Errorf("expected query uri's user = query_user, got %q (uri: %q)", u.User.Username(), query)
	}
	if pass, _ := u.User.Password(); pass != "query_pass" {
		t.Errorf("expected query uri's password = query_pass, got %q (uri: %q)", pass, query)
	}
	if u.Hostname() != "db.internal" || u.Port() != "5432" || u.Path != "/mydb" {
		t.Errorf("expected host/port/database preserved from pg.uri, got %q", query)
	}
}
