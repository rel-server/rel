// Tests for specs/templating-2.md : jet templating served from static,
// rel(), and the upload/jet exclusion — driven directly against
// NewStaticHandler (not through the full chi router/GateMiddleware, since
// none of that is needed to exercise this feature) and this package's own
// testDb/testCfg fixtures (route_test.go's TestMain).
package route

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rel-server/rel/config"
	"github.com/rel-server/rel/static"
)

func writeStaticJetFile(t *testing.T, dir, name, content string) {
	t.Helper()
	full := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
}

// newStaticJetHandler builds a standalone NewStaticHandler over dir, with
// its own TemplateSet sharing testDb/nil wkReg — a fresh *config.Config
// copy per test so http.upload.dir doesn't leak between them.
func newStaticJetHandler(t *testing.T, dir string, uploadDir string) (http.Handler, *config.Config) {
	t.Helper()
	cfg := *testCfg
	cfg.Http.Static.Path = dir
	cfg.Http.Upload.Dir = uploadDir
	staticSrv := static.New(cfg.Http)
	if staticSrv == nil {
		t.Fatalf("expected a non-nil static.Server for %q", dir)
	}
	templates := NewTemplateSet(&cfg, staticSrv.Dirs, testDb, nil)
	return NewStaticHandler(testDb, &cfg, staticSrv, templates), &cfg
}

func TestStaticJet_RendersWithNonce_NoCacheHeaders(t *testing.T) {
	dir := t.TempDir()
	writeStaticJetFile(t, dir, "index.html.jet", "nonce={{ Nonce }}")
	handler, _ := newStaticJetHandler(t, dir, "")

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.HasPrefix(rec.Body.String(), "nonce=") || len(rec.Body.String()) <= len("nonce=") {
		t.Errorf("expected a non-empty nonce in the body, got %q", rec.Body.String())
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf(`expected Cache-Control: no-store, got %q`, got)
	}
	if rec.Header().Get("ETag") != "" || rec.Header().Get("Last-Modified") != "" {
		t.Errorf("expected no ETag/Last-Modified on a jet render, got ETag=%q Last-Modified=%q", rec.Header().Get("ETag"), rec.Header().Get("Last-Modified"))
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Errorf("expected text/html content type for an .html.jet render, got %q", ct)
	}

	// Two requests get two different nonces — a fresh one per render, not a
	// cached/shared value.
	rec2 := httptest.NewRecorder()
	handler.ServeHTTP(rec2, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Body.String() == rec2.Body.String() {
		t.Errorf("expected a different nonce across two renders, got the same body twice: %q", rec.Body.String())
	}
}

func TestStaticJet_ExtensionDeterminesContentType(t *testing.T) {
	dir := t.TempDir()
	writeStaticJetFile(t, dir, "widget.svg.jet", "<svg></svg>")
	handler, _ := newStaticJetHandler(t, dir, "")

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/widget.svg", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "image/svg+xml") {
		t.Errorf("expected image/svg+xml for widget.svg -> widget.svg.jet, got %q", ct)
	}
}

func TestStaticJet_RelReadsRows(t *testing.T) {
	if _, err := testDb.Pool.Exec(context.Background(), "delete from stream_upload_log"); err != nil {
		t.Fatalf("clearing stream_upload_log: %v", err)
	}
	if _, err := testDb.Pool.Exec(context.Background(), `insert into stream_upload_log (upload) values ('{"marker":"static-jet-rel-test"}'::jsonb)`); err != nil {
		t.Fatalf("seeding stream_upload_log: %v", err)
	}

	dir := t.TempDir()
	writeStaticJetFile(t, dir, "rows.json.jet",
		`{{ range _, row := rel(map("relation", "stream_upload_log", "schema", "public", "select", slice("own"))) }}marker={{ row.upload.marker }};{{ end }}`)
	handler, _ := newStaticJetHandler(t, dir, "")

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/rows.json", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "marker=static-jet-rel-test;") {
		t.Errorf("expected rel() to surface the seeded row, got %q", rec.Body.String())
	}
}

func TestStaticJet_RelRejectsWrite(t *testing.T) {
	dir := t.TempDir()
	writeStaticJetFile(t, dir, "write.json.jet",
		`{{ rel(map("query", map("relation", "stream_upload_log", "schema", "public"), "data", map())) }}`)
	handler, _ := newStaticJetHandler(t, dir, "")

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/write.json", nil))

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500 (rel() must refuse a write), got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestStaticJet_UploadDirExcludedFromExecution(t *testing.T) {
	dir := t.TempDir()
	writeStaticJetFile(t, dir, "uploads/secret.html.jet", "should never render")
	handler, _ := newStaticJetHandler(t, dir, "uploads")

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/uploads/secret", nil))

	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for a .jet file under http.upload.dir, got %d: %s", rec.Code, rec.Body.String())
	}
}

// TestStaticJet_LeadingSlashIncludeReachesTemplatesPath and
// TestStaticJet_BareIncludeResolvesRelativeToStaticFile pin down jet's own
// include/extends addressing against the shared multiRootLoader : a
// leading "/" reaches http.templates.path directly, while a bare name (no
// "./", no leading "/") resolves relative to the including template's own
// location — the same as "./" would — rather than against templates.path.
// This is jet's own resolution rule, not a distinct third addressing mode ;
// specs/templating-2.md's "any other name follows the loader/templates.path
// machinery" is only true for a leading-"/" name in practice.
func TestStaticJet_LeadingSlashIncludeReachesTemplatesPath(t *testing.T) {
	staticDir := t.TempDir()
	templatesDir := t.TempDir()
	writeStaticJetFile(t, staticDir, "page.html.jet", `{{ include "/layout.jet" }}`)
	writeStaticJetFile(t, templatesDir, "layout.jet", "from templates.path")

	cfg := *testCfg
	cfg.Http.Static.Path = staticDir
	cfg.Http.Templates.Path = templatesDir
	staticSrv := static.New(cfg.Http)
	templates := NewTemplateSet(&cfg, staticSrv.Dirs, testDb, nil)
	handler := NewStaticHandler(testDb, &cfg, staticSrv, templates)

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/page", nil))

	if rec.Code != http.StatusOK || rec.Body.String() != "from templates.path" {
		t.Errorf(`expected a leading-"/" include to reach http.templates.path, got %d %q`, rec.Code, rec.Body.String())
	}
}

func TestStaticJet_BareIncludeResolvesRelativeToStaticFile(t *testing.T) {
	dir := t.TempDir()
	writeStaticJetFile(t, dir, "sub/page.html.jet", `{{ include "sibling.jet" }}`)
	writeStaticJetFile(t, dir, "sub/sibling.jet", "sibling in the same static directory")
	handler, _ := newStaticJetHandler(t, dir, "")

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/sub/page", nil))

	if rec.Code != http.StatusOK || rec.Body.String() != "sibling in the same static directory" {
		t.Errorf("expected a bare include to resolve relative to the including static file, got %d %q", rec.Code, rec.Body.String())
	}
}

func TestStaticJet_DirectoryWithoutTrailingSlash_Redirects(t *testing.T) {
	dir := t.TempDir()
	writeStaticJetFile(t, dir, "sub/index.html.jet", "index rendered")
	handler, _ := newStaticJetHandler(t, dir, "")

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/sub", nil))

	if rec.Code != http.StatusMovedPermanently {
		t.Fatalf("expected 301 redirect to the trailing-slash URL, got %d: %s", rec.Code, rec.Body.String())
	}
	if loc := rec.Header().Get("Location"); loc != "/sub/" {
		t.Errorf(`expected Location: "/sub/", got %q`, loc)
	}
}

func TestStaticJet_PlainFileStillWinsOverJetFallback(t *testing.T) {
	dir := t.TempDir()
	writeStaticJetFile(t, dir, "foo.html", "plain file wins")
	writeStaticJetFile(t, dir, "foo.html.jet", "should never render")
	handler, _ := newStaticJetHandler(t, dir, "")

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/foo", nil))

	if rec.Code != http.StatusOK || rec.Body.String() != "plain file wins" {
		t.Errorf("expected the plain foo.html to win over foo.html.jet, got %d %q", rec.Code, rec.Body.String())
	}
}
