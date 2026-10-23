package config

import (
	"net"
	"net/url"
	"testing"
)

func TestPostgresURI(t *testing.T) {
	got := PostgresURI("db.internal", 5432, "rel_db", "rel_user", "p@ss w/ord")
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
	got := PostgresURI("::1", 5432, "rel_db", "rel_user", "pw")
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
