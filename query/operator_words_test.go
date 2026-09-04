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

// TestOperatorWordSynonym_ProducesIdenticalTreeToSymbolForm checks the
// word-form spelling (specs/query-json.md) resolves to the identical tree.
func TestOperatorWordSynonym_ProducesIdenticalTreeToSymbolForm(t *testing.T) {
	cases := []struct {
		word   string
		symbol string
	}{
		{`["gte", "year", 1999]`, `[">=", "year", 1999]`},
		{`["eq", "active", true]`, `["=", "active", true]`},
		{`["is_null", "name"]`, `["is_null", "name"]`},
		{`["not_in", "status", "open", "closed"]`, `["not_in", "status", "open", "closed"]`},
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

// TestOperatorWordSynonym_AnyAllOperatorPosition covers any/all's second
// operator position, normalized separately from the leading array tag.
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

// TestOperatorWords_NoCollisionWithExistingTags checks no word form collides
// with an existing tag, which would silently reinterpret persisted JSON.
func TestOperatorWords_NoCollisionWithExistingTags(t *testing.T) {
	fixedKeywords := map[string]bool{
		"between": true, "not_between": true, "bigint": true, "numeric": true,
		"in": true, "not_in": true, "any": true, "all": true,
		"concat_ws": true, "coalesce": true, "format": true,
		// "aggregate" excluded: parseArrayExpression already treats it as
		// a synonym of "agg", so the overlap is harmless, not a collision.
		"agg": true, "call": true,
		"own": true, "full": true,
		"own_except": true, "full_except": true,
		"own_and": true, "full_and": true,
		"own_except_and": true, "full_except_and": true,
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
