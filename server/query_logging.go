package server

import (
	"context"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/rel-server/rel/logging"
	"github.com/rel-server/rel/query"
)

// loggingQuerier wraps a query.Querier so specs/logging.md ## Access
// logging's per-item debug detail covers every SQL statement a write item
// actually sends — ExecuteWriteStateParamsOpts issues several internal
// Exec/Query calls per item, not just one, so wrapping the Querier it's
// given is the only place that sees all of them.
type loggingQuerier struct {
	inner query.Querier
}

func (q loggingQuerier) Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	logging.FromContext(ctx).Debug("executing write", "sql", sql, "args", args)
	return q.inner.Exec(ctx, sql, args...)
}

func (q loggingQuerier) Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error) {
	logging.FromContext(ctx).Debug("executing write", "sql", sql, "args", args)
	return q.inner.Query(ctx, sql, args...)
}

func (q loggingQuerier) CopyFrom(ctx context.Context, tableName pgx.Identifier, columnNames []string, rowSrc pgx.CopyFromSource) (int64, error) {
	logging.FromContext(ctx).Debug("executing write copy", "table", tableName.Sanitize(), "columns", columnNames)
	return q.inner.CopyFrom(ctx, tableName, columnNames, rowSrc)
}

// SendBatch makes loggingQuerier a query.Batcher when the wrapped connection is one ;
// a write's statements then travel in one round trip, each still logged.
func (q loggingQuerier) SendBatch(ctx context.Context, b *pgx.Batch) pgx.BatchResults {
	for _, item := range b.QueuedQueries {
		logging.FromContext(ctx).Debug("executing write", "sql", item.SQL, "args", item.Arguments)
	}
	return q.inner.(query.Batcher).SendBatch(ctx, b)
}
