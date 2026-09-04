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

func TestType_CompositeRelation_Direct(t *testing.T) {
	venue := relationByName(t, "venue")
	home := venue.ColumnsMap["home"]
	if home == nil {
		t.Fatalf("expected venue.home to exist")
	}
	if home.Type.CompositeRelation() == nil {
		t.Errorf("expected venue.home (addr_t) to report a composite relation directly")
	}
}

func TestType_CompositeRelation_ThroughDomain(t *testing.T) {
	// depot.location is plain composite (nested_t) ; the domain wrapping
	// only shows up one level deeper, on nested_t's own "addr" field.
	depot := relationByName(t, "depot")
	loc := depot.ColumnsMap["location"]
	if loc == nil {
		t.Fatalf("expected depot.location to exist")
	}
	nestedRel := loc.Type.CompositeRelation()
	if nestedRel == nil {
		t.Fatalf("expected depot.location (nested_t) to report a composite relation directly")
	}

	addrField := nestedRel.ColumnsMap["addr"]
	if addrField == nil {
		t.Fatalf("expected nested_t.addr to exist")
	}
	if addrField.Type.IsComposite() {
		t.Errorf("expected IsComposite() checked directly on nested_t.addr (a domain) to be false (regression guard : this is the bug being fixed)")
	}
	rel := addrField.Type.CompositeRelation()
	if rel == nil {
		t.Fatalf("expected CompositeRelation() to unwrap the domain and find addr_t's relation")
	}
	if rel.ColumnsMap["city"] == nil {
		t.Errorf("expected the unwrapped composite relation to have a \"city\" column")
	}
}

func TestType_CompositeRelation_NotComposite(t *testing.T) {
	director := relationByName(t, "director")
	name := director.ColumnsMap["name"]
	if name.Type.CompositeRelation() != nil {
		t.Errorf("expected a plain text column to have no composite relation")
	}
}
