package route

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/rel-server/rel/config"
)

// TestEncodeBody_JSON : body decodes to the actual JSON value, never a
// JSON-encoded string of it.
func TestEncodeBody_JSON(t *testing.T) {
	raw, err := encodeBody("application/json", []byte(`{"a":1,"b":[true,null]}`), false)
	if err != nil {
		t.Fatalf("encodeBody: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("decoding result: %v", err)
	}
	if decoded["a"] != float64(1) {
		t.Errorf("expected a=1, got %v", decoded["a"])
	}
}

// TestEncodeBody_PlusJsonSuffix covers "any +json suffix" per spec, e.g.
// application/vnd.api+json.
func TestEncodeBody_PlusJsonSuffix(t *testing.T) {
	raw, err := encodeBody("application/vnd.api+json", []byte(`{"ok":true}`), false)
	if err != nil {
		t.Fatalf("encodeBody: %v", err)
	}
	if string(raw) != `{"ok":true}` {
		t.Errorf("expected raw JSON passthrough, got %s", raw)
	}
}

func TestEncodeBody_MalformedJSON_Is400(t *testing.T) {
	_, err := encodeBody("application/json", []byte(`{"a":`), false)
	if err == nil {
		t.Fatalf("expected an error for malformed JSON")
	}
	if _, ok := err.(*badBodyError); !ok {
		t.Errorf("expected *badBodyError, got %T", err)
	}
}

// TestEncodeBody_TextPlain covers text/* : body is the plain string.
func TestEncodeBody_TextPlain(t *testing.T) {
	raw, err := encodeBody("text/plain; charset=utf-8", []byte("hello world"), false)
	if err != nil {
		t.Fatalf("encodeBody: %v", err)
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		t.Fatalf("decoding result: %v", err)
	}
	if s != "hello world" {
		t.Errorf("expected \"hello world\", got %q", s)
	}
}

// TestEncodeBody_FormUrlencoded : body decodes through
// querystring.DecodeStructural, including dotted-key nesting.
func TestEncodeBody_FormUrlencoded(t *testing.T) {
	raw, err := encodeBody("application/x-www-form-urlencoded", []byte("name=John&email=john%40example.com&user.role=admin"), false)
	if err != nil {
		t.Fatalf("encodeBody: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("decoding result: %v", err)
	}
	if decoded["name"] != "John" || decoded["email"] != "john@example.com" {
		t.Errorf("expected name/email decoded, got %#v", decoded)
	}
	user, ok := decoded["user"].(map[string]any)
	if !ok || user["role"] != "admin" {
		t.Errorf("expected dotted key user.role to nest, got %#v", decoded["user"])
	}
}

// TestEncodeBody_FormUrlencoded_Malformed_Is400 : a genuinely malformed
// percent-encoding is a 400, same as malformed JSON.
func TestEncodeBody_FormUrlencoded_Malformed_Is400(t *testing.T) {
	_, err := encodeBody("application/x-www-form-urlencoded", []byte("name=%zz"), false)
	if err == nil {
		t.Fatalf("expected an error for malformed percent-encoding")
	}
	if _, ok := err.(*badBodyError); !ok {
		t.Errorf("expected *badBodyError, got %T", err)
	}
}

// TestEncodeBody_Binary covers "anything else" : body is a base64-encoded
// string of the raw bytes.
func TestEncodeBody_Binary(t *testing.T) {
	payload := []byte{0x00, 0x01, 0xff, 0xfe, 'h', 'i'}
	raw, err := encodeBody("application/octet-stream", payload, false)
	if err != nil {
		t.Fatalf("encodeBody: %v", err)
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		t.Fatalf("decoding result: %v", err)
	}
	decoded, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		t.Fatalf("decoding base64: %v", err)
	}
	if string(decoded) != string(payload) {
		t.Errorf("expected base64 round-trip, got %v want %v", decoded, payload)
	}
}

// TestEncodeBody_NoBody covers "No body at all : body is JSON null."
func TestEncodeBody_NoBody(t *testing.T) {
	raw, err := encodeBody("application/json", nil, false)
	if err != nil {
		t.Fatalf("encodeBody: %v", err)
	}
	if string(raw) != "null" {
		t.Errorf("expected null, got %s", raw)
	}
}

// TestEncodeBody_HasFiles_AlwaysNull : body is always JSON null when files
// is declared, even for a combination that would otherwise decode to JSON.
func TestEncodeBody_HasFiles_AlwaysNull(t *testing.T) {
	raw, err := encodeBody("application/json", []byte(`{"a":1}`), true)
	if err != nil {
		t.Fatalf("encodeBody: %v", err)
	}
	if string(raw) != "null" {
		t.Errorf("expected null when hasFiles=true, got %s", raw)
	}
}

func TestMediaTypeOf_StripsParams(t *testing.T) {
	if got := mediaTypeOf("text/plain; charset=utf-8"); got != "text/plain" {
		t.Errorf("expected text/plain, got %q", got)
	}
	if got := mediaTypeOf("application/json"); got != "application/json" {
		t.Errorf("expected application/json, got %q", got)
	}
}

// TestRenderFullControlResponse_ExplicitStatusWithNullContentIsNotMasked
// is a regression test : a response that sets an explicit status (e.g. a
// terminating middleware's {status: 401}) with null/empty content is a
// real, deliberate answer — it must be sent as that status, not silently
// turned into a 404 by ## Static path masking's "returning neither serves
// a 404" rule, which is scoped to a response that set NEITHER content NOR
// its own status at all.
func TestRenderFullControlResponse_ExplicitStatusWithNullContentIsNotMasked(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/whatever", nil)
	cfg := config.Test()

	renderFullControlResponse(rec, req, cfg, "public.fn", Route{}, relHttpResponsePayload{}, relHttpResponsePayload{Status: 401}, []byte("null"), nil, nil)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("expected the explicit 401 to be sent as-is, got %d", rec.Code)
	}
}

// TestRenderFullControlResponse_NoStatusNoContentIsMaskingNotFound covers
// the actual masking case this rule protects : nothing set at all (no
// status, no content, no static_file) really does mean 404.
func TestRenderFullControlResponse_NoStatusNoContentIsMaskingNotFound(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/whatever", nil)
	cfg := config.Test()

	renderFullControlResponse(rec, req, cfg, "public.fn", Route{}, relHttpResponsePayload{}, relHttpResponsePayload{}, []byte("null"), nil, nil)

	if rec.Code != http.StatusNotFound {
		t.Errorf("expected 404 for a response setting neither status nor content, got %d", rec.Code)
	}
}
