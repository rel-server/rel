package route

import "testing"

func TestParseCommentDeclaration_HUML(t *testing.T) {
	decl, err := parseCommentDeclaration(`route:: path: "/some/path", method: "POST"`)
	if err != nil {
		t.Fatalf("parseCommentDeclaration: %v", err)
	}
	if decl == nil {
		t.Fatal("expected a declaration, got nil")
	}
	if decl.Path != "/some/path" || decl.Method != "POST" {
		t.Errorf("got %+v", decl)
	}
}

func TestParseCommentDeclaration_JSON(t *testing.T) {
	decl, err := parseCommentDeclaration(`route: {"path": "/some/path"}`)
	if err != nil {
		t.Fatalf("parseCommentDeclaration: %v", err)
	}
	if decl == nil {
		t.Fatal("expected a declaration, got nil")
	}
	if decl.Path != "/some/path" {
		t.Errorf("got %+v", decl)
	}
}

func TestParseCommentDeclaration_JSON_Middleware(t *testing.T) {
	decl, err := parseCommentDeclaration(`route: {"path": "/api", "middleware": true}`)
	if err != nil {
		t.Fatalf("parseCommentDeclaration: %v", err)
	}
	if decl == nil || !decl.Middleware {
		t.Errorf("got %+v", decl)
	}
}

func TestParseCommentDeclaration_NotARoute(t *testing.T) {
	decl, err := parseCommentDeclaration("just a normal doc comment")
	if err != nil {
		t.Fatalf("expected no error for a non-route comment, got %v", err)
	}
	if decl != nil {
		t.Errorf("expected nil declaration for a non-route comment, got %+v", decl)
	}
}

func TestParseCommentDeclaration_LooksLikeButIsnt(t *testing.T) {
	// "route: see below" must not be mistaken for a JSON declaration —
	// isJSONObjectAfterColon requires a "{" right after the colon.
	decl, err := parseCommentDeclaration("route: see below for details")
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if decl != nil {
		t.Errorf("expected nil declaration, got %+v", decl)
	}
}

func TestParseCommentDeclaration_MalformedHUML(t *testing.T) {
	_, err := parseCommentDeclaration(`route:: path: "unterminated`)
	if err == nil {
		t.Error("expected an error for malformed HUML, got nil")
	}
}
