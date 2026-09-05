// Package dbauth is the half of authentication.md's Lifecycle that
// needs an actual Postgres connection — Check (step 3, the check_session
// function) and identifier-escaping for Apply role (step 5) — shared
// between /route (route/handler.go, its own transaction) and /rel
// (server/rel.go, the request's pinned pool connection). Verify/Renew
// (steps 2/4, no DB needed) live in the jwt package instead — see
// jwt/middleware.go's own doc comment for why the split falls there.
package dbauth

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	jwtpkg "github.com/ceymard/rel/jwt"
)

// Execer is the common subset of pgx.Tx and *pgxpool.Conn this package
// needs — deliberately narrow so either caller's own connection/transaction
// value satisfies it without an adapter.
type Execer interface {
	Exec(ctx context.Context, sql string, arguments ...any) (pgconn.CommandTag, error)
}

// Querier is Execer's counterpart for a single-jsonb-in, single-jsonb-out
// call — e.g. specs/oauth-saml.md ## Callback function, which (unlike
// CheckSession/CallJSONBFunction below) returns a RelHttpResponse the
// caller must actually read.
type Querier interface {
	QueryRow(ctx context.Context, sql string, arguments ...any) pgx.Row
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

// CheckSessionIfConfigured calls CheckSession, but only when verified is
// true and qualifiedName is non-empty — a no-op (nil) otherwise.
func CheckSessionIfConfigured(ctx context.Context, exec Execer, qualifiedName string, claims jwtpkg.Claims, verified bool) error {
	if !verified || qualifiedName == "" {
		return nil
	}
	return CheckSession(ctx, exec, qualifiedName, claims)
}

// SetLocalRole issues SET LOCAL ROLE <role> — transaction-scoped, or (on a
// bare *pgxpool.Conn with no transaction open) in effect until the next
// transaction starts. Returns the raw Postgres error, unclassified ;
// callers wrap/render it however their own response shape requires.
func SetLocalRole(ctx context.Context, exec Execer, role string) error {
	_, err := exec.Exec(ctx, "SET LOCAL ROLE "+EscapeIdentifier(role))
	return err
}

// NoRoleConfiguredMessage is the exact error text for an empty role at
// Lifecycle step 5 (query.anonymous_role unset, request anonymous) —
// skipping the role switch instead would silently run the request under
// whatever role the connection already has, a privilege escalation.
// Every SET LOCAL ROLE call site must use this exact wording, not restate it.
const NoRoleConfiguredMessage = "no role configured (query.anonymous_role is unset and request is anonymous)"

// CallJSONBFunction invokes qualifiedName(payload::jsonb) — the shared
// calling convention specs/http-content.md ### Access control
// deliberately reuses from CheckSession's own : "a configured function
// name, called with a jsonb payload, RSxxx to reject, returning normally
// to allow." Void-returning by convention (the caller never reads a
// result), but works identically for any single-jsonb-argument function
// regardless of its own declared return type — Exec doesn't parse rows.
func CallJSONBFunction(ctx context.Context, exec Execer, qualifiedName string, payload []byte) error {
	schema, name := QualifiedIdentifier(qualifiedName)
	sql := "select " + EscapeIdentifier(schema) + "." + EscapeIdentifier(name) + "($1::jsonb)"
	_, err := exec.Exec(ctx, sql, payload)
	return err
}

// QualifiedIdentifier splits qualifiedName on its first "." into
// (schema, name), defaulting schema to "public" when unqualified — the
// same convention CallJSONBFunction below already applies inline, factored
// out so CallJSONBFunctionReturningJSON can share it.
func QualifiedIdentifier(qualifiedName string) (schema, name string) {
	schema, name, ok := strings.Cut(qualifiedName, ".")
	if !ok {
		return "public", qualifiedName
	}
	return schema, name
}

// CallJSONBFunctionReturningJSON invokes qualifiedName(payload::jsonb) and
// returns its own jsonb result raw — specs/oauth-saml.md ## Callback
// function's calling convention : unlike CallJSONBFunction (Exec, result
// discarded by convention), the SSO callback's whole point is its
// RelHttpResponse return value.
func CallJSONBFunctionReturningJSON(ctx context.Context, q Querier, qualifiedName string, payload []byte) ([]byte, error) {
	schema, name := QualifiedIdentifier(qualifiedName)
	sql := "select " + EscapeIdentifier(schema) + "." + EscapeIdentifier(name) + "($1::jsonb)"
	var raw []byte
	if err := q.QueryRow(ctx, sql, payload).Scan(&raw); err != nil {
		return nil, err
	}
	return raw, nil
}

// EscapeIdentifier doubles embedded double-quotes and wraps in "..." — the
// same rule pg.SqlIdentifier.EscapedString() uses, reimplemented locally
// for a single bare identifier (a role name, or one half of a schema-
// qualified pair built up by hand), not a schema-qualified pair.
func EscapeIdentifier(name string) string {
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}
