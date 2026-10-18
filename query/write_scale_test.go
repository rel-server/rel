package query

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

// The planner has no statistics on a temp table, so how it joins temp tables rests on guesses about row
// counts ; TestWriteScale_NoNestedLoopBetweenTempTables checks the guess that mattered.

func treeChildrenPayload(roots, kids int) []byte {
	buf := []byte{'['}
	for r := 0; r < roots; r++ {
		if r > 0 {
			buf = append(buf, ',')
		}
		buf = fmt.Appendf(buf, `{"name": "root %d", "children": [`, r)
		for k := 0; k < kids; k++ {
			if k > 0 {
				buf = append(buf, ',')
			}
			buf = fmt.Appendf(buf, `{"name": "kid %d-%d"}`, r, k)
		}
		buf = append(buf, ']', '}')
	}
	return append(buf, ']')
}

// the rows land where they should, whatever the plan : every child points at its own row
func TestWriteScale_SameTableIncomingIsCorrect(t *testing.T) {
	node := mustResolveQuery(t, `{
		"relation": "tree_t", "schema": "public",
		"select": {"id": ["col", "id"], "name": ["col", "name"], "children": [".", "children"]},
		"write_mode": "insert",
		"join": {"children": {"relation": "tree_t", "schema": "public", "on": {"parent_id": "id"}, "select": ["*~"], "write_mode": "merge"}}
	}`)
	conn := acquireWriteConn(t)
	ctx := context.Background()
	if _, err := ExecuteWrite(ctx, conn, node, treeChildrenPayload(500, 3)); err != nil {
		t.Fatal(err)
	}
	var wrong, kids int
	if err := conn.QueryRow(ctx, `select count(*) filter (where substring(k.name from 'kid (\d+)-') is distinct from substring(p.name from 'root (\d+)')), count(*)
		from tree_t k join tree_t p on p.id = k.parent_id where k.name like 'scale%' or k.name like 'kid %'`).Scan(&wrong, &kids); err != nil {
		t.Fatal(err)
	}
	if kids < 1500 || wrong != 0 {
		t.Errorf("%d children linked, %d to the wrong parent", kids, wrong)
	}
}

// planNodes walks a Postgres plan (EXPLAIN FORMAT JSON), calling f on every node.
func planNodes(n map[string]any, f func(map[string]any)) {
	f(n)
	if children, ok := n["Plans"].([]any); ok {
		for _, c := range children {
			if m, ok := c.(map[string]any); ok {
				planNodes(m, f)
			}
		}
	}
}

// TestWriteScale_NoNestedLoopBetweenTempTables : with a "node_id = N" filter the planner took a thousand-row
// temp table for five rows and joined two of them with a nested loop, a million comparisons that a hash join does in
// one pass. A table holding one node's rows gets no such filter. This checks the plan, which does not depend on the machine's speed.
func TestWriteScale_NoNestedLoopBetweenTempTables(t *testing.T) {
	node := mustResolveQuery(t, `{
		"relation": "movie", "schema": "public",
		"select": {"id": ["col", "id"], "title": ["col", "title"], "director": [".", "director"]},
		"write_mode": "insert",
		"join": {"director": {"relation": "director", "schema": "public", "on": {"id": "director_id"}, "select": ["*~"]}}
	}`)
	conn := acquireWriteConn(t)
	ctx := context.Background()
	if _, err := conn.Exec(ctx, `analyze studio, director, movie`); err != nil {
		t.Fatal(err)
	}
	res, err := ExecuteWriteStateParamsOpts(ctx, conn, node, buildBatchPayload(1000, 800000), &WriteState{}, nil, WriteOptions{NoFastPath: true, QueryPlan: true})
	if err != nil {
		t.Fatal(err)
	}
	checked := 0
	for _, p := range res.QueryPlan {
		for _, raw := range [][]byte{p.Insert, p.Upsert, p.Update} {
			if raw == nil {
				continue
			}
			var plans []map[string]any
			if err := json.Unmarshal(raw, &plans); err != nil || len(plans) == 0 {
				t.Fatalf("plan: %v", err)
			}
			checked++
			planNodes(plans[0]["Plan"].(map[string]any), func(n map[string]any) {
				if n["Node Type"] != "Nested Loop" {
					return
				}
				scans := 0
				planNodes(n, func(c map[string]any) {
					if c["Node Type"] == "Seq Scan" && strings.HasPrefix(fmt.Sprint(c["Relation Name"]), "__rel_w_") {
						scans++
					}
				})
				if scans >= 2 {
					t.Errorf("statement for %v joins temp tables with a nested loop: %v", p.Path, n)
				}
			})
		}
	}
	if checked < 2 {
		t.Fatalf("only %d plans checked", checked)
	}
}
