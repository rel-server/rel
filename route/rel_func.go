// This file implements specs/templating-2.md ## query functions : rel(), a
// read-only query entrypoint bound into the shared jet.Set (see templates.go).
// It runs /rel's own resolve-and-run path (relexec) in a per-call, role-scoped
// transaction, after refusing every write shape up front.
package route

import (
	"bytes"
	"context"
	"errors"

	"github.com/bytedance/sonic"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/samber/oops"

	"github.com/rel-server/rel/config"
	"github.com/rel-server/rel/dbauth"
	"github.com/rel-server/rel/errcode"
	jwtpkg "github.com/rel-server/rel/jwt"
	"github.com/rel-server/rel/pg"
	"github.com/rel-server/rel/query"
	"github.com/rel-server/rel/relexec"
	"github.com/rel-server/rel/wellknown"
)

// executeRelRead runs one rel() call : parseQuery, reject any write shape, then the same
// resolve-and-run path /rel uses (relexec) in its own short-lived role-scoped transaction, and
// return the results as JSON — a single item's result, or an array of results for a Sequence.
func executeRelRead(ctx context.Context, db *pg.DbInfos, cfg *config.Config, wkReg *wellknown.Registry, claims jwtpkg.Claims, verified bool, queryJSON []byte) ([]byte, error) {
	pq, err := query.ParseQuery(queryJSON)
	if err != nil {
		return nil, oops.Code(errcode.QueryMalformedJSON).Wrapf(err, "rel(): invalid query JSON")
	}

	items := pq.Sequence
	if items == nil {
		items = []query.ParsedQuery{pq}
	}
	for i, item := range items {
		if err := rejectRelWrite(item); err != nil {
			return nil, oops.With("item", i).Wrap(err)
		}
	}

	prepared, err := relexec.Prepare(&query.ResolveContext{Db: db, Config: cfg}, wkReg, items, relexec.Options{})
	if err != nil {
		return nil, relFuncError(err)
	}

	role := jwtpkg.ResolveRole(cfg.Pg.Query.AnonymousRole, claims, verified)
	if role == "" {
		return nil, oops.Code(errcode.NoRoleConfigured).Errorf("rel(): %s", dbauth.NoRoleConfiguredMessage)
	}
	conn, err := db.Pool.Acquire(ctx)
	if err != nil {
		return nil, oops.Code(errcode.DBUnavailable).Wrapf(err, "rel(): acquiring connection")
	}
	defer conn.Release()

	var out bytes.Buffer
	begin := func(ctx context.Context, conn *pgxpool.Conn) error { return dbauth.BeginAs(ctx, conn, claims, role) }
	if err := prepared.Execute(ctx, conn, begin, &out); err != nil {
		return nil, relFuncError(err)
	}
	return out.Bytes(), nil
}

// relFuncError is a relexec failure as the error rel() reports to its template.
func relFuncError(err error) error {
	if e, ok := errors.AsType[*relexec.Error](err); ok {
		return oops.Code(e.Code).Wrapf(e.Err, "rel()")
	}
	return oops.Wrapf(err, "rel()")
}

// rejectRelWrite enforces specs/templating-2.md ## query functions' "rel()
// only executes reads" rule — a ComplexQuery/write wrapper (item.Write) is
// out of scope regardless of whether its own Data is set, mirroring
// relexec's own isWrite derivation (checked at the parse level, since
// QueryNode.WriteMode defaults to a write mode even for a plain read once
// resolved, and so isn't a reliable signal here).
func rejectRelWrite(item query.ParsedQuery) error {
	if item.Write != nil {
		return oops.Code(errcode.RelFuncWriteNotAllowed).Errorf("rel(): write queries are not allowed from templates")
	}
	return nil
}

// jsonRawToAny decodes raw into a plain any (nil for empty/JSON null),
// matching jet's own dot-access expectations — the same shape
// templateDataValue produces for Data.
func jsonRawToAny(raw []byte) any {
	if len(raw) == 0 {
		return nil
	}
	var v any
	_ = sonic.Unmarshal(raw, &v)
	return v
}
