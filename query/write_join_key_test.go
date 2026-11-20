package query

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

// movieTitles returns the titles of director's movies, sorted.
func movieTitles(t *testing.T, conn *pgxpool.Conn, director int) []string {
	t.Helper()
	var titles []string
	if err := conn.QueryRow(context.Background(), `select coalesce(array_agg(title order by title), '{}') from movie where director_id = $1`, director).Scan(&titles); err != nil {
		t.Fatalf("movies of %d: %v", director, err)
	}
	return titles
}

// fkOf reads the nullable int column col of the row id of table.
func fkOf(t *testing.T, conn *pgxpool.Conn, table, col string, id int) *int {
	t.Helper()
	var v *int
	if err := conn.QueryRow(context.Background(), fmt.Sprintf(`select %s from %s where id = $1`, col, table), id).Scan(&v); err != nil {
		t.Fatalf("%s.%s of %d: %v", table, col, id, err)
	}
	return v
}

func checkFK(t *testing.T, conn *pgxpool.Conn, table, col string, id int, want *int, what string) {
	t.Helper()
	got := fkOf(t, conn, table, col, id)
	if (got == nil) != (want == nil) || (got != nil && *got != *want) {
		t.Errorf("%s : %s.%s = %s, want %s", what, table, col, intPtrString(got), intPtrString(want))
	}
}

func intPtrString(p *int) string {
	if p == nil {
		return "NULL"
	}
	return itoa(*p)
}

func insertID(t *testing.T, conn *pgxpool.Conn, sql string, args ...any) int {
	t.Helper()
	var id int
	if err := conn.QueryRow(context.Background(), sql, args...).Scan(&id); err != nil {
		t.Fatalf("%s: %v", sql, err)
	}
	return id
}

// An incoming join's key : absent leaves the parent row's children alone, null and [] delete them all, an array
// keeps what it names. The rows of one batch mix every case, so the choice is per row, not per statement.
func TestExecuteWrite_IncomingJoinKey_AbsentNullEmptyGiven(t *testing.T) {
	for _, mode := range []string{"merge", "deleteonly", "merge-update"} {
		t.Run(mode, func(t *testing.T) {
			conn := acquireWriteConn(t)
			ctx := context.Background()
			var dirs, keeps []int
			for i := range 4 {
				d := insertID(t, conn, `insert into director (name) values ($1) returning id`, fmt.Sprintf("Incoming %s %d", mode, i))
				dirs = append(dirs, d)
				keeps = append(keeps, insertID(t, conn, `insert into movie (director_id, title) values ($1, 'keep') returning id`, d))
				insertID(t, conn, `insert into movie (director_id, title) values ($1, 'other') returning id`, d)
			}
			node := mustResolveQuery(t, `{
				"relation": "director", "schema": "public",
				"select": {"id": ["col", "id"], "movies": [".", "movies"]},
				"write_mode": "update-existing",
				"join": {"movies": {"relation": "movie", "schema": "public", "on": {"director_id": "id"}, "select": ["*~"], "write_mode": "`+mode+`"}}
			}`)
			payload := fmt.Appendf(nil, `[{"id": %d}, {"id": %d, "movies": null}, {"id": %d, "movies": []}, {"id": %d, "movies": [{"id": %d, "title": "renamed"}]}]`,
				dirs[0], dirs[1], dirs[2], dirs[3], keeps[3])
			if _, err := ExecuteWrite(ctx, conn, node, payload); err != nil {
				t.Fatalf("ExecuteWrite: %v", err)
			}
			given := []string{"renamed"}
			if mode == "deleteonly" { // writes nothing, only keeps what it names
				given = []string{"keep"}
			}
			for i, want := range [][]string{{"keep", "other"}, {}, {}, given} {
				if got := movieTitles(t, conn, dirs[i]); !slices.Equal(got, want) {
					t.Errorf("row %d : movies %v, want %v", i, got, want)
				}
			}

			// every row lacking the key : nothing is deleted at all
			resetWriteTables(t, conn)
			if _, err := ExecuteWrite(ctx, conn, node, fmt.Appendf(nil, `[{"id": %d}]`, dirs[3])); err != nil {
				t.Fatalf("ExecuteWrite: %v", err)
			}
			if got := movieTitles(t, conn, dirs[3]); !slices.Equal(got, given) {
				t.Errorf("every key absent : movies %v, want %v", got, given)
			}
		})
	}
}

// An outgoing join's key : given sets the FK to the child's key, null clears it, absent makes the FK an ordinary
// column (the payload's value, else the current one on update, the default on insert).
func TestExecuteWrite_OutgoingJoinKey_AbsentNullGiven(t *testing.T) {
	for _, mode := range []string{"update-existing", "upsert"} {
		t.Run(mode, func(t *testing.T) {
			conn := acquireWriteConn(t)
			ctx := context.Background()
			s1 := insertID(t, conn, `insert into studio (name) values ('Old Studio') returning id`)
			s2 := insertID(t, conn, `insert into studio (name) values ('Other Studio') returning id`)
			var dirs []int
			for i := range 5 {
				dirs = append(dirs, insertID(t, conn, `insert into director (name, studio_id) values ($1, $2) returning id`, fmt.Sprintf("Outgoing %s %d", mode, i), s1))
			}
			node := mustResolveQuery(t, `{
				"relation": "director", "schema": "public",
				"select": {"id": ["col", "id"], "name": ["col", "name"], "studio_id": ["col", "studio_id"], "studio": [".", "studio"]},
				"write_mode": "`+mode+`",
				"join": {"studio": {"relation": "studio", "schema": "public", "on": {"id": "studio_id"}, "select": ["*~"]}}
			}`)
			rows := fmt.Sprintf(`{"id": %d, "name": "a"}, {"id": %d, "name": "b", "studio": null}, {"id": %d, "name": "c", "studio": {"id": %d, "name": "Other Studio"}},
				{"id": %d, "name": "d", "studio_id": %d}, {"id": %d, "name": "e", "studio_id": %d, "studio": null}`, dirs[0], dirs[1], dirs[2], s2, dirs[3], s2, dirs[4], s2)
			if mode == "upsert" {
				rows += fmt.Sprintf(`, {"name": "Outgoing new absent"}, {"name": "Outgoing new supplied", "studio_id": %d}`, s2)
			}
			if _, err := ExecuteWrite(ctx, conn, node, []byte("["+rows+"]")); err != nil {
				t.Fatalf("ExecuteWrite: %v", err)
			}
			checkFK(t, conn, "director", "studio_id", dirs[0], &s1, "key absent, FK not supplied")
			checkFK(t, conn, "director", "studio_id", dirs[1], nil, "key null")
			checkFK(t, conn, "director", "studio_id", dirs[2], &s2, "key given")
			checkFK(t, conn, "director", "studio_id", dirs[3], &s2, "key absent, FK supplied")
			checkFK(t, conn, "director", "studio_id", dirs[4], nil, "key null, FK supplied")
			if mode == "upsert" {
				for name, want := range map[string]*int{"Outgoing new absent": nil, "Outgoing new supplied": &s2} {
					id := insertID(t, conn, `select id from director where name = $1`, name)
					checkFK(t, conn, "director", "studio_id", id, want, name)
				}
			}
		})
	}
}

// With the key absent the FK is an ordinary column, so update_columns filters the payload's value ; a given key
// still writes it.
func TestExecuteWrite_OutgoingJoinKey_AbsentFollowsUpdateColumns(t *testing.T) {
	conn := acquireWriteConn(t)
	ctx := context.Background()
	s1 := insertID(t, conn, `insert into studio (name) values ('Allowlist Old') returning id`)
	s2 := insertID(t, conn, `insert into studio (name) values ('Allowlist Other') returning id`)
	d0 := insertID(t, conn, `insert into director (name, studio_id) values ('Allowlist 0', $1) returning id`, s1)
	d1 := insertID(t, conn, `insert into director (name, studio_id) values ('Allowlist 1', $1) returning id`, s1)
	for _, mode := range []string{"update-existing", "upsert"} {
		resetWriteTables(t, conn)
		node := mustResolveQuery(t, `{
			"relation": "director", "schema": "public", "update_columns": ["name"],
			"select": {"id": ["col", "id"], "name": ["col", "name"], "studio_id": ["col", "studio_id"], "studio": [".", "studio"]},
			"write_mode": "`+mode+`",
			"join": {"studio": {"relation": "studio", "schema": "public", "on": {"id": "studio_id"}, "select": ["*~"]}}
		}`)
		payload := fmt.Appendf(nil, `[{"id": %d, "name": "Allowlist 0", "studio_id": %d}, {"id": %d, "name": "Allowlist 1", "studio": {"id": %d, "name": "Allowlist Other"}}]`, d0, s2, d1, s2)
		if _, err := ExecuteWrite(ctx, conn, node, payload); err != nil {
			t.Fatalf("%s: ExecuteWrite: %v", mode, err)
		}
		checkFK(t, conn, "director", "studio_id", d0, &s1, mode+", key absent, FK supplied but not in update_columns")
		checkFK(t, conn, "director", "studio_id", d1, &s2, mode+", key given")
		if _, err := conn.Exec(ctx, `update director set studio_id = $1 where id = $2`, s1, d1); err != nil {
			t.Fatal(err)
		}
	}
}

// A merge row under an incoming grandparent : the same rules for its outgoing key, and the rows its array leaves
// out are still deleted.
func TestExecuteWrite_OutgoingJoinKey_MergeUnderIncoming(t *testing.T) {
	conn := acquireWriteConn(t)
	ctx := context.Background()
	t1 := insertID(t, conn, `insert into key_tag (name) values ('t1') returning id`)
	t2 := insertID(t, conn, `insert into key_tag (name) values ('t2') returning id`)
	owner := insertID(t, conn, `insert into key_owner (name) values ('owner') returning id`)
	var items []int
	for i := range 5 {
		items = append(items, insertID(t, conn, `insert into key_item (owner_id, tag_id, name) values ($1, $2, $3) returning id`, owner, t1, fmt.Sprintf("i%d", i)))
	}
	node := mustResolveQuery(t, `{
		"relation": "key_owner", "schema": "public",
		"select": {"id": ["col", "id"], "items": [".", "items"]},
		"write_mode": "update-existing",
		"join": {"items": {
			"relation": "key_item", "schema": "public", "on": {"owner_id": "id"},
			"select": {"id": ["col", "id"], "name": ["col", "name"], "tag_id": ["col", "tag_id"], "tag": [".", "tag"]},
			"join": {"tag": {"relation": "key_tag", "schema": "public", "on": {"id": "tag_id"}, "select": ["*~"]}}
		}}
	}`)
	payload := fmt.Appendf(nil, `[{"id": %d, "items": [
		{"id": %d, "name": "i0"},
		{"id": %d, "name": "i1", "tag": null},
		{"id": %d, "name": "i2", "tag": {"id": %d, "name": "t2"}},
		{"id": %d, "name": "i3", "tag_id": %d},
		{"name": "new absent"},
		{"name": "new supplied", "tag_id": %d}
	]}]`, owner, items[0], items[1], items[2], t2, items[3], t2, t2)
	if _, err := ExecuteWrite(ctx, conn, node, payload); err != nil {
		t.Fatalf("ExecuteWrite: %v", err)
	}
	checkFK(t, conn, "key_item", "tag_id", items[0], &t1, "key absent, FK not supplied")
	checkFK(t, conn, "key_item", "tag_id", items[1], nil, "key null")
	checkFK(t, conn, "key_item", "tag_id", items[2], &t2, "key given")
	checkFK(t, conn, "key_item", "tag_id", items[3], &t2, "key absent, FK supplied")
	for name, want := range map[string]*int{"new absent": nil, "new supplied": &t2} {
		id := insertID(t, conn, `select id from key_item where name = $1`, name)
		checkFK(t, conn, "key_item", "tag_id", id, want, name)
	}
	var left int
	if err := conn.QueryRow(ctx, `select count(*) from key_item where id = $1`, items[4]).Scan(&left); err != nil || left != 0 {
		t.Errorf("the item the array leaves out : %d left (%v), want deleted", left, err)
	}
}

// A readonly outgoing join never writes the FK : the child's key is ignored, and the column is written only when
// the select names it and the row supplies it, as any column.
func TestExecuteWrite_ReadonlyOutgoingJoin_NeverWritesFK(t *testing.T) {
	conn := acquireWriteConn(t)
	ctx := context.Background()
	s1 := insertID(t, conn, `insert into studio (name) values ('Readonly Old') returning id`)
	s2 := insertID(t, conn, `insert into studio (name) values ('Readonly Other') returning id`)
	d := insertID(t, conn, `insert into director (name, studio_id) values ('Readonly Outgoing', $1) returning id`, s1)

	node := mustResolveQuery(t, `{
		"relation": "director", "schema": "public",
		"select": {"id": ["col", "id"], "name": ["col", "name"], "studio": [".", "studio"]},
		"write_mode": "update-existing",
		"join": {"studio": {"relation": "studio", "schema": "public", "on": {"id": "studio_id"}, "write_mode": "readonly", "select": ["*~"]}}
	}`)
	for _, studio := range []string{fmt.Sprintf(`{"id": %d, "name": "Renamed"}`, s2), "null"} {
		resetWriteTables(t, conn)
		if _, err := ExecuteWrite(ctx, conn, node, fmt.Appendf(nil, `[{"id": %d, "name": "Readonly Outgoing", "studio": %s}]`, d, studio)); err != nil {
			t.Fatalf("ExecuteWrite: %v", err)
		}
		checkFK(t, conn, "director", "studio_id", d, &s1, "studio "+studio)
	}
	var name string
	if err := conn.QueryRow(ctx, `select name from studio where id = $1`, s2).Scan(&name); err != nil || name != "Readonly Other" {
		t.Errorf("readonly studio : name %q (%v), want it untouched", name, err)
	}

	resetWriteTables(t, conn)
	withCol := mustResolveQuery(t, `{
		"relation": "director", "schema": "public",
		"select": {"id": ["col", "id"], "studio_id": ["col", "studio_id"], "studio": [".", "studio"]},
		"write_mode": "update-existing",
		"join": {"studio": {"relation": "studio", "schema": "public", "on": {"id": "studio_id"}, "write_mode": "readonly", "select": ["*~"]}}
	}`)
	if _, err := ExecuteWrite(ctx, conn, withCol, fmt.Appendf(nil, `[{"id": %d, "studio_id": %d, "studio": {"id": %d}}]`, d, s2, s1)); err != nil {
		t.Fatalf("ExecuteWrite: %v", err)
	}
	checkFK(t, conn, "director", "studio_id", d, &s2, "studio_id selected and supplied")
}

// An insert parent : an absent key takes the payload's FK, else the default, and insert_columns filters the
// payload's FK but not the key's.
func TestExecuteWrite_OutgoingJoinKey_InsertParent(t *testing.T) {
	conn := acquireWriteConn(t)
	ctx := context.Background()
	s1 := insertID(t, conn, `insert into studio (name) values ('Insert Parent Studio') returning id`)
	for _, c := range []struct{ insertColumns, row string }{
		{``, `{"name": "Insert absent"}`},
		{``, `{"name": "Insert absent supplied", "studio_id": SID}`},
		{``, `{"name": "Insert null", "studio_id": SID, "studio": null}`},
		{``, `{"name": "Insert given", "studio": {"id": SID, "name": "Insert Parent Studio"}}`},
		{`, "insert_columns": ["name"]`, `{"name": "Insert filtered absent supplied", "studio_id": SID}`},
		{`, "insert_columns": ["name"]`, `{"name": "Insert filtered given", "studio": {"id": SID, "name": "Insert Parent Studio"}}`},
	} {
		resetWriteTables(t, conn)
		node := mustResolveQuery(t, `{
			"relation": "director", "schema": "public", "write_mode": "insert"`+c.insertColumns+`,
			"select": {"id": ["col", "id"], "name": ["col", "name"], "studio_id": ["col", "studio_id"], "studio": [".", "studio"]},
			"join": {"studio": {"relation": "studio", "schema": "public", "on": {"id": "studio_id"}, "select": ["*~"]}}
		}`)
		row := strings.ReplaceAll(c.row, "SID", itoa(s1))
		if _, err := ExecuteWrite(ctx, conn, node, []byte("["+row+"]")); err != nil {
			t.Fatalf("%s: ExecuteWrite: %v", row, err)
		}
	}
	for name, want := range map[string]*int{
		"Insert absent": nil, "Insert absent supplied": &s1, "Insert null": nil, "Insert given": &s1,
		"Insert filtered absent supplied": nil, "Insert filtered given": &s1,
	} {
		checkFK(t, conn, "director", "studio_id", insertID(t, conn, `select id from director where name = $1`, name), want, name)
	}
}

// A join the select doesn't name is absent from every row : the FK keeps its value and the children stay.
func TestExecuteWrite_JoinNotSelected_IsAbsent(t *testing.T) {
	conn := acquireWriteConn(t)
	ctx := context.Background()
	s1 := insertID(t, conn, `insert into studio (name) values ('Unselected Studio') returning id`)
	d := insertID(t, conn, `insert into director (name, studio_id) values ('Unselected', $1) returning id`, s1)
	insertID(t, conn, `insert into movie (director_id, title) values ($1, 'kept') returning id`, d)
	node := mustResolveQuery(t, `{
		"relation": "director", "schema": "public", "write_mode": "update-existing",
		"select": {"id": ["col", "id"], "name": ["col", "name"]},
		"join": {
			"studio": {"relation": "studio", "schema": "public", "on": {"id": "studio_id"}, "select": ["*~"]},
			"movies": {"relation": "movie", "schema": "public", "on": {"director_id": "id"}, "select": ["*~"]}
		}
	}`)
	if _, err := ExecuteWrite(ctx, conn, node, fmt.Appendf(nil, `[{"id": %d, "name": "Unselected renamed"}]`, d)); err != nil {
		t.Fatalf("ExecuteWrite: %v", err)
	}
	checkFK(t, conn, "director", "studio_id", d, &s1, "studio not selected")
	if got := movieTitles(t, conn, d); !slices.Equal(got, []string{"kept"}) {
		t.Errorf("movies not selected : %v, want [kept]", got)
	}
}

// In a CBOR payload a member holding undefined is absent, a join key as a column.
func TestExecuteWrite_CBORUndefinedMemberIsAbsent(t *testing.T) {
	conn := acquireWriteConn(t)
	ctx := context.Background()
	d := insertID(t, conn, `insert into director (name) values ('Undefined Director') returning id`)
	insertID(t, conn, `insert into movie (director_id, title) values ($1, 'kept') returning id`, d)
	node := mustResolveQuery(t, `{
		"relation": "director", "schema": "public", "write_mode": "update-existing",
		"select": {"id": ["col", "id"], "name": ["col", "name"], "movies": [".", "movies"]},
		"join": {"movies": {"relation": "movie", "schema": "public", "on": {"director_id": "id"}, "select": ["*~"]}}
	}`)
	// [{"id": d, "name": undefined, "movies": undefined}]
	payload := append([]byte{0x81, 0xa3, 0x62, 'i', 'd'}, cborBytes(t, d)...)
	payload = append(payload, 0x64, 'n', 'a', 'm', 'e', 0xf7, 0x66, 'm', 'o', 'v', 'i', 'e', 's', 0xf7)
	pn, err := NewCBORNode(payload)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ExecuteWriteNode(ctx, conn, node, pn, &WriteState{}, nil, WriteOptions{NoFastPath: true}); err != nil {
		t.Fatalf("ExecuteWriteNode: %v", err)
	}
	var name string
	if err := conn.QueryRow(ctx, `select name from director where id = $1`, d).Scan(&name); err != nil || name != "Undefined Director" {
		t.Errorf("name %q (%v), want it kept", name, err)
	}
	if got := movieTitles(t, conn, d); !slices.Equal(got, []string{"kept"}) {
		t.Errorf("movies %v, want [kept]", got)
	}
}
