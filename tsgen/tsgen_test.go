package tsgen

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ceymard/rel/config"
	"github.com/ceymard/rel/pg"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
)

var testDb *pg.DbInfos

func TestMain(m *testing.M) {
	ctx := context.Background()

	container, err := postgres.Run(ctx, "postgres:16-alpine",
		postgres.WithInitScripts("testdata/schema.sql"),
		postgres.BasicWaitStrategies(),
	)
	if err != nil {
		panic(err)
	}

	uri, cerr := container.ConnectionString(ctx, "sslmode=disable")
	if cerr != nil {
		_ = container.Terminate(ctx)
		panic(cerr)
	}
	testDb, err = pg.NewInfos(uri)
	if err != nil {
		_ = container.Terminate(ctx)
		panic(err)
	}

	code := m.Run()
	_ = container.Terminate(ctx)
	os.Exit(code)
}

func TestGenerateSchema_HotelExample(t *testing.T) {
	out := GenerateSchema(testDb, Options{Schemas: []string{"hotel"}, Blacklist: config.DefaultBlacklist()})

	for _, want := range []string{
		"interface Table__Hotel__Properties {",
		"interface Table__Hotel__RoomTypes {",
		"interface Table__Hotel__Rooms {",
		"interface View__Hotel__CleanRooms {",
		"type Type__Hotel__RoomStatus = \"clean\" | \"dirty\"",
		"type Type__Hotel__PositiveInt = number",
		`"hotel.rooms": Table__Hotel__Rooms`,
		`"hotel.properties": Table__Hotel__Properties`,
		"export interface Relationships {",
		"export interface Functions {",
		`"hotel.property_average_rating":`,
		`"hotel.rooms_available": {`,
		"type EmptyObject = Record<string, never>",
		`"hotel.property_count": {`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("generated schema missing %q ; full output:\n%s", want, out)
		}
	}

	// hotel.property_average_rating is overloaded (testdata/schema.sql) :
	// its Functions entry must be a union of both signatures, not a
	// duplicate object key (TS2300).
	overloadIdx := strings.Index(out, `"hotel.property_average_rating":`)
	if overloadIdx < 0 {
		t.Fatalf("hotel.property_average_rating entry not found")
	}
	if !strings.Contains(out[overloadIdx:overloadIdx+400], "| {") {
		t.Errorf("expected hotel.property_average_rating's Functions entry to be a union of its two overloads ; got:\n%s", out[overloadIdx:overloadIdx+400])
	}

	// hotel.property_count takes zero arguments : `args` must fall back to
	// EmptyObject, never a literal `{}` (biome's noBannedTypes).
	countIdx := strings.Index(out, `"hotel.property_count": {`)
	if countIdx < 0 {
		t.Fatalf("hotel.property_count entry not found")
	}
	countBlockEnd := strings.Index(out[countIdx:], "  }\n")
	countBlock := out[countIdx : countIdx+countBlockEnd]
	if !strings.Contains(countBlock, "args: EmptyObject") {
		t.Errorf("expected hotel.property_count's args to be EmptyObject ; got:\n%s", countBlock)
	}
	if strings.Contains(countBlock, "args: {") {
		t.Errorf("hotel.property_count's args should never be a literal {} ; got:\n%s", countBlock)
	}

	// hotel.staff -> hotel.properties is deliberately left unindexed
	// (testdata/schema.sql) : it's still a plain target relation (Relations
	// map), but neither Relationships direction should mention it.
	relStart := strings.Index(out, "export interface Relationships {")
	relEnd := strings.Index(out[relStart:], "\n}\n")
	if relStart < 0 || relEnd < 0 {
		t.Fatalf("Relationships interface not found in output:\n%s", out)
	}
	if strings.Contains(out[relStart:relStart+relEnd], "hotel.staff") {
		t.Errorf("unindexed FK from hotel.staff should have been excluded from Relationships ; got:\n%s", out[relStart:relStart+relEnd])
	}

	// hotel.rooms has two FKs (properties, room_types) -> a union, not a
	// single bare variant, under its own key.
	roomsKeyIdx := strings.Index(out, `"hotel.rooms":`)
	if roomsKeyIdx < 0 || !strings.Contains(out[roomsKeyIdx:roomsKeyIdx+400], "| {") {
		t.Errorf("expected hotel.rooms' Relationships entry to be a union of two variants ; got:\n%s", out)
	}

	// FunctionsByName : with only "hotel" whitelisted, both bare names
	// resolve unambiguously to their own hotel.* qualified key.
	for _, want := range []string{
		"export interface FunctionsByName {",
		`property_average_rating: Functions["hotel.property_average_rating"]`,
		`rooms_available: Functions["hotel.rooms_available"]`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("generated schema missing %q ; full output:\n%s", want, out)
		}
	}

	// ComputedProperties : property_average_rating's FIRST overload takes
	// hotel.properties as its own first argument and accepts arity 1, so
	// hotel.properties gets a Computed__ entry for it ; the second overload
	// (property_id int) doesn't apply to any relation, and rooms_available
	// isn't single-argument-callable at all (on_date is a SECOND parameter,
	// not the row itself), so neither contributes further entries.
	for _, want := range []string{
		"export interface ComputedProperties {",
		`"hotel.properties": Computed__Hotel__Properties`,
		"interface Computed__Hotel__Properties {",
		"property_average_rating: number",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("generated schema missing %q ; full output:\n%s", want, out)
		}
	}
}

// TestGenerateSchema_BareNameShadowing exercises bareNameWinners' own
// disambiguation rule (FunctionsByName only) : "alt" schema's own
// property_average_rating (testdata/schema.sql) shares hotel's bare name
// and comes first in search_path, so it — not hotel's — is what an
// unqualified call actually reaches.
//
// ComputedProperties is deliberately UNAFFECTED by this : it doesn't consult
// search_path at all (renderComputedProperties' own doc comment covers why
// — a real deployment's search_path frequently doesn't cover its own
// schemas, which made this whole discoverability feature vanish for no
// good reason before this fix), so hotel.properties still advertises
// property_average_rating regardless of which schema wins the bare name.
func TestGenerateSchema_BareNameShadowing(t *testing.T) {
	out := GenerateSchema(testDb, Options{Schemas: []string{"hotel", "alt"}, Blacklist: config.DefaultBlacklist()})

	if !strings.Contains(out, `property_average_rating: Functions["alt.property_average_rating"]`) {
		t.Errorf("expected the bare name to resolve to alt's (search_path-earlier) version ; got:\n%s", out)
	}
	if strings.Contains(out, `property_average_rating: Functions["hotel.property_average_rating"]`) {
		t.Errorf("bare name should NOT resolve to hotel's shadowed version ; got:\n%s", out)
	}

	computedIdx := strings.Index(out, "interface Computed__Hotel__Properties {")
	if computedIdx < 0 {
		t.Fatalf("expected hotel.properties to still advertise property_average_rating as a computed property, shadowed bare name notwithstanding ; got:\n%s", out)
	}
	blockEnd := strings.Index(out[computedIdx:], "}\n")
	computedBlock := out[computedIdx : computedIdx+blockEnd]
	if !strings.Contains(computedBlock, "property_average_rating") {
		t.Errorf("hotel.properties should still list property_average_rating : ComputedProperties doesn't depend on search_path ; got:\n%s", computedBlock)
	}

	// alt.property_score (testdata/schema.sql) is structurally eligible
	// (first argument is hotel.properties, arity 1) but lives in a
	// DIFFERENT schema than hotel.properties itself — ComputedProperties'
	// own same-schema restriction must exclude it.
	if strings.Contains(computedBlock, "property_score") {
		t.Errorf("hotel.properties should NOT list alt.property_score : ComputedProperties is restricted to the relation's own schema ; got:\n%s", computedBlock)
	}
}

// TestGenerateDatabaseTS_TypeChecks proves the concatenated, self-sufficient
// database.ts output specs/typescript.md ## database.ts describes actually
// type-checks on its own — AGENTS.md's "Always use `just check`: NO error
// MUST remain" rule, applied to generated output rather than the hand-
// maintained typescript/ draft.
func TestGenerateDatabaseTS_TypeChecks(t *testing.T) {
	out := GenerateDatabaseTS(testDb, Options{Schemas: []string{"hotel"}, Blacklist: config.DefaultBlacklist()})
	assertTypeChecks(t, out)
}

// TestGenerateDatabaseTS_TypeChecks_BareNameShadowing is
// TestGenerateSchema_BareNameShadowing's own database.ts actually
// type-checking, cross-schema FunctionsByName reference (Functions["alt....
func TestGenerateDatabaseTS_TypeChecks_BareNameShadowing(t *testing.T) {
	out := GenerateDatabaseTS(testDb, Options{Schemas: []string{"hotel", "alt"}, Blacklist: config.DefaultBlacklist()})
	assertTypeChecks(t, out)
}

func assertTypeChecks(t *testing.T, out string) {
	t.Helper()
	if _, err := exec.LookPath("tsc"); err != nil {
		t.Skip("tsc not found in PATH ; skipping generated-output type-check")
	}

	dir := t.TempDir()
	dbTsPath := filepath.Join(dir, "database.ts")
	if err := os.WriteFile(dbTsPath, []byte(out), 0o644); err != nil {
		t.Fatalf("writing generated database.ts: %v", err)
	}

	tsconfig := `{
  "compilerOptions": {
    "target": "es2022",
    "module": "esnext",
    "moduleResolution": "bundler",
    "noEmit": true,
    "strict": true,
    "lib": ["ESNext", "dom"],
    "types": []
  },
  "include": ["database.ts"]
}`
	if err := os.WriteFile(filepath.Join(dir, "tsconfig.json"), []byte(tsconfig), 0o644); err != nil {
		t.Fatalf("writing tsconfig.json: %v", err)
	}

	cmd := exec.Command("tsc", "--noEmit")
	cmd.Dir = dir
	if outBytes, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("generated database.ts failed to type-check :\n%s\n\n--- source ---\n%s", outBytes, out)
	}
}
