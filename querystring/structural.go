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

// Package querystring decodes the URL query string of GET /rel and a route's `query` field into the JSON query
// shapes. It has a structural layer (dot-path keys -> nested objects, repeated keys -> arrays) and an expression
// grammar for `where` and comma-list entries.
//
// It only builds plain JSON-like Go values, which then go through query.ParseQuery like any JSON query.
package querystring

import (
	"net/url"
	"strings"

	"github.com/samber/oops"
)

// parseRawPairs splits on '&' by hand : net/url.ParseQuery rejects a raw ';', which the grammar uses.
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
		// QueryUnescape, not PathUnescape : '+' is a space.
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

// DecodeStructural decodes raw : each key is a dot-separated path into nested objects, a repeated key builds an
// array. Values stay strings ; the caller applies any coercion or expression grammar per key.
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

// setPath sets value at path under node, creating objects ; a repeated leaf key builds an array.
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
