package route

import (
	"bytes"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// buildMultipart writes a multipart/form-data body from (fieldName,
// fileName, contentType, content) tuples ; fileName=="" writes a plain field.
type multipartField struct {
	FieldName   string
	FileName    string
	ContentType string
	Content     []byte
}

func buildMultipart(t *testing.T, fields []multipartField) (*bytes.Buffer, string) {
	t.Helper()
	buf := &bytes.Buffer{}
	w := multipart.NewWriter(buf)
	for _, f := range fields {
		var part io.Writer
		var err error
		if f.FileName != "" {
			part, err = w.CreatePart(map[string][]string{
				"Content-Disposition": {`form-data; name="` + f.FieldName + `"; filename="` + f.FileName + `"`},
				"Content-Type":        {f.ContentType},
			})
		} else {
			part, err = w.CreateFormField(f.FieldName)
		}
		if err != nil {
			t.Fatalf("creating multipart part: %v", err)
		}
		if _, err := part.Write(f.Content); err != nil {
			t.Fatalf("writing multipart part: %v", err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatalf("closing multipart writer: %v", err)
	}
	return buf, w.FormDataContentType()
}

// TestHandler_Upload_MultipleFiles proves a files-route round-trips 2+
// multipart files correctly.
func TestHandler_Upload_MultipleFiles(t *testing.T) {
	body, contentType := buildMultipart(t, []multipartField{
		{FieldName: "a", FileName: "a.txt", ContentType: "text/plain", Content: []byte("hello")},
		{FieldName: "b", FileName: "b.bin", ContentType: "application/octet-stream", Content: []byte{0x00, 0x01, 0x02}},
	})

	req := httptest.NewRequest(http.MethodPost, "/route/public/fn_upload", body)
	req.Header.Set("Content-Type", contentType)
	rec := httptest.NewRecorder()
	testHandler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	// content is written directly as the HTTP body, unwrapped.
	var decoded struct {
		Body  any      `json:"body"`
		Count int      `json:"count"`
		Files []string `json:"files"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &decoded); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if decoded.Count != 2 {
		t.Fatalf("expected 2 files, got %d", decoded.Count)
	}
	if decoded.Body != nil {
		t.Errorf("expected body=null on a files route, got %v", decoded.Body)
	}
}

// TestHandler_UploadWithHeaders_PartsMetadata : correct name/filename/
// content_type per part, including a mixed plain-field-plus-file submission.
func TestHandler_UploadWithHeaders_PartsMetadata(t *testing.T) {
	body, contentType := buildMultipart(t, []multipartField{
		{FieldName: "title", Content: []byte("my document")}, // plain field, no filename
		{FieldName: "doc", FileName: "report.txt", ContentType: "text/plain", Content: []byte("report contents")},
	})

	req := httptest.NewRequest(http.MethodPost, "/route/public/fn_upload_with_headers", body)
	req.Header.Set("Content-Type", contentType)
	rec := httptest.NewRecorder()
	testHandler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var decoded struct {
		PartsHeaders []struct {
			Name        *string `json:"name"`
			Filename    *string `json:"filename"`
			ContentType *string `json:"content_type"`
		} `json:"parts_headers"`
		Contents []string `json:"contents"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &decoded); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if len(decoded.PartsHeaders) != 2 {
		t.Fatalf("expected 2 parts_headers entries, got %d", len(decoded.PartsHeaders))
	}
	titlePart := decoded.PartsHeaders[0]
	if titlePart.Name == nil || *titlePart.Name != "title" {
		t.Errorf("expected parts_headers[0].name=title, got %v", titlePart.Name)
	}
	if titlePart.Filename != nil {
		t.Errorf("expected parts_headers[0].filename=null for a plain field, got %v", *titlePart.Filename)
	}
	docPart := decoded.PartsHeaders[1]
	if docPart.Name == nil || *docPart.Name != "doc" {
		t.Errorf("expected parts_headers[1].name=doc, got %v", docPart.Name)
	}
	if docPart.Filename == nil || *docPart.Filename != "report.txt" {
		t.Errorf("expected parts_headers[1].filename=report.txt, got %v", docPart.Filename)
	}
	if docPart.ContentType == nil || *docPart.ContentType != "text/plain" {
		t.Errorf("expected parts_headers[1].content_type=text/plain, got %v", docPart.ContentType)
	}
	if decoded.Contents[0] != "my document" || decoded.Contents[1] != "report contents" {
		t.Errorf("expected decoded file contents to round-trip, got %#v", decoded.Contents)
	}
}

// TestHandler_Upload_SingleBinaryPOST_SynthesizesOneElementFilesArray : a
// raw binary POST becomes a one-element files array plus a pseudo-part.
func TestHandler_Upload_SingleBinaryPOST_SynthesizesOneElementFilesArray(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/route/public/fn_upload_with_headers", bytes.NewReader([]byte("raw binary payload")))
	req.Header.Set("Content-Type", "application/octet-stream")
	rec := httptest.NewRecorder()
	testHandler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var decoded struct {
		PartsHeaders []struct {
			Name        *string `json:"name"`
			Filename    *string `json:"filename"`
			ContentType *string `json:"content_type"`
		} `json:"parts_headers"`
		Contents []string `json:"contents"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &decoded); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if len(decoded.PartsHeaders) != 1 {
		t.Fatalf("expected exactly one synthesized pseudo-part, got %d", len(decoded.PartsHeaders))
	}
	part := decoded.PartsHeaders[0]
	if part.Name != nil {
		t.Errorf("expected synthesized pseudo-part name=null, got %v", *part.Name)
	}
	if part.Filename != nil {
		t.Errorf("expected synthesized pseudo-part filename=null, got %v", *part.Filename)
	}
	if part.ContentType == nil || *part.ContentType != "application/octet-stream" {
		t.Errorf("expected synthesized pseudo-part content_type=application/octet-stream, got %v", part.ContentType)
	}
	if decoded.Contents[0] != "raw binary payload" {
		t.Errorf("expected the whole raw body as files[0], got %q", decoded.Contents[0])
	}
}

// TestHandler_FilesRoute_NoBodyAtAll_NotA415 : a files-declaring route
// with no body gets empty files/parts_headers, not a 415.
func TestHandler_FilesRoute_NoBodyAtAll_NotA415(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/route/public/fn_upload", nil)
	rec := httptest.NewRecorder()
	testHandler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 (empty files, not a 415), got %d: %s", rec.Code, rec.Body.String())
	}
	var decoded struct {
		Count int `json:"count"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &decoded); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if decoded.Count != 0 {
		t.Errorf("expected 0 files, got %d", decoded.Count)
	}
}

// TestHandler_FilesRoute_ZeroPartMultipart_NotA415 : a syntactically
// valid multipart envelope with zero parts is not a 415.
func TestHandler_FilesRoute_ZeroPartMultipart_NotA415(t *testing.T) {
	body, contentType := buildMultipart(t, nil)
	req := httptest.NewRequest(http.MethodPost, "/route/public/fn_upload", body)
	req.Header.Set("Content-Type", contentType)
	rec := httptest.NewRecorder()
	testHandler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 (zero-part multipart, not a 415), got %d: %s", rec.Code, rec.Body.String())
	}
}

// TestHandler_NonFilesRoute_ZeroPartMultipart_NotA415 : the same zero-part
// carve-out applies on a plain (req)-only route too.
func TestHandler_NonFilesRoute_ZeroPartMultipart_NotA415(t *testing.T) {
	body, contentType := buildMultipart(t, nil)
	req := httptest.NewRequest(http.MethodPost, "/route/public/fn_echo1", body)
	req.Header.Set("Content-Type", contentType)
	rec := httptest.NewRecorder()
	testHandler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 (zero-part multipart on a non-files route, not a 415), got %d: %s", rec.Code, rec.Body.String())
	}
}

// TestHandler_FilesRoute_JSONBody_Is415 : a files route receiving a
// JSON/text/form-urlencoded content_type is a 415.
func TestHandler_FilesRoute_JSONBody_Is415(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/route/public/fn_upload", strings.NewReader(`{"a":1}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	testHandler.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("expected 415, got %d: %s", rec.Code, rec.Body.String())
	}
}

// TestHandler_NonFilesRoute_RealMultipart_Is415 : a route not declaring
// files, receiving real multipart data, is a 415.
func TestHandler_NonFilesRoute_RealMultipart_Is415(t *testing.T) {
	body, contentType := buildMultipart(t, []multipartField{
		{FieldName: "a", FileName: "a.txt", ContentType: "text/plain", Content: []byte("hello")},
	})
	req := httptest.NewRequest(http.MethodPost, "/route/public/fn_echo1", body)
	req.Header.Set("Content-Type", contentType)
	rec := httptest.NewRecorder()
	testHandler.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("expected 415, got %d: %s", rec.Code, rec.Body.String())
	}
}

// TestHandler_MaxBodySize_RejectsOversizedRequest covers http.max_body_size
// rejecting via both a declared Content-Length and an actual stream.
func TestHandler_MaxBodySize_RejectsOversizedRequest(t *testing.T) {
	small := *testCfg
	small.Http.MaxBodySize = 8
	handler := NewHandler(testDb, &small, testReg, nil)

	req := httptest.NewRequest(http.MethodPost, "/route/public/fn_echo1", strings.NewReader(strings.Repeat("x", 100)))
	req.Header.Set("Content-Type", "text/plain")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("expected 413, got %d: %s", rec.Code, rec.Body.String())
	}
}

// TestHandler_MaxBodySize_RejectsStreamedOverflow_NoDeclaredLength forces
// ContentLength -1, so MaxBytesReader itself must catch the overflow.
func TestHandler_MaxBodySize_RejectsStreamedOverflow_NoDeclaredLength(t *testing.T) {
	small := *testCfg
	small.Http.MaxBodySize = 8
	handler := NewHandler(testDb, &small, testReg, nil)

	req := httptest.NewRequest(http.MethodPost, "/route/public/fn_echo1", strings.NewReader(strings.Repeat("x", 100)))
	req.Header.Set("Content-Type", "text/plain")
	req.ContentLength = -1 // unknown length, e.g. a chunked request
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("expected 413, got %d: %s", rec.Code, rec.Body.String())
	}
}

// TestHandler_MaxBodySize_RejectsStreamedOverflow_MidMultipartPart :
// MaxBytesReader tripping mid-part is 413, not a generic 400.
func TestHandler_MaxBodySize_RejectsStreamedOverflow_MidMultipartPart(t *testing.T) {
	small := *testCfg
	small.Http.MaxBodySize = 16
	handler := NewHandler(testDb, &small, testReg, nil)

	body, contentType := buildMultipart(t, []multipartField{
		{FieldName: "a", FileName: "a.txt", ContentType: "text/plain", Content: []byte(strings.Repeat("x", 1000))},
	})
	req := httptest.NewRequest(http.MethodPost, "/route/public/fn_upload", body)
	req.Header.Set("Content-Type", contentType)
	req.ContentLength = -1
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("expected 413, got %d: %s", rec.Code, rec.Body.String())
	}
}

// TestHandler_MaxPartCount_RejectsTooManyParts covers http.max_part_count
// actually rejecting a request with too many multipart parts.
func TestHandler_MaxPartCount_RejectsTooManyParts(t *testing.T) {
	small := *testCfg
	small.Http.MaxPartCount = 2
	handler := NewHandler(testDb, &small, testReg, nil)

	var fields []multipartField
	for i := 0; i < 5; i++ {
		fields = append(fields, multipartField{FieldName: "f", FileName: "f.txt", ContentType: "text/plain", Content: []byte("x")})
	}
	body, contentType := buildMultipart(t, fields)
	req := httptest.NewRequest(http.MethodPost, "/route/public/fn_upload", body)
	req.Header.Set("Content-Type", contentType)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("expected 413, got %d: %s", rec.Code, rec.Body.String())
	}
}

// TestHandler_FormUrlencodedBody_EndToEnd proves form-urlencoded body
// decoding through a real route function, via fn_echo1's echo.
func TestHandler_FormUrlencodedBody_EndToEnd(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/route/public/fn_echo1", strings.NewReader("name=John&user.role=admin"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	testHandler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var echoed map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &echoed); err != nil {
		t.Fatalf("decoding echoed request: %v", err)
	}
	body, ok := echoed["body"].(map[string]any)
	if !ok {
		t.Fatalf("expected body to be a decoded object, got %#v", echoed["body"])
	}
	if body["name"] != "John" {
		t.Errorf("expected body.name=John, got %v", body["name"])
	}
	user, ok := body["user"].(map[string]any)
	if !ok || user["role"] != "admin" {
		t.Errorf("expected body.user.role=admin, got %#v", body["user"])
	}
}

// TestHandler_FormUrlencodedBody_HeavierNestedPayload_EndToEnd combines
// two-deep nesting with a repeated key, proving both survive together.
func TestHandler_FormUrlencodedBody_HeavierNestedPayload_EndToEnd(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/route/public/fn_echo1", strings.NewReader("a.b.c=1&tags=x&tags=y&user.prefs.theme=dark"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	testHandler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var echoed map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &echoed); err != nil {
		t.Fatalf("decoding echoed request: %v", err)
	}
	body, ok := echoed["body"].(map[string]any)
	if !ok {
		t.Fatalf("expected body to be a decoded object, got %#v", echoed["body"])
	}

	a, ok := body["a"].(map[string]any)
	if !ok {
		t.Fatalf("expected body.a to be an object, got %#v", body["a"])
	}
	b, ok := a["b"].(map[string]any)
	if !ok || b["c"] != "1" {
		t.Errorf("expected body.a.b.c==\"1\" (two-deep dotted nesting), got %#v", a["b"])
	}

	tags, ok := body["tags"].([]any)
	if !ok || len(tags) != 2 || tags[0] != "x" || tags[1] != "y" {
		t.Errorf("expected body.tags==[\"x\",\"y\"] (repeated key -> array), got %#v", body["tags"])
	}

	user, ok := body["user"].(map[string]any)
	if !ok {
		t.Fatalf("expected body.user to be an object, got %#v", body["user"])
	}
	prefs, ok := user["prefs"].(map[string]any)
	if !ok || prefs["theme"] != "dark" {
		t.Errorf("expected body.user.prefs.theme==dark, got %#v", user["prefs"])
	}
}

// TestHandler_TextMimeTypeDomainResponse proves a text-underlying mimetype
// domain route responds with the string directly, no base64.
func TestHandler_TextMimeTypeDomainResponse(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/route/public/fn_text_domain", nil)
	rec := httptest.NewRecorder()
	testHandler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); ct != "text/plain" {
		t.Errorf("expected Content-Type=text/plain, got %q", ct)
	}
	if rec.Body.String() != "hello text domain" {
		t.Errorf("expected the domain's raw text value directly, got %q", rec.Body.String())
	}
}
