// Copyright 2025 Christophe Eymard
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package pg

// MaskSlot is one position of a relation's presence layout : a column, or a
// sub-field of a composite column, and the bit that records whether a written
// row supplied it (specs/typed-write-loading.md ## Presence slots).
type MaskSlot struct {
	// Path is the column, followed by the composite sub-fields leading to this
	// slot ; length 1 for a plain column. Callers must not modify it.
	Path []*Column

	// Word is the index of the 64-bit mask word holding the slot's bit, Bit its position in that word.
	Word, Bit int
}

type maskLayout struct {
	slots []MaskSlot
	roots map[*Column]*maskNode
}

type maskNode struct {
	slot     int
	children map[*Column]*maskNode
}

// MaskSlots returns r's presence slots in layout order : each column in
// attribute order, and right after a composite column (or a domain over one)
// its sub-fields, depth first. Array types are not expanded. The layout is
// computed on first use, then shared : safe for concurrent use, and stable
// until the next introspection builds new relations. Callers must not modify
// the result.
func (r *Relation) MaskSlots() []MaskSlot {
	return r.maskLayout().slots
}

// MaskWords returns how many 64-bit mask words hold r's slots : one per 64
// slots, and at least one.
func (r *Relation) MaskWords() int {
	return max(1, (len(r.maskLayout().slots)+63)/64)
}

// MaskSlotFor returns the slot of a column path : the column, followed by the
// sub-fields leading to the wanted one. ok is false when r has no such slot.
func (r *Relation) MaskSlotFor(path ...*Column) (slot MaskSlot, ok bool) {
	l := r.maskLayout()
	nodes := l.roots
	var n *maskNode
	for _, col := range path {
		if n = nodes[col]; n == nil {
			return MaskSlot{}, false
		}
		nodes = n.children
	}
	if n == nil {
		return MaskSlot{}, false
	}
	return l.slots[n.slot], true
}

func (r *Relation) maskLayout() *maskLayout {
	r.maskOnce.Do(func() {
		l := &maskLayout{}
		l.roots = l.addColumns(nil, r.Columns, map[*Relation]bool{r: true})
		r.mask = l
	})
	return r.mask
}

// addColumns appends a slot for each of cols, expanding composites ; seen
// holds the relations being expanded, which stops a (malformed) self-containing composite.
func (l *maskLayout) addColumns(prefix []*Column, cols []*Column, seen map[*Relation]bool) map[*Column]*maskNode {
	nodes := make(map[*Column]*maskNode, len(cols))
	for _, col := range cols {
		path := make([]*Column, len(prefix)+1)
		copy(path, prefix)
		path[len(prefix)] = col

		n := &maskNode{slot: len(l.slots)}
		l.slots = append(l.slots, MaskSlot{Path: path, Word: n.slot / 64, Bit: n.slot % 64})
		nodes[col] = n

		if sub := col.Type.CompositeRelation(); sub != nil && !seen[sub] {
			seen[sub] = true
			n.children = l.addColumns(path, sub.Columns, seen)
			delete(seen, sub)
		}
	}
	return nodes
}
