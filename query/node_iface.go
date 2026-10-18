package query

import "github.com/bytedance/sonic/ast"

// NodeKind is the JSON/CBOR value category a Node holds.
type NodeKind int

const (
	KindInvalid NodeKind = iota
	KindNull
	KindBool
	KindNumber
	KindString
	KindArray
	KindObject
)

// Node is the subset of a decoded document the query parser reads.
type Node interface {
	Kind() NodeKind
	StrictString() (string, error)
	StrictBool() (bool, error)
	StrictInt64() (int64, error)
	StrictFloat64() (float64, error)
	// ArrayItems returns all elements, converted once (one allocation).
	ArrayItems() ([]Node, error)
	ObjectGet(key string) (Node, bool)
	ObjectEach(fn func(key string, v Node) error) error
	// Raw returns the node's source JSON text (passthrough of "data"/"params").
	Raw() (string, error)
}

// nodeList is an array's elements, so "rest := items[1:]" keeps working.
type nodeList struct {
	items []Node
	len   int
}

func (l nodeList) at(i int) Node       { return l.items[i] }
func (l nodeList) from(k int) nodeList { return nodeList{l.items[k:], l.len - k} }

func arrayItems(n Node) (nodeList, error) {
	if n.Kind() != KindArray {
		return nodeList{}, errNotArray
	}
	items, err := n.ArrayItems()
	return nodeList{items, len(items)}, err
}

type nodeError string

func (e nodeError) Error() string { return string(e) }

const (
	errNotArray  = nodeError("value is not an array")
	errNotObject = nodeError("value is not an object")
)

// sonicNode adapts *ast.Node.
type sonicNode struct{ n *ast.Node }

func (s sonicNode) Kind() NodeKind {
	switch s.n.TypeSafe() {
	case ast.V_NULL:
		return KindNull
	case ast.V_TRUE, ast.V_FALSE:
		return KindBool
	case ast.V_NUMBER:
		return KindNumber
	case ast.V_STRING:
		return KindString
	case ast.V_ARRAY:
		return KindArray
	case ast.V_OBJECT:
		return KindObject
	}
	return KindInvalid
}
func (s sonicNode) StrictString() (string, error)   { return s.n.StrictString() }
func (s sonicNode) StrictBool() (bool, error)       { return s.n.StrictBool() }
func (s sonicNode) StrictInt64() (int64, error)     { return s.n.StrictInt64() }
func (s sonicNode) StrictFloat64() (float64, error) { return s.n.StrictFloat64() }
func (s sonicNode) Raw() (string, error)            { return s.n.Raw() }

func (s sonicNode) ArrayItems() ([]Node, error) {
	items, err := s.n.ArrayUseNode()
	if err != nil {
		return nil, err
	}
	out := make([]Node, len(items))
	for i := range items {
		out[i] = sonicNode{&items[i]}
	}
	return out, nil
}
func (s sonicNode) ObjectGet(key string) (Node, bool) {
	c := s.n.Get(key)
	if !c.Exists() {
		return nil, false
	}
	return sonicNode{c}, true
}
func (s sonicNode) ObjectEach(fn func(string, Node) error) error {
	m, err := s.n.MapUseNode()
	if err != nil {
		return err
	}
	for k, v := range m {
		v := v
		if err := fn(k, sonicNode{&v}); err != nil {
			return err
		}
	}
	return nil
}
