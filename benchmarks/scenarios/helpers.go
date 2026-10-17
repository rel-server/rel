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

package scenarios

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/bytedance/sonic"
	"github.com/rel-server/rel/benchmarks/harness"
)

type (
	srv = harness.Server
	env = harness.Env
	req = harness.Request
)

var jsonHeader = http.Header{"Content-Type": {"application/json"}}

func relReq(body map[string]any) req {
	b, err := sonic.Marshal(body)
	if err != nil {
		panic(err) // a scenario builds only marshalable values
	}
	return req{Method: "POST", Path: "/rel", Header: jsonHeader, Body: b}
}

func restGet(path string) req { return req{Method: "GET", Path: path} }

func gqlReq(query string) req {
	b, err := sonic.Marshal(map[string]any{"query": query})
	if err != nil {
		panic(err)
	}
	return req{Method: "POST", Path: "/graphql", Header: jsonHeader, Body: b}
}

// relQuery assembles a /rel read of table with fields.
func relQuery(table string, fields []Field, where any, order []any, limit, offset int) req {
	sel, join := relSelect(fields)
	q := map[string]any{"relation": table, "schema": "hotel", "select": sel}
	if len(join) > 0 {
		q["join"] = join
	}
	if where != nil {
		q["where"] = where
	}
	if order != nil {
		q["order_by"] = order
	}
	if limit > 0 {
		q["limit"] = limit
	}
	if offset > 0 {
		q["offset"] = offset
	}
	return relReq(q)
}

// restQuery assembles a PostgREST read ; query is the filter/order/page part.
func restQuery(table string, fields []Field, query string) req {
	p := "/" + table + "?select=" + url.QueryEscape(restSelect(fields))
	if query != "" {
		p += "&" + query
	}
	return restGet(p)
}

// gqlQuery assembles a GraphQL read. args is the argument list without parentheses.
func gqlQuery(root string, fields []Field, args string, single bool, d dialect) req {
	if args != "" {
		root += "(" + args + ")"
	}
	if single {
		return gqlReq(fmt.Sprintf("{ %s { %s } }", root, gqlSelect(fields, d)))
	}
	return gqlReq(fmt.Sprintf("{ %s { nodes { %s } } }", root, gqlSelect(fields, d)))
}

// idEnum is how each PostGraphile spells the order-by enum of the id column.
func (d dialect) idEnum() string {
	if d.v5 {
		return "ROW_ID"
	}
	return "ID"
}

// enum spells an enum value : v4 upper-cases them, v5 keeps the database's spelling.
func (d dialect) enum(v string) string {
	if d.v5 {
		return v
	}
	return strings.ToUpper(v)
}

func (d dialect) pk() string {
	if d.v5 {
		return "rowId"
	}
	return "id"
}

// read describes one read scenario. rel, rest and gql build the request for
// operation n ; n = 0 is the one used for the equivalence check.
type read struct {
	name    string
	fields  []Field
	gqlRoot string
	single  bool
	rel     func(n uint64, e *env) req
	rest    func(n uint64, e *env) req
	gql     func(n uint64, e *env, d dialect) req
	// notes per server, for the support column ; a missing server is Native.
	notes map[srv]string
	// unsupported lists servers that cannot express the scenario.
	unsupported map[srv]string
	allow       map[srv]string
}

func (r read) scenario() harness.Scenario {
	run := func(build func(n uint64, e *env) req) func(*harness.Client, *env, uint64) ([]byte, error) {
		return func(c *harness.Client, e *env, n uint64) ([]byte, error) { return c.Do(build(n, e)) }
	}
	impls := map[srv]harness.Impl{
		harness.Rel:       {Support: harness.Native, Run: run(r.rel), Normalize: normalizer(r.fields, srcRel, "", false)},
		harness.PostgREST: {Support: harness.Native, Run: run(r.rest), Normalize: normalizer(r.fields, srcRest, "", false)},
	}
	for _, d := range []dialect{{false}, {true}} {
		d := d
		impls[d.server()] = harness.Impl{
			Support:   harness.Native,
			Run:       run(func(n uint64, e *env) req { return r.gql(n, e, d) }),
			Normalize: normalizer(r.fields, srcGql, r.gqlRoot, r.single),
		}
	}
	for s, note := range r.notes {
		i := impls[s]
		i.Note = note
		impls[s] = i
	}
	for s := range r.unsupported {
		impls[s] = harness.Impl{Support: harness.Unsupported, Note: r.unsupported[s]}
	}
	return harness.Scenario{Name: r.name, Group: "read", Impls: impls, AllowMismatch: r.allow}
}

func pick[T any](xs []T, n uint64) T { return xs[int(n%uint64(len(xs)))] }
