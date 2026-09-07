// Denormalization : walks a JSON payload alongside the resolved query tree,
// producing one flat "_data" row per node instance (specs/query-engine.md
// ## Writing Algorithm step 2). The payload is expected to mirror the same
// shape reads produce for this tree — same selectFieldsFor (sql.go) that
// answers "what does this node's JSON shape look like" for compiling a
// SELECT answers it here too, for walking the payload's mirror shape.
package query

import (
	"encoding/json"
	"slices"

	"github.com/bytedance/sonic/ast"
	"github.com/samber/oops"
)

// dataRow is one row destined for "_data" ; Data is already-marshaled jsonb
// text (a flat, physical-column-keyed object — see extractRowData).
type dataRow struct {
	RowID    int
	NodeID   int
	ParentID *int
	Data     []byte
}

// denormalize parses payload against root, producing every writable node's
// flat "_data" rows ; startRowID continues __row_id numbering across calls.
func denormalize(root *QueryNode, ids map[*QueryNode]int, payload []byte, startRowID int) ([]dataRow, int, error) {
	parsed, perr := ast.NewParser(string(payload)).Parse()
	if perr != 0 {
		return nil, startRowID, oops.Wrapf(perr, "write: invalid payload JSON")
	}

	items, err := parsed.ArrayUseNode()
	if err != nil {
		return nil, startRowID, oops.Wrapf(err, "write: payload must be an array of root rows")
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

// walkNode appends raw's row to d.rows and recurses into every child embed
// found via selectFieldsFor. parentID is nil for the root.
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
				return oops.With("node", node.InnerName, "relation", f.key).Wrapf(err, "write: expected an array for incoming relation")
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

// isIncoming reports to-many (array payload) vs OutgoingNodes' to-one
// (object payload) ; isOutgoingOf (sql.go) is its counterpart.
func isIncoming(node, child *QueryNode) bool {
	return slices.Contains(node.IncomingNodes, child)
}

// extractRowData reshapes raw into a flat JSON object keyed by
// columnPathFlatName ; an absent column is omitted, not an error.
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
			return nil, oops.With("node", node.InnerName, "json_path", ex.JsonPath).Wrapf(err, "write: reading field")
		}
		flat[columnPathFlatName(ex.Path)] = json.RawMessage(text)
	}
	return json.Marshal(flat)
}
