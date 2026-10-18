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

import "testing"

func TestColumn_DeclaredType(t *testing.T) {
	cases := []struct {
		relation, column, want string
	}{
		{"typmod_t", "a", "character varying(20)"},
		{"typmod_t", "b", "numeric(10,2)"},
		{"typmod_t", "c", "text"},
		{"typmod_t", "d", "short_text"},
		{"typmod_t", "e", "timestamp(3) without time zone"},
		{"typmod_t", "f", "character varying(5)[]"},
		{"typmod_t", "g", "typmod_comp"},
		{"typmod_t", "h", "integer"},
		// a view's and a materialized view's columns repeat their source's modifiers
		{"typmod_v", "a", "character varying(20)"},
		{"typmod_v", "b", "numeric(10,2)"},
		{"typmod_m", "a", "character varying(20)"},
		{"typmod_m", "b", "numeric(10,2)"},
	}
	for _, c := range cases {
		col := relationByName(t, c.relation).ColumnsMap[c.column]
		if col == nil {
			t.Errorf("%s.%s: no such column", c.relation, c.column)
			continue
		}
		if col.DeclaredType != c.want {
			t.Errorf("%s.%s: DeclaredType = %q, want %q", c.relation, c.column, col.DeclaredType, c.want)
		}
	}
}

// A standalone composite type's attributes carry their modifiers too.
func TestColumn_DeclaredType_CompositeAttributes(t *testing.T) {
	comp := relationByName(t, "typmod_t").ColumnsMap["g"].Type.CompositeRelation()
	if comp == nil {
		t.Fatal("typmod_comp has no composite relation")
	}
	for name, want := range map[string]string{"code": "character varying(3)", "amount": "numeric(6,1)"} {
		if got := comp.ColumnsMap[name].DeclaredType; got != want {
			t.Errorf("typmod_comp.%s: DeclaredType = %q, want %q", name, got, want)
		}
	}
}

// Every introspected column, whatever its relation kind, has one.
func TestColumn_DeclaredType_NeverEmptyOnTables(t *testing.T) {
	for _, r := range testDb.Relations {
		for _, c := range r.Columns {
			if c.DeclaredType == "" {
				t.Errorf("%s.%s has no DeclaredType", r.Identifier.String(), c.Name)
			}
		}
	}
}
