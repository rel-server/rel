package server

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/rel-server/rel/errcode"
	"github.com/rel-server/rel/logging"
)

// A /rel POST body over http.max_body_size is a 413, whether Content-Length announces it or the body streams
// past the limit with no length.
func TestRelHandler_BodyOverMaxBodySize_413(t *testing.T) {
	cfg := *testCfg
	cfg.Http.MaxBodySize = 64
	handler := logging.AccessLog(NewRelHandler(testDb, &cfg, nil))
	body := `{"relation": "users", "schema": "public", "where": "` + strings.Repeat("x", 100) + `"}`

	cases := map[string]func() *http.Request{
		"declared length": func() *http.Request {
			return httptest.NewRequest(http.MethodPost, "/rel", strings.NewReader(body))
		},
		"unknown length": func() *http.Request {
			req := httptest.NewRequest(http.MethodPost, "/rel", io.NopCloser(strings.NewReader(body)))
			req.ContentLength = -1
			return req
		},
	}
	for name, mk := range cases {
		t.Run(name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, mk())
			if rec.Code != http.StatusRequestEntityTooLarge {
				t.Fatalf("expected 413, got %d : %s", rec.Code, rec.Body.String())
			}
			if got := errcode.Code(rec.Header().Get(errcode.Header)); got != errcode.BodyTooLarge {
				t.Errorf("X-Rel-Errorcode = %q, want %s", got, errcode.BodyTooLarge)
			}
			resp := decodeJSON[map[string]any](t, rec.Body.Bytes())
			if msg, _ := resp["error"].(string); !strings.Contains(msg, "http.max_body_size") {
				t.Errorf("error message should name http.max_body_size, got %q", msg)
			}
		})
	}
}
