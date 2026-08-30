package rpc

import (
	"bytes"
	"encoding/json"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/ceymard/rel/static"
)

// TestResolveUnderDir_RejectsTraversal regression-tests a real bug found by
// adversarial review : filepath.Join(dir, filepath.Clean(sep+rel)) does NOT
// reject a traversing rel — Clean roots ".." components at "/" before Join
// ever runs, so "../../etc/passwd" (or an absolute "/etc/passwd") used to
// silently resolve to a DIFFERENT, unintended path still under dir instead
// of failing. ### Upload destinations' "Placement" paragraph requires a hard
// rejection here, not a silent rewrite into some other location under dir.
func TestResolveUnderDir_RejectsTraversal(t *testing.T) {
	dir := "/srv/static"
	for _, rel := range []string{
		"../../etc/passwd",
		"../secret",
		"..",
		"/etc/passwd",
		"a/../../b",
	} {
		if _, ok := resolveUnderDir(dir, rel); ok {
			t.Errorf("resolveUnderDir(%q, %q): expected rejection, got a resolved path", dir, rel)
		}
	}
}

func TestResolveUnderDir_AllowsOrdinaryRelativePaths(t *testing.T) {
	dir := "/srv/static"
	for rel, want := range map[string]string{
		"photo.jpg":        "/srv/static/photo.jpg",
		"a/b/c.jpg":        "/srv/static/a/b/c.jpg",
		"./photo.jpg":      "/srv/static/photo.jpg",
		"a/../b/photo.jpg": "/srv/static/b/photo.jpg",
	} {
		got, ok := resolveUnderDir(dir, rel)
		if !ok {
			t.Errorf("resolveUnderDir(%q, %q): expected success", dir, rel)
			continue
		}
		if got != want {
			t.Errorf("resolveUnderDir(%q, %q) = %q, want %q", dir, rel, got, want)
		}
	}
}

// newUploadTestHandler builds a fresh handler with http.static.path pointed
// at a real, writable temp directory (config.Test()'s own default,
// DefaultHttpStaticPath, doesn't exist in the test environment, so
// static.New would return nil and every "path" upload would 500) — returns
// the handler and the directory itself, for tests to assert on what
// actually landed on disk.
func newUploadTestHandler(t *testing.T) (http.Handler, string) {
	t.Helper()
	dir := t.TempDir()
	cfg := *testCfg
	cfg.Http.Static.Path = dir
	staticSrv := static.New(cfg.Http)
	if staticSrv == nil {
		t.Fatalf("expected static.New to find %s", dir)
	}
	return NewHandler(testDb, &cfg, testReg, staticSrv), dir
}

func multipartUploadRequest(t *testing.T, target, fieldName, filename, contentType string, content []byte) *http.Request {
	t.Helper()
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	part, err := w.CreatePart(map[string][]string{
		"Content-Disposition": {fmt.Sprintf(`form-data; name=%q; filename=%q`, fieldName, filename)},
		"Content-Type":        {contentType},
	})
	if err != nil {
		t.Fatalf("CreatePart: %v", err)
	}
	if _, err := part.Write(content); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, target, &buf)
	req.Header.Set("Content-Type", w.FormDataContentType())
	return req
}

// TestUpload_HappyPath_Multipart covers the full ordering : __prepare
// decides a path, bytes stream to a temp file, the mandatory function
// commits, and the file appears at the final path only after that commit.
func TestUpload_HappyPath_Multipart(t *testing.T) {
	handler, dir := newUploadTestHandler(t)

	content := []byte("hello upload destination")
	req := multipartUploadRequest(t, "/rpc/public/fn_dest_upload?path=uploads/hello.txt&mkdir=true", "file", "hello.txt", "text/plain", content)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	final := filepath.Join(dir, "uploads", "hello.txt")
	got, err := os.ReadFile(final)
	if err != nil {
		t.Fatalf("expected file at %s: %v", final, err)
	}
	if !bytes.Equal(got, content) {
		t.Errorf("unexpected file content: %q", got)
	}

	var body struct {
		Size int64 `json:"size"`
		Part struct {
			Filename string `json:"filename"`
		} `json:"part"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if body.Size != int64(len(content)) {
		t.Errorf("expected size %d, got %d", len(content), body.Size)
	}
	if body.Part.Filename != "hello.txt" {
		t.Errorf("expected part.filename=hello.txt, got %q", body.Part.Filename)
	}

	// No leftover dot-prefixed temp/backup files.
	entries, _ := os.ReadDir(filepath.Join(dir, "uploads"))
	for _, e := range entries {
		if e.Name()[0] == '.' {
			t.Errorf("unexpected leftover temp file: %s", e.Name())
		}
	}
}

// TestUpload_NoUpload_PrepareStillRuns covers "part is null" (no upload at
// all) still invoking __prepare, with the mandatory function then running
// with part/size null and nothing written to disk.
func TestUpload_NoUpload_PrepareStillRuns(t *testing.T) {
	handler, _ := newUploadTestHandler(t)

	req := httptest.NewRequest(http.MethodGet, "/rpc/public/fn_dest_upload", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var body struct {
		Part any `json:"part"`
		Size any `json:"size"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if body.Part != nil || body.Size != nil {
		t.Errorf("expected part/size both null, got part=%v size=%v", body.Part, body.Size)
	}
}

// TestUpload_PrepareRejects_BeforeBytesRead proves an RSxxx from __prepare
// ends the request before the mandatory function (and any byte streaming)
// ever runs.
func TestUpload_PrepareRejects_BeforeBytesRead(t *testing.T) {
	handler, dir := newUploadTestHandler(t)

	req := multipartUploadRequest(t, "/rpc/public/fn_dest_upload?reject=true", "file", "x.txt", "text/plain", []byte("data"))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", rec.Code, rec.Body.String())
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 0 {
		t.Errorf("expected nothing written to disk, found %v", entries)
	}
}

// TestUpload_SecondMultipartPart_Is415 proves a second part triggers 415
// before the mandatory function ever runs, and the temp file is deleted.
func TestUpload_SecondMultipartPart_Is415(t *testing.T) {
	handler, dir := newUploadTestHandler(t)

	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	fw, _ := w.CreateFormFile("file", "a.txt")
	_, _ = fw.Write([]byte("first"))
	fw2, _ := w.CreateFormFile("extra", "b.txt")
	_, _ = fw2.Write([]byte("second"))
	_ = w.Close()

	req := httptest.NewRequest(http.MethodPost, "/rpc/public/fn_dest_upload?path=twoparts.txt", &buf)
	req.Header.Set("Content-Type", w.FormDataContentType())
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("expected 415, got %d: %s", rec.Code, rec.Body.String())
	}
	if _, err := os.Stat(filepath.Join(dir, "twoparts.txt")); err == nil {
		t.Errorf("expected no file written at the final path")
	}
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if e.Name()[0] != '.' {
			continue
		}
		t.Errorf("expected leftover temp file to be deleted, found %s", e.Name())
	}
}

// TestUpload_MandatoryRejects_TempFileDeleted proves that when the
// mandatory function raises AFTER bytes are already on disk, the temp file
// is removed and nothing ever appears at the final path.
func TestUpload_MandatoryRejects_TempFileDeleted(t *testing.T) {
	handler, dir := newUploadTestHandler(t)

	req := multipartUploadRequest(t, "/rpc/public/fn_dest_fail?path=never.txt", "file", "never.txt", "text/plain", []byte("data"))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != 422 {
		t.Fatalf("expected 422, got %d: %s", rec.Code, rec.Body.String())
	}
	if _, err := os.Stat(filepath.Join(dir, "never.txt")); err == nil {
		t.Errorf("expected no file at the final path")
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 0 {
		t.Errorf("expected no leftover files, found %v", entries)
	}
}

// TestUpload_OverwriteDisallow_ExistingFile_Is409 proves the early
// overwrite:'disallow' fast-fail check.
func TestUpload_OverwriteDisallow_ExistingFile_Is409(t *testing.T) {
	handler, dir := newUploadTestHandler(t)

	existing := filepath.Join(dir, "taken.txt")
	if err := os.WriteFile(existing, []byte("already here"), 0o644); err != nil {
		t.Fatalf("seeding existing file: %v", err)
	}

	req := multipartUploadRequest(t, "/rpc/public/fn_dest_upload?path=taken.txt", "file", "taken.txt", "text/plain", []byte("new data"))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d: %s", rec.Code, rec.Body.String())
	}
	got, _ := os.ReadFile(existing)
	if string(got) != "already here" {
		t.Errorf("expected existing file untouched, got %q", got)
	}
}

// TestUpload_JsonBody_Is415 proves a route in this family rejects a
// JSON/text/form body outright, decided from Content-Type alone.
func TestUpload_JsonBody_Is415(t *testing.T) {
	handler, _ := newUploadTestHandler(t)

	req := httptest.NewRequest(http.MethodPost, "/rpc/public/fn_dest_upload", bytes.NewReader([]byte(`{"a":1}`)))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("expected 415, got %d: %s", rec.Code, rec.Body.String())
	}
}

// TestUpload_OrphanFunctions_NotRoutable proves an orphan __prepare or
// orphan mandatory function (no matching sibling) is never discovered as a
// route at all.
func TestUpload_OrphanFunctions_NotRoutable(t *testing.T) {
	if _, ok := testReg.Lookup("public", "fn_orphan_prepare", http.MethodGet); ok {
		t.Errorf("expected fn_orphan_prepare__prepare's orphan base name not to be routable")
	}
	if _, ok := testReg.Lookup("public", "fn_orphan_mandatory", http.MethodGet); ok {
		t.Errorf("expected fn_orphan_mandatory (no __prepare sibling) not to be routable")
	}
}

// TestUpload_DiscardPath proves an omitted "path" streams and measures the
// upload but writes nothing to the served directory tree.
func TestUpload_DiscardPath(t *testing.T) {
	handler, dir := newUploadTestHandler(t)

	req := multipartUploadRequest(t, "/rpc/public/fn_dest_upload", "file", "discard.txt", "text/plain", []byte("throwaway"))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var body struct {
		Size int64 `json:"size"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if body.Size != int64(len("throwaway")) {
		t.Errorf("expected size %d, got %d", len("throwaway"), body.Size)
	}

	// Nothing visible/servable should remain — only dot-prefixed staging
	// files, if any leftover at all (there shouldn't be, once cleaned up).
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if e.Name()[0] != '.' {
			t.Errorf("expected no publicly-visible file for a discarded upload, found %s", e.Name())
		}
	}
}
