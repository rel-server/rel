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

import (
	"fmt"
	"sync"
	"testing"
)

func plainCols(names ...string) []*Column {
	cols := make([]*Column, len(names))
	for i, n := range names {
		cols[i] = &Column{Name: n, Type: &Type{}}
	}
	return cols
}

func numberedCols(n int) []*Column {
	names := make([]string, n)
	for i := range names {
		names[i] = fmt.Sprintf("c%d", i)
	}
	return plainCols(names...)
}

// composite makes a column whose type is a composite of cols.
func composite(name string, cols ...*Column) *Column {
	return &Column{Name: name, Type: &Type{Relation: &Relation{Columns: cols}}}
}

func pathNames(s MaskSlot) string {
	out := ""
	for i, c := range s.Path {
		if i > 0 {
			out += "."
		}
		out += c.Name
	}
	return out
}

func slotNames(r *Relation) []string {
	var out []string
	for _, s := range r.MaskSlots() {
		out = append(out, pathNames(s))
	}
	return out
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestMaskWords_Boundaries(t *testing.T) {
	for _, c := range []struct{ slots, words int }{
		{0, 1}, {1, 1}, {63, 1}, {64, 1}, {65, 2}, {127, 2}, {128, 2}, {129, 3}, {192, 3}, {193, 4},
	} {
		r := &Relation{Columns: numberedCols(c.slots)}
		if got := r.MaskWords(); got != c.words {
			t.Errorf("%d slots: MaskWords() = %d, want %d", c.slots, got, c.words)
		}
		if got := len(r.MaskSlots()); got != c.slots {
			t.Errorf("%d columns: %d slots", c.slots, got)
		}
	}
}

func TestMaskSlots_WordAndBitPositions(t *testing.T) {
	r := &Relation{Columns: numberedCols(130)}
	for i, s := range r.MaskSlots() {
		if s.Word != i/64 || s.Bit != i%64 {
			t.Errorf("slot %d: word %d bit %d, want word %d bit %d", i, s.Word, s.Bit, i/64, i%64)
		}
	}
	last, _ := r.MaskSlotFor(r.Columns[129])
	if last.Word != 2 || last.Bit != 1 {
		t.Errorf("column 129: word %d bit %d, want word 2 bit 1", last.Word, last.Bit)
	}
}

func TestMaskSlots_CompositeExpansionOrder(t *testing.T) {
	city := composite("city", plainCols("zip", "country")...)
	home := composite("home", append(plainCols("street"), city)...)
	r := &Relation{Columns: append(append(plainCols("a"), home), plainCols("b")...)}

	want := []string{"a", "home", "home.street", "home.city", "home.city.zip", "home.city.country", "b"}
	if got := slotNames(r); !equalStrings(got, want) {
		t.Errorf("slots = %v, want %v", got, want)
	}
}

func TestMaskSlots_DomainOverCompositeIsExpanded(t *testing.T) {
	inner := composite("inner", plainCols("x", "y")...)
	dom := &Column{Name: "d", Type: &Type{BaseType: inner.Type}}
	r := &Relation{Columns: []*Column{dom}}

	want := []string{"d", "d.x", "d.y"}
	if got := slotNames(r); !equalStrings(got, want) {
		t.Errorf("slots = %v, want %v", got, want)
	}
}

func TestMaskSlots_ArraysAreNotExpanded(t *testing.T) {
	elem := &Type{Relation: &Relation{Columns: plainCols("x", "y")}}
	arr := &Type{ElementType: elem}
	elem.ArrayType = arr
	if !arr.IsArray() {
		t.Fatal("fixture: expected an array type")
	}
	r := &Relation{Columns: []*Column{{Name: "pts", Type: arr}, {Name: "after", Type: &Type{}}}}

	want := []string{"pts", "after"}
	if got := slotNames(r); !equalStrings(got, want) {
		t.Errorf("slots = %v, want %v", got, want)
	}
}

func TestMaskSlots_SelfContainingCompositeTerminates(t *testing.T) {
	rel := &Relation{}
	self := &Column{Name: "self", Type: &Type{Relation: rel}}
	rel.Columns = []*Column{self}
	r := &Relation{Columns: []*Column{{Name: "c", Type: &Type{Relation: rel}}}}

	want := []string{"c", "c.self"}
	if got := slotNames(r); !equalStrings(got, want) {
		t.Errorf("slots = %v, want %v", got, want)
	}
}

func TestMaskSlotFor_RoundTrip(t *testing.T) {
	street := &Column{Name: "street", Type: &Type{}}
	home := composite("home", street)
	id := &Column{Name: "id", Type: &Type{}}
	r := &Relation{Columns: []*Column{id, home}}

	for i, s := range r.MaskSlots() {
		got, ok := r.MaskSlotFor(s.Path...)
		if !ok || got.Word != s.Word || got.Bit != s.Bit || pathNames(got) != pathNames(s) {
			t.Errorf("slot %d (%s): lookup gave %+v, ok=%v", i, pathNames(s), got, ok)
		}
	}
	if s, ok := r.MaskSlotFor(home, street); !ok || s.Bit != 2 {
		t.Errorf("home.street: %+v ok=%v, want bit 2", s, ok)
	}

	other := &Column{Name: "id", Type: &Type{}}
	for name, path := range map[string][]*Column{
		"foreign column":      {other},
		"empty path":          nil,
		"sub-field of plain":  {id, street},
		"sub-field wrong way": {street},
	} {
		if s, ok := r.MaskSlotFor(path...); ok {
			t.Errorf("%s: found %+v", name, s)
		}
	}
}

func TestMaskSlots_ConcurrentFirstUse(t *testing.T) {
	r := &Relation{Columns: append(numberedCols(70), composite("c", plainCols("x")...))}
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if n := len(r.MaskSlots()); n != 72 {
				t.Errorf("slots = %d, want 72", n)
			}
			if r.MaskWords() != 2 {
				t.Errorf("words = %d, want 2", r.MaskWords())
			}
		}()
	}
	wg.Wait()
}

func TestMaskSlots_Introspected(t *testing.T) {
	venue := relationByName(t, "venue")
	want := []string{"id", "name", "home", "home.street", "home.city", "work", "work.street", "work.city", "metadata"}
	if got := slotNames(venue); !equalStrings(got, want) {
		t.Errorf("venue slots = %v, want %v", got, want)
	}
	if venue.MaskWords() != 1 {
		t.Errorf("venue words = %d", venue.MaskWords())
	}
	home := venue.ColumnsMap["home"]
	city := home.Type.CompositeRelation().ColumnsMap["city"]
	if s, ok := venue.MaskSlotFor(home, city); !ok || s.Bit != 4 {
		t.Errorf("home.city: %+v ok=%v, want bit 4", s, ok)
	}
}

// Two introspections of the same schema lay every relation out identically.
func TestMaskSlots_StableAcrossIntrospections(t *testing.T) {
	again, err := NewInfos(testDbURI)
	if err != nil {
		t.Fatal(err)
	}
	byRelid := map[int]*Relation{}
	for _, r := range again.Relations {
		byRelid[r.PgRelId] = r
	}
	checked := 0
	for _, r := range testDb.Relations {
		other, ok := byRelid[r.PgRelId]
		if !ok {
			t.Fatalf("relation %s missing from the second introspection", r.Identifier.String())
		}
		a, b := r.MaskSlots(), other.MaskSlots()
		if len(a) != len(b) || r.MaskWords() != other.MaskWords() {
			t.Fatalf("%s: %d slots / %d words, then %d / %d", r.Identifier.String(), len(a), r.MaskWords(), len(b), other.MaskWords())
		}
		for i := range a {
			if pathNames(a[i]) != pathNames(b[i]) || a[i].Word != b[i].Word || a[i].Bit != b[i].Bit {
				t.Fatalf("%s: slot %d is %s, then %s", r.Identifier.String(), i, pathNames(a[i]), pathNames(b[i]))
			}
		}
		checked++
	}
	if checked == 0 {
		t.Fatal("no relation compared")
	}
}
