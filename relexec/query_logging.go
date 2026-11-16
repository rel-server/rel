package relexec

import (
	"context"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/rel-server/rel/logging"
	"github.com/rel-server/rel/query"
)

// loggingQuerier debug-logs every statement a write sends, a write item sending several.
type loggingQuerier struct {
	inner query.Querier
}

// Conn gives the write path the pgx connection under the wrapper, which it needs to create the temp tables.
func (q loggingQuerier) Conn() *pgx.Conn {
	if c, ok := q.inner.(interface{ Conn() *pgx.Conn }); ok {
		return c.Conn()
	}
	return nil
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
