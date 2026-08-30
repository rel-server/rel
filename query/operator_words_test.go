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

package query

import (
	"reflect"
	"testing"
)

// TestOperatorWordSynonym_ProducesIdenticalTreeToSymbolForm is point 4 of
// this session's task : specs/query_json.md's word-form operator spelling
// must be an accepted synonym in POST /rel's JSON body, resolving to the
// EXACT same tree the canonical symbolic spelling already produces.
func TestOperatorWordSynonym_ProducesIdenticalTreeToSymbolForm(t *testing.T) {
	cases := []struct {
		word   string
		symbol string
	}{
		{`["gte", "year", 1999]`, `[">=", "year", 1999]`},
		{`["eq", "active", true]`, `["=", "active", true]`},
		{`["is_null", "name"]`, `["is-null", "name"]`},
		{`["not_in", "status", "open", "closed"]`, `["not-in", "status", "open", "closed"]`},
		{`["and", ["gte", "year", 1999], ["lt", "year", 2020]]`, `["and", [">=", "year", 1999], ["<", "year", 2020]]`},
	}
	for _, tc := range cases {
		wordExpr, err := ParseExpression([]byte(tc.word))
		if err != nil {
			t.Fatalf("ParseExpression(%s): %v", tc.word, err)
		}
		symbolExpr, err := ParseExpression([]byte(tc.symbol))
		if err != nil {
			t.Fatalf("ParseExpression(%s): %v", tc.symbol, err)
		}
		if !reflect.DeepEqual(wordExpr, symbolExpr) {
			t.Errorf("word form %s produced a different tree than symbol form %s :\n  word:   %#v\n  symbol: %#v",
				tc.word, tc.symbol, wordExpr, symbolExpr)
		}
	}
}

// TestOperatorWordSynonym_AnyAllOperatorPosition covers any/all's own
// second-position operator string, which parseArrayExpression normalizes
// separately from the leading array tag.
func TestOperatorWordSynonym_AnyAllOperatorPosition(t *testing.T) {
	wordExpr, err := ParseExpression([]byte(`["any", "gte", "x", "arr"]`))
	if err != nil {
		t.Fatalf("ParseExpression: %v", err)
	}
	symbolExpr, err := ParseExpression([]byte(`["any", ">=", "x", "arr"]`))
	if err != nil {
		t.Fatalf("ParseExpression: %v", err)
	}
	if !reflect.DeepEqual(wordExpr, symbolExpr) {
		t.Errorf("word-form any() operator produced a different tree :\n  word:   %#v\n  symbol: %#v", wordExpr, symbolExpr)
	}
}

// TestOperatorWords_NoCollisionWithExistingTags is this session's
// mechanical safety net for the "strictly additive, non-breaking" claim :
// every word form that differs from its own canonical spelling must not
// already mean something else as a raw array tag (an existing unary/
// binary/folded operator key, or a fixed keyword parseArrayExpression's
// switch dispatches on directly) — otherwise adding the synonym would
// silently reinterpret existing, already-persisted JSON.
func TestOperatorWords_NoCollisionWithExistingTags(t *testing.T) {
	fixedKeywords := map[string]bool{
		"between": true, "not-between": true, "bigint": true, "numeric": true,
		"in": true, "not-in": true, "any": true, "all": true,
		"concat_ws": true, "coalesce": true, "format": true,
		// "aggregate" deliberately excluded : parseArrayExpression's own
		// switch already treats "agg" and "aggregate" as exact synonyms
		// (case "agg", "aggregate":), so OperatorWords normalizing
		// "aggregate" -> "agg" doesn't change behavior — it's the harmless
		// kind of overlap, not the "silently reinterprets existing JSON"
		// kind this test actually guards against.
		"agg": true, "call": true,
		"own": true, "full": true,
		"own-except": true, "full-except": true,
		"own-and": true, "full-and": true,
		"own-except-and": true, "full-except-and": true,
		"arr": true, "array": true, "lst": true, "list": true,
		"index": true, "slice": true,
		"get-set": true, "get": true, "set": true,
		"$param": true,
	}

	for word, canonical := range OperatorWords {
		if word == canonical {
			continue // identity mapping, nothing to collide with
		}
		if _, ok := unaryOperators[word]; ok {
			t.Errorf("word form %q collides with an existing UnaryOperator tag", word)
		}
		if _, ok := binaryOperators[word]; ok {
			t.Errorf("word form %q collides with an existing BinaryOperator tag", word)
		}
		if _, ok := foldedOperators[word]; ok {
			t.Errorf("word form %q collides with an existing FoldedOperator tag", word)
		}
		if fixedKeywords[word] {
			t.Errorf("word form %q collides with an existing fixed-keyword tag", word)
		}
	}
}
