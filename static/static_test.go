package static

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/ceymard/rel/config"
)

func TestHasDotSegment(t *testing.T) {
	cases := map[string]bool{
		"app.js":            false,
		"css/style.css":     false,
		".env":              true,
		".git/config":       true,
		"a/.htpasswd":       true,
		"a/b/c.txt":         false,
		"":                  false,
		"private/.hidden/x": true,
	}
	for path, want := range cases {
		if got := hasDotSegment(path); got != want {
			t.Errorf("hasDotSegment(%q) = %v, want %v", path, got, want)
		}
	}
}

func TestNew_MissingDirectoriesSilentlySkipped(t *testing.T) {
	if srv := New(config.Http{Static: config.HttpStatic{Path: "/does/not/exist:/also/missing"}}); srv != nil {
		t.Fatalf("expected nil (no mount) when every listed directory is missing, got %+v", srv)
	}
}

func TestNew_ColonSeparatedSearchList_ExistingOnly(t *testing.T) {
	dir1 := t.TempDir()
	dir2 := t.TempDir()
	srv := New(config.Http{Static: config.HttpStatic{Path: "/does/not/exist:" + dir1 + ":" + dir2}})
	if srv == nil {
		t.Fatalf("expected a non-nil server")
	}
	if len(srv.Dirs) != 2 || srv.Dirs[0] != dir1 || srv.Dirs[1] != dir2 {
		t.Errorf("expected only the two existing directories, in order, got %v", srv.Dirs)
	}
}

func TestServer_WriteDir_IsFirstListedDirectory(t *testing.T) {
	dir1 := t.TempDir()
	dir2 := t.TempDir()
	srv := New(config.Http{Static: config.HttpStatic{Path: dir1 + ":" + dir2}})
	if got := srv.WriteDir(); got != dir1 {
		t.Errorf("expected WriteDir() = %q, got %q", dir1, got)
	}
}

func writeFile(t *testing.T, dir, name, content string) {
	t.Helper()
	full := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
}

func serveUngated(t *testing.T, srv *Server, path string) *httptest.ResponseRecorder {
	t.Helper()
	handler := http.StripPrefix("/static/", srv.Handler(nil, &config.Config{}))
	req := httptest.NewRequest(http.MethodGet, "/static/"+path, nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec
}

func TestServer_ServesPlainFile(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "app.js", "console.log(1)")
	srv := New(config.Http{Static: config.HttpStatic{Path: dir}})

	rec := serveUngated(t, srv, "app.js")
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if rec.Body.String() != "console.log(1)" {
		t.Errorf("unexpected body: %q", rec.Body.String())
	}
}

func TestServer_DotfileIsNotFound_EvenIfItExists(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, ".env", "SECRET=1")
	srv := New(config.Http{Static: config.HttpStatic{Path: dir}})

	rec := serveUngated(t, srv, ".env")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for a dotfile, got %d", rec.Code)
	}
}

func TestServer_DirectoryWithoutIndex_Is404_NoListing(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "sub/file.txt", "hi")
	srv := New(config.Http{Static: config.HttpStatic{Path: dir}})

	rec := serveUngated(t, srv, "sub/")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for a directory with no index.html, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestServer_DirectoryWithIndex_ServesIt(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "sub/index.html", "<h1>hi</h1>")
	srv := New(config.Http{Static: config.HttpStatic{Path: dir}})

	rec := serveUngated(t, srv, "sub/")
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if rec.Body.String() != "<h1>hi</h1>" {
		t.Errorf("unexpected body: %q", rec.Body.String())
	}
}

func TestServer_MissingFile_Is404(t *testing.T) {
	dir := t.TempDir()
	srv := New(config.Http{Static: config.HttpStatic{Path: dir}})

	rec := serveUngated(t, srv, "nope.txt")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", rec.Code)
	}
}

// TestServer_MultiDirectoryLayering_FirstMatchWins proves an override in
// an earlier-listed directory shadows the same path in a later one.
func TestServer_MultiDirectoryLayering_FirstMatchWins(t *testing.T) {
	dir1 := t.TempDir()
	dir2 := t.TempDir()
	writeFile(t, dir1, "shared.txt", "from dir1")
	writeFile(t, dir2, "shared.txt", "from dir2")
	writeFile(t, dir2, "only-in-dir2.txt", "dir2 only")

	srv := New(config.Http{Static: config.HttpStatic{Path: dir1 + ":" + dir2}})

	rec := serveUngated(t, srv, "shared.txt")
	if rec.Body.String() != "from dir1" {
		t.Errorf("expected dir1's own file to win, got %q", rec.Body.String())
	}

	rec2 := serveUngated(t, srv, "only-in-dir2.txt")
	if rec2.Code != http.StatusOK || rec2.Body.String() != "dir2 only" {
		t.Errorf("expected dir2's own file to be found when dir1 doesn't have it, got %d %q", rec2.Code, rec2.Body.String())
	}
}
