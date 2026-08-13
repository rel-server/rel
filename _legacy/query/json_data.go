package query

/**
In this file, we will flatten the json data into a list of FlatJson objects that will be inserted into a temp table from which the insert / updates / deletes / merge will be launched from.
*/

import (
	"fmt"

	"github.com/bytedance/sonic"
	"github.com/bytedance/sonic/ast"
)

type flatContext struct {
	lastId int64
	nodes  [][]any
}

func (ctx *flatContext) nextId() int64 {
	ctx.lastId++
	return ctx.lastId
}

func (ctx *flatContext) NewNode(id int64, parentId int64, tableId int, json ast.Node) []any {

	bytes, _ := json.MarshalJSON()

	var res = []any{
		id,
		parentId,
		tableId,
		bytes,
	}
	ctx.nodes = append(ctx.nodes, res)
	// pp.Println(res)
	return res
}

// Flatten the json data into a list of FlatJson objects, traversing relations.

type jsonLocalBuilder struct {
	ctx      *flatContext
	current  *ast.Node
	parentId int64
	sel      *AstSelection
	cbk      func(pair ast.Pair)
}

func (builder *jsonLocalBuilder) VisitFieldGroup(group *AstFieldGroup) error {

	var base = builder.current.Get(group.Name)
	if base == nil {
		return nil
	}

	var current2 = jsonLocalBuilder{
		ctx:      builder.ctx,
		current:  base,
		parentId: builder.parentId,
		sel:      builder.sel,
		cbk:      builder.cbk,
	}

	for _, field := range group.Fields {
		if err := field.Visit(&current2); err != nil {
			return err
		}
	}
	return nil
}

// Emit all the columns that are not excluded
func (builder *jsonLocalBuilder) VisitStarSelector(selector *AstStarSelector) error {
	for _, col := range builder.sel.ResolvedTable.ColumnsInOrder {
		if selector.Exclusions.Has(col.Name) {
			continue
		}

		value := builder.current.Get(col.Name)
		if value == nil {
			continue
		}
		builder.cbk(ast.Pair{Key: col.Name, Value: *value})
	}
	return nil
}

func (builder *jsonLocalBuilder) VisitSimpleField(field *AstSimpleField) error {
	if colname, ok := field.Expression.(*Identifier); ok {
		value := builder.current.Get(colname.String())
		if value == nil {
			return nil
		}
		builder.cbk(ast.Pair{Key: colname.String(), Value: *value})
		return nil
	} else if raw_sql, ok := field.Expression.(AstRawSql); ok {
		value := builder.current.Get(raw_sql.String())
		if value == nil {
			return nil
		}
		builder.cbk(ast.Pair{Key: raw_sql.String(), Value: *value})
		return nil
	}

	return nil
}

func (builder *jsonLocalBuilder) VisitRelationshipField(field *AstRelationshipField) error {

	node := builder.current.Get(field.Selection.Alias)

	if field.IsReadOnly {
		// Ignore the field if it's readonly
		// FIXME : we probably want to get its foreign key stuff... But then again, it's readonly.
		return nil
	}

	if node == nil {
		return fmt.Errorf("node %s not found", field.Selection.Alias)
	}

	var iter ast.Node
	switch node.TypeSafe() {
	case ast.V_ARRAY:
		values, err := node.Values()
		if err != nil {
			return err
		}

		for values.Next(&iter) {
			if err := field.Selection.FlattenJsonDataItem(&iter, builder.parentId, builder.ctx); err != nil {
				return err
			}
		}
	case ast.V_OBJECT:
		if err := field.Selection.FlattenJsonDataItem(node, builder.parentId, builder.ctx); err != nil {
			return err
		}
	default:
		return fmt.Errorf("invalid node type %d", node.TypeSafe())
	}

	return nil
}

// Flatten a single item, Operation and relations figure wether they deal with arrays or not
func (sel *AstSelection) FlattenJsonDataItem(item *ast.Node, parentId int64, ctx *flatContext) error {

	// When in a node, we're going to build a JSON object, picking the keys wherever they are.

	var pairs []ast.Pair = make([]ast.Pair, len(sel.Fields))
	var myId int64 = ctx.nextId()

	// var len_outgoing = len(sel.ResolvedOutgoingRelationships)

	// The builder visits the field expression for each json object and populates the pairs array.
	var builder = jsonLocalBuilder{
		ctx:      ctx,
		current:  item,
		parentId: myId,
		sel:      sel,
		cbk: func(pair ast.Pair) {
			pairs = append(pairs, pair)
		},
	}

	for _, field := range sel.Fields {
		if err := field.Visit(&builder); err != nil {
			return err
		}
	}

	var node = ast.NewObject(pairs)
	ctx.NewNode(myId, parentId, sel.SelectionIndex, node)

	return nil
}

func (op *AstOperation) FlattenJsonData(body []byte) ([][]any, error) {
	ctx := &flatContext{
		lastId: 0,
		nodes:  make([][]any, 0),
	}

	// Right now, we suppose that there is only $body that was asked for, but really, the root should be introspected according to op.Operand.
	// like root := jsonResolver.Get(op.Operand, body)
	var root ast.Node
	root, err := sonic.Get(body)
	if err != nil {
		return nil, err
	}

	switch root.TypeSafe() {
	case ast.V_ARRAY:

		var node ast.Node
		v, err := root.Values()
		if err != nil {
			return nil, err
		}

		for v.Next(&node) {
			if err = op.Selection.FlattenJsonDataItem(&node, 0, ctx); err != nil {
				return nil, err
			}
		}

	case ast.V_OBJECT:
		if err := op.Selection.FlattenJsonDataItem(&root, 0, ctx); err != nil {
			return nil, err
		}

	default:
		return nil, fmt.Errorf("invalid json data")
	}
	return ctx.nodes, nil
}
