package query

import (
	"strings"
	"testing"
)

// TestCompileSelect_ParamExprReservesNamedPlaceholder pins ["$param", ...]
// codegen (sql_expr.go's ParamExpr case) : it must reserve a $N placeholder
// via writer.SQLWriter.BindParam rather than error, share the same
// positional sequence as any literal Bind on the same statement, and cast
// to the declared Cast when present.
func TestCompileSelect_ParamExprReservesNamedPlaceholder(t *testing.T) {
	node := mustResolveQuery(t, `{
		"relation": "director", "schema": "public", "select": ["own"],
		"where": ["=", "name", ["$param", "director_name", "text"]]
	}`)

	w, err := CompileSelect(node)
	if err != nil {
		t.Fatalf("CompileSelect: %v", err)
	}
	if !strings.Contains(w.String(), "$1::text") {
		t.Fatalf("expected a $1::text placeholder, got sql: %s", w.String())
	}

	names := w.ParamNames()
	if len(names) != 1 || names[0] != "director_name" {
		t.Fatalf("expected ParamNames() == [director_name], got %#v", names)
	}

	args, err := w.ResolveArgs(map[string]any{"director_name": "Denis Villeneuve"})
	if err != nil {
		t.Fatalf("ResolveArgs: %v", err)
	}
	if len(args) != 1 || args[0] != "Denis Villeneuve" {
		t.Fatalf("expected resolved args == [Denis Villeneuve], got %#v", args)
	}
}

// TestCompileSelect_ParamExprDefaultsToJsonbCast pins the "no explicit
// cast" default (specs/well-known-queries.md ## Definition : "unless if it
// is JSON since it will be JSON by default").
func TestCompileSelect_ParamExprDefaultsToJsonbCast(t *testing.T) {
	node := mustResolveQuery(t, `{
		"relation": "director", "schema": "public", "select": ["own"],
		"where": ["=", "name", ["$param", "director_name"]]
	}`)

	w, err := CompileSelect(node)
	if err != nil {
		t.Fatalf("CompileSelect: %v", err)
	}
	if !strings.Contains(w.String(), "$1::jsonb") {
		t.Fatalf("expected a $1::jsonb placeholder, got sql: %s", w.String())
	}
}
