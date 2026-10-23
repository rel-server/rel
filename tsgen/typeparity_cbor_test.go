package tsgen

import (
	"testing"

	"github.com/rel-server/rel/query"
)

// A column rel reads natively as CBOR must not be typed `unknown` for the client : adding a scalar to the
// CBOR reader without a TypeScript type would hand clients a value they cannot use without a cast.
func TestBaseScalarTypes_EveryNativeCBORScalarIsTyped(t *testing.T) {
	for _, typ := range testDb.TypeMapByOid {
		if typ.Underlying() != typ || typ.IsEnum() || typ.IsArray() || typ.IsComposite() || !query.NativeCBORType(typ) {
			continue
		}
		if got := tsTypeExpr(typ, newTypeCollector()); got == "unknown" && typ.PgIdentifier.Name != "json" && typ.PgIdentifier.Name != "jsonb" {
			t.Errorf("%s is read natively as CBOR but typed %q in TypeScript", typ.PgIdentifier.String(), got)
		}
	}
}
