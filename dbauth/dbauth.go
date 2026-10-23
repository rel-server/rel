// Package dbauth is the half of authentication.md's Lifecycle that
// needs an actual Postgres connection — role-switching and identifier-
// escaping for Apply role (step 5) — used by the reqauth package, which
// opens every request's role-scoped transaction. Verify/Renew (steps 2/4, no DB
// needed) live in the jwt package instead. Check (step 3) was check_session, a single configured
// function ; specs/new-routes.md ## Middleware supersedes it — a session
// check is now an ordinary middleware function.
package dbauth

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	jwtpkg "github.com/rel-server/rel/jwt"
	"github.com/rel-server/rel/pg"
)

// Execer is the common subset of pgx.Tx and *pgxpool.Conn this package
// needs — deliberately narrow so either caller's own connection/transaction
// value satisfies it without an adapter.
type Execer interface {
	Exec(ctx context.Context, sql string, arguments ...any) (pgconn.CommandTag, error)
}

// Querier is Execer's counterpart for a single-jsonb-in, single-jsonb-out
// call — e.g. specs/oauth-saml.md ## Callback function, whose whole point
// is the RelHttpResponse it returns (see CallJSONBFunctionReturningJSON
// below).
type Querier interface {
	QueryRow(ctx context.Context, sql string, arguments ...any) pgx.Row
}

// SetLocalRole issues SET LOCAL ROLE <role> — transaction-scoped, or (on a
// bare *pgxpool.Conn with no transaction open) in effect until the next
// transaction starts. Returns the raw Postgres error, unclassified ;
// callers wrap/render it however their own response shape requires.
func SetLocalRole(ctx context.Context, exec Execer, role string) error {
	_, err := exec.Exec(ctx, "SET LOCAL ROLE "+EscapeIdentifier(role))
	return err
}

// ClaimsSettingName is the current_setting()/set_config() GUC name the
// verified JWT's claims are exposed under — "rel.jwt.claims", the same
// two-dot custom-GUC shape PostgREST's own request.jwt.claims already
// established as safe (Postgres has no fixed-depth restriction on a
// placeholder GUC's name, only that it contain at least one dot). Readable
// from any function running inside the same transaction as SetLocalClaims
// — a middleware function or a query/route function's own nested calls —
// via current_setting('rel.jwt.claims', true)::jsonb ; the missing_ok
// second argument is defensive, for a connection outside rel's own pool
// (a superuser's direct psql session) rather than anything rel itself ever
// leaves unset.
const ClaimsSettingName = "rel.jwt.claims"

// BatchSender is the connection surface BeginAs needs ; *pgxpool.Conn satisfies it.
type BatchSender interface {
	SendBatch(ctx context.Context, b *pgx.Batch) pgx.BatchResults
}

// StepError says which of BeginAs's three statements failed.
type StepError struct {
	Step string // "begin", "claims" or "role"
	Err  error
}

func (e *StepError) Error() string { return e.Step + ": " + e.Err.Error() }
func (e *StepError) Unwrap() error { return e.Err }

// BeginAs opens a transaction, exposes claims (SetLocalClaims) and switches to
// role (SetLocalRole), in one round trip instead of three. On error the
// transaction may be open : the caller rolls back, as it would after a failed step.
func BeginAs(ctx context.Context, conn BatchSender, claims jwtpkg.Claims, role string) error {
	claimsJSON, err := json.Marshal(claims)
	if err != nil {
		return &StepError{Step: "claims", Err: err}
	}
	var b pgx.Batch
	b.Queue("begin")
	b.Queue("select set_config($1, $2, true)", ClaimsSettingName, string(claimsJSON))
	b.Queue("SET LOCAL ROLE " + EscapeIdentifier(role))
	res := conn.SendBatch(ctx, &b)
	for _, step := range []string{"begin", "claims", "role"} {
		if _, err := res.Exec(); err != nil {
			_ = res.Close()
			return &StepError{Step: step, Err: err}
		}
	}
	return res.Close()
}

// SetLocalClaims exposes claims under ClaimsSettingName, transaction-
// scoped exactly like SetLocalRole (set_config's own third argument,
// is_local=true) — reverted by Postgres at COMMIT/ROLLBACK, before the
// connection can be released back to the pool, so it can never leak into
// whatever unrelated request the pool hands that connection to next. This
// is why every call site sets it inside a real transaction, never on a
// bare connection : set_config(..., true) called outside a transaction
// block is NOT transaction-scoped — it behaves as a bare session-level
// SET instead (Postgres's own documented behavior), which is exactly the
// leak this exists to prevent.
//
// claims nil (anonymous, or not yet verified) mints the GUC as the JSON
// literal "null" rather than leaving it unset, so a reader can always do
// current_setting('rel.jwt.claims', true)::jsonb->>'x' with no separate
// "no session" case to special-case.
func SetLocalClaims(ctx context.Context, exec Execer, claims jwtpkg.Claims) error {
	claimsJSON, err := json.Marshal(claims)
	if err != nil {
		return err
	}
	_, err = exec.Exec(ctx, "select set_config($1, $2, true)", ClaimsSettingName, string(claimsJSON))
	return err
}

// NoRoleConfiguredMessage is the exact error text for an empty role at
// Lifecycle step 5 (query.anonymous_role unset, request anonymous) —
// skipping the role switch instead would silently run the request under
// whatever role the connection already has, a privilege escalation.
// Every SET LOCAL ROLE call site must use this exact wording, not restate it.
const NoRoleConfiguredMessage = "no role configured (query.anonymous_role is unset and request is anonymous)"

// QualifiedIdentifier splits qualifiedName on its first "." into
// (schema, name), defaulting schema to "public" when unqualified — shared
// by CallJSONBFunctionReturningJSON below.
func QualifiedIdentifier(qualifiedName string) (schema, name string) {
	schema, name, ok := strings.Cut(qualifiedName, ".")
	if !ok {
		return "public", qualifiedName
	}
	return schema, name
}

// CallJSONBFunctionReturningJSON invokes qualifiedName(payload::jsonb) and
// returns its own jsonb result raw — specs/oauth-saml.md ## Callback
// function's calling convention : the SSO callback's whole point is its
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

// EscapeIdentifier doubles embedded double-quotes and wraps in "..." — for a
// single bare identifier (a role name, or one half of a schema-qualified
// pair built up by hand), not a schema-qualified pair.
func EscapeIdentifier(name string) string {
	return pg.EscapeIdentifierPart(name)
}
