// Package dbauth is the half of jwt-roles-and-http.md's Lifecycle that
// needs an actual Postgres connection — Check (step 3, the check_session
// function) and identifier-escaping for Apply role (step 5) — shared
// between /rpc (rpc/handler.go, its own transaction) and /rel
// (server/rel.go, the request's pinned pool connection). Verify/Renew
// (steps 2/4, no DB needed) live in the jwt package instead — see
// jwt/middleware.go's own doc comment for why the split falls there.
package dbauth

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/jackc/pgx/v5/pgconn"

	jwtpkg "github.com/ceymard/rel/jwt"
)

// Execer is the common subset of pgx.Tx and *pgxpool.Conn this package
// needs — deliberately narrow so either caller's own connection/transaction
// value satisfies it without an adapter.
type Execer interface {
	Exec(ctx context.Context, sql string, arguments ...any) (pgconn.CommandTag, error)
}

// CheckSession invokes http.functions.check_session — a void-returning
// function, called only when a JWT already verified (Lifecycle step 3),
// immediately before the role switch. An RSxxx exception here aborts the
// whole request ; any other Postgres error is a genuine 500 — both are the
// caller's own responsibility to classify (see pgerr.RSStatus).
func CheckSession(ctx context.Context, exec Execer, qualifiedName string, claims jwtpkg.Claims) error {
	claimsJSON, err := json.Marshal(claims)
	if err != nil {
		return err
	}
	return CallJSONBFunction(ctx, exec, qualifiedName, claimsJSON)
}

// CallJSONBFunction invokes qualifiedName(payload::jsonb) — the shared
// calling convention specs/04-http-content.md ### Access control
// deliberately reuses from CheckSession's own : "a configured function
// name, called with a jsonb payload, RSxxx to reject, returning normally
// to allow." Void-returning by convention (the caller never reads a
// result), but works identically for any single-jsonb-argument function
// regardless of its own declared return type — Exec doesn't parse rows.
func CallJSONBFunction(ctx context.Context, exec Execer, qualifiedName string, payload []byte) error {
	schema, name, ok := strings.Cut(qualifiedName, ".")
	if !ok {
		schema, name = "public", qualifiedName
	}
	sql := "select " + EscapeIdentifier(schema) + "." + EscapeIdentifier(name) + "($1::jsonb)"
	_, err := exec.Exec(ctx, sql, payload)
	return err
}

// EscapeIdentifier doubles embedded double-quotes and wraps in "..." — the
// same rule pg.SqlIdentifier.EscapedString() uses, reimplemented locally
// for a single bare identifier (a role name, or one half of a schema-
// qualified pair built up by hand), not a schema-qualified pair.
func EscapeIdentifier(name string) string {
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}
