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

// OperatorWords maps every word-form operator spelling onto its query.ts tag ; the only copy of the table. The
// querystring filter grammar uses it to tell a known operator from a plain `["call", ...]`, and the JSON parser
// to normalize word-form tags. Identity entries ("and", "in", ...) are kept so the table is exhaustive.
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

	// op_qcolon : name still provisional.
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
