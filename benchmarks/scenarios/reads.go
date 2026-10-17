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
	"net/url"

	"github.com/rel-server/rel/benchmarks/harness"
)

var (
	bookingFields = []Field{{Name: "id", Kind: Str}, {Name: "guest_id", Kind: Int}, {Name: "room_id", Kind: Int}, {Name: "status", Kind: Enum}}
	guestFields   = []Field{{Name: "id", Kind: Int}, {Name: "email", Kind: Str}, {Name: "first_name", Kind: Str}, {Name: "last_name", Kind: Str}}
)

func col(name string) []any { return []any{"col", name} }

// page returns the offset of the (n mod pages)-th page of size.
func page(n uint64, pages, size int) int { return int(n%uint64(pages)) * size }

func reads() []harness.Scenario {
	pageOf := func(e *env, size int) int { return max(1, int(e.Guests)/size) }
	list := []read{
		{
			name: "read/pk-lookup", fields: bookingFields, gqlRoot: "booking", single: true,
			rel: func(n uint64, e *env) req {
				return relQuery("bookings", bookingFields, []any{"=", col("id"), pick(e.BookingIDs, n)}, nil, 0, 0)
			},
			rest: func(n uint64, e *env) req {
				return restQuery("bookings", bookingFields, "id=eq."+pick(e.BookingIDs, n))
			},
			gql: func(n uint64, e *env, d dialect) req {
				return gqlQuery("booking", bookingFields, d.pk()+": $id", true, d, gvar{"id", "UUID!", pick(e.BookingIDs, n)})
			},
		},
		{
			name: "read/filter-sort-paginate", fields: guestFields, gqlRoot: "guests",
			rel: func(n uint64, e *env) req {
				return relQuery("guests", guestFields, []any{"=", col("loyalty_tier_id"), 2}, []any{col("last_name"), col("id")}, 20, page(n, pageOf(e, 60), 20))
			},
			rest: func(n uint64, e *env) req {
				return restQuery("guests", guestFields, fmt.Sprintf("loyalty_tier_id=eq.2&order=last_name,id&limit=20&offset=%d", page(n, pageOf(e, 60), 20)))
			},
			gql: func(n uint64, e *env, d dialect) req {
				return gqlQuery("guests", guestFields, fmt.Sprintf("filter: {loyaltyTierId: {equalTo: \"2\"}}, orderBy: [LAST_NAME_ASC, %s_ASC], first: 20, offset: $offset", d.idEnum()), false, d, gvar{"offset", "Int", page(n, pageOf(e, 60), 20)})
			},
		},
		{
			name: "read/boolean-filter-like", fields: guestFields, gqlRoot: "guests",
			rel: func(n uint64, e *env) req {
				return relQuery("guests", guestFields,
					[]any{"and", []any{"like", col("last_name"), "S%"}, []any{"in", col("loyalty_tier_id"), 1, 2}},
					[]any{col("id")}, 50, page(n, 2, 50))
			},
			rest: func(n uint64, e *env) req {
				return restQuery("guests", guestFields, fmt.Sprintf("last_name=like.S*&loyalty_tier_id=in.(1,2)&order=id&limit=50&offset=%d", page(n, 2, 50)))
			},
			gql: func(n uint64, e *env, d dialect) req {
				return gqlQuery("guests", guestFields, fmt.Sprintf(`filter: {lastName: {startsWith: "S"}, loyaltyTierId: {in: ["1", "2"]}}, orderBy: %s_ASC, first: 50, offset: $offset`, d.idEnum()), false, d, gvar{"offset", "Int", page(n, 2, 50)})
			},
		},
		embedBookings(),
		{
			name: "read/one-to-many-embed", fields: propertyRoomTypes, gqlRoot: "properties",
			rel: func(n uint64, e *env) req {
				return relQuery("properties", propertyRoomTypes, nil, []any{col("id")}, 10, page(n, max(1, int(e.Properties)/10), 10))
			},
			rest: func(n uint64, e *env) req {
				return restQuery("properties", propertyRoomTypes, fmt.Sprintf("order=id&limit=10&offset=%d", page(n, max(1, int(e.Properties)/10), 10)))
			},
			gql: func(n uint64, e *env, d dialect) req {
				return gqlQuery("properties", propertyRoomTypes, fmt.Sprintf("orderBy: %s_ASC, first: 10, offset: $offset", d.idEnum()), false, d, gvar{"offset", "Int", page(n, max(1, int(e.Properties)/10), 10)})
			},
		},
		{
			name: "read/deep-nested-embed", fields: propertyDeep, gqlRoot: "properties",
			rel: func(n uint64, e *env) req {
				return relQuery("properties", propertyDeep, nil, []any{col("id")}, 5, page(n, max(1, int(e.Properties)/5), 5))
			},
			rest: func(n uint64, e *env) req {
				return restQuery("properties", propertyDeep, fmt.Sprintf("order=id&limit=5&offset=%d", page(n, max(1, int(e.Properties)/5), 5)))
			},
			gql: func(n uint64, e *env, d dialect) req {
				return gqlQuery("properties", propertyDeep, fmt.Sprintf("orderBy: %s_ASC, first: 5, offset: $offset", d.idEnum()), false, d, gvar{"offset", "Int", page(n, max(1, int(e.Properties)/5), 5)})
			},
		},
		{
			name: "read/aggregate-count", fields: propertyCounts, gqlRoot: "properties",
			rel: func(n uint64, e *env) req {
				return relQuery("properties", propertyCounts, nil, []any{col("id")}, 10, page(n, max(1, int(e.Properties)/10), 10))
			},
			rest: func(n uint64, e *env) req {
				return restQuery("properties", propertyCounts, fmt.Sprintf("order=id&limit=10&offset=%d", page(n, max(1, int(e.Properties)/10), 10)))
			},
			gql: func(n uint64, e *env, d dialect) req {
				return gqlQuery("properties", propertyCounts, fmt.Sprintf("orderBy: %s_ASC, first: 10, offset: $offset", d.idEnum()), false, d, gvar{"offset", "Int", page(n, max(1, int(e.Properties)/10), 10)})
			},
		},
		{
			name: "read/full-text-search-function", fields: idName, gqlRoot: "searchProperties",
			rel: func(n uint64, e *env) req {
				sel, _ := relSelect(idName)
				return relReq(map[string]any{"function": "search_properties", "schema": "hotel", "arguments": map[string]any{"query": searchWord}, "select": sel})
			},
			rest: func(n uint64, e *env) req {
				return restGet("/rpc/search_properties?query=" + url.QueryEscape(searchWord) + "&select=" + restSelect(idName))
			},
			gql: func(n uint64, e *env, d dialect) req {
				return gqlQuery("searchProperties", idName, fmt.Sprintf("query: %q", searchWord), false, d)
			},
		},
		{
			name: "read/array-column-filter", fields: roomFields, gqlRoot: "rooms",
			rel: func(n uint64, e *env) req {
				return relQuery("rooms", roomFields, []any{"@>", col("features"), []any{"arr", "Sea View"}}, []any{col("id")}, 20, page(n, 5, 20))
			},
			rest: func(n uint64, e *env) req {
				return restQuery("rooms", roomFields, fmt.Sprintf(`features=cs.%s&order=id&limit=20&offset=%d`, url.QueryEscape(`{"Sea View"}`), page(n, 5, 20)))
			},
			gql: func(n uint64, e *env, d dialect) req {
				return gqlQuery("rooms", roomFields, fmt.Sprintf(`filter: {features: {contains: ["Sea View"]}}, orderBy: %s_ASC, first: 20, offset: $offset`, d.idEnum()), false, d, gvar{"offset", "Int", page(n, 5, 20)})
			},
		},
		{
			name: "read/composite-column-field", fields: guestCity, gqlRoot: "guests",
			rel: func(n uint64, e *env) req {
				return relQuery("guests", guestCity, nil, []any{col("id")}, 50, page(n, pageOf(e, 50), 50))
			},
			rest: func(n uint64, e *env) req {
				return restQuery("guests", guestCity, fmt.Sprintf("order=id&limit=50&offset=%d", page(n, pageOf(e, 50), 50)))
			},
			gql: func(n uint64, e *env, d dialect) req {
				return gqlQuery("guests", guestCity, fmt.Sprintf("orderBy: %s_ASC, first: 50, offset: $offset", d.idEnum()), false, d, gvar{"offset", "Int", page(n, pageOf(e, 50), 50)})
			},
		},
		{
			name: "read/wide-unscoped", fields: wideFields, gqlRoot: "guests",
			rel: func(n uint64, e *env) req {
				return relQuery("guests", wideFields, nil, []any{col("id")}, 1000, 0)
			},
			rest: func(n uint64, e *env) req { return restQuery("guests", wideFields, "order=id&limit=1000") },
			gql: func(n uint64, e *env, d dialect) req {
				return gqlQuery("guests", wideFields, fmt.Sprintf("orderBy: %s_ASC, first: 1000", d.idEnum()), false, d)
			},
		},
	}
	out := make([]harness.Scenario, len(list))
	for i, r := range list {
		out[i] = r.scenario()
	}
	return out
}

var (
	idName = []Field{{Name: "id", Kind: Int}, {Name: "name", Kind: Str}}

	propertyRoomTypes = []Field{
		{Name: "id", Kind: Int}, {Name: "name", Kind: Str}, {Name: "star_rating", Kind: Int},
		{Name: "room_types", Kind: Many, Relation: "room_types", On: map[string]string{"property_id": "id"}, Gql: "roomTypes",
			Sub: []Field{{Name: "id", Kind: Int}, {Name: "name", Kind: Str}, {Name: "base_price", Kind: Num}}},
	}

	propertyDeep = []Field{
		{Name: "id", Kind: Int}, {Name: "name", Kind: Str},
		{Name: "room_types", Kind: Many, Relation: "room_types", On: map[string]string{"property_id": "id"}, Gql: "roomTypes",
			Sub: []Field{{Name: "id", Kind: Int}, {Name: "name", Kind: Str},
				{Name: "rooms", Kind: Many, Relation: "rooms", On: map[string]string{"room_type_id": "id"},
					Sub: []Field{{Name: "id", Kind: Int}, {Name: "room_number", Kind: Str}, {Name: "status", Kind: Enum}}}}},
	}

	propertyCounts = []Field{
		{Name: "id", Kind: Int}, {Name: "name", Kind: Str},
		{Name: "room_type_count", Kind: Count, Relation: "room_types", On: map[string]string{"property_id": "id"}, Gql: "roomTypes"},
	}

	roomFields = []Field{{Name: "id", Kind: Int}, {Name: "room_number", Kind: Str}}

	guestCity = []Field{{Name: "id", Kind: Int}, {Name: "email", Kind: Str}, {Name: "city", Kind: Composite, Col: "billing_address", Key: "city"}}

	wideFields = []Field{
		{Name: "id", Kind: Int}, {Name: "email", Kind: Str}, {Name: "first_name", Kind: Str}, {Name: "last_name", Kind: Str},
		{Name: "phone", Kind: Str}, {Name: "date_of_birth", Kind: Str},
	}
)

// searchWord is a stem every seeded description contains at least once.
const searchWord = "point"

func embedBookings() read {
	fields := append(append([]Field(nil), bookingFields...),
		Field{Name: "guest", Kind: One, Relation: "guests", On: map[string]string{"id": "guest_id"}, RestFK: "guest_id", Gql: "guest",
			Sub: []Field{{Name: "id", Kind: Int}, {Name: "email", Kind: Str}}},
		Field{Name: "room", Kind: One, Relation: "rooms", On: map[string]string{"id": "room_id"}, RestFK: "room_id", Gql: "room",
			Sub: []Field{{Name: "id", Kind: Int}, {Name: "room_number", Kind: Str}}},
	)
	where := []any{"=", col("status"), "confirmed"}
	pg := func(n uint64) int { return page(n, 10, 20) }
	return read{
		name: "read/two-many-to-one-embeds", fields: fields, gqlRoot: "bookings",
		rel: func(n uint64, e *env) req { return relQuery("bookings", fields, where, []any{col("id")}, 20, pg(n)) },
		rest: func(n uint64, e *env) req {
			return restQuery("bookings", fields, fmt.Sprintf("status=eq.confirmed&order=id&limit=20&offset=%d", pg(n)))
		},
		gql: func(n uint64, e *env, d dialect) req {
			return gqlQuery("bookings", fields, fmt.Sprintf("condition: {status: %s}, orderBy: %s_ASC, first: 20, offset: $offset", d.enum("confirmed"), d.idEnum()), false, d, gvar{"offset", "Int", pg(n)})
		},
	}
}
