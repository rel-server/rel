// Pass 1, decode step for well-known query definition files : {name, params, query} or an array of them. A request
// invoking one is rawWellKnown instead. Nothing here touches pg or config.
package query

import (
	"fmt"

	"github.com/bytedance/sonic/ast"
)

// RawWellKnownParam is one {type?, default?} declaration. HasDefault false means required ; a "default": null
// makes it optional with SQL NULL.
type RawWellKnownParam struct {
	Type       string
	HasDefault bool
	Default    []byte // raw JSON, meaningful only when HasDefault is true
}

// RawWellKnownDefinition is one parsed, not yet resolved, entry of a well-known query file.
type RawWellKnownDefinition struct {
	Name   string
	Params map[string]RawWellKnownParam
	Query  *rawRelation

	// QueryRaw is "query"'s verbatim JSON text : the TypeScript generation embeds it so TS infers its type.
	QueryRaw []byte
}

// ParseWellKnownFile decodes a well-known query file : one definition or an array of them.
func ParseWellKnownFile(data []byte) ([]RawWellKnownDefinition, error) {
	root, err := parseJSON(data)
	if err != nil {
		return nil, fmt.Errorf("wellknown: invalid JSON: %w", err)
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
	rel, err := parseRawRelation(sonicNode{queryNode})
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
