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

// Package querystring implements specs/query-json.md : GET /rel's and
// /route's `query` field's textual encoding of a subset of query.ts's JSON
// shapes, carried in a URL query string. Two cooperating layers, per that
// spec :
//
//   - structural.go : the structural decoder (this file) — dot-path query
//     keys -> nested JSON, repeated keys -> arrays. Generic, reusable for
//     both Relation's own shape and /route's free-form `query` field.
//   - expr.go : the filter expression grammar — a small S-expression-style
//     parser for `where` and each comma-list entry.
//   - relation.go : glues both together into one GET /rel query string ->
//     query.ts Relation JSON pipeline, plus the read-only/single-relation
//     restriction check.
//
// Every function in this package builds a plain Go value tree (string,
// float64, bool, nil, []any, map[string]any) — never a second, parallel
// Expression AST. The tree is handed to sonic.Marshal and then to the
// EXISTING query.ParseQuery/query.ParseExpression pipeline, unchanged —
// reusing it keeps one JSON-consuming Expression parser in the codebase,
// not two to keep in sync.
package querystring

import (
	"net/url"
	"strings"

	"github.com/samber/oops"
)

// parseRawPairs splits on '&' only, by hand — not net/url.ParseQuery,
// which rejects any raw ';' even grammar-internal to a value (query-json.md).
func parseRawPairs(raw string) ([][2]string, error) {
	raw = strings.TrimPrefix(raw, "?")
	if raw == "" {
		return nil, nil
	}
	parts := strings.Split(raw, "&")
	out := make([][2]string, 0, len(parts))
	for _, p := range parts {
		if p == "" {
			continue
		}
		k, v, _ := strings.Cut(p, "=")
		// url.QueryUnescape, not url.PathUnescape : decodes '+' as a literal
		// space, which specs/query-json.md's examples rely on implicitly.
		kd, err := url.QueryUnescape(k)
		if err != nil {
			return nil, oops.Wrapf(err, "decoding query key %q", k)
		}
		vd, err := url.QueryUnescape(v)
		if err != nil {
			return nil, oops.Wrapf(err, "decoding query value for key %q", kd)
		}
		out = append(out, [2]string{kd, vd})
	}
	return out, nil
}

// DecodeStructural implements specs/query-json.md's ## Structural layer :
// every query key is a dot-separated path into a nested JSON object,
// repeating the exact same key builds an array. Values stay plain strings
// — a caller needing number/bool coercion or the filter expression grammar
// applies that on top, per key, since only the caller knows which keys
// need it (see relation.go).
func DecodeStructural(raw string) (map[string]any, error) {
	pairs, err := parseRawPairs(raw)
	if err != nil {
		return nil, err
	}
	root := map[string]any{}
	for _, kv := range pairs {
		path := strings.Split(kv[0], ".")
		for _, seg := range path {
			if seg == "" {
				return nil, oops.Errorf("query key %q has an empty path segment", kv[0])
			}
		}
		if err := setPath(root, path, kv[1]); err != nil {
			return nil, oops.Wrapf(err, "query key %q", kv[0])
		}
	}
	return root, nil
}

// setPath walks/creates node along path, setting the final segment to
// value. Repeating a leaf key builds an array — first repeat makes a []any.
func setPath(node map[string]any, path []string, value string) error {
	key := path[0]
	if len(path) == 1 {
		existing, ok := node[key]
		if !ok {
			node[key] = value
			return nil
		}
		switch e := existing.(type) {
		case string:
			node[key] = []any{e, value}
		case []any:
			node[key] = append(e, value)
		default:
			return oops.Errorf("key %q is used both as a value and as a nested path", key)
		}
		return nil
	}

	child, ok := node[key]
	var childMap map[string]any
	if !ok {
		childMap = map[string]any{}
		node[key] = childMap
	} else {
		childMap, ok = child.(map[string]any)
		if !ok {
			return oops.Errorf("key %q is used both as a value and as a nested path", key)
		}
	}
	return setPath(childMap, path[1:], value)
}
