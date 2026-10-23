package tsgen

import "testing"

// baseScalarTypes is keyed by type name, so a misspelt key silently types nothing. Every key must be a
// real type of the database (citext comes from an extension the test database does not install).
func TestBaseScalarTypes_EveryKeyIsARealType(t *testing.T) {
	known := map[string]bool{}
	for _, typ := range testDb.TypeMapByOid {
		if typ.PgIdentifier.Schema == "pg_catalog" {
			known[typ.PgIdentifier.Name] = true
		}
	}
	for name := range baseScalarTypes {
		if name == "citext" {
			continue
		}
		if !known[name] {
			t.Errorf("baseScalarTypes[%q] names no pg_catalog type", name)
		}
	}
}
