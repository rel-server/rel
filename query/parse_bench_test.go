package query

import (
	"fmt"
	"strings"
	"testing"
)

// Bodies use only the public byte-level entry points (ParseQuery,
// ParseExpression) so the same file benchmarks both the *ast.Node and the
// Node-interface versions of the parser.
var parseBenchBodies = []struct{ name, body string }{
	{"small", `{"relation": "director", "schema": "public", "select": ["*~"], "where": ["=", ["col", "name"], "Denis Villeneuve"]}`},
	{"medium", `{"relation": "director", "schema": "public", "alias": "d",
		"select": {"name": ["col", "name"], "display": ["call", {"schema": "public", "name": "director_display_name"}, [".", "d"]], "n": ["::", ["col", "id"], "text"]},
		"where": ["and", ["like", ["col", "name"], "Count Join Director%"], [">", ["agg", {"schema": "pg_catalog", "name": "count"}, [[".", "movies"]]], 0]],
		"join": {"movies": {"relation": "movie", "schema": "public", "on": {"director_id": "id"}, "select": ["*~"], "order_by": [["col", "title"]]}},
		"order_by": [["col", "name"]], "limit": 10}`},
	{"large", benchLargeBody()},
}

// benchLargeBody is a wide select object (50 computed fields) plus a deep
// nested where tree.
func benchLargeBody() string {
	var sel []string
	for i := range 50 {
		sel = append(sel, fmt.Sprintf(`"f%d": ["+", ["col", "id"], %d, ["::", ["col", "name"], "text"]]`, i, i))
	}
	where := `["=", ["col", "id"], 1]`
	for i := range 12 {
		where = fmt.Sprintf(`["and", ["<", ["col", "id"], %d], ["or", %s, ["like", ["col", "name"], "x%%"]]]`, i+100, where)
	}
	return `{"relation": "director", "schema": "public", "select": {` + strings.Join(sel, ", ") + `}, "where": ` + where + `}`
}

func BenchmarkParseQuery(b *testing.B) {
	for _, c := range parseBenchBodies {
		data := []byte(c.body)
		b.Run(c.name, func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				if _, err := ParseQuery(data); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func BenchmarkParseExpression(b *testing.B) {
	data := []byte(`["and", ["like", ["col", "name"], "Count%"], [">", ["agg", {"schema": "pg_catalog", "name": "count"}, [[".", "movies"]]], 0], ["in", ["col", "id"], ["arr", 1, 2, 3]]]`)
	b.ReportAllocs()
	for b.Loop() {
		if _, err := ParseExpression(data); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkParseResolveCompile puts the parse cost in proportion : parse,
// resolve against the test schema, derive shapes and compile to SQL (no
// database round trip).
func BenchmarkParseResolveCompile(b *testing.B) {
	for _, c := range parseBenchBodies[:2] { // the large body's fields don't resolve against the test schema
		data := []byte(c.body)
		b.Run(c.name, func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				pq, err := ParseQuery(data)
				if err != nil {
					b.Fatal(err)
				}
				ctx := &ResolveContext{Db: testDb, Config: testCfg}
				node, err := ctx.Resolve(pq.Relation)
				if err != nil {
					b.Fatal(err)
				}
				if _, err := CompileSelect(node); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
