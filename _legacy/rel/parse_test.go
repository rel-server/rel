package rel

import (
	"testing"

	"sales-way.com/server/pg"
	"sales-way.com/server/query"
)

func testTables() pg.DBAllTables {
	parent := &pg.DBTable{
		Schema: "api",
		Name:   "items",
		Columns: pg.DBAllColumns{
			"id":   {Name: "id"},
			"name": {Name: "name"},
		},
		ColumnsInOrder: pg.DBAllColumnsSlice{
			{Name: "id"},
			{Name: "name"},
		},
		Relationships:    []*pg.RelationShip{},
		RelationshipsMap: pg.RelationShipMap{},
	}
	child := &pg.DBTable{
		Schema: "api",
		Name:   "items_tags",
		Columns: pg.DBAllColumns{
			"item_id": {Name: "item_id"},
			"tag":     {Name: "tag"},
		},
		ColumnsInOrder: pg.DBAllColumnsSlice{
			{Name: "item_id"},
			{Name: "tag"},
		},
		Relationships:    []*pg.RelationShip{},
		RelationshipsMap: pg.RelationShipMap{},
	}
	rel := &pg.RelationShip{
		LocalRelation:       parent,
		ColumnsNames:        []string{"id"},
		DistantRelation:     child,
		DistantColumnsNames: []string{"item_id"},
		IsReverse:           true,
		IsMultiple:          true,
	}
	parent.Relationships = append(parent.Relationships, rel)
	parent.RelationshipsMap = pg.RelationShipMap{
		"<<api.items_tags(item_id)": rel,
	}
	parent.DbTableName()
	child.DbTableName()

	tables := pg.DBAllTables{
		"api.items":      parent,
		"api.items_tags": child,
	}
	return tables
}

func TestParseAndResolveQuery(t *testing.T) {
	body := []byte(`{
		"query": {
			"relation": {"schema": "api", "relation": "items"},
			"write": {"mode": "merge"},
			"where": [">=", "id", 1],
			"rel": {
				"tags": {
					"relation": {"schema": "api", "relation": "items_tags"},
					"on": {"id": "item_id"}
				}
			},
			"select": {
				"*": [],
				"tags": "tags"
			}
		},
		"data": [{"id": 1, "name": "a", "tags": [{"tag": "x"}]}]
	}`)

	ops, err := ParseFromBytes(testTables(), nil, body)
	if err != nil {
		t.Fatal(err)
	}
	if len(ops) != 1 {
		t.Fatalf("expected 1 op, got %d", len(ops))
	}

	scope := query.NewScope()
	ctx := NewResolveContext(testTables(), nil, scope)
	if err := ops.Resolve(ctx); err != nil {
		t.Fatal(err)
	}

	q := ops[0].Query
	if q.ResolvedTable == nil || q.ResolvedTable.Name != "items" {
		t.Fatalf("expected items table, got %#v", q.ResolvedTable)
	}
	if len(q.IncomingsRels) != 1 {
		t.Fatalf("expected 1 incoming rel, got %d", len(q.IncomingsRels))
	}
	if q.Where == nil {
		t.Fatal("expected where clause")
	}

	flats, err := ops[0].FlattenJsonData(body)
	if err != nil {
		t.Fatal(err)
	}
	if len(flats) != 2 {
		t.Fatalf("expected 2 flat rows, got %d", len(flats))
	}
}
