package rel

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
	res := []any{id, parentId, tableId, bytes}
	ctx.nodes = append(ctx.nodes, res)
	return res
}

type jsonLocalBuilder struct {
	ctx      *flatContext
	current  *ast.Node
	parentId int64
	sel      *Query
	cbk      func(pair ast.Pair)
}

func (builder *jsonLocalBuilder) VisitFieldGroup(group *AstFieldGroup) error {
	base := builder.current.Get(group.Name)
	if base == nil {
		return nil
	}
	current2 := jsonLocalBuilder{
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
	key := field.Name
	if key == "" {
		return nil
	}
	value := builder.current.Get(key)
	if value == nil {
		return nil
	}
	builder.cbk(ast.Pair{Key: key, Value: *value})
	return nil
}

func (builder *jsonLocalBuilder) VisitRelationshipField(field *AstRelationshipField) error {
	if field.Query == nil {
		return fmt.Errorf("relationship field %s is not resolved", field.Alias)
	}

	node := builder.current.Get(field.Alias)
	if field.IsReadOnly {
		return nil
	}
	if node == nil {
		return fmt.Errorf("node %s not found", field.Alias)
	}

	switch node.TypeSafe() {
	case ast.V_ARRAY:
		values, err := node.Values()
		if err != nil {
			return err
		}
		var iter ast.Node
		for values.Next(&iter) {
			if err := field.Query.FlattenJsonDataItem(&iter, builder.parentId, builder.ctx); err != nil {
				return err
			}
		}
	case ast.V_OBJECT:
		if err := field.Query.FlattenJsonDataItem(node, builder.parentId, builder.ctx); err != nil {
			return err
		}
	default:
		return fmt.Errorf("invalid node type %d for %s", node.TypeSafe(), field.Alias)
	}
	return nil
}

func (sel *Query) FlattenJsonDataItem(item *ast.Node, parentId int64, ctx *flatContext) error {
	pairs := make([]ast.Pair, 0)
	myId := ctx.nextId()

	builder := jsonLocalBuilder{
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

	node := ast.NewObject(pairs)
	ctx.NewNode(myId, parentId, sel.RelIndex, node)
	return nil
}

func (op *ServerOperation) FlattenJsonData(body []byte) ([][]any, error) {
	ctx := &flatContext{
		lastId: 0,
		nodes:  make([][]any, 0),
	}

	var root ast.Node
	if op.Data != nil {
		root = *op.Data
	} else {
		var err error
		root, err = sonic.Get(body)
		if err != nil {
			return nil, err
		}
	}

	switch root.TypeSafe() {
	case ast.V_ARRAY:
		values, err := root.Values()
		if err != nil {
			return nil, err
		}
		var node ast.Node
		for values.Next(&node) {
			if err = op.Query.FlattenJsonDataItem(&node, 0, ctx); err != nil {
				return nil, err
			}
		}
	case ast.V_OBJECT:
		if err := op.Query.FlattenJsonDataItem(&root, 0, ctx); err != nil {
			return nil, err
		}
	default:
		return nil, fmt.Errorf("invalid json data")
	}
	return ctx.nodes, nil
}
