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

func TestComputedFields_ScalarEligibleFunction(t *testing.T) {
	director := relationByName(t, "director")

	fn := director.ComputedFields["director_display_name"]
	if fn == nil {
		t.Fatalf("expected director.ComputedFields to include director_display_name, got %v", director.ComputedFields)
	}
	if fn.Identifier.Name != "director_display_name" {
		t.Errorf("expected the registered function to be director_display_name, got %q", fn.Identifier.Name)
	}
}

func TestComputedFields_SetofRelationReturnIsStillEligible(t *testing.T) {
	director := relationByName(t, "director")

	if director.ComputedFields["director_movies"] == nil {
		t.Fatalf("expected director.ComputedFields to include director_movies (a SETOF-returning computed field), got %v", director.ComputedFields)
	}
}

func TestComputedFields_RealColumnWinsOverSameNamedFunction(t *testing.T) {
	movie := relationByName(t, "movie")

	if movie.ColumnsMap["title"] == nil {
		t.Fatalf("expected movie.title to exist as a real column (fixture assumption)")
	}
	if fn := movie.ComputedFields["title"]; fn != nil {
		t.Errorf("expected movie.ComputedFields NOT to include \"title\" — a real column of that name already exists, got %v", fn)
	}
}

func TestComputedFields_CrossSchemaFunctionExcluded(t *testing.T) {
	director := relationByName(t, "director")

	if fn := director.ComputedFields["director_tagline"]; fn != nil {
		t.Errorf("expected director.ComputedFields NOT to include director_tagline — it's declared in alt_schema, not director's own schema, got %v", fn)
	}
}

func TestComputedFields_NeverIncludedInColumnsMap(t *testing.T) {
	director := relationByName(t, "director")

	if director.ColumnsMap["director_display_name"] != nil {
		t.Errorf("expected a computed field to never appear in ColumnsMap — own/full must never auto-include it")
	}
}

func TestComputedFields_UnrelatedRelationsGetEmptyMapNotNil(t *testing.T) {
	target := relationByName(t, "target_t")

	if target.ComputedFields == nil {
		t.Errorf("expected ComputedFields to be an initialized empty map, not nil, even when nothing is eligible")
	}
	if len(target.ComputedFields) != 0 {
		t.Errorf("expected target_t to have no eligible computed fields, got %v", target.ComputedFields)
	}
}
