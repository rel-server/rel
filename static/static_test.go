package static

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/rel-server/rel/config"
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

// TestServer_DotfileSegment_MidPath_NotFound : hasDotSegment's rule
// applies anywhere in the path, not just the final segment.
func TestServer_DotfileSegment_MidPath_NotFound(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, ".git/config", "[core]\n")
	srv := New(config.Http{Static: config.HttpStatic{Path: dir}})

	rec := serveUngated(t, srv, ".git/config")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for a dotfile segment mid-path, got %d: %s", rec.Code, rec.Body.String())
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

// specs/templating-2.md's generalized Resolve fallback chain — pure lookup
// logic, no jet execution (that's route's job, exercised in
// route/static_jet_test.go instead).

func TestResolve_ExactFileWins_NoJetFallback(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "app.js", "content")
	writeFile(t, dir, "app.js.jet", "should never be considered")
	srv := New(config.Http{Static: config.HttpStatic{Path: dir}})

	servable, jc, ok := srv.Resolve("app.js")
	if !ok || jc != nil || servable != filepath.Join(dir, "app.js") {
		t.Errorf("expected the exact file to win with no jet candidate, got servable=%q jet=%+v ok=%v", servable, jc, ok)
	}
}

func TestResolve_ExtensionedFallsBackToExtJet(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "widget.svg.jet", "<svg/>")
	srv := New(config.Http{Static: config.HttpStatic{Path: dir}})

	servable, jc, ok := srv.Resolve("widget.svg")
	if !ok || servable != "" || jc == nil {
		t.Fatalf("expected a jet candidate, got servable=%q jet=%+v ok=%v", servable, jc, ok)
	}
	if jc.Ext != "svg" || jc.AbsPath != filepath.Join(dir, "widget.svg.jet") {
		t.Errorf("unexpected jet candidate: %+v", jc)
	}
}

func TestResolve_ExtensionlessTriesHtmlThenHtmlJet(t *testing.T) {
	dir := t.TempDir()
	srv := New(config.Http{Static: config.HttpStatic{Path: dir}})

	if _, _, ok := srv.Resolve("foo"); ok {
		t.Fatalf("expected no match when neither foo, foo.html nor foo.html.jet exist")
	}

	writeFile(t, dir, "foo.html.jet", "rendered")
	servable, jc, ok := srv.Resolve("foo")
	if !ok || servable != "" || jc == nil || jc.Ext != "html" {
		t.Fatalf("expected an html jet candidate for extensionless foo, got servable=%q jet=%+v ok=%v", servable, jc, ok)
	}

	writeFile(t, dir, "foo.html", "plain html wins")
	servable, jc, ok = srv.Resolve("foo")
	if !ok || jc != nil || servable != filepath.Join(dir, "foo.html") {
		t.Errorf("expected foo.html to win over foo.html.jet once it exists, got servable=%q jet=%+v ok=%v", servable, jc, ok)
	}
}

func TestResolve_DirectoryIndexHtmlThenIndexHtmlJet(t *testing.T) {
	dir := t.TempDir()
	srv := New(config.Http{Static: config.HttpStatic{Path: dir}})
	writeFile(t, dir, "sub/placeholder.txt", "just to create the dir")

	if _, _, ok := srv.Resolve("sub/"); ok {
		t.Fatalf("expected no match for a directory with neither index.html nor index.html.jet")
	}

	writeFile(t, dir, "sub/index.html.jet", "rendered index")
	servable, jc, ok := srv.Resolve("sub/")
	if !ok || servable != "" || jc == nil || jc.Ext != "html" {
		t.Fatalf("expected an html jet candidate for the directory index, got servable=%q jet=%+v ok=%v", servable, jc, ok)
	}

	writeFile(t, dir, "sub/index.html", "plain index wins")
	servable, jc, ok = srv.Resolve("sub/")
	if !ok || jc != nil || servable != filepath.Join(dir, "sub/index.html") {
		t.Errorf("expected sub/index.html to win over sub/index.html.jet once it exists, got servable=%q jet=%+v ok=%v", servable, jc, ok)
	}
}

func TestNew_UploadDirResolvedUnderFirstStaticDir(t *testing.T) {
	dir1 := t.TempDir()
	dir2 := t.TempDir()
	srv := New(config.Http{
		Static: config.HttpStatic{Path: dir1 + ":" + dir2},
		Upload: config.HttpUpload{Dir: "uploads"},
	})
	if want := filepath.Join(dir1, "uploads"); srv.UploadDir != want {
		t.Errorf("expected UploadDir %q (under the FIRST static dir), got %q", want, srv.UploadDir)
	}
}

func TestNew_UploadDirEmptyWhenUnset(t *testing.T) {
	dir := t.TempDir()
	srv := New(config.Http{Static: config.HttpStatic{Path: dir}})
	if srv.UploadDir != "" {
		t.Errorf("expected no UploadDir when http.upload.dir is unset, got %q", srv.UploadDir)
	}
}

func TestResolve_JetCandidateExcludedUnderUploadDir(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "uploads/evil.html.jet", "should never execute")
	srv := New(config.Http{
		Static: config.HttpStatic{Path: dir},
		Upload: config.HttpUpload{Dir: "uploads"},
	})

	servable, jc, ok := srv.Resolve("uploads/evil")
	if !ok || servable != "" || jc == nil {
		t.Fatalf("expected a jet candidate (excluded, not absent), got servable=%q jet=%+v ok=%v", servable, jc, ok)
	}
	if !jc.Excluded {
		t.Errorf("expected the candidate under http.upload.dir to be marked Excluded, got %+v", jc)
	}
}

func TestResolve_JetCandidateOutsideUploadDirNotExcluded(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "public/widget.html.jet", "fine")
	srv := New(config.Http{
		Static: config.HttpStatic{Path: dir},
		Upload: config.HttpUpload{Dir: "uploads"},
	})

	_, jc, ok := srv.Resolve("public/widget")
	if !ok || jc == nil || jc.Excluded {
		t.Errorf("expected a non-excluded jet candidate outside http.upload.dir, got jet=%+v ok=%v", jc, ok)
	}
}

func TestStat_JetFallback_ReportsSourceFileStats(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "widget.svg.jet", "<svg/>")
	srv := New(config.Http{Static: config.HttpStatic{Path: dir}})

	info := srv.Stat("widget.svg")
	if !info.Exists {
		t.Fatalf("expected Stat to report the .jet source as existing")
	}
	fi, err := os.Stat(filepath.Join(dir, "widget.svg.jet"))
	if err != nil {
		t.Fatalf("stat fixture: %v", err)
	}
	if info.Size != fi.Size() {
		t.Errorf("expected Stat's Size to be the .jet SOURCE file's own size, got %d want %d", info.Size, fi.Size())
	}
}

func TestServer_DirectoryWithoutTrailingSlash_Redirects(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "sub/index.html", "hi")
	srv := New(config.Http{Static: config.HttpStatic{Path: dir}})

	rec := serveUngated(t, srv, "sub")
	if rec.Code != http.StatusMovedPermanently {
		t.Fatalf("expected a 301 redirect for a directory missing its trailing slash, got %d", rec.Code)
	}
}

func TestStat_ExcludedJetCandidate_ReportsNotExists(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "uploads/evil.html.jet", "should never execute")
	srv := New(config.Http{
		Static: config.HttpStatic{Path: dir},
		Upload: config.HttpUpload{Dir: "uploads"},
	})

	if info := srv.Stat("uploads/evil"); info.Exists {
		t.Errorf("expected an excluded jet candidate to report as nonexistent, got %+v", info)
	}
}
