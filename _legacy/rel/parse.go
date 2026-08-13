package rel

import (
	"github.com/bytedance/sonic"
	"github.com/bytedance/sonic/ast"
	"gitlab.com/tozd/go/errors"
	"sales-way.com/server/pg"
)

type ServerOperation struct {
	Query *Query
	Data  *ast.Node
}

type ServerOperations []*ServerOperation

func ParseFromBytes(tables pg.DBAllTables, functions pg.DBFunctionMap, body []byte) (ServerOperations, error) {
	root, err := sonic.Get(body)
	if err != nil {
		return nil, errors.WithStack(err)
	}
	ctx := &ParsingContext{
		Tables:    tables,
		Functions: functions,
	}
	return parseServerNode(ctx, &root)
}

func parseServerNode(ctx *ParsingContext, node *ast.Node) (ServerOperations, error) {
	switch node.TypeSafe() {
	case ast.V_ARRAY:
		ops := make(ServerOperations, 0)
		values, err := node.Values()
		if err != nil {
			return nil, errors.WithStack(err)
		}
		var item ast.Node
		for values.Next(&item) {
			batch, err := parseServerNode(ctx, &item)
			if err != nil {
				return nil, err
			}
			ops = append(ops, batch...)
		}
		return ops, nil
	case ast.V_OBJECT:
		return parseServerObject(ctx, node)
	default:
		return nil, errors.New("server query must be an object or array")
	}
}

func parseServerObject(ctx *ParsingContext, node *ast.Node) (ServerOperations, error) {
	queryNode := node.Get("query")
	if queryNode != nil {
		op, err := parseFullQuery(ctx, node, queryNode)
		if err != nil {
			return nil, err
		}
		return ServerOperations{op}, nil
	}

	op, err := parseQueryOperation(ctx, node)
	if err != nil {
		return nil, err
	}
	return ServerOperations{op}, nil
}

func parseFullQuery(ctx *ParsingContext, root *ast.Node, queryNode *ast.Node) (*ServerOperation, error) {
	op, err := parseQueryFromNode(ctx, queryNode)
	if err != nil {
		return nil, err
	}
	data := root.Get("data")
	if data != nil {
		op.Data = data
	}
	return op, nil
}

func parseQueryOperation(ctx *ParsingContext, node *ast.Node) (*ServerOperation, error) {
	return parseQueryFromNode(ctx, node)
}

func parseQueryFromNode(ctx *ParsingContext, node *ast.Node) (*ServerOperation, error) {
	ctx.RelationshipNb++
	q := &Query{RelIndex: ctx.RelationshipNb}
	if err := q.FromNode(ctx, node); err != nil {
		return nil, err
	}
	return &ServerOperation{Query: q}, nil
}
