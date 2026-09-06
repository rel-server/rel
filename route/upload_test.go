package route

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

	"github.com/rel-server/rel/static"
)

// TestResolveUnderDir_RejectsTraversal : a traversing or absolute rel is
// rejected outright, never silently resolved elsewhere under dir.
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

// newUploadTestHandler points http.static.path at a real, writable temp
// dir ; config.Test()'s own default doesn't exist in the test environment.
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
// decides a path, bytes stream to temp, and the file lands only after commit.
func TestUpload_HappyPath_Multipart(t *testing.T) {
	handler, dir := newUploadTestHandler(t)

	content := []byte("hello upload destination")
	req := multipartUploadRequest(t, "/route/public/fn_dest_upload?path=uploads/hello.txt&mkdir=true", "file", "hello.txt", "text/plain", content)
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

// TestUpload_NoUpload_PrepareStillRuns : no upload still invokes
// __prepare, then the mandatory function with part/size null.
func TestUpload_NoUpload_PrepareStillRuns(t *testing.T) {
	handler, _ := newUploadTestHandler(t)

	req := httptest.NewRequest(http.MethodGet, "/route/public/fn_dest_upload", nil)
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
// ends the request before the mandatory function or streaming ever runs.
func TestUpload_PrepareRejects_BeforeBytesRead(t *testing.T) {
	handler, dir := newUploadTestHandler(t)

	req := multipartUploadRequest(t, "/route/public/fn_dest_upload?reject=true", "file", "x.txt", "text/plain", []byte("data"))
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

// TestUpload_PrepareMaxSize_TightensLimit proves __prepare's returned
// max_size rejects an upload the global http.max_upload_size would have
// allowed, with the temp file cleaned up.
func TestUpload_PrepareMaxSize_TightensLimit(t *testing.T) {
	handler, dir := newUploadTestHandler(t)

	content := bytes.Repeat([]byte("x"), 10)
	req := multipartUploadRequest(t, "/route/public/fn_dest_upload?path=toobig.txt&max_size=5", "file", "toobig.txt", "text/plain", content)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("expected 413, got %d: %s", rec.Code, rec.Body.String())
	}
	if _, err := os.Stat(filepath.Join(dir, "toobig.txt")); err == nil {
		t.Errorf("expected no file written at the final path")
	}
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if e.Name()[0] == '.' {
			t.Errorf("expected leftover temp file to be deleted, found %s", e.Name())
		}
	}
}

// TestUpload_PrepareMaxSize_AllowsWithinLimit proves a max_size at or above
// the actual payload size still lets the upload through.
func TestUpload_PrepareMaxSize_AllowsWithinLimit(t *testing.T) {
	handler, dir := newUploadTestHandler(t)

	content := []byte("hello")
	req := multipartUploadRequest(t, "/route/public/fn_dest_upload?path=fits.txt&max_size=5", "file", "fits.txt", "text/plain", content)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	got, err := os.ReadFile(filepath.Join(dir, "fits.txt"))
	if err != nil {
		t.Fatalf("expected file at final path: %v", err)
	}
	if string(got) != "hello" {
		t.Errorf("got content %q, want %q", got, "hello")
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

	req := httptest.NewRequest(http.MethodPost, "/route/public/fn_dest_upload?path=twoparts.txt", &buf)
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

// TestUpload_MandatoryRejects_TempFileDeleted : when the mandatory
// function raises after bytes are on disk, the temp file is removed.
func TestUpload_MandatoryRejects_TempFileDeleted(t *testing.T) {
	handler, dir := newUploadTestHandler(t)

	req := multipartUploadRequest(t, "/route/public/fn_dest_fail?path=never.txt", "file", "never.txt", "text/plain", []byte("data"))
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

	req := multipartUploadRequest(t, "/route/public/fn_dest_upload?path=taken.txt", "file", "taken.txt", "text/plain", []byte("new data"))
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

	req := httptest.NewRequest(http.MethodPost, "/route/public/fn_dest_upload", bytes.NewReader([]byte(`{"a":1}`)))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("expected 415, got %d: %s", rec.Code, rec.Body.String())
	}
}

// TestUpload_NoContentTypeHeader_StillStreams : gated on r.ContentLength
// != 0, not on a non-empty Content-Type header.
func TestUpload_NoContentTypeHeader_StillStreams(t *testing.T) {
	handler, dir := newUploadTestHandler(t)

	content := []byte("no content-type header at all")
	req := httptest.NewRequest(http.MethodPost, "/route/public/fn_dest_upload?path=noct.txt", bytes.NewReader(content))
	req.Header.Del("Content-Type") // httptest.NewRequest never sets one for a plain io.Reader body, but be explicit
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	final := filepath.Join(dir, "noct.txt")
	got, err := os.ReadFile(final)
	if err != nil {
		t.Fatalf("expected file at %s: %v", final, err)
	}
	if !bytes.Equal(got, content) {
		t.Errorf("unexpected file content: %q", got)
	}

	// synthesizedPseudoPart's content_type is nil, not "", for no header.
	var body struct {
		Part struct {
			ContentType *string `json:"content_type"`
		} `json:"part"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if body.Part.ContentType != nil {
		t.Errorf("expected part.content_type=null for a request with no Content-Type header, got %q", *body.Part.ContentType)
	}
}

// TestUpload_OrphanFunctions_NotRoutable proves an orphan __prepare or
// mandatory function (no matching sibling) is never discovered as a route.
func TestUpload_OrphanFunctions_NotRoutable(t *testing.T) {
	if _, ok := testReg.Lookup("public", "fn_orphan_prepare", http.MethodGet); ok {
		t.Errorf("expected fn_orphan_prepare__prepare's orphan base name not to be routable")
	}
	if _, ok := testReg.Lookup("public", "fn_orphan_mandatory", http.MethodGet); ok {
		t.Errorf("expected fn_orphan_mandatory (no __prepare sibling) not to be routable")
	}
}

// TestUpload_Mkdir_CreatesMissingParentDirectory checks absent-before,
// present-after — the happy-path test alone would pass even if mkdir no-op'd.
func TestUpload_Mkdir_CreatesMissingParentDirectory(t *testing.T) {
	handler, dir := newUploadTestHandler(t)

	subdir := filepath.Join(dir, "brand", "new", "nested")
	if _, err := os.Stat(subdir); !os.IsNotExist(err) {
		t.Fatalf("expected %s not to exist before the request, stat err=%v", subdir, err)
	}

	req := multipartUploadRequest(t, "/route/public/fn_dest_upload?path=brand/new/nested/file.txt&mkdir=true", "file", "file.txt", "text/plain", []byte("data"))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	fi, err := os.Stat(subdir)
	if err != nil || !fi.IsDir() {
		t.Fatalf("expected %s to now exist as a directory, err=%v", subdir, err)
	}
}

// TestUpload_OverwriteAllow_RoundTrip : write twice through the same
// route, confirm the disk content is the second write, not the first.
func TestUpload_OverwriteAllow_RoundTrip(t *testing.T) {
	handler, dir := newUploadTestHandler(t)
	final := filepath.Join(dir, "roundtrip.txt")

	first := multipartUploadRequest(t, "/route/public/fn_dest_upload?path=roundtrip.txt&overwrite=allow", "file", "roundtrip.txt", "text/plain", []byte("first write"))
	rec1 := httptest.NewRecorder()
	handler.ServeHTTP(rec1, first)
	if rec1.Code != http.StatusOK {
		t.Fatalf("first write: expected 200, got %d: %s", rec1.Code, rec1.Body.String())
	}
	got1, err := os.ReadFile(final)
	if err != nil || string(got1) != "first write" {
		t.Fatalf("expected 'first write' on disk after the first write, got %q err=%v", got1, err)
	}

	second := multipartUploadRequest(t, "/route/public/fn_dest_upload?path=roundtrip.txt&overwrite=allow", "file", "roundtrip.txt", "text/plain", []byte("second write, different length"))
	rec2 := httptest.NewRecorder()
	handler.ServeHTTP(rec2, second)
	if rec2.Code != http.StatusOK {
		t.Fatalf("second write: expected 200, got %d: %s", rec2.Code, rec2.Body.String())
	}
	got2, err := os.ReadFile(final)
	if err != nil {
		t.Fatalf("reading final file: %v", err)
	}
	if string(got2) != "second write, different length" {
		t.Errorf("expected the SECOND write's content to win, got %q", got2)
	}
}

// TestUpload_AnonymousAuthorization_RequiresBothHalves : AnonymousAuthorized
// is the AND of both halves — restricting either one still 401s.
func TestUpload_AnonymousAuthorization_RequiresBothHalves(t *testing.T) {
	for _, base := range []string{"fn_dest_anon_prepare_only", "fn_dest_anon_mandatory_only"} {
		t.Run(base, func(t *testing.T) {
			handler, _ := newUploadTestHandler(t)
			req := multipartUploadRequest(t, "/route/public/"+base, "file", "x.txt", "text/plain", []byte("data"))
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)
			if rec.Code != http.StatusUnauthorized {
				t.Fatalf("expected 401 (only one half of the pair is anon-reachable), got %d: %s", rec.Code, rec.Body.String())
			}
		})
	}
}

// TestUpload_OrphanFunctions_HTTPLevel404 covers the same rule as
// TestUpload_OrphanFunctions_NotRoutable, but over real HTTP.
func TestUpload_OrphanFunctions_HTTPLevel404(t *testing.T) {
	handler, _ := newUploadTestHandler(t)
	for _, base := range []string{"fn_orphan_prepare", "fn_orphan_mandatory"} {
		t.Run(base, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/route/public/"+base, nil)
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)
			if rec.Code != http.StatusNotFound {
				t.Fatalf("expected 404 for an orphan half, got %d: %s", rec.Code, rec.Body.String())
			}
		})
	}
}

// TestUpload_DiscardPath proves an omitted "path" streams and measures the
// upload but writes nothing to the served directory tree.
func TestUpload_DiscardPath(t *testing.T) {
	handler, dir := newUploadTestHandler(t)

	req := multipartUploadRequest(t, "/route/public/fn_dest_upload", "file", "discard.txt", "text/plain", []byte("throwaway"))
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
