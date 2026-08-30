package rpc

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
// must never become a discoverable /rpc route even though
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

func TestBuildRegistry_UnknownRouteMisses(t *testing.T) {
	reg, err := BuildRegistry(testDb, testCfg)
	if err != nil {
		t.Fatalf("BuildRegistry: %v", err)
	}
	if _, ok := reg.Lookup("public", "does_not_exist", "GET"); ok {
		t.Errorf("expected no match for a nonexistent function")
	}
}
