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
		`"hotel.property_average_rating": {`,
		`"hotel.rooms_available": {`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("generated schema missing %q ; full output:\n%s", want, out)
		}
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
}

// TestGenerateDatabaseTS_TypeChecks proves the concatenated, self-sufficient
// database.ts output specs/typescript.md ## database.ts describes actually
// type-checks on its own — AGENTS.md's "Always use `just check`: NO error
// MUST remain" rule, applied to generated output rather than the hand-
// maintained typescript/ draft.
func TestGenerateDatabaseTS_TypeChecks(t *testing.T) {
	if _, err := exec.LookPath("tsc"); err != nil {
		t.Skip("tsc not found in PATH ; skipping generated-output type-check")
	}

	out := GenerateDatabaseTS(testDb, Options{Schemas: []string{"hotel"}, Blacklist: config.DefaultBlacklist()})

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
