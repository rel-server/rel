package route

import "testing"

func TestBuildRouteSet_SimpleGet(t *testing.T) {
	set, err := BuildRegistry(testDb, testCfg)
	if err != nil {
		t.Fatalf("BuildRegistry: %v", err)
	}
	e, ok := set.find("public", "fn_new_echo0")
	if !ok {
		t.Fatal("expected fn_new_echo0 to be discovered")
	}
	if e.Path != "/new/echo0" || e.AnonPath != "/new/echo0" {
		t.Errorf("got path=%q anonPath=%q", e.Path, e.AnonPath)
	}
	if len(e.Methods) != 1 || e.Methods[0] != "GET" {
		t.Errorf("expected inferred GET, got %v", e.Methods)
	}
}

func TestBuildRouteSet_MethodInferredPostForBytesArray(t *testing.T) {
	set, err := BuildRegistry(testDb, testCfg)
	if err != nil {
		t.Fatalf("BuildRegistry: %v", err)
	}
	e, ok := set.find("public", "fn_new_upload")
	if !ok {
		t.Fatal("expected fn_new_upload to be discovered")
	}
	if !e.AcceptsBytesArray {
		t.Error("expected AcceptsBytesArray")
	}
	if len(e.Methods) != 1 || e.Methods[0] != "POST" {
		t.Errorf("expected inferred POST, got %v", e.Methods)
	}
}

func TestBuildRouteSet_TextPathArgument(t *testing.T) {
	set, err := BuildRegistry(testDb, testCfg)
	if err != nil {
		t.Fatalf("BuildRegistry: %v", err)
	}
	e, ok := set.find("public", "fn_new_byid")
	if !ok {
		t.Fatal("expected fn_new_byid to be discovered")
	}
	if e.AnonPath != "/new/items/{}" {
		t.Errorf("expected anonymized path /new/items/{}, got %q", e.AnonPath)
	}
	if len(e.PathArgs) != 1 || e.PathArgs[0] != "id" {
		t.Errorf("expected PathArgs [id], got %v", e.PathArgs)
	}
}

func TestBuildRouteSet_StreamUploadNoByteaArg(t *testing.T) {
	set, err := BuildRegistry(testDb, testCfg)
	if err != nil {
		t.Fatalf("BuildRegistry: %v", err)
	}
	e, ok := set.find("public", "fn_new_stream")
	if !ok {
		t.Fatal("expected fn_new_stream to be discovered")
	}
	if !e.StreamUpload {
		t.Error("expected StreamUpload")
	}
	if !e.FullControl {
		t.Error("expected stream_upload to require the full-control shape")
	}
	if e.AcceptsBytes || e.AcceptsBytesArray {
		t.Error("stream_upload function must not accept bytes directly")
	}
	if len(e.Methods) != 1 || e.Methods[0] != "POST" {
		t.Errorf("expected inferred POST for stream_upload, got %v", e.Methods)
	}
}

func TestBuildRouteSet_FullControlShape(t *testing.T) {
	set, err := BuildRegistry(testDb, testCfg)
	if err != nil {
		t.Fatalf("BuildRegistry: %v", err)
	}
	e, ok := set.find("public", "fn_new_fullcontrol")
	if !ok {
		t.Fatal("expected fn_new_fullcontrol to be discovered")
	}
	if !e.FullControl {
		t.Error("expected FullControl")
	}
}

func TestBuildRouteSet_Middleware(t *testing.T) {
	set, err := BuildRegistry(testDb, testCfg)
	if err != nil {
		t.Fatalf("BuildRegistry: %v", err)
	}
	e, ok := set.find("public", "fn_new_middleware")
	if !ok {
		t.Fatal("expected fn_new_middleware to be discovered")
	}
	if !e.IsMiddleware || !e.FullControl {
		t.Errorf("expected middleware + full-control, got %+v", e)
	}
	found := false
	for _, m := range set.Middleware {
		if m.Function.Identifier.Name == "fn_new_middleware" {
			found = true
		}
	}
	if !found {
		t.Error("expected fn_new_middleware in set.Middleware")
	}
	for _, r := range set.Routes {
		if r.Function.Identifier.Name == "fn_new_middleware" {
			t.Error("middleware function must not also appear in set.Routes")
		}
	}
}

func TestBuildRouteSet_CollisionExcludesBoth(t *testing.T) {
	set, err := BuildRegistry(testDb, testCfg)
	if err != nil {
		t.Fatalf("BuildRegistry: %v", err)
	}
	if _, ok := set.find("public", "fn_new_collide_a"); ok {
		t.Error("expected fn_new_collide_a to be excluded by collision")
	}
	if _, ok := set.find("public", "fn_new_collide_b"); ok {
		t.Error("expected fn_new_collide_b to be excluded by collision")
	}
}

func TestBuildRouteSet_DisjointMethodsCoexist(t *testing.T) {
	set, err := BuildRegistry(testDb, testCfg)
	if err != nil {
		t.Fatalf("BuildRegistry: %v", err)
	}
	if _, ok := set.find("public", "fn_new_disjoint_get"); !ok {
		t.Error("expected fn_new_disjoint_get to coexist (disjoint method)")
	}
	if _, ok := set.find("public", "fn_new_disjoint_post"); !ok {
		t.Error("expected fn_new_disjoint_post to coexist (disjoint method)")
	}
}

func TestBuildRouteSet_ReservedPathExcluded(t *testing.T) {
	set, err := BuildRegistry(testDb, testCfg)
	if err != nil {
		t.Fatalf("BuildRegistry: %v", err)
	}
	if _, ok := set.find("public", "fn_new_reserved"); ok {
		t.Error("expected fn_new_reserved to be excluded (reserved /auth/* path)")
	}
}

func TestBuildRouteSet_TemplateBinaryIncompatible(t *testing.T) {
	set, err := BuildRegistry(testDb, testCfg)
	if err != nil {
		t.Fatalf("BuildRegistry: %v", err)
	}
	if _, ok := set.find("public", "fn_new_template_binary"); ok {
		t.Error("expected fn_new_template_binary to be excluded (template + binary return)")
	}
}

func TestBuildRouteSet_MalformedDeclarationSkipped(t *testing.T) {
	set, err := BuildRegistry(testDb, testCfg)
	if err != nil {
		t.Fatalf("BuildRegistry: %v", err)
	}
	if _, ok := set.find("public", "fn_new_malformed"); ok {
		t.Error("expected fn_new_malformed to be excluded (malformed HUML)")
	}
}

func TestBuildRouteSet_ExtraArgNotInPathExcluded(t *testing.T) {
	set, err := BuildRegistry(testDb, testCfg)
	if err != nil {
		t.Fatalf("BuildRegistry: %v", err)
	}
	if _, ok := set.find("public", "fn_new_extra_arg"); ok {
		t.Error("expected fn_new_extra_arg to be excluded (extra IN arg not in path)")
	}
}

func TestBuildRouteSet_UndeclaredNotDiscovered(t *testing.T) {
	set, err := BuildRegistry(testDb, testCfg)
	if err != nil {
		t.Fatalf("BuildRegistry: %v", err)
	}
	if _, ok := set.find("public", "fn_new_undeclared"); ok {
		t.Error("expected fn_new_undeclared to be excluded (no declaration)")
	}
}

func TestBuildRouteSet_ConfigOnly(t *testing.T) {
	set, err := BuildRegistry(testDb, testCfg)
	if err != nil {
		t.Fatalf("BuildRegistry: %v", err)
	}
	e, ok := set.find("public", "fn_new_configonly")
	if !ok {
		t.Fatal("expected fn_new_configonly (config-declared, no comment) to be discovered")
	}
	if e.Path != "/new/configonly" {
		t.Errorf("got path %q", e.Path)
	}
}

func TestBuildRouteSet_ConfigOverridesComment(t *testing.T) {
	set, err := BuildRegistry(testDb, testCfg)
	if err != nil {
		t.Fatalf("BuildRegistry: %v", err)
	}
	e, ok := set.find("public", "fn_new_override")
	if !ok {
		t.Fatal("expected fn_new_override to be discovered")
	}
	if e.Path != "/new/from-config" {
		t.Errorf("expected config's path to win, got %q", e.Path)
	}
	if e.Source != "config" {
		t.Errorf("expected Source config, got %q", e.Source)
	}
}

func TestBuildRouteSet_RoutesSortedLongestAnonPathFirst(t *testing.T) {
	set, err := BuildRegistry(testDb, testCfg)
	if err != nil {
		t.Fatalf("BuildRegistry: %v", err)
	}
	for i := 1; i < len(set.Routes); i++ {
		if len(set.Routes[i-1].AnonPath) < len(set.Routes[i].AnonPath) {
			t.Fatalf("routes not sorted longest-anonpath-first at index %d: %q before %q",
				i, set.Routes[i-1].AnonPath, set.Routes[i].AnonPath)
		}
	}
}

func TestBuildRouteSet_MiddlewareSortedShortestPrefixFirst(t *testing.T) {
	set, err := BuildRegistry(testDb, testCfg)
	if err != nil {
		t.Fatalf("BuildRegistry: %v", err)
	}
	for i := 1; i < len(set.Middleware); i++ {
		if len(set.Middleware[i-1].AnonPath) > len(set.Middleware[i].AnonPath) {
			t.Fatalf("middleware not sorted shortest-prefix-first at index %d: %q before %q",
				i, set.Middleware[i-1].AnonPath, set.Middleware[i].AnonPath)
		}
	}
}
