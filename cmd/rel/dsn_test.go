package main

import (
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
