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
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/bytedance/sonic"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rel-server/rel/benchmarks/harness"
	"github.com/samber/oops"
)

const (
	bulkRows   = 100 // rows per request in write/bulk-insert
	upsertKeys = 50  // distinct emails write/upsert cycles through
)

var returnRep = http.Header{"Content-Type": {"application/json"}, "Prefer": {"return=representation"}}

func restWrite(method, path string, body any, prefer string) req {
	h := http.Header{"Content-Type": {"application/json"}, "Prefer": {prefer}}
	if body == nil {
		return req{Method: method, Path: path, Header: h}
	}
	b, err := sonic.Marshal(body)
	if err != nil {
		panic(err)
	}
	return req{Method: method, Path: path, Header: h, Body: b}
}

func relWrite(query map[string]any, data any) req {
	return relReq(map[string]any{"query": query, "data": data})
}

func sel(names ...string) map[string]any {
	m := map[string]any{}
	for _, n := range names {
		m[n] = col(n)
	}
	return m
}

// count runs a single-integer query and compares it with want.
func expectCount(ctx context.Context, db *pgxpool.Pool, what, sql string, want int) error {
	var got int
	if err := db.QueryRow(ctx, sql).Scan(&got); err != nil {
		return oops.With("sql", sql).Wrapf(err, "checking %s", what)
	}
	if got != want {
		return oops.Errorf("%s: database has %d, expected %d", what, got, want)
	}
	return nil
}

// jsonID reads the first "id" (or other key) from the first row of a
// PostgREST representation, `[{"id":1}]`.
func restID(body []byte) (int64, error) {
	n, err := sonic.Get(body, 0, "id")
	if err != nil {
		return 0, oops.With("body", string(body)).Wrapf(err, "reading id from response")
	}
	return n.Int64()
}

// gqlID reads data.<root>.<obj>.<key> as an integer ; PostGraphile sends bigints as strings.
func gqlID(body []byte, root, obj, key string) (int64, error) {
	n, err := sonic.Get(body, "data", root, obj, key)
	if err != nil {
		return 0, oops.With("body", string(body)).Wrapf(err, "reading %s from GraphQL response", key)
	}
	s, err := n.String()
	if err != nil {
		return 0, oops.Wrapf(err, "id is not a string")
	}
	var id int64
	if _, err := fmt.Sscan(s, &id); err != nil {
		return 0, oops.Wrapf(err, "parsing id %q", s)
	}
	return id, nil
}

// gqlNoErrors turns a 200 response carrying GraphQL errors into an error.
func gqlDo(c *harness.Client, mutation string, vars ...gvar) ([]byte, error) {
	body, err := c.Do(gqlOp("mutation", mutation, vars))
	if err != nil {
		return body, err
	}
	if errs, err := sonic.Get(body, "errors"); err == nil {
		if raw, _ := errs.Raw(); raw != "" {
			return body, oops.With("body", string(body)).Errorf("graphql errors")
		}
	}
	return body, nil
}

func writes() []harness.Scenario {
	var prepared int // set by write/delete's Prepare, read by its Check (they run one after the other)

	insert := harness.Scenario{
		Name: "write/insert-one", Group: "write", Write: true,
		Check: func(ctx context.Context, db *pgxpool.Pool, ok int) error {
			return expectCount(ctx, db, "inserted guests", `select count(*) from hotel.guests where email like 'bench-ins-%'`, ok)
		},
		Impls: map[srv]harness.Impl{
			harness.Rel: {Support: harness.Native, Run: func(c *harness.Client, e *env, n uint64) ([]byte, error) {
				return c.Do(relWrite(map[string]any{"relation": "guests", "schema": "hotel", "select": sel("id", "email", "first_name", "last_name")},
					[]any{map[string]any{"email": fmt.Sprintf("bench-ins-%d@example.com", n), "first_name": "Ada", "last_name": "Bench"}}))
			}},
			harness.PostgREST: {Support: harness.Native, Run: func(c *harness.Client, e *env, n uint64) ([]byte, error) {
				return c.Do(restWrite("POST", "/guests?select=id,email,first_name,last_name", map[string]any{"email": fmt.Sprintf("bench-ins-%d@example.com", n), "first_name": "Ada", "last_name": "Bench"}, "return=representation"))
			}},
		},
	}
	for _, d := range []dialect{{false}, {true}} {
		d := d
		insert.Impls[d.server()] = harness.Impl{Support: harness.Native, Run: func(c *harness.Client, e *env, n uint64) ([]byte, error) {
			return gqlDo(c, fmt.Sprintf(`{ createGuest(input: {guest: {email: $email, firstName: "Ada", lastName: "Bench"}}) { guest { %s email firstName lastName } } }`, d.pk()),
				gvar{"email", "String!", fmt.Sprintf("bench-ins-%d@example.com", n)})
		}}
	}

	update := harness.Scenario{
		Name: "write/update-by-pk", Group: "write", Write: true,
		Check: func(ctx context.Context, db *pgxpool.Pool, ok int) error {
			var total int
			if err := db.QueryRow(ctx, `select count(*) from hotel.guests`).Scan(&total); err != nil {
				return oops.Wrapf(err, "counting guests")
			}
			return expectCount(ctx, db, "updated guests", `select count(*) from hotel.guests where phone like 'bench-upd-%'`, min(ok, total))
		},
		Impls: map[srv]harness.Impl{
			harness.Rel: {Support: harness.Native, Run: func(c *harness.Client, e *env, n uint64) ([]byte, error) {
				return c.Do(relWrite(map[string]any{"relation": "guests", "schema": "hotel", "write_mode": "update",
					"where": []any{"=", col("id"), pick(e.GuestIDs, n)}, "select": sel("id", "phone")},
					map[string]any{"phone": fmt.Sprintf("bench-upd-%d", n)}))
			}},
			harness.PostgREST: {Support: harness.Native, Run: func(c *harness.Client, e *env, n uint64) ([]byte, error) {
				return c.Do(restWrite("PATCH", fmt.Sprintf("/guests?id=eq.%d&select=id,phone", pick(e.GuestIDs, n)), map[string]any{"phone": fmt.Sprintf("bench-upd-%d", n)}, "return=representation"))
			}},
		},
	}
	for _, d := range []dialect{{false}, {true}} {
		d := d
		update.Impls[d.server()] = harness.Impl{Support: harness.Native, Run: func(c *harness.Client, e *env, n uint64) ([]byte, error) {
			return gqlDo(c, fmt.Sprintf(`{ updateGuest(input: {%s: $id, patch: {phone: $phone}}) { guest { %s phone } } }`, d.pk(), d.pk()),
				gvar{"id", "BigInt!", fmt.Sprint(pick(e.GuestIDs, n))}, gvar{"phone", "String", fmt.Sprintf("bench-upd-%d", n)})
		}}
	}

	del := harness.Scenario{
		Name: "write/delete-by-pk", Group: "write", Write: true,
		Prepare: func(ctx context.Context, db *pgxpool.Pool, ops int) error {
			prepared = ops
			_, err := db.Exec(ctx, `insert into hotel.amenities (name) select 'scratch-' || i from generate_series(1, $1) i`, ops)
			return oops.Wrapf(err, "inserting scratch amenities")
		},
		Check: func(ctx context.Context, db *pgxpool.Pool, ok int) error {
			return expectCount(ctx, db, "remaining scratch amenities", `select count(*) from hotel.amenities where name like 'scratch-%'`, prepared-ok)
		},
		Impls: map[srv]harness.Impl{
			harness.Rel: {Support: harness.Native, Run: func(c *harness.Client, e *env, n uint64) ([]byte, error) {
				return c.Do(relReq(map[string]any{"query": map[string]any{"relation": "amenities", "schema": "hotel", "write_mode": "delete",
					"where": []any{"=", col("id"), pick(e.ScratchIDs, n)}}}))
			}},
			harness.PostgREST: {Support: harness.Native, Run: func(c *harness.Client, e *env, n uint64) ([]byte, error) {
				return c.Do(restWrite("DELETE", fmt.Sprintf("/amenities?id=eq.%d&select=id", pick(e.ScratchIDs, n)), nil, "return=representation"))
			}},
		},
	}
	for _, d := range []dialect{{false}, {true}} {
		d := d
		del.Impls[d.server()] = harness.Impl{Support: harness.Native, Run: func(c *harness.Client, e *env, n uint64) ([]byte, error) {
			return gqlDo(c, fmt.Sprintf(`{ deleteAmenity(input: {%s: $id}) { amenity { %s } } }`, d.pk(), d.pk()),
				gvar{"id", "BigInt!", fmt.Sprint(pick(e.ScratchIDs, n))})
		}}
	}

	bulk := harness.Scenario{
		Name: "write/bulk-insert-100", Group: "write", Write: true,
		Check: func(ctx context.Context, db *pgxpool.Pool, ok int) error {
			return expectCount(ctx, db, "bulk-inserted amenities", `select count(*) from hotel.amenities where name like 'bench-bulk-%'`, ok*bulkRows)
		},
		Impls: map[srv]harness.Impl{
			harness.Rel: {Support: harness.Native, Run: func(c *harness.Client, e *env, n uint64) ([]byte, error) {
				return c.Do(relWrite(map[string]any{"relation": "amenities", "schema": "hotel", "select": sel("id", "name")}, bulkAmenities(n)))
			}},
			harness.PostgREST: {Support: harness.Native, Run: func(c *harness.Client, e *env, n uint64) ([]byte, error) {
				return c.Do(restWrite("POST", "/amenities?select=id,name", bulkAmenities(n), "return=representation"))
			}},
		},
	}
	for _, d := range []dialect{{false}, {true}} {
		d := d
		bulk.Impls[d.server()] = harness.Impl{Support: harness.Emulated, Note: "1 request, 100 mutations", Run: func(c *harness.Client, e *env, n uint64) ([]byte, error) {
			var b strings.Builder
			vars := make([]gvar, bulkRows)
			b.WriteString("{")
			for i := range bulkRows {
				fmt.Fprintf(&b, ` m%d: createAmenity(input: {amenity: {name: $n%d}}) { amenity { %s name } }`, i, i, d.pk())
				vars[i] = gvar{fmt.Sprintf("n%d", i), "String!", fmt.Sprintf("bench-bulk-%d-%d", n, i)}
			}
			b.WriteString(" }")
			return gqlDo(c, b.String(), vars...)
		}}
	}

	nested := harness.Scenario{
		Name: "write/nested-three-level", Group: "write", Write: true,
		Check: func(ctx context.Context, db *pgxpool.Pool, ok int) error {
			if err := expectCount(ctx, db, "nested chains", `select count(*) from hotel.chains where name like 'bench-nest-%'`, ok); err != nil {
				return err
			}
			if err := expectCount(ctx, db, "nested properties", `select count(*) from hotel.properties where name like 'bench-nest-%'`, ok); err != nil {
				return err
			}
			return expectCount(ctx, db, "nested room types", `select count(*) from hotel.room_types rt join hotel.properties p on p.id = rt.property_id where p.name like 'bench-nest-%'`, 2*ok)
		},
		Impls: map[srv]harness.Impl{
			harness.Rel:       {Support: harness.Native, Run: relNested},
			harness.PostgREST: {Support: harness.Emulated, Note: "3 round trips", Run: restNested},
		},
	}
	nested.Impls[harness.PostGraphile4] = harness.Impl{Support: harness.Native, Note: "nested-mutations plugin", Run: func(c *harness.Client, e *env, n uint64) ([]byte, error) {
		return gqlDo(c, `{ createChain(input: {chain: {name: $name, propertiesUsingId: {create: [{name: $name, starRating: 4, roomTypesUsingId: {create: [
			{name: "Standard", basePrice: "129.00", capacity: 2}, {name: "Suite", basePrice: "249.00", capacity: 4}]}}]}}}) { chain { id } } }`,
			gvar{"name", "String!", fmt.Sprintf("bench-nest-%d", n)})
	}}
	nested.Impls[harness.PostGraphile5] = harness.Impl{Support: harness.Emulated, Note: "3 round trips", Run: func(c *harness.Client, e *env, n uint64) ([]byte, error) {
		body, err := gqlDo(c, `{ createChain(input: {chain: {name: $name}}) { chain { rowId } } }`,
			gvar{"name", "String!", fmt.Sprintf("bench-nest-%d", n)})
		if err != nil {
			return nil, err
		}
		chain, err := gqlID(body, "createChain", "chain", "rowId")
		if err != nil {
			return nil, err
		}
		body, err = gqlDo(c, `{ createProperty(input: {property: {chainId: $chain, name: $name, starRating: 4}}) { property { rowId } } }`,
			gvar{"chain", "BigInt!", fmt.Sprint(chain)}, gvar{"name", "String!", fmt.Sprintf("bench-nest-%d", n)})
		if err != nil {
			return nil, err
		}
		prop, err := gqlID(body, "createProperty", "property", "rowId")
		if err != nil {
			return nil, err
		}
		return gqlDo(c, `{
			a: createRoomType(input: {roomType: {propertyId: $prop, name: "Standard", basePrice: "129.00", capacity: 2}}) { roomType { rowId } }
			b: createRoomType(input: {roomType: {propertyId: $prop, name: "Suite", basePrice: "249.00", capacity: 4}}) { roomType { rowId } } }`,
			gvar{"prop", "BigInt!", fmt.Sprint(prop)})
	}}

	upsert := harness.Scenario{
		Name: "write/upsert", Group: "write", Write: true,
		Check: func(ctx context.Context, db *pgxpool.Pool, ok int) error {
			return expectCount(ctx, db, "upserted guests", `select count(*) from hotel.guests where email like 'bench-ups-%'`, min(ok, upsertKeys))
		},
		Impls: map[srv]harness.Impl{
			harness.Rel: {Support: harness.Native, Run: func(c *harness.Client, e *env, n uint64) ([]byte, error) {
				return c.Do(relWrite(map[string]any{"relation": "guests", "schema": "hotel", "write_mode": "upsert", "on_conflict": []string{"email"}, "update_columns": []string{"first_name", "last_name"}, "select": sel("id", "email", "first_name", "last_name")},
					[]any{upsertRow(n)}))
			}},
			harness.PostgREST: {Support: harness.Native, Run: func(c *harness.Client, e *env, n uint64) ([]byte, error) {
				return c.Do(restWrite("POST", "/guests?on_conflict=email&select=id,email,first_name,last_name", upsertRow(n), "resolution=merge-duplicates,return=representation"))
			}},
			harness.PostGraphile4: {Support: harness.Unsupported, Note: "no upsert mutation without an extra plugin"},
			harness.PostGraphile5: {Support: harness.Unsupported, Note: "no upsert mutation without an extra plugin"},
		},
	}
	return []harness.Scenario{insert, update, del, bulk, nested, upsert}
}

func upsertRow(n uint64) map[string]any {
	return map[string]any{"email": fmt.Sprintf("bench-ups-%d@example.com", n%upsertKeys), "first_name": fmt.Sprintf("Run%d", n), "last_name": "Bench"}
}

func bulkAmenities(n uint64) []any {
	rows := make([]any, bulkRows)
	for i := range rows {
		rows[i] = map[string]any{"name": fmt.Sprintf("bench-bulk-%d-%d", n, i)}
	}
	return rows
}

func nestedRoomTypes() []any {
	return []any{
		map[string]any{"name": "Standard", "base_price": "129.00", "capacity": 2},
		map[string]any{"name": "Suite", "base_price": "249.00", "capacity": 4},
	}
}

func relNested(c *harness.Client, e *env, n uint64) ([]byte, error) {
	name := fmt.Sprintf("bench-nest-%d", n)
	rtSel := sel("id", "property_id", "name", "base_price", "capacity")
	return c.Do(relWrite(map[string]any{
		"relation": "chains", "schema": "hotel",
		"join": map[string]any{"properties": map[string]any{
			"relation": "properties", "schema": "hotel", "on": map[string]string{"chain_id": "id"}, "write_mode": "insert",
			"join":   map[string]any{"room_types": map[string]any{"relation": "room_types", "schema": "hotel", "on": map[string]string{"property_id": "id"}, "select": rtSel}},
			"select": map[string]any{"id": col("id"), "chain_id": col("chain_id"), "name": col("name"), "star_rating": col("star_rating"), "room_types": []any{".", "room_types"}},
		}},
		"select": map[string]any{"id": col("id"), "name": col("name"), "properties": []any{".", "properties"}},
	}, []any{map[string]any{"name": name, "properties": []any{map[string]any{"name": name, "star_rating": 4, "room_types": nestedRoomTypes()}}}}))
}

func restNested(c *harness.Client, e *env, n uint64) ([]byte, error) {
	name := fmt.Sprintf("bench-nest-%d", n)
	body, err := c.Do(restWrite("POST", "/chains?select=id", map[string]any{"name": name}, "return=representation"))
	if err != nil {
		return nil, err
	}
	chain, err := restID(body)
	if err != nil {
		return nil, err
	}
	body, err = c.Do(restWrite("POST", "/properties?select=id", map[string]any{"chain_id": chain, "name": name, "star_rating": 4}, "return=representation"))
	if err != nil {
		return nil, err
	}
	prop, err := restID(body)
	if err != nil {
		return nil, err
	}
	rts := nestedRoomTypes()
	for _, r := range rts {
		r.(map[string]any)["property_id"] = prop
	}
	return c.Do(restWrite("POST", "/room_types?select=id", rts, "return=representation"))
}
