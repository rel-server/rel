// Denormalization : walks a payload alongside the resolved query tree and loads one
// row per node instance into the typed temp table of that node's relation (specs/typed-write-loading.md
// ## General path). The payload is expected to mirror the same shape reads produce
// for this tree : the same selectFieldsFor (sql.go) that answers "what does this
// node's JSON shape look like" for compiling a SELECT answers it here too, for
// walking the payload's mirror shape.
package query

import (
	"slices"

	"github.com/bytedance/sonic/ast"
	"github.com/rel-server/rel/pg"
	"github.com/samber/oops"
)

// extractorPlan is one Extractor resolved against its node's temp table : where to find the value in
// the payload, which table column receives it, and which presence slot it sets.
type extractorPlan struct {
	jsonPath []string
	colIdx   int
	subPath  []string // sub-field names under the column, for a composite sub-field extractor
	word     int
	bit      uint
}

// nodePlan is the per-node state the walk reuses for every row of that node.
type nodePlan struct {
	table      *tempTable
	extractors []extractorPlan
	vals       []Node // scratch : one slot per table column
	mask       []int64
	synth      map[int]*synthObject // scratch : composites built from sub-field extractors
}

func newNodePlan(node *QueryNode, t *tempTable) (*nodePlan, error) {
	p := &nodePlan{table: t, vals: make([]Node, len(t.cols)), mask: make([]int64, t.maskWords)}
	for _, ex := range node.Shape.Extractors {
		cols := ex.Path.Path
		idx, ok := t.colIdx[cols[0]]
		if !ok {
			return nil, oops.With("node", node.InnerName, "column", cols[0].Name).Errorf("write: column %q is not a column of %s", cols[0].Name, node.Relation.Identifier.String())
		}
		slot, ok := node.Relation.MaskSlotFor(cols...)
		if !ok {
			return nil, oops.With("node", node.InnerName, "column", ex.Path.Key()).Errorf("write: no presence slot for column path %s", ex.Path.Key())
		}
		ep := extractorPlan{jsonPath: ex.JsonPath, colIdx: idx, word: slot.Word, bit: uint(slot.Bit)}
		for _, c := range cols[1:] {
			ep.subPath = append(ep.subPath, c.Name)
		}
		p.extractors = append(p.extractors, ep)
	}
	return p, nil
}

// fill reads raw's columns into the plan's scratch values and mask.
func (p *nodePlan) fill(raw Node) {
	clear(p.vals)
	clear(p.mask)
	clear(p.synth)
	for i := range p.extractors {
		ep := &p.extractors[i]
		v := raw
		found := true
		for _, key := range ep.jsonPath {
			var ok bool
			if v, ok = v.ObjectGet(key); !ok {
				found = false
				break
			}
		}
		if !found {
			continue
		}
		p.mask[ep.word] |= int64(1) << ep.bit
		if len(ep.subPath) == 0 {
			p.vals[ep.colIdx] = v
			continue
		}
		// a sub-field of a composite column : the column's value is built from every sub-field supplied
		if p.synth == nil {
			p.synth = map[int]*synthObject{}
		}
		root := p.synth[ep.colIdx]
		if root == nil {
			root = &synthObject{}
			p.synth[ep.colIdx] = root
			p.vals[ep.colIdx] = root
		}
		root.set(ep.subPath, v)
	}
}

// synthObject is an object Node assembled from sub-field values, for a composite column that a
// request supplies field by field. Only what the composite encoders read is implemented.
type synthObject struct {
	fields map[string]Node
}

func (s *synthObject) set(path []string, v Node) {
	cur := s
	for _, name := range path[:len(path)-1] {
		next, _ := cur.fields[name].(*synthObject)
		if next == nil {
			next = &synthObject{}
			cur.put(name, next)
		}
		cur = next
	}
	cur.put(path[len(path)-1], v)
}

func (s *synthObject) put(name string, v Node) {
	if s.fields == nil {
		s.fields = map[string]Node{}
	}
	s.fields[name] = v
}

func (s *synthObject) Kind() NodeKind                  { return KindObject }
func (s *synthObject) StrictString() (string, error)   { return "", errNotString }
func (s *synthObject) StrictBool() (bool, error)       { return false, errNotString }
func (s *synthObject) StrictInt64() (int64, error)     { return 0, errNotString }
func (s *synthObject) StrictFloat64() (float64, error) { return 0, errNotString }
func (s *synthObject) ArrayItems() ([]Node, error)     { return nil, errNotArray }
func (s *synthObject) Raw() (string, error) {
	return "", oops.Errorf("write: an object assembled from sub-fields has no source text")
}
func (s *synthObject) ObjectGet(key string) (Node, bool) {
	v, ok := s.fields[key]
	return v, ok
}
func (s *synthObject) ObjectEach(fn func(string, Node) error) error {
	for k, v := range s.fields {
		if err := fn(k, v); err != nil {
			return err
		}
	}
	return nil
}

const errNotString = nodeError("value is not a string")

// writeRows is what denormalize produces : the batches to load, and what the write needs to know about them.
type writeRows struct {
	batches   map[*tempTable]*rowBatch
	populated map[int]bool // node ids that received a row
	submitted map[int]int  // rows per node id
	// tableNodes is the node ids that received rows, per temp table.
	tableNodes map[*tempTable]map[int]bool
	nextRowID  int
	count      int
	// patchMask holds the presence words of the first row of the first batch built for an "update"
	// root : the columns its patch names.
	patchMask []int64
	rootRowID int32
}

// denormalize walks payload (JSON text) against root and builds the rows of every writable node ;
// startRowID continues row id numbering across calls. tables holds the temp table of each relation
// written. general false builds fast rows, without metadata.
func denormalize(root *QueryNode, ids map[*QueryNode]int, payload []byte, tables map[*pg.Relation]*tempTable, startRowID int, general bool, ec *encodeContext) (*writeRows, error) {
	parsed, perr := ast.NewParser(string(payload)).Parse()
	if perr != 0 {
		return nil, oops.Wrapf(perr, "write: invalid payload JSON")
	}
	return denormalizeNode(root, ids, sonicNode{&parsed}, tables, startRowID, general, ec)
}

func denormalizeNode(root *QueryNode, ids map[*QueryNode]int, payload Node, tables map[*pg.Relation]*tempTable, startRowID int, general bool, ec *encodeContext) (*writeRows, error) {
	if payload.Kind() != KindArray {
		return nil, oops.Errorf("write: payload must be an array of root rows")
	}
	items, err := payload.ArrayItems()
	if err != nil {
		return nil, oops.Wrapf(err, "write: payload must be an array of root rows")
	}
	d := &denormalizer{
		ids: ids, tables: tables, general: general, ec: ec,
		plans: map[*QueryNode]*nodePlan{},
		out: &writeRows{
			batches: map[*tempTable]*rowBatch{}, populated: map[int]bool{}, submitted: map[int]int{},
			tableNodes: map[*tempTable]map[int]bool{},
			nextRowID:  startRowID,
		},
	}
	for i := range items {
		if err := d.walkNode(root, items[i], nil); err != nil {
			return nil, err
		}
	}
	return d.out, nil
}

type denormalizer struct {
	ids     map[*QueryNode]int
	tables  map[*pg.Relation]*tempTable
	general bool
	ec      *encodeContext
	plans   map[*QueryNode]*nodePlan
	out     *writeRows
}

func (d *denormalizer) planFor(node *QueryNode) (*nodePlan, error) {
	if p, ok := d.plans[node]; ok {
		return p, nil
	}
	t := d.tables[node.Relation]
	if t == nil {
		return nil, oops.With("node", node.InnerName).Errorf("write: no temp table prepared for %s", node.Relation.Identifier.String())
	}
	p, err := newNodePlan(node, t)
	if err != nil {
		return nil, err
	}
	d.plans[node] = p
	return p, nil
}

// walkNode appends raw's row to its table's batch and recurses into every child embed found via
// selectFieldsFor. parentID is nil for the root.
func (d *denormalizer) walkNode(node *QueryNode, raw Node, parentID *int32) error {
	nodeID, ok := d.ids[node]
	if !ok {
		return nil // READONLY (or nested under one) : not part of the write at all
	}
	if raw.Kind() != KindObject {
		return oops.With("node", node.InnerName, "received", kindName(raw.Kind())).Errorf("write: expected an object for a row of %q, got a %s", node.InnerName, kindName(raw.Kind()))
	}
	plan, err := d.planFor(node)
	if err != nil {
		return err
	}

	rowID := int32(d.out.nextRowID)
	d.out.nextRowID++
	plan.fill(raw)

	batch := d.out.batches[plan.table]
	if batch == nil {
		batch = plan.table.newBatch(d.general, 16)
		d.out.batches[plan.table] = batch
	}
	meta := rowMeta{RowID: rowID, NodeID: int32(nodeID)}
	if parentID != nil {
		meta.ParentID, meta.HasParent = *parentID, true
	}
	if err := batch.addRow(meta, plan.vals, plan.mask, d.ec); err != nil {
		return oops.With("node", node.InnerName).Wrapf(err, "write: row of %q", node.InnerName)
	}
	d.out.populated[nodeID] = true
	if d.out.tableNodes[plan.table] == nil {
		d.out.tableNodes[plan.table] = map[int]bool{}
	}
	d.out.tableNodes[plan.table][nodeID] = true
	d.out.submitted[nodeID]++
	d.out.count++
	if d.out.patchMask == nil {
		d.out.patchMask = slices.Clone(plan.mask)
		d.out.rootRowID = rowID
	}

	fields, err := selectFieldsFor(node)
	if err != nil {
		return err
	}
	for _, f := range fields {
		child := embedChildOf(f)
		if child == nil {
			continue
		}
		value, ok := raw.ObjectGet(f.key)
		if !ok || value.Kind() == KindNull {
			continue
		}
		if isIncoming(node, child) {
			if value.Kind() != KindArray {
				return oops.With("node", node.InnerName, "relation", f.key).Errorf("write: expected an array for incoming relation %q", f.key)
			}
			items, err := value.ArrayItems()
			if err != nil {
				return oops.With("node", node.InnerName, "relation", f.key).Wrapf(err, "write: expected an array for incoming relation")
			}
			for i := range items {
				if err := d.walkNode(child, items[i], &rowID); err != nil {
					return err
				}
			}
		} else if err := d.walkNode(child, value, &rowID); err != nil {
			return err
		}
	}
	return nil
}

// isIncoming reports to-many (array payload) vs OutgoingNodes' to-one
// (object payload) ; isOutgoingOf (sql.go) is its counterpart.
func isIncoming(node, child *QueryNode) bool {
	return slices.Contains(node.IncomingNodes, child)
}
