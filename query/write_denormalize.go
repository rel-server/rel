// Denormalization : walks a JSON payload alongside the resolved query tree,
// producing one flat "_data" row per node instance (specs/query-engine.md
// ## Writing Algorithm step 2). The payload is expected to mirror the same
// shape reads produce for this tree — same selectFieldsFor (sql.go) that
// answers "what does this node's JSON shape look like" for compiling a
// SELECT answers it here too, for walking the payload's mirror shape.
package query

import (
	"encoding/json"
	"fmt"
	"slices"

	"github.com/bytedance/sonic/ast"
)

// dataRow is one row destined for "_data" ; Data is already-marshaled jsonb
// text (a flat, physical-column-keyed object — see extractRowData).
type dataRow struct {
	RowID    int
	NodeID   int
	ParentID *int
	Data     []byte
}

// denormalize parses payload and walks it against root, producing every
// writable node's flat "_data" rows. ids is assignNodeIDs' output — a node
// absent from ids (READONLY, or nested under one) contributes no rows and
// its own payload subtree is skipped entirely, even if present.
//
// The root itself is always an array of row objects, one per root-level row
// being written : unlike a nested embed, whose cardinality (one object vs.
// an array) comes from its outgoing/incoming classification against its
// parent, the root has no parent to derive that from — a write request is
// inherently "here are N rows to write", plural.
//
// startRowID is assignNodeIDs' startAt counterpart for __row_id (the
// primary key) — a caller running several ExecuteWrite calls against the
// same "_data" table must continue numbering rows where the previous call
// left off, or two items' rows collide on __row_id. Returns the next free
// row id alongside the produced rows.
func denormalize(root *QueryNode, ids map[*QueryNode]int, payload []byte, startRowID int) ([]dataRow, int, error) {
	parsed, perr := ast.NewParser(string(payload)).Parse()
	if perr != 0 {
		return nil, startRowID, fmt.Errorf("write: invalid payload JSON: %w", perr)
	}

	items, err := parsed.ArrayUseNode()
	if err != nil {
		return nil, startRowID, fmt.Errorf("write: payload must be an array of root rows: %w", err)
	}

	d := &denormalizer{ids: ids, nextID: startRowID}
	for i := range items {
		if err := d.walkNode(root, &items[i], nil); err != nil {
			return nil, startRowID, err
		}
	}
	return d.rows, d.nextID, nil
}

type denormalizer struct {
	ids    map[*QueryNode]int
	nextID int
	rows   []dataRow
}

// walkNode processes one row of node (raw is that row's own JSON object),
// appending it to d.rows and recursing into every child embed found via
// selectFieldsFor. parentID is the just-appended row's own __parent_id
// (nil for the root).
func (d *denormalizer) walkNode(node *QueryNode, raw *ast.Node, parentID *int) error {
	nodeID, ok := d.ids[node]
	if !ok {
		return nil // READONLY (or nested under one) : not part of the write at all
	}

	rowID := d.nextID
	d.nextID++

	data, err := extractRowData(node, raw)
	if err != nil {
		return err
	}
	d.rows = append(d.rows, dataRow{RowID: rowID, NodeID: nodeID, ParentID: parentID, Data: data})

	fields, err := selectFieldsFor(node)
	if err != nil {
		return err
	}
	myID := rowID
	for _, f := range fields {
		child := embedChildOf(f)
		if child == nil {
			continue
		}
		value := raw.Get(f.key)
		if !value.Exists() || value.TypeSafe() == ast.V_NULL {
			continue
		}
		if isIncoming(node, child) {
			items, err := value.ArrayUseNode()
			if err != nil {
				return fmt.Errorf("write: %q: expected an array for incoming relation %q: %w", node.InnerName, f.key, err)
			}
			for i := range items {
				if err := d.walkNode(child, &items[i], &myID); err != nil {
					return err
				}
			}
		} else {
			if err := d.walkNode(child, value, &myID); err != nil {
				return err
			}
		}
	}
	return nil
}

// isIncoming reports whether child is one of node's IncomingNodes (to-many,
// payload shaped as an array) as opposed to OutgoingNodes (to-one, payload
// shaped as a single object).
func isIncoming(node, child *QueryNode) bool {
	return slices.Contains(node.IncomingNodes, child)
}

// extractRowData reshapes raw (one row, shaped per node.Select) into a flat
// JSON object, keyed by columnPathFlatName — physical-column-name-keyed for
// a plain column (unchanged from before composite writes existed), or the
// "__"-joined synthetic key for a composite sub-field (e.g. "home__city") —
// the "rehydrate the row" step ### Insertion/Updates assumes data already
// looks like. A column absent from the payload is simply omitted from the
// result (write_dml.go's default-value/composite-cast cases handle that),
// not an error. The extracted VALUE itself is always the leaf's own raw
// JSON scalar, regardless of Path length — a composite sub-field's value is
// never itself an object here (query.ts's own get/get-set/set granular
// selectors only ever target a plain column by name, never a path, so a
// composite sub-field is only ever reached via a bare "." chain, whose
// resolved value is the leaf field itself, per query-engine.md ##
// Writability).
func extractRowData(node *QueryNode, raw *ast.Node) ([]byte, error) {
	flat := make(map[string]json.RawMessage, len(node.Shape.Extractors))
	for _, ex := range node.Shape.Extractors {
		v := raw
		for _, key := range ex.JsonPath {
			v = v.Get(key)
		}
		if !v.Exists() {
			continue
		}
		text, err := v.Raw()
		if err != nil {
			return nil, fmt.Errorf("write: %q: reading %v: %w", node.InnerName, ex.JsonPath, err)
		}
		flat[columnPathFlatName(ex.Path)] = json.RawMessage(text)
	}
	return json.Marshal(flat)
}
