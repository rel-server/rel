package tsgen

import (
	"testing"

	"github.com/bytedance/sonic"
	"github.com/rel-server/rel/config"
)

func generateTestJSON(t *testing.T, opts Options) *DatabaseJSON {
	t.Helper()
	raw, err := GenerateDatabaseJSON(testDb, opts, nil)
	if err != nil {
		t.Fatalf("GenerateDatabaseJSON: %v", err)
	}
	var doc DatabaseJSON
	if err := sonic.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("unmarshaling generated database.json: %v\n%s", err, raw)
	}
	return &doc
}

// TestGenerateDatabaseJSON_Relations is specs/database-json.md ## `relations` :
// kind/comment/columns/primary_key/unique_constraints/indexes, all sourced
// straight from testdata/schema.sql's own hotel.rooms (two indexes, an FK
// each) and hotel.room_types (a UNIQUE constraint distinct from its PK).
func TestGenerateDatabaseJSON_Relations(t *testing.T) {
	doc := generateTestJSON(t, Options{Schemas: []string{"hotel"}, Blacklist: config.DefaultBlacklist()})

	properties, ok := doc.Relations["hotel.properties"]
	if !ok {
		t.Fatalf("hotel.properties missing from relations ; got %#v", doc.Relations)
	}
	if properties.Kind != "table" {
		t.Errorf("hotel.properties should be kind \"table\" ; got %q", properties.Kind)
	}
	if properties.Comment != "a hotel property" {
		t.Errorf("hotel.properties' own COMMENT ON should surface verbatim ; got %q", properties.Comment)
	}
	if properties.PrimaryKey == nil || len(properties.PrimaryKey.Columns) != 1 || properties.PrimaryKey.Columns[0] != "id" {
		t.Errorf("hotel.properties' primary_key should name its own id column ; got %#v", properties.PrimaryKey)
	}
	if properties.PrimaryKey.Name == "" {
		t.Errorf("hotel.properties' primary_key should carry the real constraint name, not be blank")
	}

	cleanRooms, ok := doc.Relations["hotel.clean_rooms"]
	if !ok || cleanRooms.Kind != "view" {
		t.Errorf("hotel.clean_rooms should be exported with kind \"view\" ; got %#v", cleanRooms)
	}

	roomTypes, ok := doc.Relations["hotel.room_types"]
	if !ok {
		t.Fatalf("hotel.room_types missing from relations")
	}
	foundUnique := false
	for _, u := range roomTypes.UniqueConstraints {
		if u.Name == "room_types_name_key" && len(u.Columns) == 1 && u.Columns[0] == "name" {
			foundUnique = true
		}
	}
	if !foundUnique {
		t.Errorf("hotel.room_types should list its room_types_name_key UNIQUE constraint ; got %#v", roomTypes.UniqueConstraints)
	}

	rooms, ok := doc.Relations["hotel.rooms"]
	if !ok {
		t.Fatalf("hotel.rooms missing from relations")
	}
	if len(rooms.Indexes) < 2 {
		t.Errorf("hotel.rooms should carry both its own indexes ; got %#v", rooms.Indexes)
	}
	var propertyIdCol *ColumnInfoJSON
	for i := range rooms.Columns {
		if rooms.Columns[i].Name == "property_id" {
			propertyIdCol = &rooms.Columns[i]
		}
	}
	if propertyIdCol == nil {
		t.Fatalf("hotel.rooms.property_id column missing")
	}
	if propertyIdCol.IsNullable {
		t.Errorf("hotel.rooms.property_id is NOT NULL ; is_nullable should be false")
	}
}

// TestGenerateDatabaseJSON_TypesRegistryClosure is the transitive-closure
// rule (specs/database-json.md ## `types` and its ## Endpoint amendment) :
// hotel.suppliers.mailing_address's own composite type (hotel.address) must
// resolve in `types`, with its backing relation present in `relations` even
// though it's never itself a queryable table ; hotel.landmarks.location's
// hidden.coordinates must resolve the same way even though "hidden" is
// never whitelisted at all.
func TestGenerateDatabaseJSON_TypesRegistryClosure(t *testing.T) {
	doc := generateTestJSON(t, Options{Schemas: []string{"hotel"}, Blacklist: config.DefaultBlacklist()})

	suppliers, ok := doc.Relations["hotel.suppliers"]
	if !ok {
		t.Fatalf("hotel.suppliers missing from relations")
	}
	var addressCol *ColumnInfoJSON
	for i := range suppliers.Columns {
		if suppliers.Columns[i].Name == "mailing_address" {
			addressCol = &suppliers.Columns[i]
		}
	}
	if addressCol == nil {
		t.Fatalf("hotel.suppliers.mailing_address column missing")
	}

	addressType, ok := doc.Types[addressCol.Type]
	if !ok {
		t.Fatalf("mailing_address's own type %q missing from types registry ; got %#v", addressCol.Type, doc.Types)
	}
	if addressType.Kind != "composite" {
		t.Errorf("hotel.address should register as kind \"composite\" ; got %q", addressType.Kind)
	}
	if _, ok := doc.Relations[addressType.Relation]; !ok {
		t.Errorf("hotel.address's own backing relation %q should resolve in relations ; got %#v", addressType.Relation, doc.Relations)
	}

	landmarks, ok := doc.Relations["hotel.landmarks"]
	if !ok {
		t.Fatalf("hotel.landmarks missing from relations")
	}
	var locationCol *ColumnInfoJSON
	for i := range landmarks.Columns {
		if landmarks.Columns[i].Name == "location" {
			locationCol = &landmarks.Columns[i]
		}
	}
	if locationCol == nil {
		t.Fatalf("hotel.landmarks.location column missing")
	}
	if locationCol.Type != "hidden.coordinates" {
		t.Fatalf("hotel.landmarks.location should type as hidden.coordinates ; got %q", locationCol.Type)
	}

	hiddenType, ok := doc.Types["hidden.coordinates"]
	if !ok {
		t.Fatalf("hidden.coordinates missing from types even though \"hidden\" is never whitelisted ; got %#v", doc.Types)
	}
	if hiddenType.Kind != "composite" {
		t.Errorf("hidden.coordinates should register as kind \"composite\" ; got %q", hiddenType.Kind)
	}
	if _, ok := doc.Relations["hidden.coordinates"]; !ok {
		t.Errorf("hidden.coordinates' own backing relation should resolve in relations despite its schema never being whitelisted ; got %#v", doc.Relations)
	} else if doc.Relations["hidden.coordinates"].Kind != "type" {
		t.Errorf("hidden.coordinates' own relation entry should be kind \"type\" ; got %q", doc.Relations["hidden.coordinates"].Kind)
	}
}

// TestGenerateDatabaseJSON_Relationships is the widened-visibility rule
// (specs/database-json.md ## `relationships`) : hotel.staff -> hotel.properties
// is deliberately left unindexed (testdata/schema.sql) — unlike database.ts,
// it must still appear here, in both directions, flagged eligible: false.
// hotel.rooms' two FKs must appear as two distinct, eligible variants.
func TestGenerateDatabaseJSON_Relationships(t *testing.T) {
	doc := generateTestJSON(t, Options{Schemas: []string{"hotel"}, Blacklist: config.DefaultBlacklist()})

	staffVariants, ok := doc.Relationships["hotel.staff"]
	if !ok || len(staffVariants) != 1 {
		t.Fatalf("expected exactly one relationship variant under hotel.staff ; got %#v", staffVariants)
	}
	sv := staffVariants[0]
	if sv.Target != "hotel.properties" || sv.Direction != "outgoing" {
		t.Errorf("hotel.staff's own variant should be an outgoing FK to hotel.properties ; got %#v", sv)
	}
	if sv.Eligible {
		t.Errorf("hotel.staff.property_id is deliberately unindexed ; eligible should be false ; got %#v", sv)
	}
	if sv.On["property_id"] != "id" {
		t.Errorf("hotel.staff's own \"on\" should map property_id -> id ; got %#v", sv.On)
	}
	if sv.ConstraintName == "" {
		t.Errorf("hotel.staff's own variant should carry the real FK constraint name")
	}

	propertiesVariants := doc.Relationships["hotel.properties"]
	foundIncomingFromStaff := false
	for _, v := range propertiesVariants {
		if v.Target == "hotel.staff" {
			foundIncomingFromStaff = true
			if v.Direction != "incoming" {
				t.Errorf("hotel.properties' variant pointing at hotel.staff should be \"incoming\" ; got %q", v.Direction)
			}
			if v.Eligible {
				t.Errorf("the reverse side of the unindexed hotel.staff FK should also report eligible: false ; got %#v", v)
			}
		}
	}
	if !foundIncomingFromStaff {
		t.Errorf("hotel.properties should list an incoming relationship from hotel.staff even though it's ineligible ; got %#v", propertiesVariants)
	}

	roomsVariants := doc.Relationships["hotel.rooms"]
	if len(roomsVariants) != 2 {
		t.Fatalf("expected hotel.rooms to have two relationship variants (properties, room_types) ; got %#v", roomsVariants)
	}
	for _, v := range roomsVariants {
		if !v.Eligible {
			t.Errorf("hotel.rooms' own FKs are indexed (testdata/schema.sql) ; expected eligible: true for %#v", v)
		}
		if v.Direction != "outgoing" {
			t.Errorf("hotel.rooms' own FK-holding variants should be \"outgoing\" ; got %#v", v)
		}
	}
}

// TestGenerateDatabaseJSON_Functions is FunctionInfo's own mode/volatility/
// record-shape coverage (specs/database-json.md ## `functions`) :
// hotel.property_summary (RETURNS TABLE) must report returns: null with its
// row shape carried by "out"/"table"-mode args ; hotel.rooms_available (a
// real SETOF hotel.properties) must resolve `relation`.
func TestGenerateDatabaseJSON_Functions(t *testing.T) {
	doc := generateTestJSON(t, Options{Schemas: []string{"hotel"}, Blacklist: config.DefaultBlacklist()})

	summaries, ok := doc.Functions["hotel.property_summary"]
	if !ok || len(summaries) != 1 {
		t.Fatalf("hotel.property_summary missing or overloaded unexpectedly ; got %#v", doc.Functions["hotel.property_summary"])
	}
	summary := summaries[0]
	if summary.Returns != nil {
		t.Errorf("a RETURNS TABLE function has no real backing type ; returns should be null ; got %v", *summary.Returns)
	}
	var sawOut, sawIn int
	for _, a := range summary.Args {
		switch a.Mode {
		case "table":
			sawOut++
		case "in":
			sawIn++
		default:
			t.Errorf("unexpected arg mode %q on hotel.property_summary ; got %#v", a.Mode, summary.Args)
		}
	}
	if sawOut != 2 {
		t.Errorf("hotel.property_summary should carry both its own RETURNS TABLE columns as \"table\"-mode args ; got %d", sawOut)
	}
	if sawIn != 1 {
		t.Errorf("hotel.property_summary should still carry its own property_id IN argument ; got %d", sawIn)
	}

	available, ok := doc.Functions["hotel.rooms_available"]
	if !ok || len(available) != 1 {
		t.Fatalf("hotel.rooms_available missing ; got %#v", doc.Functions["hotel.rooms_available"])
	}
	if available[0].Relation == nil || *available[0].Relation != "hotel.properties" {
		t.Errorf("hotel.rooms_available's SETOF hotel.properties should resolve relation to hotel.properties ; got %#v", available[0].Relation)
	}
	foundOptional := false
	for _, a := range available[0].Args {
		if a.Name == "on_date" {
			foundOptional = a.HasDefault
		}
	}
	if !foundOptional {
		t.Errorf("hotel.rooms_available's on_date argument has a default ; has_default should be true ; got %#v", available[0].Args)
	}

	count, ok := doc.Functions["hotel.property_count"]
	if !ok || len(count) != 1 {
		t.Fatalf("hotel.property_count missing")
	}
	if count[0].Volatility == "" {
		t.Errorf("hotel.property_count should report a non-empty volatility")
	}
	if len(count[0].Args) != 0 {
		t.Errorf("hotel.property_count takes no arguments ; got %#v", count[0].Args)
	}
}

// TestGenerateDatabaseJSON_Blacklist proves a blacklisted relation is
// absent from every section, same as database.ts.
func TestGenerateDatabaseJSON_Blacklist(t *testing.T) {
	bl := config.Blacklist{Relations: map[string]map[string]string{"hotel": {"staff": "y"}}}
	doc := generateTestJSON(t, Options{Schemas: []string{"hotel"}, Blacklist: bl})

	if _, ok := doc.Relations["hotel.staff"]; ok {
		t.Errorf("blacklisted hotel.staff should be absent from relations")
	}
	if _, ok := doc.Relationships["hotel.staff"]; ok {
		t.Errorf("blacklisted hotel.staff should be absent from relationships")
	}
	for _, v := range doc.Relationships["hotel.properties"] {
		if v.Target == "hotel.staff" {
			t.Errorf("blacklisted hotel.staff should not appear as another relation's own target ; got %#v", v)
		}
	}
}

// TestGenerateDatabaseJSON_SchemasParamFiltering proves the schemas
// whitelist restricts root export the same way database.ts's own does,
// while the types closure (TestGenerateDatabaseJSON_TypesRegistryClosure)
// still reaches past it.
func TestGenerateDatabaseJSON_SchemasParamFiltering(t *testing.T) {
	doc := generateTestJSON(t, Options{Schemas: []string{"alt"}, Blacklist: config.DefaultBlacklist()})

	if _, ok := doc.Relations["hotel.properties"]; ok {
		t.Errorf("hotel.properties should be excluded when only \"alt\" is whitelisted ; got %#v", doc.Relations)
	}
	if _, ok := doc.Functions["alt.property_average_rating"]; !ok {
		t.Errorf("alt.property_average_rating should be exported when \"alt\" is whitelisted ; got %#v", doc.Functions)
	}
}
