package tsgen

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rel-server/rel/config"
	"github.com/rel-server/rel/wellknown"
)

// The decl schema of testdata/schema.sql : a declared type on each kind of column, broken ones, an import name
// two modules export and one the embedded files declare.

func declOptions(t *testing.T, codec string) Options {
	t.Helper()
	c, err := ParseCodec(codec)
	if err != nil {
		t.Fatal(err)
	}
	return Options{
		Schemas: []string{"decl"}, Blacklist: config.DefaultBlacklist(), Codec: c,
		Types: map[string]config.TypeDecl{"decl.doc.configured": {Type: "string", Comment: "from the config"}},
	}
}

func declWellknowns(t *testing.T) *wellknown.Registry {
	t.Helper()
	dir := t.TempDir()
	for name, def := range map[string]string{
		"doc_meta.json": `{"name": "doc_meta",
			"params": {"s": {"type": "jsonb", "ts": {"type": "Settings", "from": "./settings", "comment": "the settings"}}},
			"query": {"schema": "decl", "relation": "doc", "select": {"meta": [".", "meta"]}, "where": ["=", [".", "meta"], ["$param", "s", "jsonb"]]}}`,
		"doc_by_id.json": `{"name": "doc_by_id",
			"params": {"id": {"type": "int", "ts": {"type": "string", "comment": "the id"}}, "m": {"type": "jsonb", "ts": {"tipe": "X"}}},
			"query": {"schema": "decl", "relation": "doc", "select": {"id": [".", "id"]},
				"where": ["and", ["=", [".", "id"], ["$param", "id", "int"]], ["=", [".", "meta"], ["$param", "m", "jsonb"]]]}}`,
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(def), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	cfg := &config.Config{}
	cfg.Pg.Query.WellKnownDirs = dir
	cfg.Pg.Query.MaxDepth = config.DefaultMaxDepth
	reg, err := wellknown.BuildRegistry(testDb, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if len(reg.All()) != 2 {
		t.Fatalf("want both well-known queries loaded, got %d", len(reg.All()))
	}
	return reg
}

func TestGenerateSchema_DeclaredTypes(t *testing.T) {
	out := GenerateSchema(testDb, declOptions(t, "cbor"), declWellknowns(t))
	for _, want := range []string{
		// imports first, sorted by module ; the second Settings is aliased
		"import type { Body } from \"./body\"\nimport type { Settings } from \"./other\"\nimport type { Querier } from \"./q\"\nimport type { Settings as Settings__2 } from \"./settings\"\n",
		"/** user settings */\ntype Type__Decl__Settings = Settings__2\n",
		"/** documents */\ninterface Table__Decl__Doc {\n",
		"  /** the meta */\n  meta: ({ a: number }) | null\n",
		"  body: Body | null\n",
		"  raw: Type__Decl__Cbor\n",
		"  tags: string[]\n",
		"  settings: Type__Decl__Settings | null\n",
		"  /** multi-line */\n  callback: (() => void) | null\n",
		"  other: Settings\n",
		"  querier: Querier | null\n",
		"  /** from the config */\n  configured: string | null\n",
		"  /** a count */\n  n: number | null\n",
		"  broken: unknown | null\n",
		"  /** ends *\\/ early */\n  plain: unknown | null\n",
		"params: { /** the settings */ s: Settings__2 }",
		"params: { /** the id */ id: number; m: unknown }",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q", want)
		}
	}
	if strings.Contains(out, "column::") || strings.Contains(out, "table::") || strings.Contains(out, "type::") || strings.Contains(out, "__rel_import_") {
		t.Errorf("a declaration or a placeholder leaked into the output")
	}
	if t.Failed() {
		t.Log(out)
	}
}

// Under JSON, the server refuses a CBOR value either way : its column, nullable or not, and the cbor domain are
// never, whatever is declared.
func TestGenerateSchema_DeclaredTypes_CBORUnderJSON(t *testing.T) {
	out := GenerateSchema(testDb, declOptions(t, "json"), nil)
	for _, want := range []string{"  body: never\n", "  raw: never\n", "type Type__Decl__Cbor = never\n", "  meta: ({ a: number }) | null\n"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q", want)
		}
	}
	if strings.Contains(out, "from \"./body\"") {
		t.Errorf("a type nothing uses is still imported")
	}
	if t.Failed() {
		t.Log(out)
	}
}

const declUse = `import { relation } from "./database"
import type { Relations, Wellknowns } from "./database"
type Doc = Relations["decl.doc"]
type IsNever<T> = [T] extends [never] ? true : false
const docs = relation("decl.doc", { select: { id: ["col", "id"], meta: ["col", "meta"] } })
type DocRow = typeof docs.$row
export const rowIsNotNever: IsNever<DocRow> = false
export const row: DocRow = { id: 1, meta: { a: 1 } }
// @ts-expect-error a selected column keeps its declared type
export const badRow: DocRow = { id: 1, meta: { a: "x" } }
export const drafted = docs.draft({ id: 1, meta: { a: 1 } })
// @ts-expect-error a written column keeps its declared type
docs.draft({ id: 1, meta: { a: "x" } })
const all = relation("decl.doc")
export const defaultRowIsNotNever: IsNever<typeof all.$row> = false
export const meta: Doc["meta"] = { a: 1 }
// @ts-expect-error a declared type is checked
export const badMeta: Doc["meta"] = { a: "x" }
export const settings: Doc["settings"] = { theme: "dark" }
export const other: Doc["other"] = { other: 1 }
export const querier: Doc["querier"] = { q: 1 }
export const callback: Doc["callback"] = () => {}
type Row = Wellknowns["doc_meta"]["shape"][number]
export const rowMeta: Row["meta"] = { a: 1 }
// @ts-expect-error a selected column keeps its declared type
export const badRowMeta: Row["meta"] = { a: "x" }
export const params: Wellknowns["doc_meta"]["params"] = { s: { theme: "dark" } }
// @ts-expect-error a param keeps its declared type
export const badParams: Wellknowns["doc_meta"]["params"] = { s: 1 }
`

var declModules = map[string]string{
	"settings.ts": "export interface Settings { theme: string }\n",
	"other.ts":    "export interface Settings { other: number }\n",
	"body.ts":     "export interface Body { body: boolean }\n",
	"q.ts":        "export interface Querier { q: number }\n",
}

func withFiles(extra map[string]string) map[string]string {
	out := map[string]string{}
	for k, v := range declModules {
		out[k] = v
	}
	for k, v := range extra {
		out[k] = v
	}
	return out
}

// The whole file : Querier, a name querier.ts declares, is aliased once the embedded files are in.
func TestGenerateDatabaseTS_DeclaredTypes_TypeChecks(t *testing.T) {
	wk := declWellknowns(t)
	out := GenerateDatabaseTS(testDb, declOptions(t, "cbor"), wk)
	if !strings.Contains(out, "import type { Querier as Querier__2 } from \"./q\"\n") || !strings.Contains(out, "  querier: Querier__2 | null\n") {
		t.Errorf("Querier should be aliased in database.ts")
	}
	assertTypeChecksWithFiles(t, out, `"ESNext", "dom"`, withFiles(map[string]string{"use.ts": declUse + `
export const body: Doc["body"] = { body: true }
const withBody = relation("decl.doc", { select: { id: ["col", "id"], body: ["col", "body"] } })
withBody.draft({ id: 1, body: { body: true } })
// @ts-expect-error a CBOR column keeps its declared type
withBody.draft({ id: 1, body: { body: 1 } })
`}))

	out = GenerateDatabaseTS(testDb, declOptions(t, "json"), wk)
	assertTypeChecksWithFiles(t, out, `"ESNext", "dom"`, withFiles(map[string]string{"use.ts": declUse + `
// @ts-expect-error a CBOR column is never under JSON
export const body: Doc["body"] = { body: true }
const withBody = relation("decl.doc", { select: { id: ["col", "id"], body: ["col", "body"] } })
export const withBodyIsNotNever: IsNever<typeof withBody.$row> = false
// @ts-expect-error writing a CBOR column fails to compile under JSON
withBody.draft({ id: 1, body: { body: true } })
// selecting one does not ; the row has no such key, and the server refuses the query
export const selectedBody: "body" extends keyof typeof withBody.$row ? "kept" : "dropped" = "dropped"
// the default selection keeps it, never
export const defaultBody: IsNever<(typeof all.$row)["body"]> = true
// @ts-expect-error even when not null
export const raw: Doc["raw"] = new Uint8Array()
`}))
}

func TestDeclarationWarnings(t *testing.T) {
	opts := declOptions(t, "cbor")
	opts.Types["decl.doc.nope"] = config.TypeDecl{Type: "X"}
	opts.Types["decl.doc.tags"] = config.TypeDecl{Type: "A", From: `./a"`}
	got := strings.Join(DeclarationWarnings(testDb, opts), "\n")
	for _, want := range []string{
		"typescript.types overrides the type declared in the comment on column decl.doc.configured",
		"column decl.doc.n : a declared type only applies to a json, jsonb or cbor column",
		"the column:: comment on column decl.doc.broken is ignored",
		"typescript.types.decl.doc.nope names no column",
		"typescript.types for column decl.doc.tags ignored",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing warning %q", want)
		}
	}
	if strings.Count(got, "\n")+1 != 5 {
		t.Errorf("want exactly 5 warnings, got:\n%s", got)
	}
}

func TestGenerateDatabaseJSON_DeclaredTypes(t *testing.T) {
	raw, err := GenerateDatabaseJSON(testDb, declOptions(t, "cbor"), declWellknowns(t))
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Types     map[string]map[string]any `json:"types"`
		Relations map[string]struct {
			Comment string           `json:"comment"`
			Columns []ColumnInfoJSON `json:"columns"`
		} `json:"relations"`
		Wellknowns map[string]WellknownInfoJSON `json:"wellknowns"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	rel := doc.Relations["decl.doc"]
	if rel.Comment != "documents" {
		t.Errorf("relation comment %q", rel.Comment)
	}
	cols := map[string]ColumnInfoJSON{}
	for _, c := range rel.Columns {
		cols[c.Name] = c
	}
	if c := cols["meta"]; c.TSType != "{ a: number }" || c.Comment != "the meta" || c.TSFrom != "" {
		t.Errorf("meta : %+v", c)
	}
	if c := cols["body"]; c.TSType != "Body" || c.TSFrom != "./body" || c.Comment != "" {
		t.Errorf("body : %+v", c)
	}
	if c := cols["configured"]; c.TSType != "string" || c.Comment != "from the config" {
		t.Errorf("configured : %+v", c)
	}
	if c := cols["broken"]; c.TSType != "" || c.Comment != "" {
		t.Errorf("broken : %+v", c)
	}
	if d := doc.Types["decl.settings"]; d["ts_type"] != "Settings" || d["ts_from"] != "./settings" || d["comment"] != "user settings" {
		t.Errorf("decl.settings : %v", d)
	}
	if p := doc.Wellknowns["doc_meta"].Params[0]; p.TSType != "Settings" || p.TSFrom != "./settings" || p.Comment != "the settings" {
		t.Errorf("doc_meta param : %+v", p)
	}
}
