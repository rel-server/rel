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

// OperatorWords is the single source of truth for specs/query-json.md's
// "Filter expression grammar" operator table : every word-form spelling
// (used by the query-string filter-expression grammar's `call` identifier,
// see the querystring package) mapped 1:1 onto the exact query.ts tag string
// it means. Two things reuse this exact table, never a second copy :
//
//  1. The querystring package's filter-expression-grammar compiler, which
//     looks up a `call`'s bare identifier here to decide whether it's a
//     known operator (and which query.ts tag/arity it compiles to) or an
//     unrecognized name that falls back to `["call", ident, ...args]`.
//  2. This package's own pass-1 JSON parsing (parseArrayExpression in
//     expression_parse.go), which normalizes a word-form tag to its
//     canonical query.ts spelling immediately upon reading it — see that
//     file's use of operatorWordToCanonical.
//
// Entries whose word form is spelled identically to the canonical query.ts
// tag (e.g. "and", "in", "between", "agg", "like") are included too, as
// harmless identity mappings — this keeps the table a complete, exhaustive
// transcription of specs/query-json.md's operator table, not just the
// entries that happen to differ.
var OperatorWords = map[string]string{
	"and": "and",
	"or":  "or",

	"add": "+",
	"sub": "-",
	"mul": "*",
	"div": "/",
	"pow": "^",
	"mod": "%",

	"bor":  "|",
	"band": "&",

	"json_get":       "->",
	"json_get_text":  "->>",
	"json_path":      "#>",
	"json_path_text": "#>>",

	"dot":             ".",
	"concat":          "||",
	"concat_coalesce": "||?",
	"ifnull":          "??",

	"lte": "<=",
	"gte": ">=",
	"lt":  "<",
	"gt":  ">",
	"eq":  "=",
	"ne":  "<>",

	"is_distinct_from":     "is_distinct_from",
	"is_not_distinct_from": "is_not_distinct_from",

	"neg":  "-",
	"not":  "not",
	"bnot": "~",

	"is_null":      "is_null",
	"is_true":      "is_true",
	"is_false":     "is_false",
	"is_not_null":  "is_not_null",
	"is_not_true":  "is_not_true",
	"is_not_false": "is_not_false",

	"sqrt": "|/",
	"cbrt": "||/",

	"like":   "like",
	"ilike":  "ilike",
	"match":  "~",
	"imatch": "~*",
	"cast":   "::",

	"overlap":  "&&",
	"distance": "<->",
	"adjacent": "-|-",
	"shl":      "<<",
	"shr":      ">>",

	"contains":     "@>",
	"contained_by": "<@",

	"has_key":      "?",
	"has_any_key":  "?|",
	"has_all_keys": "?&",

	"overlaps_or_left":  "&<",
	"overlaps_or_right": "&>",

	// op_qcolon : name explicitly marked TBD by specs/query-json.md — kept
	// as-is, this is a naming choice not a semantic one (see that file's
	// note directly under the operator table).
	"op_qcolon":  "?:",
	"matches_ts": "@@",

	"in":     "in",
	"not_in": "not_in",
	"any":    "any",
	"all":    "all",

	"between":     "between",
	"not_between": "not_between",

	"agg":       "agg",
	"aggregate": "agg",
	"call":      "call",

	"concat_ws": "concat_ws",
	"coalesce":  "coalesce",
	"format":    "format",
}
