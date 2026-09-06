package websec

import (
	"testing"

	"github.com/rel-server/rel/config"
)

func TestIsPreflight(t *testing.T) {
	cases := []struct {
		method, origin, reqMethod string
		want                      bool
	}{
		{"OPTIONS", "https://example.com", "GET", true},
		{"OPTIONS", "", "GET", false},
		{"OPTIONS", "https://example.com", "", false},
		{"OPTIONS", "", "", false},
		{"GET", "https://example.com", "GET", false},
	}
	for _, c := range cases {
		if got := IsPreflight(c.method, c.origin, c.reqMethod); got != c.want {
			t.Errorf("IsPreflight(%q,%q,%q) = %v, want %v", c.method, c.origin, c.reqMethod, got, c.want)
		}
	}
}

func TestResolveOrigin_EmptyConfigClosesEverything(t *testing.T) {
	d := ResolveOrigin("", "https://example.com")
	if d.Allowed {
		t.Errorf("expected empty allowed_origins to deny every origin, got %+v", d)
	}
}

func TestResolveOrigin_Wildcard(t *testing.T) {
	d := ResolveOrigin("*", "https://example.com")
	if !d.Allowed || d.HeaderValue != "*" || d.AllowCredential || d.Vary {
		t.Errorf("expected wildcard: allowed, '*', no credentials, no vary — got %+v", d)
	}
}

func TestResolveOrigin_ExactAllowlistMatch(t *testing.T) {
	d := ResolveOrigin("https://a.example.com,https://b.example.com", "https://b.example.com")
	if !d.Allowed || d.HeaderValue != "https://b.example.com" || !d.AllowCredential || !d.Vary {
		t.Errorf("expected reflected origin with credentials+vary, got %+v", d)
	}
}

func TestResolveOrigin_NoMatch(t *testing.T) {
	d := ResolveOrigin("https://a.example.com", "https://evil.example.com")
	if d.Allowed {
		t.Errorf("expected no match to deny, got %+v", d)
	}
}

func TestBuildCorsHeaders_PreflightIncludesMethodsHeadersMaxAge(t *testing.T) {
	cfg := config.HttpCors{AllowedOrigins: "https://a.example.com", AllowedMethods: "GET, POST", AllowedHeaders: "Content-Type", MaxAge: 600}
	h, ok := BuildCorsHeaders(cfg, "https://a.example.com", true)
	if !ok {
		t.Fatalf("expected allowed")
	}
	if h.AllowMethods != "GET, POST" || h.AllowHeaders != "Content-Type" || h.MaxAge != "600" {
		t.Errorf("unexpected preflight headers: %+v", h)
	}
}

func TestBuildCorsHeaders_ActualResponseOmitsPreflightOnlyFields(t *testing.T) {
	cfg := config.HttpCors{AllowedOrigins: "https://a.example.com", AllowedMethods: "GET, POST", AllowedHeaders: "Content-Type", MaxAge: 600}
	h, ok := BuildCorsHeaders(cfg, "https://a.example.com", false)
	if !ok {
		t.Fatalf("expected allowed")
	}
	if h.AllowMethods != "" || h.AllowHeaders != "" || h.MaxAge != "" {
		t.Errorf("expected preflight-only fields empty on actual response: %+v", h)
	}
	if h.AllowOrigin != "https://a.example.com" || !h.AllowCredentials || !h.Vary {
		t.Errorf("unexpected actual-response headers: %+v", h)
	}
}
