package route

import (
	"testing"
)

func TestBuildRegistry_DiscoversExpectedRoutes(t *testing.T) {
	reg, err := BuildRegistry(testDb, testCfg)
	if err != nil {
		t.Fatalf("BuildRegistry: %v", err)
	}

	if _, ok := reg.Lookup("public", "fn_echo0", "GET"); !ok {
		t.Errorf("expected fn_echo0 to be discovered")
	}
	if _, ok := reg.Lookup("public", "fn_echo1", "POST"); !ok {
		t.Errorf("expected fn_echo1 to be discovered")
	}
	if _, ok := reg.Lookup("public", "fn_secret", "GET"); !ok {
		t.Errorf("expected fn_secret to be discovered")
	}
	if _, ok := reg.Lookup("public", "fn_login", "POST"); !ok {
		t.Errorf("expected fn_login to be discovered")
	}
}

func TestBuildRegistry_ExcludesLeadingUnderscore(t *testing.T) {
	reg, err := BuildRegistry(testDb, testCfg)
	if err != nil {
		t.Fatalf("BuildRegistry: %v", err)
	}
	if _, ok := reg.Lookup("public", "_fn_internal", "POST"); ok {
		t.Errorf("expected _fn_internal to be excluded (leading underscore)")
	}
}

// TestBuildRegistry_ExcludesWrongShape covers fn_check_session specifically
// : it takes one jsonb argument, NOT one RelHttpRequest argument, so it
// must never become a discoverable /route function even though
// http.functions.check_session names it — check_session is invoked
// directly by the JWT lifecycle, never dispatched as a route.
func TestBuildRegistry_ExcludesWrongShape(t *testing.T) {
	reg, err := BuildRegistry(testDb, testCfg)
	if err != nil {
		t.Fatalf("BuildRegistry: %v", err)
	}
	if _, ok := reg.Lookup("public", "fn_check_session", "POST"); ok {
		t.Errorf("expected fn_check_session (jsonb arg, not RelHttpRequest) to be excluded from route discovery")
	}
}

func TestBuildRegistry_VerbSplitting(t *testing.T) {
	reg, err := BuildRegistry(testDb, testCfg)
	if err != nil {
		t.Fatalf("BuildRegistry: %v", err)
	}
	getRoute, ok := reg.Lookup("public", "fn_verbtest", "GET")
	if !ok {
		t.Fatalf("expected fn_verbtest__GET to answer GET")
	}
	if getRoute.Function.Identifier.Name != "fn_verbtest__get" {
		t.Errorf("expected the GET route to point at fn_verbtest__get, got %q", getRoute.Function.Identifier.Name)
	}
	postRoute, ok := reg.Lookup("public", "fn_verbtest", "POST")
	if !ok {
		t.Fatalf("expected fn_verbtest__POST to answer POST")
	}
	if postRoute.Function.Identifier.Name != "fn_verbtest__post" {
		t.Errorf("expected the POST route to point at fn_verbtest__post, got %q", postRoute.Function.Identifier.Name)
	}
	if _, ok := reg.Lookup("public", "fn_verbtest", "DELETE"); ok {
		t.Errorf("expected DELETE to have no match : both variants are verb-suffixed, no unsuffixed fallback exists")
	}
}

func TestBuildRegistry_MimeTypeDomain(t *testing.T) {
	reg, err := BuildRegistry(testDb, testCfg)
	if err != nil {
		t.Fatalf("BuildRegistry: %v", err)
	}
	route, ok := reg.Lookup("public", "fn_image", "GET")
	if !ok {
		t.Fatalf("expected fn_image to be discovered")
	}
	if route.MimeType != "image/png" {
		t.Errorf("expected MimeType=image/png, got %q", route.MimeType)
	}
}

func TestBuildRegistry_AllowedRoutesRestricts(t *testing.T) {
	restricted := *testCfg
	restricted.Http.Functions.AllowedRoutes = `^public\.fn_login$`
	reg, err := BuildRegistry(testDb, &restricted)
	if err != nil {
		t.Fatalf("BuildRegistry: %v", err)
	}
	if _, ok := reg.Lookup("public", "fn_login", "POST"); !ok {
		t.Errorf("expected fn_login to still be discovered (matches allowed_routes)")
	}
	if _, ok := reg.Lookup("public", "fn_echo0", "GET"); ok {
		t.Errorf("expected fn_echo0 to be excluded (doesn't match allowed_routes)")
	}
}

// TestBuildRegistry_TextMimeTypeDomain covers the mimetype-domain
// generalization to text-underlying domains (## HTTP's opening
// paragraphs), alongside the existing bytea-underlying case.
func TestBuildRegistry_TextMimeTypeDomain(t *testing.T) {
	reg, err := BuildRegistry(testDb, testCfg)
	if err != nil {
		t.Fatalf("BuildRegistry: %v", err)
	}
	route, ok := reg.Lookup("public", "fn_text_domain", "GET")
	if !ok {
		t.Fatalf("expected fn_text_domain to be discovered")
	}
	if route.MimeType != "text/plain" {
		t.Errorf("expected MimeType=text/plain, got %q", route.MimeType)
	}
}

// TestBuildRegistry_FilesShapes covers ## Request bodies' (req, files
// bytea[]) and (req, files bytea[], parts_headers jsonb) shapes being
// discovered, with the right Route flags set.
func TestBuildRegistry_FilesShapes(t *testing.T) {
	reg, err := BuildRegistry(testDb, testCfg)
	if err != nil {
		t.Fatalf("BuildRegistry: %v", err)
	}

	upload, ok := reg.Lookup("public", "fn_upload", "POST")
	if !ok {
		t.Fatalf("expected fn_upload to be discovered")
	}
	if !upload.AcceptsFiles || upload.AcceptsPartsHeaders {
		t.Errorf("expected fn_upload AcceptsFiles=true AcceptsPartsHeaders=false, got %+v", upload)
	}

	uploadWithHeaders, ok := reg.Lookup("public", "fn_upload_with_headers", "POST")
	if !ok {
		t.Fatalf("expected fn_upload_with_headers to be discovered")
	}
	if !uploadWithHeaders.AcceptsFiles || !uploadWithHeaders.AcceptsPartsHeaders {
		t.Errorf("expected fn_upload_with_headers AcceptsFiles=true AcceptsPartsHeaders=true, got %+v", uploadWithHeaders)
	}
}

// TestBuildRegistry_ReorderedFilesShapeExcluded proves the four shapes are
// matched by TYPE SEQUENCE, not by "has the right types somewhere" :
// fn_wrong_shape declares (req, parts_headers jsonb, files bytea[]) —
// jsonb before bytea[], the reverse of the one recognized order — and must
// not be discovered at all.
func TestBuildRegistry_ReorderedFilesShapeExcluded(t *testing.T) {
	reg, err := BuildRegistry(testDb, testCfg)
	if err != nil {
		t.Fatalf("BuildRegistry: %v", err)
	}
	if _, ok := reg.Lookup("public", "fn_wrong_shape", "POST"); ok {
		t.Errorf("expected fn_wrong_shape (reordered files/parts_headers) to be excluded from route discovery")
	}
}

// TestBuildRegistry_AmbiguousRoute_TwoCollidingFunctions_NeitherRoutable is
// a regression test for a real bug found by hand : two Postgres overloads
// colliding on the same (schema, base, verb) registry key used to leave
// the FIRST one silently registered (only the second was skipped), even
// though the log line already promised "skipping both". schema.sql defines
// fn_dupe() and fn_dupe(req) — both collide on (public, "fn_dupe", "").
func TestBuildRegistry_AmbiguousRoute_TwoCollidingFunctions_NeitherRoutable(t *testing.T) {
	reg, err := BuildRegistry(testDb, testCfg)
	if err != nil {
		t.Fatalf("BuildRegistry: %v", err)
	}
	if _, ok := reg.Lookup("public", "fn_dupe", "GET"); ok {
		t.Errorf("expected fn_dupe to be excluded entirely once two overloads collide on the same route key")
	}
}

// TestBuildRegistry_AmbiguousRoute_ThirdCollidingFunction_StillNotRoutable
// covers the specific gap the hand-fix's "ambiguous" tracking set closes :
// schema.sql ALSO defines a third overload, fn_dupe(req, files bytea[]),
// colliding on the exact same key. A naive fix that only deletes the map
// entry on the SECOND collision would leave room for this third function to
// walk in afterward and register itself as the key's sole (and, from the
// registry's point of view, unambiguous-looking) owner — this test proves
// that doesn't happen : the key stays excluded even after a third collider.
func TestBuildRegistry_AmbiguousRoute_ThirdCollidingFunction_StillNotRoutable(t *testing.T) {
	reg, err := BuildRegistry(testDb, testCfg)
	if err != nil {
		t.Fatalf("BuildRegistry: %v", err)
	}
	route, ok := reg.Lookup("public", "fn_dupe", "POST")
	if ok {
		t.Errorf("expected fn_dupe to stay excluded even after a third colliding overload, got route pointing at %s", route.Function.Identifier.String())
	}
}

func TestBuildRegistry_UnknownRouteMisses(t *testing.T) {
	reg, err := BuildRegistry(testDb, testCfg)
	if err != nil {
		t.Fatalf("BuildRegistry: %v", err)
	}
	if _, ok := reg.Lookup("public", "does_not_exist", "GET"); ok {
		t.Errorf("expected no match for a nonexistent function")
	}
}
