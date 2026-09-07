package route

// Shared opening sequence for every entrypoint that runs DB work inside one
// role-switched transaction (authentication.md's Lifecycle step 5, Apply
// role) — route/handler.go, route/middleware.go, and route/upload_handler.go
// (twice, once per stream_upload call) all need the same acquire/begin/set-
// claims/resolve-or-reuse-role/set-role sequence before dispatching into
// their own per-route logic.

import (
	"context"
	"net/http"

	"github.com/jackc/pgx/v5"

	"github.com/rel-server/rel/config"
	"github.com/rel-server/rel/dbauth"
	"github.com/rel-server/rel/errcode"
	jwtpkg "github.com/rel-server/rel/jwt"
	"github.com/rel-server/rel/pg"
)

// acquireTxAndSetClaims acquires a connection, begins a transaction on it,
// and exposes claims via dbauth.SetLocalClaims — the common opening every
// role-switched transaction needs before deciding/setting a role. On
// failure, the appropriate error response is already written and any
// partially-acquired resource released/rolled back ; ok is false and
// tx/release are not meaningful.
func acquireTxAndSetClaims(ctx context.Context, w http.ResponseWriter, db *pg.DbInfos, claims jwtpkg.Claims) (tx pgx.Tx, release func(), ok bool) {
	conn, err := db.Pool.Acquire(ctx)
	if err != nil {
		writeServerError(ctx, w, http.StatusInternalServerError, errcode.DBUnavailable, "acquiring connection", err)
		return nil, nil, false
	}
	tx, err = conn.Begin(ctx)
	if err != nil {
		conn.Release()
		writeServerError(ctx, w, http.StatusInternalServerError, errcode.TransactionError, "starting transaction", err)
		return nil, nil, false
	}
	if err := dbauth.SetLocalClaims(ctx, tx, claims); err != nil {
		_ = tx.Rollback(ctx)
		conn.Release()
		writeServerError(ctx, w, http.StatusInternalServerError, errcode.Internal, "setting jwt claims", err)
		return nil, nil, false
	}
	return tx, conn.Release, true
}

// beginRoleScopedTx is acquireTxAndSetClaims plus resolving and setting the
// request's role (Lifecycle step 5's role-switch half) — the full sequence
// route/handler.go and route/middleware.go each need once, and
// upload_handler.go needs for its first (metadata-only) stream_upload call.
// On failure, the appropriate error response is already written and tx/the
// connection are rolled back/released ; ok is false and tx/release/role are
// not meaningful.
func beginRoleScopedTx(ctx context.Context, w http.ResponseWriter, db *pg.DbInfos, cfg *config.Config, claims jwtpkg.Claims, verified bool) (tx pgx.Tx, release func(), role string, ok bool) {
	tx, release, ok = acquireTxAndSetClaims(ctx, w, db, claims)
	if !ok {
		return nil, nil, "", false
	}
	role = jwtpkg.ResolveRole(cfg.Pg.Query.AnonymousRole, claims, verified)
	if role == "" {
		_ = tx.Rollback(ctx)
		release()
		writePlainError(w, http.StatusInternalServerError, errcode.NoRoleConfigured, dbauth.NoRoleConfiguredMessage)
		return nil, nil, "", false
	}
	if err := dbauth.SetLocalRole(ctx, tx, role); err != nil {
		_ = tx.Rollback(ctx)
		release()
		writeErrorForPgErr(ctx, w, err, cfg.Dev)
		return nil, nil, "", false
	}
	return tx, release, role, true
}

// beginTxWithRole is acquireTxAndSetClaims plus setting an ALREADY-resolved
// role — upload_handler.go's second stream_upload call, which reuses the
// role its first call resolved rather than re-running ResolveRole. On
// failure, the appropriate error response is already written and tx/the
// connection are rolled back/released.
func beginTxWithRole(ctx context.Context, w http.ResponseWriter, db *pg.DbInfos, cfg *config.Config, claims jwtpkg.Claims, role string) (tx pgx.Tx, release func(), ok bool) {
	tx, release, ok = acquireTxAndSetClaims(ctx, w, db, claims)
	if !ok {
		return nil, nil, false
	}
	if err := dbauth.SetLocalRole(ctx, tx, role); err != nil {
		_ = tx.Rollback(ctx)
		release()
		writeErrorForPgErr(ctx, w, err, cfg.Dev)
		return nil, nil, false
	}
	return tx, release, true
}
