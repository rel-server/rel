// Package relexec runs a /rel request: the items of one query document, resolved against the
// database, executed in one transaction and written out as the response body (JSON or CBOR).
//
// A request goes through two steps. Prepare resolves and compiles every item without touching the
// database, so a bad query costs no connection. Prepared.Execute then runs the items on a
// connection and writes the body.
//
// The package knows nothing about HTTP, JWT or roles: the caller owns those and plugs its
// transaction start in as a Begin function.
package relexec

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/samber/oops"

	"github.com/rel-server/rel/cborenc"
	"github.com/rel-server/rel/errcode"
	"github.com/rel-server/rel/pg"
	"github.com/rel-server/rel/query"
	"github.com/rel-server/rel/wellknown"
	"github.com/rel-server/rel/writer"
)

// Policy is what this server lets a ComplexQuery ask for. A flag that is not granted degrades
// silently (an empty value in the envelope), except Rollback, which is refused.
type Policy struct {
	Count, Stats, QueryPlan, SQL, Rollback bool
}

// Options is how a request is answered.
type Options struct {
	// Format is the response representation, from cborenc.Negotiate. The zero value is JSON.
	Format cborenc.Format
	Policy Policy
	// Buffer is how many bytes of the response Execute holds back before writing any, so that a
	// failure while a response that small is produced, or while it is committed, is answered with
	// an error instead of a cut-short body. 0 holds nothing back.
	Buffer int
}

// Fault says whose mistake an Error is: the request's (an HTTP caller maps it to a 4xx) or the
// server's.
type Fault int

const (
	ClientFault Fault = iota
	ServerFault
)

// Error is what Prepare and Execute return on failure, always as an *Error.
type Error struct {
	// Item is the index of the failing item, -1 when the failure is not about one item.
	Item  int
	Fault Fault
	Code  errcode.Code
	// Err is the cause; its message already starts with "item N: " when Item is set.
	Err error
	// Postgres is true when Err comes from a statement Postgres ran, so the caller may classify
	// it further (constraint violation, permission...). It is never shown to a client as is.
	Postgres bool
	// Started is true when response bytes were already written: the response can no longer be
	// replaced by an error, only cut short.
	Started bool
}

func (e *Error) Error() string { return e.Err.Error() }
func (e *Error) Unwrap() error { return e.Err }

// NewError makes an Error that is not about one item, for a Begin function to return.
func NewError(fault Fault, code errcode.Code, err error) *Error {
	return &Error{Item: -1, Fault: fault, Code: code, Err: err}
}

// itemError wraps err as item i's, with the "item N: " prefix.
func itemError(i int, fault Fault, code errcode.Code, err error) *Error {
	return &Error{Item: i, Fault: fault, Code: code, Err: fmt.Errorf("item %d: %w", i, err)}
}

// codeOrUnclassified reads back the query package's own oc.Code(...) tag
// off err (or a wrapped ancestor), falling back to errcode.Unclassified.
func codeOrUnclassified(err error) errcode.Code {
	oe, ok := oops.AsOops(err)
	if !ok {
		return errcode.Unclassified
	}
	if code, ok := oe.Code().(errcode.Code); ok {
		return code
	}
	return errcode.Unclassified
}

// resolvedItem is one request item, past resolution: no further chance of a "the query itself is
// wrong" error past this.
type resolvedItem struct {
	root    *query.QueryNode
	isWrite bool
	data    query.Node

	// Non-nil only for a well-known item ; threaded into ResolveArgs so a
	// $param slot resolves without a separate code path for ordinary items.
	paramValues map[string]any

	// Non-nil only for a well-known READ item, reusing the statement
	// compiled once at load time (well-known-queries.md ## Behaviour).
	precompiledRead *writer.SQLWriter
	// precompiledReadCBOR is the same statement for a CBOR response.
	precompiledReadCBOR *writer.SQLWriter

	// enc is non-nil when the response is CBOR ; it turns the binary rows into CBOR.
	enc *query.ResultEncoder

	// returns/count/stats/queryPlan/sql/rollback are ComplexQuery's own
	// response-shaping flags (specs/complex-query.md) — zero values (no
	// flag set) for a bare RelationQuery/WellKnownQuery item, since a bare
	// "wellknown" object never takes them directly.
	returns   string
	count     bool
	stats     bool
	queryPlan bool
	sql       bool
	rollback  bool
}

// Prepared is a request whose items are resolved and compiled, ready to Execute. It is not
// reusable: Execute runs once.
type Prepared struct {
	db       *pg.DbInfos
	items    []resolvedItem
	format   query.OutputFormat
	wire     cborenc.Format
	policy   Policy
	buffer   int
	anyWrite bool
	targets  []string
}

// AnyWrite reports whether at least one item writes.
func (p *Prepared) AnyWrite() bool { return p.anyWrite }

// Targets returns the schema-qualified relation or function each item reads or writes, in item
// order, for logging.
func (p *Prepared) Targets() []string { return p.targets }

// Prepare resolves every item against the database metadata in rctx, with wk answering the
// well-known ones. Nothing is sent to the database. It returns an *Error, whose Fault says
// whether the query itself is wrong.
func Prepare(rctx *query.ResolveContext, wk *wellknown.Registry, items []query.ParsedQuery, opts Options) (*Prepared, error) {
	p := &Prepared{db: rctx.Db, format: query.FormatJSON, wire: opts.Format, policy: opts.Policy, buffer: opts.Buffer}
	if opts.Format != cborenc.JSON {
		p.format = query.FormatCBOR
	}

	p.items = make([]resolvedItem, 0, len(items))
	for i, item := range items {
		ri, err := prepareItem(rctx, wk, item, opts.Policy)
		if err != nil {
			return nil, itemError(i, ClientFault, codeOrUnclassified(err), err)
		}
		p.items = append(p.items, ri)
	}

	if p.format == query.FormatCBOR {
		// one table for the whole response : a structure id means one list of names in every item
		var structures *query.StructureTable
		if p.wire == cborenc.CBORStructures {
			structures = query.NewStructureTable()
		}
		for i := range p.items {
			enc, err := query.NewResultEncoder(rctx.Db, p.items[i].root, structures)
			if err != nil {
				return nil, itemError(i, ServerFault, errcode.Internal, err)
			}
			p.items[i].enc = enc
		}
	}

	p.targets = make([]string, len(p.items))
	for i, ri := range p.items {
		p.anyWrite = p.anyWrite || ri.isWrite
		p.targets[i] = targetIdentifier(ri.root)
	}
	return p, nil
}

// prepareItem resolves one item. Its errors carry the errcode of the query package where there is one.
func prepareItem(rctx *query.ResolveContext, wk *wellknown.Registry, item query.ParsedQuery, policy Policy) (resolvedItem, error) {
	var ri resolvedItem
	switch {
	case item.WellKnown != nil:
		// Bare well-known read, already resolved at load time : just a name lookup here.
		return resolveWellKnownItem(wk, item.WellKnown.WellKnown, item.WellKnown.Params, nil)

	case item.Write != nil && item.Write.WellKnown != nil:
		w := item.Write.WellKnown
		var err error
		if ri, err = resolveWellKnownItem(wk, w.WellKnown, w.Params, item.Write.Data); err != nil {
			return ri, err
		}

	case item.Write != nil || item.Relation != nil:
		raw := item.Relation
		if item.Write != nil {
			raw = item.Write.Query
		}
		root, err := rctx.Resolve(raw)
		if err != nil {
			return ri, err
		}
		ri.root = root
		if item.Write != nil {
			// specs/complex-query.md ## Parsing : "data"'s presence, not item.Write's, decides
			// whether this is a write — a ComplexQuery with no "data" is a valid read.
			// write_mode "delete" carries no data, so the root decides there.
			ri.data = item.Write.Data
			ri.isWrite = item.Write.Data != nil || root.WriteMode == query.DELETE
		}

	default:
		return ri, oops.Code(errcode.QueryMalformedJSON).Errorf("empty query")
	}

	if w := item.Write; w != nil {
		ri.returns, ri.count, ri.stats, ri.queryPlan, ri.sql, ri.rollback = w.Returns, w.Count, w.Stats, w.QueryPlan, w.Sql, w.Rollback
	}
	if err := validateComplexFlags(&ri); err != nil {
		return ri, err
	}
	return ri, checkRollbackGrant(&ri, policy)
}

// resolveWellKnownItem resolves name via wk (well-known-queries.md ## Execution Errors for the
// codes) ; data non-nil means a write.
func resolveWellKnownItem(wk *wellknown.Registry, name string, paramsRaw []byte, data query.Node) (resolvedItem, error) {
	def, ok := wk.Lookup(name)
	if !ok {
		return resolvedItem{}, oops.Code(errcode.WellKnownUnknownQuery).Errorf("well-known query %q is not registered", name)
	}
	paramValues, err := def.ResolveParams(paramsRaw)
	if err != nil {
		return resolvedItem{}, err
	}
	if data != nil {
		return resolvedItem{root: def.Root, isWrite: true, data: data, paramValues: paramValues}, nil
	}
	return resolvedItem{root: def.Root, isWrite: false, paramValues: paramValues, precompiledRead: def.Read, precompiledReadCBOR: def.ReadCBOR}, nil
}

// targetIdentifier is the schema-qualified function name for a function-root item, otherwise the
// schema-qualified relation name.
func targetIdentifier(root *query.QueryNode) string {
	if root.Function != nil {
		return root.Function.Identifier.String()
	}
	if root.Relation != nil {
		return root.Relation.Identifier.String()
	}
	return ""
}

// Begin starts the transaction the items run in, on conn, which is not in a transaction yet.
// The caller decides how: which role to switch to, which claims to expose to SQL. It must leave
// conn inside the transaction when it returns nil. It may return an *Error to choose the code of
// its failure; any other error is a server fault. Execute rolls back on failure, so Begin never
// has to.
type Begin func(ctx context.Context, conn *pgxpool.Conn) error

// Execute runs the items on conn, in one transaction started by begin, and writes the response
// body to w: the item's result, or a JSON array/CBOR array of them for several items. conn
// belongs to the caller, who must release it.
//
// The first Options.Buffer bytes of the body are held back until the transaction is committed, so
// a response that fits is delivered whole or not at all. A longer one starts streaming once the
// buffer is full and is committed after its last byte.
//
// Execute returns an *Error. When Error.Started is false nothing was written to w and the caller
// can still answer with an error; otherwise the body is cut short. The transaction is rolled back
// on every error. A Prepared is executed once.
func (p *Prepared) Execute(ctx context.Context, conn *pgxpool.Conn, begin Begin, w io.Writer) (err error) {
	cw := newHoldWriter(w, p.buffer)
	defer cw.Close()
	begun := false
	defer func() {
		if err == nil {
			return
		}
		if begun {
			_, _ = conn.Exec(ctx, "rollback")
		}
		var e *Error
		if !errors.As(err, &e) {
			e = NewError(ServerFault, errcode.Internal, err)
			err = e
		}
		e.Started = cw.released
	}()

	// A timestamp is answered as an instant in the connection's time zone : a zone rel cannot load is
	// refused here, before anything is sent.
	for i := range p.items {
		if p.items[i].enc == nil {
			continue
		}
		if err := p.items[i].enc.BindSession(conn); err != nil {
			return itemError(i, ServerFault, errcode.Internal, err)
		}
	}

	// The write temp tables are per physical connection, and a switched role has no default
	// privileges on them : they are created and granted once per connection and table, here,
	// because that cannot happen inside the transaction begun next. Their rows disappear at commit
	// or rollback, so nothing ever needs clearing.
	var writeRoots []*query.QueryNode
	for _, item := range p.items {
		if item.isWrite {
			writeRoots = append(writeRoots, item.root)
		}
	}
	if len(writeRoots) > 0 {
		if err := query.PrepareWrite(ctx, loggingQuerier{inner: conn}, writeRoots...); err != nil {
			return NewError(ServerFault, errcode.TransactionError, fmt.Errorf("preparing the write tables: %w", err))
		}
	}

	begun = true
	if err := begin(ctx, conn); err != nil {
		if _, ok := errors.AsType[*Error](err); ok {
			return err
		}
		return NewError(ServerFault, errcode.TransactionError, err)
	}

	writes, err := p.writePhase(ctx, conn)
	if err != nil {
		return err
	}

	// NO commit here : the write phase and every item's read-back stay in one transaction,
	// committed only at the very end.
	envelopes, statements, args, err := p.readPhase(ctx, conn, writes)
	if err != nil {
		return err
	}

	multi := len(p.items) > 1
	if multi {
		var head []byte
		if p.format == query.FormatCBOR {
			head = cborenc.AppendArrayHead(nil, len(p.items))
		} else {
			head = []byte("[")
		}
		if _, err := cw.Write(head); err != nil {
			return err
		}
	}
	for i := range p.items {
		if multi && i > 0 && p.format == query.FormatJSON {
			if _, err := cw.Write([]byte(",")); err != nil {
				return err
			}
		}
		if err := writeItemResponse(ctx, cw, conn, &p.items[i], statements[i], args[i], envelopes[i], writes.buffered[i]); err != nil {
			// A failing read rolls the whole transaction back too.
			e := itemError(i, ServerFault, errcode.Internal, err)
			e.Postgres = true
			return e
		}
	}

	// The single commit for the whole request. A body longer than the buffer is already on its way,
	// so a failure here cuts it short ; a shorter one is still held and is replaced by the error.
	if _, err := conn.Exec(ctx, "commit"); err != nil {
		return NewError(ServerFault, errcode.TransactionError, fmt.Errorf("commit: %w", err))
	}
	if multi && p.format == query.FormatJSON {
		if _, err := cw.Write([]byte("]")); err != nil {
			return err
		}
	}
	return cw.Flush()
}

// writeState is what the write phase leaves for the read phase.
type writeState struct {
	nodeIDs []int
	results []*query.WriteResult
	// buffered holds a rolled-back write's own read-back rows, captured while its SAVEPOINT is
	// still live : the response must still show what happened, but the rows (and this item's temp
	// rows) are gone the instant "rollback to savepoint" runs.
	buffered [][][]byte
}

// writePhase runs every write item in order, each in its own savepoint when it asks for rollback.
func (p *Prepared) writePhase(ctx context.Context, conn *pgxpool.Conn) (*writeState, error) {
	// One shared WriteState across every write item : __row_id numbering
	// must continue across items, or two writes in one Sequence collide.
	state := &query.WriteState{}
	ws := &writeState{
		nodeIDs:  make([]int, len(p.items)),
		results:  make([]*query.WriteResult, len(p.items)),
		buffered: make([][][]byte, len(p.items)),
	}
	for i, item := range p.items {
		if !item.isWrite {
			continue
		}
		spName := fmt.Sprintf("sp%d", i)
		if item.rollback {
			if _, err := conn.Exec(ctx, "savepoint "+spName); err != nil {
				return nil, itemError(i, ServerFault, errcode.TransactionError, fmt.Errorf("savepoint: %w", err))
			}
		}
		opts := query.WriteOptions{
			Stats:     item.stats && p.policy.Stats,
			QueryPlan: item.queryPlan && p.policy.QueryPlan,
			Sql:       item.sql && p.policy.SQL,
			Format:    p.format,
		}
		result, err := query.ExecuteWriteNode(ctx, loggingQuerier{inner: conn}, item.root, item.data, state, item.paramValues, opts)
		if err != nil {
			e := itemError(i, ClientFault, codeOrUnclassified(err), err)
			e.Postgres = true
			return nil, e
		}
		ws.nodeIDs[i] = result.NodeIDs[item.root]
		ws.results[i] = result
		if item.root.WriteMode == query.DELETE {
			ws.buffered[i] = [][]byte{} // the rows are gone : the response is the empty selection
		}
		if result.Rows != nil {
			ws.buffered[i] = result.Rows // the write built its own response (fast path)
		}
		if item.rollback {
			if item.returns != "none" && ws.buffered[i] == nil {
				rows, err := bufferReadback(ctx, conn, item.root, ws.nodeIDs[i], item.paramValues, p.format)
				if err != nil {
					return nil, itemError(i, ServerFault, errcode.Internal, fmt.Errorf("buffering read-back before rollback: %w", err))
				}
				ws.buffered[i] = rows
			}
			// Undoes only this item's own effects, immediately after its own execution — the rest
			// of the request's transaction is unaffected.
			if _, err := conn.Exec(ctx, "rollback to savepoint "+spName); err != nil {
				return nil, itemError(i, ServerFault, errcode.TransactionError, fmt.Errorf("rollback to savepoint: %w", err))
			}
		}
	}
	return ws, nil
}

// readPhase compiles every item's response statement, runs "count", and collects sql/query_plan,
// all before any response byte is written : once streaming starts there's no clean error left.
func (p *Prepared) readPhase(ctx context.Context, conn *pgxpool.Conn, ws *writeState) (envelopes []envelopeData, statements []*writer.SQLWriter, args [][]any, err error) {
	statements = make([]*writer.SQLWriter, len(p.items))
	args = make([][]any, len(p.items))
	envelopes = make([]envelopeData, len(p.items))
	for i, item := range p.items {
		// returns=="none" on a write skips compiling/running the read-back
		// SELECT entirely (specs/complex-query.md ## Response shape).
		skipReadback := item.isWrite && (item.returns == "none" || item.root.WriteMode == query.DELETE ||
			(ws.results[i] != nil && ws.results[i].Rows != nil))

		if !skipReadback {
			var sw *writer.SQLWriter
			var cerr error
			switch {
			case item.isWrite:
				sw, cerr = query.CompileSelectForDataNodeFormat(item.root, ws.nodeIDs[i], p.format)
			case item.precompiledRead != nil:
				sw = item.precompiledRead
				if p.format == query.FormatCBOR {
					sw = item.precompiledReadCBOR
				}
			default:
				sw, cerr = query.CompileSelectFormat(item.root, p.format)
			}
			if cerr != nil {
				return nil, nil, nil, itemError(i, ServerFault, errcode.Internal, fmt.Errorf("compiling response: %w", cerr))
			}
			statements[i] = sw
			// ResolveArgs, not Args() : degrades to Args() when paramValues is
			// nil, but also resolves a well-known item's named $param slots.
			a, aerr := sw.ResolveArgs(item.paramValues)
			if aerr != nil {
				return nil, nil, nil, itemError(i, ServerFault, errcode.Internal, fmt.Errorf("resolving params: %w", aerr))
			}
			args[i] = a
		}

		if item.count && p.policy.Count {
			n, cerr := runCount(ctx, conn, item.root, item.paramValues)
			if cerr != nil {
				return nil, nil, nil, itemError(i, ServerFault, errcode.Internal, fmt.Errorf("count: %w", cerr))
			}
			envelopes[i].count = &n
			envelopes[i].offset = item.root.Offset
			envelopes[i].limit = item.root.Limit
		}

		if item.stats {
			envelopes[i].stats = []query.Stat{}
			if p.policy.Stats && ws.results[i] != nil {
				envelopes[i].stats = ws.results[i].Stats
			}
		}
		if item.sql {
			envelopes[i].sql = []query.SqlResult{}
			if p.policy.SQL {
				if ws.results[i] != nil {
					envelopes[i].sql = append(envelopes[i].sql, ws.results[i].Sql...)
				}
				if !skipReadback {
					envelopes[i].sql = append(envelopes[i].sql, query.SqlResult{Path: emptyPath, Select: statements[i].String()})
				}
			}
		}
		if item.queryPlan {
			envelopes[i].queryPlan = []query.PlanResult{}
			if p.policy.QueryPlan {
				if ws.results[i] != nil {
					envelopes[i].queryPlan = append(envelopes[i].queryPlan, ws.results[i].QueryPlan...)
				}
				if !skipReadback {
					plan, perr := explainFormatJSON(ctx, conn, statements[i].String(), args[i])
					if perr != nil {
						return nil, nil, nil, itemError(i, ServerFault, errcode.Internal, fmt.Errorf("explain: %w", perr))
					}
					envelopes[i].queryPlan = append(envelopes[i].queryPlan, query.PlanResult{Path: emptyPath, Select: plan})
				}
			}
		}
	}
	return envelopes, statements, args, nil
}
