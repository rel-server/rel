// Pass 1, decode step for well-known query definition files — parses
// specs/well-known-queries.md ## Definition's WellKnownQuery{name, params,
// query} shape (and its WellKnownQuery[] array form), reusing
// parseRawRelation (node_parse.go) for the "query" field's own Relation
// tree unchanged. Distinct from ParseQuery/node_parse.go's rawWellKnown :
// that one decodes a REQUEST invoking an already-loaded well-known query
// ({wellknown, params, data}) ; this one decodes the DEFINITION FILE that
// creates one in the first place. Nothing here touches pg or config —
// resolving the parsed *rawRelation against a live schema is
// node_resolve.go's job, same split ParseQuery already follows.
package query

import (
	"bytes"
	"fmt"

	"github.com/bytedance/sonic/ast"
)

// RawWellKnownParam is one WellKnownParam{type?, default?} declaration.
// HasDefault distinguishes "no default key at all" (required — ##
// Definition : WELL_KNOWN_PARAM_REQUIRED if the caller omits it) from
// "default: null" (optional, defaults to SQL NULL) from "default: <value>"
// (optional, defaults to that value) — a plain map[string]any lookup can't
// tell the first two apart, so this is decoded presence-aware directly off
// the ast.Node instead.
type RawWellKnownParam struct {
	Type       string
	HasDefault bool
	Default    []byte // raw JSON, meaningful only when HasDefault is true
}

// RawWellKnownDefinition is one parsed (not yet DB-resolved) entry from a
// well-known query file.
type RawWellKnownDefinition struct {
	Name   string
	Params map[string]RawWellKnownParam
	Query  *rawRelation

	// QueryRaw is "query"'s own verbatim JSON text, captured alongside the
	// parsed *rawRelation — specs/typescript.md ## Wellknowns embeds this
	// directly as a TS literal and lets ShapeFromRelationQuery (shapes.ts)
	// infer its type, rather than duplicating that inference in Go.
	QueryRaw []byte
}

// ParseWellKnownFile decodes one well-known query file's top-level content
// — "either one WellKnownQuery or WellKnownQuery[]" (## Definition).
func ParseWellKnownFile(data []byte) ([]RawWellKnownDefinition, error) {
	root, perr := ast.NewParser(string(bytes.TrimSpace(data))).Parse()
	if perr != 0 {
		return nil, fmt.Errorf("wellknown: invalid JSON: %w", perr)
	}
	if root.TypeSafe() == ast.V_ARRAY {
		items, err := root.ArrayUseNode()
		if err != nil {
			return nil, fmt.Errorf("wellknown: invalid array: %w", err)
		}
		if len(items) == 0 {
			return nil, fmt.Errorf("wellknown: empty WellKnownQuery[] array")
		}
		out := make([]RawWellKnownDefinition, len(items))
		for i := range items {
			def, err := parseWellKnownDefinition(&items[i])
			if err != nil {
				return nil, fmt.Errorf("wellknown: item %d: %w", i, err)
			}
			out[i] = def
		}
		return out, nil
	}
	def, err := parseWellKnownDefinition(&root)
	if err != nil {
		return nil, err
	}
	return []RawWellKnownDefinition{def}, nil
}

func parseWellKnownDefinition(n *ast.Node) (RawWellKnownDefinition, error) {
	nameNode := n.Get("name")
	if !nameNode.Exists() {
		return RawWellKnownDefinition{}, fmt.Errorf(`wellknown: missing "name"`)
	}
	name, err := nameNode.StrictString()
	if err != nil {
		return RawWellKnownDefinition{}, fmt.Errorf(`wellknown: "name" must be a string: %w`, err)
	}

	queryNode := n.Get("query")
	if !queryNode.Exists() {
		return RawWellKnownDefinition{}, fmt.Errorf(`wellknown: %q: missing "query"`, name)
	}
	rel, err := parseRawRelation(queryNode)
	if err != nil {
		return RawWellKnownDefinition{}, fmt.Errorf(`wellknown: %q: "query": %w`, name, err)
	}
	queryRaw, err := queryNode.Raw()
	if err != nil {
		return RawWellKnownDefinition{}, fmt.Errorf(`wellknown: %q: "query": %w`, name, err)
	}

	params := map[string]RawWellKnownParam{}
	if p := n.Get("params"); p.Exists() && p.TypeSafe() != ast.V_NULL {
		fields, err := p.MapUseNode()
		if err != nil {
			return RawWellKnownDefinition{}, fmt.Errorf(`wellknown: %q: "params": expected an object: %w`, name, err)
		}
		for pname, pnode := range fields {
			param := RawWellKnownParam{}
			if t := pnode.Get("type"); t.Exists() {
				s, err := t.StrictString()
				if err != nil {
					return RawWellKnownDefinition{}, fmt.Errorf(`wellknown: %q: param %q: "type" must be a string: %w`, name, pname, err)
				}
				param.Type = s
			}
			if d := pnode.Get("default"); d.Exists() {
				raw, err := d.Raw()
				if err != nil {
					return RawWellKnownDefinition{}, fmt.Errorf(`wellknown: %q: param %q: "default": %w`, name, pname, err)
				}
				param.HasDefault = true
				param.Default = []byte(raw)
			}
			params[pname] = param
		}
	}

	return RawWellKnownDefinition{Name: name, Params: params, Query: rel, QueryRaw: []byte(queryRaw)}, nil
}
