// Package dbauth is the database half of authentication : exposing the session's claims and switching role, for
// reqauth's role-scoped transaction.
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

// Execer is satisfied by both pgx.Tx and *pgxpool.Conn.
type Execer interface {
	Exec(ctx context.Context, sql string, arguments ...any) (pgconn.CommandTag, error)
}

// Querier is Execer's counterpart for CallJSONBFunctionReturningJSON.
type Querier interface {
	QueryRow(ctx context.Context, sql string, arguments ...any) pgx.Row
}

// SetLocalRole issues SET LOCAL ROLE <role>. Returns the raw Postgres error.
func SetLocalRole(ctx context.Context, exec Execer, role string) error {
	_, err := exec.Exec(ctx, "SET LOCAL ROLE "+EscapeIdentifier(role))
	return err
}

// ClaimsSettingName is the setting the session's claims are exposed under, read in the same transaction with
// current_setting('rel.jwt.claims', true)::jsonb.
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

// SetLocalClaims exposes claims under ClaimsSettingName for the transaction. Call it inside a transaction only :
// outside one the setting outlives the request on the pooled connection.
//
// nil claims set the JSON "null", so readers have no unset case.
func SetLocalClaims(ctx context.Context, exec Execer, claims jwtpkg.Claims) error {
	claimsJSON, err := json.Marshal(claims)
	if err != nil {
		return err
	}
	_, err = exec.Exec(ctx, "select set_config($1, $2, true)", ClaimsSettingName, string(claimsJSON))
	return err
}

// NoRoleConfiguredMessage is the error for an anonymous request with no anonymous role. The request must fail :
// skipping the role switch would run it as the connection's role.
const NoRoleConfiguredMessage = "no role configured (query.anonymous_role is unset and request is anonymous)"

// QualifiedIdentifier splits qualifiedName on its first "." into (schema, name) ; schema defaults to "public".
func QualifiedIdentifier(qualifiedName string) (schema, name string) {
	schema, name, ok := strings.Cut(qualifiedName, ".")
	if !ok {
		return "public", qualifiedName
	}
	return schema, name
}

// CallJSONBFunctionReturningJSON invokes qualifiedName(payload::jsonb) and returns its jsonb result raw.
func CallJSONBFunctionReturningJSON(ctx context.Context, q Querier, qualifiedName string, payload []byte) ([]byte, error) {
	schema, name := QualifiedIdentifier(qualifiedName)
	sql := "select " + EscapeIdentifier(schema) + "." + EscapeIdentifier(name) + "($1::jsonb)"
	var raw []byte
	if err := q.QueryRow(ctx, sql, payload).Scan(&raw); err != nil {
		return nil, err
	}
	return raw, nil
}

// EscapeIdentifier escapes a single identifier part, not a schema-qualified pair.
func EscapeIdentifier(name string) string {
	return pg.EscapeIdentifierPart(name)
}
