package query

import (
	"sort"
	"strconv"

	"gitlab.com/tozd/go/errors"
	"sales-way.com/server/pg"
	"sales-way.com/server/utils"
)

var VERBS = []string{"insert", "update", "delete", "upsert", "merge"}

func Parse2(tables pg.DBAllTables, buf []byte) (AstOperations, error) {
	return ParseOperations(&ParsingContext{
		Tables: tables,
		lexer:  NewLexer(buf),
	})
}

// ParseOperations parses a list of operations from the input
func ParseOperations(ctx *ParsingContext) (AstOperations, error) {
	var ops = AstOperations{}

	for {
		if ctx.lexer.Peek().IsEOF() && len(ops) > 0 {
			// Allow the last operation to be terminated by ;
			break
		}

		if op, err := ParseTopLevelOperation(ctx); err != nil {
			return nil, errors.WithMessage(err, "in top level operation")
		} else {
			ops = append(ops, op)
			if op.Selection.Alias == "" {
				op.Selection.Alias = "__root" + strconv.Itoa(len(ops))
			}
		}

		if ctx.lexer.Consume(TK_SEMICOLON) == nil {
			break
		}
	}

	if peek := ctx.lexer.Peek(); !peek.IsEOF() {
		return nil, ctx.lexer.last.ErrorMessage("expected operation or end of input, but '" + peek.String() + "' (" + peek.Name() + ") is next")
	}

	return ops, nil
}

// operation:
func ParseTopLevelOperation(ctx *ParsingContext) (*AstOperation, error) {
	var op = &AstOperation{}
	var verb = ctx.DefaultVerb

	if ctx.lexer.ConsumeString("rollback") != nil {
		op.Rollback = true
	}

	// verb?
	if vb := ctx.lexer.ConsumeStringIgnoreCase(VERBS...); vb != nil {
		verb_name := vb.String()
		switch verb_name {
		case "insert":
			verb = OP_INSERT
		case "update":
			verb = OP_UPDATE
		case "delete":
			verb = OP_DELETE
		case "upsert":
			verb = OP_UPSERT
		case "merge":
			verb = OP_MERGE
		}
	}

	// Data-altering operations have a data operand
	if verb == OP_MERGE || verb == OP_UPSERT || verb == OP_INSERT {
		if xp, err := parseExpression(ctx, 0); err != nil {
			return nil, errors.WithMessage(err, "expected data operand after")
		} else {
			op.Operand = xp
		}

		if ctx.lexer.ConsumeStringIgnoreCase("into") == nil {
			return nil, ctx.lexer.last.ErrorMessage("expected 'into' after data operand")
		}
	}

	alias := ""
	backup := ctx.lexer.last
	// Try to parse an alias for this selection
	if tk := ctx.lexer.Consume(TK_IDENT); tk != nil {
		if ctx.lexer.ConsumeString(":") != nil {
			alias = tk.String()
		} else {
			ctx.lexer.SetPosition(backup)
		}
	}

	var name IAstExpression
	if xp, err := parseExpression(ctx, ABOVE_DOT); err != nil {
		return nil, errors.WithMessage(err, "expected relation name")
	} else {
		if !ExpressionIsIdentificationChain(xp) {
			if xp != nil {
				return nil, ctx.lexer.last.ErrorMessage("relation name is not a valid identifier " + xp.String())
			} else {
				return nil, ctx.lexer.last.ErrorMessage("relation name is not a valid identifier")
			}
		}
		name = xp
	}

	op.Verb = verb

	// select clause ; *, fields, expressions, or relationships
	if selection, err := ParseSelection(ctx); err != nil {
		return nil, errors.WithMessage(err, "in selection")
	} else {
		selection.NameExpression = name
		selection.IsOperation = true
		op.Selection = selection
		selection.Alias = alias
	}

	return op, nil
}

func ParseSelection(ctx *ParsingContext) (*AstSelection, error) {
	sel := &AstSelection{}
	ctx.RelationshipNb++
	sel.SelectionIndex = ctx.RelationshipNb

	if ctx.lexer.ConsumeString("(") != nil {
		if arguments, err := ParseFunctionCall(ctx); err != nil {
			return nil, errors.WithMessage(err, "in function call arguments")
		} else {
			sel.FunctionCallArguments = arguments
		}
	}

	if where, err := ParseWhereClause(ctx); err != nil {
		return nil, errors.WithMessage(err, "in where clause")
	} else {
		sel.WhereClause = where
	}

	if ctx.lexer.ConsumeString("{") != nil {
		if fields, err := ParseSelectFields(ctx); err != nil {
			return nil, errors.WithMessage(err, "in select clause")
		} else {
			sel.Fields = fields
		}
	}

	if ctx.lexer.ConsumeString("order") != nil {
		// By is optional
		ctx.lexer.ConsumeString("by")

		sel.OrderBy = make([]*AstOrder, 0)

		for {
			if xp, err := parseExpression(ctx, 0); err != nil {
				return nil, errors.WithMessage(err, "in order clause")
			} else {

				order := &AstOrder{
					Expression: xp,
					Asc:        true,
				}

				if ctx.lexer.ConsumeString("asc") != nil {
					order.Asc = true
				} else if ctx.lexer.ConsumeString("desc") != nil {
					order.Asc = false
				}

				sel.OrderBy = append(sel.OrderBy, order)
			}

			if ctx.lexer.ConsumeString(",") == nil {
				break
			}
		}
	}

	if ctx.lexer.ConsumeString("limit") != nil {
		if next := ctx.lexer.Consume(TK_NUMBER); next != nil {
			if limit, err := strconv.Atoi(next.String()); err != nil {
				return nil, errors.WithMessage(err, "in limit clause")
			} else {
				sel.Limit = limit
			}
		} else {
			return nil, ctx.lexer.last.ErrorMessage("expected number after limit")
		}
	}

	if ctx.lexer.ConsumeString("offset") != nil {
		if next := ctx.lexer.Consume(TK_NUMBER); next != nil {
			if offset, err := strconv.Atoi(next.String()); err != nil {
				return nil, errors.WithMessage(err, "in offset clause")
			} else {
				sel.Offset = offset
			}
		} else {
			return nil, ctx.lexer.last.ErrorMessage("expected number after offset")
		}
	}

	return sel, nil
}

func ParseFunctionCall(ctx *ParsingContext) (FunctionCallArguments, error) {
	var args = make(FunctionCallArguments, 0)
	found_named_argument := false

	if err := optParseUntilSeparatedBy(ctx, ")", ",", func() error {

		named_argument := ""

		// try to parse a field name, which would
		backup := ctx.lexer.last
		if tk_ident := ctx.lexer.Consume(TK_IDENT); tk_ident != nil {
			if ctx.lexer.ConsumeString(":") != nil {
				named_argument = tk_ident.String()
				found_named_argument = true
			} else {
				ctx.lexer.SetPosition(backup)
			}
		}

		if xp, err := parseExpression(ctx, 0); err != nil {
			return errors.WithMessage(err, "in function call")
		} else {
			if named_argument != "" {
				args = append(args, &AstNamedFunctionArgument{
					Alias:      named_argument,
					Expression: xp,
				})
			} else {
				if found_named_argument {
					return ctx.lexer.last.ErrorMessage("named arguments must be at the end of the argument list")
				}
				args = append(args, xp)
			}
		}

		return nil
	}); err != nil {
		return nil, errors.WithMessage(err, "in function call")
	}

	return args, nil
}

// select_clause
func ParseSelectFields(ctx *ParsingContext) ([]IAstField, error) {
	var selectClause = []IAstField{}

	if err := optParseUntilSeparatedBy(ctx, "}", ",", func() error {

		if alias := ctx.lexer.Consume(TK_IDENT); alias != nil {

			if ctx.lexer.ConsumeString(":") != nil {

				if ctx.lexer.ConsumeString("{") != nil {
					if fields, err := ParseSelectFields(ctx); err != nil {
						return errors.WithMessage(err, "in field group")
					} else {
						selectClause = append(selectClause, &AstFieldGroup{
							Name:   alias.String(),
							Fields: fields,
						})
						return nil
					}
				}

				if field, err := parseField(ctx, alias.String()); err != nil {
					return errors.WithMessage(err, "in field")
				} else {
					selectClause = append(selectClause, field)
				}

				return nil
			} else {
				selectClause = append(selectClause, &AstSimpleField{
					Name: alias.String(),
					Expression: &Identifier{
						Identifier: alias.String(),
					},
				})
				return nil
			}

		}

		if field, err := parseField(ctx, ""); err != nil {
			return errors.WithMessage(err, "in field")
		} else {
			selectClause = append(selectClause, field)
		}
		return nil
	}); err != nil {
		return nil, errors.WithMessage(err, "parsing fields")
	}

	return selectClause, nil
}

func parseField(ctx *ParsingContext, alias string) (IAstField, error) {
	// "*"
	if ctx.lexer.ConsumeString("*") != nil {
		if star, err := ParseStarSelector(ctx); err != nil {
			return nil, errors.WithMessage(err, "in star selector")
		} else {
			star.Name = alias
			return star, nil
		}
	}

	readonly := false
	if ctx.lexer.ConsumeString("readonly") != nil {
		readonly = true
	}

	if direction := ctx.lexer.ConsumeString("<<", ">>"); direction != nil {
		if name, err := parseExpression(ctx, ABOVE_DOT); err != nil || name == nil {
			return nil, ctx.lexer.last.ErrorMessage("expected relationship name after <- or ->")
		} else {
			rel := &AstRelationshipField{
				Operator:   direction.String(),
				Alias:      alias,
				IsReadOnly: readonly,
				Name:       name,
			}

			// A hint is given
			if ctx.lexer.ConsumeString("(") != nil {
				var field_names = []string{}
				if err := optParseUntilSeparatedBy(ctx, ")", ",", func() error {
					if tk := ctx.lexer.Consume(TK_IDENT); tk != nil {
						field_names = append(field_names, tk.String())
					} else {
						return ctx.lexer.last.ErrorMessage("expected field name")
					}
					return nil
				}); err != nil {
					return nil, errors.WithMessage(err, "expected field or contraint names after (")
				} else {
					rel.FieldNames = field_names
					sort.Strings(rel.FieldNames)
				}
				ctx.lexer.ConsumeString(")")
			}

			// Allow to specify a selection after the relationship name
			backup := ctx.lexer.last
			if ctx.lexer.ConsumeString("[") != nil {
				if ctx.lexer.ConsumeString("]") != nil {
				} else {
					ctx.lexer.SetPosition(backup)
				}
			}

			if sel, err := ParseSelection(ctx); err != nil {
				return nil, errors.WithMessage(err, "in relationship field")
			} else {
				rel.Selection = sel
			}

			return rel, nil
		}

	}

	if alias != "" {
		if exp, err := parseExpression(ctx, 0); err != nil {
			return nil, errors.WithMessage(err, "in field")
		} else {
			return &AstSimpleField{
				Name:       alias,
				Expression: exp,
			}, nil
		}
	}

	// The identifier is directly a field name
	return nil, ctx.lexer.last.ErrorMessage("expected '*' or field name")
}

func ParseWhereClause(ctx *ParsingContext) (IAstExpression, error) {
	if ctx.lexer.ConsumeString("where") == nil {
		return nil, nil
	}

	if where, err := parseExpression(ctx, 0); err != nil {
		return nil, errors.WithMessage(err, "in where clause")
	} else {
		return where, nil
	}
}

// ParseStarSelector parses a * field selector and its exclusions (* - field1 - field2)
func ParseStarSelector(ctx *ParsingContext) (*AstStarSelector, error) {
	star := &AstStarSelector{
		Exclusions: utils.Set[string]{},
	}

	for {
		// "-"
		if ctx.lexer.ConsumeString("-") == nil {
			break
		}

		// field name
		if tk := ctx.lexer.Consume(TK_IDENT); tk != nil {
			star.Exclusions.Add(tk.String())
		} else {
			return nil, ctx.lexer.last.ErrorMessage("expected field name for exclusion")
		}
	}

	return star, nil
}
