package rel

import (
	"strconv"

	"github.com/bytedance/sonic/ast"
	"gitlab.com/tozd/go/errors"
	"sales-way.com/server/query"
)

func (id *Identifier) FromNode(node *ast.Node) (err error) {
	typ := node.TypeSafe()

	switch typ {
	case ast.V_STRING:
		id.Name, err = node.StrictString()
	case ast.V_OBJECT:
		node.ForEach(func(path ast.Sequence, node *ast.Node) bool {
			switch *path.Key {
			case "schema":
				id.Schema, err = node.StrictString()
			case "db":
				id.Db, err = node.StrictString()
			case "name":
				id.Name, err = node.StrictString()
			default:
				err = errors.New("unknown field: " + *path.Key)
				return false
			}
			return true
		})
	default:
		err = errors.New("invalid type for identifier")
	}
	return err
}

func getStringSliceFromNode(key string, node *ast.Node) ([]string, error) {
	if node.TypeSafe() != ast.V_ARRAY {
		return nil, errors.New(key + " must be an array")
	}
	var err error
	strings := make([]string, 0)
	node.ForEach(func(path ast.Sequence, node *ast.Node) bool {
		str, err_inner := node.StrictString()
		if err_inner != nil {
			err = err_inner
			return false
		}
		strings = append(strings, str)
		return true
	})
	return strings, err
}

func (wr *Write) FromNode(node *ast.Node) error {
	wr.Mode = MODE_READONLY

	if node.TypeSafe() != ast.V_OBJECT {
		return errors.New("write must be an object")
	}

	props, err := node.Properties()
	if err != nil {
		return errors.WithStack(err)
	}

	var pair ast.Pair
	for props.Next(&pair) {
		switch pair.Key {
		case "relation":
			if err := wr.Relation.FromNode(&pair.Value); err != nil {
				return errors.Wrap(err, "when parsing relation")
			}
		case "mode":
			str, err := pair.Value.StrictString()
			if err != nil {
				return errors.Wrap(err, "when parsing mode")
			}
			mode, err := parseWriteMode(str)
			if err != nil {
				return errors.Wrap(err, "when parsing mode")
			}
			wr.Mode = mode
		case "on_conflict":
			wr.OnConflict, err = getStringSliceFromNode("on_conflict", &pair.Value)
			if err != nil {
				return errors.WithStack(err)
			}
		case "insert_columns", "insert_only":
			wr.InsertOnly, err = getStringSliceFromNode(pair.Key, &pair.Value)
			if err != nil {
				return errors.WithStack(err)
			}
		case "update_columns", "update_only":
			wr.UpdateOnly, err = getStringSliceFromNode(pair.Key, &pair.Value)
			if err != nil {
				return errors.WithStack(err)
			}
		default:
			return errors.New("unknown field in write: " + pair.Key)
		}
	}

	return nil
}

func parseWriteMode(str string) (WriteMode, error) {
	switch str {
	case "readonly":
		return MODE_READONLY, nil
	case "insert":
		return MODE_INSERT, nil
	case "upsert":
		return MODE_UPSERT, nil
	case "merge":
		return MODE_MERGE, nil
	case "mergenew":
		return MODE_MERGENEW, nil
	case "merge-update", "mergeupdate":
		return MODE_MERGEUPDATE, nil
	case "update":
		return MODE_UPDATE, nil
	case "deleteonly", "deleteother":
		return MODE_DELETEOTHER, nil
	default:
		return MODE_READONLY, errors.New("unknown mode: " + str)
	}
}

func (q *Query) FromNode(ctx *ParsingContext, node *ast.Node) error {
	if node.TypeSafe() != ast.V_OBJECT {
		return errors.New("query must be an object")
	}

	props, err := node.Properties()
	if err != nil {
		return errors.WithStack(err)
	}

	var pair ast.Pair
	for props.Next(&pair) {
		switch pair.Key {
		case "relation":
			if err := q.Relation.FromNode(&pair.Value); err != nil {
				return errors.WithStack(err)
			}
		case "arguments":
			if err := q.parseArguments(ctx, &pair.Value); err != nil {
				return err
			}
		case "write":
			if err := q.Write.FromNode(&pair.Value); err != nil {
				return errors.WithStack(err)
			}
		case "where":
			expr, err := ExpressionFromNode(ctx, &pair.Value)
			if err != nil {
				return err
			}
			q.Where = expr
		case "select":
			fields, err := selectFieldsFromNode(ctx, &pair.Value)
			if err != nil {
				return err
			}
			q.Fields = fields
		case "on":
			if !q.IsRel {
				return errors.New("on can only be used in a rel")
			}
			q.On = make(map[string]string)
			if pair.Value.TypeSafe() != ast.V_OBJECT {
				return errors.New("on must be an object")
			}
			pair.Value.ForEach(func(path ast.Sequence, node *ast.Node) bool {
				str, err_inner := node.StrictString()
				if err_inner != nil {
					err = err_inner
					return false
				}
				q.On[*path.Key] = str
				return true
			})
			if err != nil {
				return err
			}
		case "rels":
			if pair.Value.TypeSafe() != ast.V_OBJECT {
				return errors.New("rels must be an object")
			}
			q.Rels = make(map[string]Query)
			pair.Value.ForEach(func(path ast.Sequence, rel_node *ast.Node) bool {
				ctx.RelationshipNb++
				rel := Query{
					RelIndex: ctx.RelationshipNb,
					IsRel:    true,
					Alias:    *path.Key,
				}
				if innerErr := rel.FromNode(ctx, rel_node); innerErr != nil {
					err = innerErr
					return false
				}
				q.Rels[*path.Key] = rel
				return true
			})
			if err != nil {
				return err
			}
		case "on_conflict":
			q.OnConflict, err = getStringSliceFromNode("on_conflict", &pair.Value)
			if err != nil {
				return errors.WithStack(err)
			}
		case "insert_columns", "insert_only":
			q.InsertOnly, err = getStringSliceFromNode(pair.Key, &pair.Value)
			if err != nil {
				return errors.WithStack(err)
			}
		case "update_columns", "update_only":
			q.UpdateOnly, err = getStringSliceFromNode(pair.Key, &pair.Value)
			if err != nil {
				return errors.WithStack(err)
			}
		case "offset":
			q.Offset, err = parseIntNode(&pair.Value)
			if err != nil {
				return errors.WithStack(err)
			}
		case "limit":
			q.Limit, err = parseIntNode(&pair.Value)
			if err != nil {
				return errors.WithStack(err)
			}
		default:
			return errors.New("unknown field in query: " + pair.Key)
		}
	}

	return nil
}

func (q *Query) parseArguments(ctx *ParsingContext, node *ast.Node) error {
	switch node.TypeSafe() {
	case ast.V_ARRAY:
		q.Arguments = make([]query.IAstExpression, 0)
		var err error
		node.ForEach(func(path ast.Sequence, child *ast.Node) bool {
			expr, innerErr := ExpressionFromNode(ctx, child)
			if innerErr != nil {
				err = innerErr
				return false
			}
			q.Arguments = append(q.Arguments, expr)
			return true
		})
		return err
	case ast.V_OBJECT:
		args := make(query.FunctionCallArguments, 0)
		var err error
		node.ForEach(func(path ast.Sequence, child *ast.Node) bool {
			expr, innerErr := ExpressionFromNode(ctx, child)
			if innerErr != nil {
				err = innerErr
				return false
			}
			args = append(args, &query.AstNamedFunctionArgument{
				Alias:      *path.Key,
				Expression: expr,
			})
			return true
		})
		if err != nil {
			return err
		}
		q.Arguments = []query.IAstExpression{query.AstExpressionList(args)}
		return nil
	default:
		return errors.New("arguments must be an array or object")
	}
}

func parseIntNode(node *ast.Node) (int, error) {
	switch node.TypeSafe() {
	case ast.V_NUMBER:
		raw, err := node.Raw()
		if err != nil {
			return 0, errors.WithStack(err)
		}
		return strconv.Atoi(raw)
	default:
		return 0, errors.New("expected number")
	}
}
