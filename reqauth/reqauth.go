// Package reqauth is how a request becomes a user with a role, and how that user's database work
// starts (authentication.md's Lifecycle): verify the session, refuse an anonymous user when there is
// no anonymous role, then open one transaction that carries the session's claims and runs as its role.
//
// Every entry point that runs SQL on behalf of a user goes through it: /rel, /route, both calls of
// an upload route, the middleware gate, and rel() in templates. Each keeps its own response format,
// so a failure comes back as an *Error that the caller renders.
//
// One rule covers the claims: SQL (the rel.jwt.claims setting) and request.jwt always see the
// claims exactly as the user's request carried them. Renewing the session only changes the cookie
// sent back, and its timing is the caller's (Renew), because a request that a middleware rejects
// must not receive a fresh cookie.
//
// The SSO callback is deliberately outside this package: it runs before the user has a session,
// as the connection's own role (the callback function is "security definer"), and only needs
// jwt.VerifyRequest.
package reqauth

import (
	"context"
	"errors"
	"net/http"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/rel-server/rel/config"
	"github.com/rel-server/rel/dbauth"
	"github.com/rel-server/rel/errcode"
	jwtpkg "github.com/rel-server/rel/jwt"
	"github.com/rel-server/rel/pg"
	"github.com/rel-server/rel/relexec"
)

// Session is a user as one request presents them. Claims is nil and Verified false for an anonymous
// user.
type Session struct {
	Claims   jwtpkg.Claims
	Verified bool
	// Role is the Postgres role the transaction runs as. It is empty when the user is anonymous and
	// query.anonymous_role is unset, or the claims carry no role: Begin then refuses.
	Role string

	jwt config.Jwt
}

// NewSession builds the session of already verified claims, with no anonymous-role check: for a
// caller that has no request of its own (rel() in a template).
func NewSession(cfg *config.Config, claims jwtpkg.Claims, verified bool) *Session {
	return &Session{
		Claims:   claims,
		Verified: verified,
		Role:     jwtpkg.ResolveRole(cfg.Pg.Query.AnonymousRole, claims, verified),
		jwt:      cfg.Jwt,
	}
}

// Authenticate verifies r's session cookie. An anonymous user is refused (401, AnonymousDisabled)
// when the database has no anonymous role. The returned Session is never nil, so a caller can log
// who the user was even when the request is refused.
func Authenticate(cfg *config.Config, db *pg.DbInfos, r *http.Request) (*Session, error) {
	claims, verified := jwtpkg.VerifyRequest(cfg.Jwt, r)
	s := NewSession(cfg, claims, verified)
	if !verified && !db.AnonymousRoleExists {
		return s, newError(http.StatusUnauthorized, errcode.AnonymousDisabled, errcode.AnonymousDisabledMessage, nil)
	}
	return s, nil
}

// Renew writes the renewed session cookie when the session is due for one (Lifecycle step 4). It
// changes nothing the SQL or request.jwt sees. An anonymous session is left alone.
func (s *Session) Renew(w http.ResponseWriter) {
	if s.Verified {
		jwtpkg.RenewIfDue(s.jwt, w, s.Claims)
	}
}

// Begin starts a transaction on conn, which is not in one yet, that exposes the claims to SQL and
// runs as the session's role, in one round trip. It is the Begin that relexec.Execute takes, once
// converted by RelexecBegin. On failure the transaction may be open: the caller rolls back.
func (s *Session) Begin(ctx context.Context, conn dbauth.BatchSender) error {
	if s.Role == "" {
		return newError(http.StatusInternalServerError, errcode.NoRoleConfigured, dbauth.NoRoleConfiguredMessage, nil)
	}
	err := dbauth.BeginAs(ctx, conn, s.Claims, s.Role)
	if err == nil {
		return nil
	}
	step, _ := errors.AsType[*dbauth.StepError](err)
	switch {
	case step == nil || step.Step == "begin":
		return newError(http.StatusInternalServerError, errcode.TransactionError, "starting transaction", err)
	case step.Step == "claims":
		return newError(http.StatusInternalServerError, errcode.Internal, "setting jwt claims", err)
	}
	// The role step is a Postgres error (a role that does not exist, one the login role may not
	// become): the caller's renderer classifies it.
	e := newError(http.StatusInternalServerError, errcode.Internal, "applying role", step.Err)
	e.Postgres = true
	return e
}

// RelexecBegin is Begin as a relexec.Begin, which wants its failures as a *relexec.Error.
func (s *Session) RelexecBegin() relexec.Begin {
	return func(ctx context.Context, conn *pgxpool.Conn) error {
		err := s.Begin(ctx, conn)
		if e, ok := errors.AsType[*Error](err); ok {
			re := relexec.NewError(relexec.ServerFault, e.Code, e.Err)
			re.Postgres = e.Postgres
			return re
		}
		return err
	}
}

// Open takes a connection from pool and begins the session's transaction on it. The caller ends it
// with Commit or Rollback and must call Release.
func (s *Session) Open(ctx context.Context, pool *pgxpool.Pool) (*Tx, error) {
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return nil, newError(http.StatusInternalServerError, errcode.DBUnavailable, "acquiring connection", err)
	}
	if err := s.Begin(ctx, conn); err != nil {
		_, _ = conn.Exec(ctx, "rollback")
		conn.Release()
		return nil, err
	}
	return &Tx{conn: conn}, nil
}

// Tx is a user's transaction on one connection. Rollback after Commit does nothing, so a deferred
// Rollback is a safe backstop.
type Tx struct {
	conn  *pgxpool.Conn
	ended bool
}

func (t *Tx) Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error) {
	return t.conn.Query(ctx, sql, args...)
}

func (t *Tx) QueryRow(ctx context.Context, sql string, args ...any) pgx.Row {
	return t.conn.QueryRow(ctx, sql, args...)
}

func (t *Tx) Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	return t.conn.Exec(ctx, sql, args...)
}

func (t *Tx) CopyFrom(ctx context.Context, name pgx.Identifier, cols []string, src pgx.CopyFromSource) (int64, error) {
	return t.conn.CopyFrom(ctx, name, cols, src)
}

// Conn is the underlying connection, for code that reads its settings (the session time zone).
func (t *Tx) Conn() *pgx.Conn { return t.conn.Conn() }

// Commit ends the transaction. A failed commit (a deferred constraint, say) has rolled it back.
func (t *Tx) Commit(ctx context.Context) error {
	if t.ended {
		return pgx.ErrTxClosed
	}
	t.ended = true
	_, err := t.conn.Exec(ctx, "commit")
	return err
}

// Rollback ends the transaction unless Commit already has.
func (t *Tx) Rollback(ctx context.Context) error {
	if t.ended {
		return nil
	}
	t.ended = true
	_, err := t.conn.Exec(ctx, "rollback")
	return err
}

// Release gives the connection back to the pool. A connection still inside a transaction is
// discarded by the pool instead of reused.
func (t *Tx) Release() { t.conn.Release() }

// Error is why a user's request could not start. Each caller renders it in its own format.
type Error struct {
	Status  int // the HTTP status it deserves
	Code    errcode.Code
	Message string // what a client may be told
	Err     error  // the cause, for logs
	// Postgres is true when Err is a Postgres error that the caller should classify (pgerr.Classify)
	// rather than report as Message.
	Postgres bool
}

func newError(status int, code errcode.Code, message string, cause error) *Error {
	if cause == nil {
		cause = errors.New(message)
	}
	return &Error{Status: status, Code: code, Message: message, Err: cause}
}

func (e *Error) Error() string {
	if e.Err.Error() == e.Message {
		return e.Message
	}
	return e.Message + ": " + e.Err.Error()
}
func (e *Error) Unwrap() error { return e.Err }
