package route

// End-to-end tests for specs/new-routes.md's new dispatch mechanism,
// driven through a real http.Handler (testHandler, or a dedicated one with
// a real static.Server for the stream_upload case) rather than calling
// internal functions directly — proving the whole request lifecycle
// (chi pattern matching, JWT verify, anon authorization, body resolution,
// invocation, response writing) actually works together.

import (
	"bytes"
	"context"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/fxamacker/cbor/v2"
	jwtpkg "github.com/rel-server/rel/jwt"
	"github.com/rel-server/rel/static"
)

func TestE2E_SimpleGet(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/new/echo0", nil)
	rec := httptest.NewRecorder()
	testHandler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("expected application/json, got %q", ct)
	}
	if rec.Body.String() != "{}" {
		t.Errorf("expected raw jsonb body {}, got %q", rec.Body.String())
	}
}

func TestE2E_PathArgument(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/new/items/42", nil)
	rec := httptest.NewRecorder()
	testHandler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"id"`) || !strings.Contains(rec.Body.String(), `"42"`) {
		t.Errorf("expected the path placeholder bound into id, got %q", rec.Body.String())
	}
}

func TestE2E_FullControl_JWTAndCookie(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/new/login", nil)
	rec := httptest.NewRecorder()
	testHandler.ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"ok"`) || !strings.Contains(rec.Body.String(), "true") {
		t.Errorf("expected the second OUT column as the body, got %q", rec.Body.String())
	}

	foundJwt, foundGreeting := false, false
	for _, c := range rec.Result().Cookies() {
		if c.Name == testCfg.Jwt.CookieName {
			foundJwt = true
		}
		if c.Name == "greeting" && c.Value == "hello" {
			foundGreeting = true
		}
	}
	if !foundJwt {
		t.Error("expected a session cookie from the envelope's jwt field")
	}
	if !foundGreeting {
		t.Error("expected the envelope's plain cookies.greeting to be set")
	}
}

// TestE2E_MultipartUpload_BytesArray drives fn_new_upload's plain bytea[]
// shape (not stream_upload) through a real multipart request — exercising
// the finishMultipartBody -> route.AcceptsBytesArray -> buildInvokeCall
// $N::bytea[] path, which no other test invokes.
func TestE2E_MultipartUpload_BytesArray(t *testing.T) {
	var body strings.Builder
	mw := multipart.NewWriter(&body)
	for _, name := range []string{"a.txt", "b.txt"} {
		part, err := mw.CreateFormFile("file", name)
		if err != nil {
			t.Fatalf("CreateFormFile: %v", err)
		}
		if _, err := part.Write([]byte("content of " + name)); err != nil {
			t.Fatalf("writing part: %v", err)
		}
	}
	if err := mw.Close(); err != nil {
		t.Fatalf("closing multipart writer: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/new/upload", strings.NewReader(body.String()))
	req.Header.Set("Content-Type", mw.FormDataContentType())
	rec := httptest.NewRecorder()
	testHandler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"count"`) || !strings.Contains(rec.Body.String(), "2") {
		t.Errorf("expected count: 2 for the two uploaded files, got %q", rec.Body.String())
	}
}

func TestE2E_AnonymousForbidden(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/new/full", nil)
	rec := httptest.NewRecorder()
	testHandler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 (fn_new_fullcontrol has no anonymous grant), got %d: %s", rec.Code, rec.Body.String())
	}
}

// TestE2E_JWTRenewal proves jwtpkg.RenewIfDue still runs on the new
// dispatch path : a token past its jwt.renew_after fraction (default 0.5)
// but not yet expired gets a fresh Set-Cookie on an otherwise-ordinary 200.
func TestE2E_JWTRenewal(t *testing.T) {
	iat := time.Now().Add(-100 * time.Minute)
	exp := time.Now().Add(10 * time.Minute) // lifespan 110min, elapsed 100min > 0.5*110min
	token, err := jwtpkg.Sign(testCfg.Jwt, jwtpkg.Claims{
		"role":      "app_user",
		"iat":       float64(iat.Unix()),
		"exp":       float64(exp.Unix()),
		"auth_time": float64(iat.Unix()),
	})
	if err != nil {
		t.Fatalf("signing token: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/new/echo0", nil)
	req.AddCookie(&http.Cookie{Name: testCfg.Jwt.CookieName, Value: token})
	rec := httptest.NewRecorder()
	testHandler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	renewed := false
	for _, c := range rec.Result().Cookies() {
		if c.Name == testCfg.Jwt.CookieName && c.Value != "" && c.Value != token {
			renewed = true
		}
	}
	if !renewed {
		t.Errorf("expected a renewed session cookie, got Set-Cookie headers: %v", rec.Header().Values("Set-Cookie"))
	}
}

// TestE2E_RSxxxClassification proves an application-raised RSxxx exception
// (pgerr.Classify) surfaces through the new dispatch path at the right
// status, not just in pgerr's own unit tests.
// TestE2E_SniffedContentType_Multipart proves ## Content-type sniffing's
// Part.sniffed_content_type : rel detects the part's actual bytes
// regardless of what Content-Type the client claimed for it.
func TestE2E_SniffedContentType_Multipart(t *testing.T) {
	var body strings.Builder
	mw := multipart.NewWriter(&body)
	part, err := mw.CreatePart(map[string][]string{
		"Content-Disposition": {`form-data; name="file"; filename="fake.txt"`},
		"Content-Type":        {"text/plain"}, // claimed ; the actual bytes are a PNG header
	})
	if err != nil {
		t.Fatalf("CreatePart: %v", err)
	}
	if _, err := part.Write([]byte("\x89PNG\r\n\x1a\n" + strings.Repeat("x", 20))); err != nil {
		t.Fatalf("writing part: %v", err)
	}
	if err := mw.Close(); err != nil {
		t.Fatalf("closing multipart writer: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/new/sniff/multipart", strings.NewReader(body.String()))
	req.Header.Set("Content-Type", mw.FormDataContentType())
	rec := httptest.NewRecorder()
	testHandler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "image/png") {
		t.Errorf("expected the sniffed image/png (not the claimed text/plain), got %q", rec.Body.String())
	}
}

// TestE2E_SniffedContentType_RawBody proves ## Content-type sniffing's
// HttpRequest.sniffed_content_type for a raw (non-multipart) bytea body.
func TestE2E_SniffedContentType_RawBody(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/new/sniff/body", strings.NewReader("\x89PNG\r\n\x1a\n"+strings.Repeat("x", 20)))
	req.Header.Set("Content-Type", "application/octet-stream")
	rec := httptest.NewRecorder()
	testHandler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "image/png") {
		t.Errorf("expected the sniffed image/png, got %q", rec.Body.String())
	}
}

// TestE2E_SniffedContentType_StreamUploadSecondCallOnly proves stream_upload
// never sniffs on the first call (no bytes yet) but does on the second,
// from the already-streamed temp file's head.
func TestE2E_SniffedContentType_StreamUploadSecondCallOnly(t *testing.T) {
	dir := t.TempDir()
	cfg := *testCfg
	cfg.Http.Static.Path = dir
	cfg.Http.Upload.Dir = "uploads"
	if err := os.MkdirAll(filepath.Join(dir, "uploads"), 0o755); err != nil {
		t.Fatalf("mkdir uploads: %v", err)
	}
	staticSrv := static.New(cfg.Http)
	handler := NewHandler(testDb, &cfg, testReg, staticSrv, nil)

	var body strings.Builder
	mw := multipart.NewWriter(&body)
	part, err := mw.CreatePart(map[string][]string{
		"Content-Disposition": {`form-data; name="file"; filename="fake.txt"`},
		"Content-Type":        {"text/plain"},
	})
	if err != nil {
		t.Fatalf("CreatePart: %v", err)
	}
	if _, err := part.Write([]byte("\x89PNG\r\n\x1a\n" + strings.Repeat("x", 20))); err != nil {
		t.Fatalf("writing part: %v", err)
	}
	if err := mw.Close(); err != nil {
		t.Fatalf("closing multipart writer: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/new/stream?path=sniffed.png", strings.NewReader(body.String()))
	req.Header.Set("Content-Type", mw.FormDataContentType())
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "image/png") {
		t.Errorf("expected the second call's sniffed image/png (from the temp file's head), got %q", rec.Body.String())
	}
}

func TestE2E_RSxxxClassification(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/new/raises", nil)
	rec := httptest.NewRecorder()
	testHandler.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403 (RS403), got %d: %s", rec.Code, rec.Body.String())
	}
}

// TestE2E_TemplateRendering proves a declared template actually renders
// over a real HTTP request — the single-return route's jsonb return value
// becomes the template's Data, per specs/new-routes.md ## Templates. No
// existing test exercised writeTemplateResponse/NewTemplateSet through the
// real dispatch path at all before this.
func TestE2E_TemplateRendering(t *testing.T) {
	templatesDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(templatesDir, "greet.jet"), []byte("Hello, {{Data.name}}!"), 0o644); err != nil {
		t.Fatalf("writing greet.jet: %v", err)
	}

	cfg := *testCfg
	cfg.Http.Templates.Path = templatesDir
	handler := NewHandler(testDb, &cfg, testReg, nil, nil)

	req := httptest.NewRequest(http.MethodGet, "/new/templated", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if rec.Body.String() != "Hello, world!" {
		t.Errorf("expected the rendered template, got %q", rec.Body.String())
	}
}

// TestE2E_TemplateRendering_RelAvailable proves rel() is available from a
// database-function-return template, not only a static one
// (specs/templating-2.md) — the shared jet.Set's Set-global registration
// reaches both.
func TestE2E_TemplateRendering_RelAvailable(t *testing.T) {
	if _, err := testDb.Pool.Exec(context.Background(), "delete from stream_upload_log"); err != nil {
		t.Fatalf("clearing stream_upload_log: %v", err)
	}
	if _, err := testDb.Pool.Exec(context.Background(), `insert into stream_upload_log (upload) values ('{"marker":"db-template-rel-test"}'::jsonb)`); err != nil {
		t.Fatalf("seeding stream_upload_log: %v", err)
	}

	templatesDir := t.TempDir()
	tmpl := `{{ range _, row := rel(map("relation", "stream_upload_log", "schema", "public", "select", slice("*~"))) }}marker={{ row.upload.marker }};{{ end }}`
	if err := os.WriteFile(filepath.Join(templatesDir, "greet.jet"), []byte(tmpl), 0o644); err != nil {
		t.Fatalf("writing greet.jet: %v", err)
	}

	cfg := *testCfg
	cfg.Http.Templates.Path = templatesDir
	handler := NewHandler(testDb, &cfg, testReg, nil, nil)

	req := httptest.NewRequest(http.MethodGet, "/new/templated", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "marker=db-template-rel-test;") {
		t.Errorf("expected rel() to be available and return the seeded row, got %q", rec.Body.String())
	}
}

// TestE2E_TemplateMissingConfig proves a declared template with no
// http.templates.path configured is a clean 500, not a silent fallback to
// the raw return value.
func TestE2E_TemplateMissingConfig(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/new/templated", nil)
	rec := httptest.NewRecorder()
	testHandler.ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500 (no http.templates.path configured), got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestE2E_NotFound(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/nothing/declared/here", nil)
	rec := httptest.NewRecorder()
	testHandler.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", rec.Code)
	}
}

// TestE2E_StreamUpload_TwoCallFlow drives an actual multipart upload
// through fn_new_stream (## `stream_upload`) : first call decides the
// destination (via the "path" query param, matching the old __prepare
// fixture's own convention), second call runs after bytes are streamed to
// disk, and the response echoes the recorded upload metadata including the
// real observed size.
func TestE2E_StreamUpload_TwoCallFlow(t *testing.T) {
	dir := t.TempDir()
	cfg := *testCfg
	cfg.Http.Static.Path = dir
	cfg.Http.Upload.Dir = "uploads"
	if err := os.MkdirAll(filepath.Join(dir, "uploads"), 0o755); err != nil {
		t.Fatalf("mkdir uploads: %v", err)
	}
	staticSrv := static.New(cfg.Http)
	handler := NewHandler(testDb, &cfg, testReg, staticSrv, nil)

	var body strings.Builder
	mw := multipart.NewWriter(&body)
	part, err := mw.CreateFormFile("file", "hello.txt")
	if err != nil {
		t.Fatalf("CreateFormFile: %v", err)
	}
	if _, err := part.Write([]byte("hello upload")); err != nil {
		t.Fatalf("writing part: %v", err)
	}
	if err := mw.Close(); err != nil {
		t.Fatalf("closing multipart writer: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/new/stream?path=uploaded.txt", strings.NewReader(body.String()))
	req.Header.Set("Content-Type", mw.FormDataContentType())
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"size"`) || !strings.Contains(rec.Body.String(), "12") {
		t.Errorf(`expected the real observed size (12 bytes) in the response, got %q`, rec.Body.String())
	}

	written, err := os.ReadFile(filepath.Join(dir, "uploads", "uploaded.txt"))
	if err != nil {
		t.Fatalf("reading uploaded file: %v", err)
	}
	if string(written) != "hello upload" {
		t.Errorf("expected the file's real content, got %q", written)
	}
}

func getAccepting(path, accept string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, path, nil)
	if accept != "" {
		req.Header.Set("Accept", accept)
	}
	rec := httptest.NewRecorder()
	testHandler.ServeHTTP(rec, req)
	return rec
}

func decodeCBORAny(t *testing.T, b []byte) any {
	t.Helper()
	dm, err := cbor.DecOptions{DefaultMapType: reflect.TypeOf(map[string]any{})}.DecMode()
	if err != nil {
		t.Fatal(err)
	}
	var v any
	if err := dm.Unmarshal(b, &v); err != nil {
		t.Fatalf("not CBOR (%v) : %x", err, b)
	}
	return v
}

func TestE2E_AnyTypeReturn_JSON(t *testing.T) {
	for path, want := range map[string]string{
		"/cbor/int":      "42",
		"/cbor/null":     "null",
		"/cbor/interval": `"1 day"`,
		"/cbor/row":      `{"id":7,"at":"2023-11-14T22:13:20.5+00:00","price":12.50,"tags":["a","b"]}`,
	} {
		rec := getAccepting(path, "")
		if rec.Code != 200 || rec.Header().Get("Content-Type") != "application/json" {
			t.Errorf("%s : %d %q %s", path, rec.Code, rec.Header().Get("Content-Type"), rec.Body.String())
			continue
		}
		var got, exp any
		if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
			t.Errorf("%s : %v : %s", path, err, rec.Body.String())
		}
		_ = json.Unmarshal([]byte(want), &exp)
		if !reflect.DeepEqual(got, exp) {
			t.Errorf("%s = %s, want %s", path, rec.Body.String(), want)
		}
	}
}

func TestE2E_AcceptCBOR_Routes(t *testing.T) {
	// native : the value's own types
	rec := getAccepting("/cbor/int", "application/cbor")
	if rec.Code != 200 || rec.Header().Get("Content-Type") != "application/cbor" || rec.Header().Get("Vary") != "Accept" || !bytes.Equal(rec.Body.Bytes(), []byte{0x18, 42}) {
		t.Errorf("int : %d %q %q %x", rec.Code, rec.Header().Get("Content-Type"), rec.Header().Get("Vary"), rec.Body.Bytes())
	}
	if rec = getAccepting("/cbor/null", "application/cbor"); !bytes.Equal(rec.Body.Bytes(), []byte{0xf6}) {
		t.Errorf("null = %x", rec.Body.Bytes())
	}
	rec = getAccepting("/cbor/row", "application/cbor")
	row, _ := decodeCBORAny(t, rec.Body.Bytes()).(map[string]any)
	if at, ok := row["at"].(time.Time); !ok || at.UnixNano() != 1700000000500000000 {
		t.Errorf("row = %#v", row)
	}
	if tags, _ := row["tags"].([]any); len(tags) != 2 || row["id"] != uint64(7) {
		t.Errorf("row = %#v", row)
	}
	// a type with no CBOR form of its own falls back to its JSON, converted
	rec = getAccepting("/cbor/interval", "application/cbor")
	if rec.Header().Get("Content-Type") != "application/cbor" || decodeCBORAny(t, rec.Body.Bytes()) != "1 day" {
		t.Errorf("interval : %q %x", rec.Header().Get("Content-Type"), rec.Body.Bytes())
	}
	// json/jsonb : converted as a whole
	rec = getAccepting("/cbor/json", "application/cbor")
	m, _ := decodeCBORAny(t, rec.Body.Bytes()).(map[string]any)
	if rec.Header().Get("Content-Type") != "application/cbor" || m["b"] != "é" || len(m["a"].([]any)) != 3 {
		t.Errorf("json : %q %#v", rec.Header().Get("Content-Type"), m)
	}
	// JSON remains the default, and the answer varies by Accept either way
	for _, accept := range []string{"", "*/*", "application/json", "application/cbor;q=0.1, application/json"} {
		rec = getAccepting("/cbor/json", accept)
		if rec.Header().Get("Content-Type") != "application/json" || rec.Header().Get("Vary") != "Accept" {
			t.Errorf("%q : %q %q", accept, rec.Header().Get("Content-Type"), rec.Header().Get("Vary"))
		}
	}
	// full control : the status and headers are untouched, the content is converted
	rec = getAccepting("/cbor/full", "application/cbor")
	if rec.Code != 201 || rec.Header().Get("Content-Type") != "application/cbor" {
		t.Fatalf("full : %d %q %s", rec.Code, rec.Header().Get("Content-Type"), rec.Body.String())
	}
	if rows, _ := decodeCBORAny(t, rec.Body.Bytes()).([]any); len(rows) != 1 || rows[0].(map[string]any)["tags"].([]any)[0] != "x" {
		t.Errorf("full = %#v", decodeCBORAny(t, rec.Body.Bytes()))
	}
	if rec = getAccepting("/cbor/full", ""); rec.Code != 201 || rec.Header().Get("Content-Type") != "application/json" {
		t.Errorf("full as JSON : %d %q %s", rec.Code, rec.Header().Get("Content-Type"), rec.Body.String())
	}
}
