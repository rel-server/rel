// Copyright 2025 Christophe Eymard
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package boot

import (
	"bytes"
	"compress/gzip"
	"compress/zlib"
	"crypto/rand"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/klauspost/compress/zstd"

	"github.com/rel-server/rel/config"
)

func gzipped(t *testing.T, b []byte) []byte {
	var buf bytes.Buffer
	w := gzip.NewWriter(&buf)
	_, _ = w.Write(b)
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func zlibbed(t *testing.T, b []byte) []byte {
	var buf bytes.Buffer
	w := zlib.NewWriter(&buf)
	_, _ = w.Write(b)
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func zstded(t *testing.T, b []byte) []byte {
	w, err := zstd.NewWriter(nil)
	if err != nil {
		t.Fatal(err)
	}
	return w.EncodeAll(b, nil)
}

// echoed is what the handler behind decodeRequestBody saw, or the refusal's status and text.
type echoed struct {
	code     int
	body     string
	length   int64
	encoding string
	readErr  error
}

func serveDecoded(t *testing.T, cfg *config.Config, body []byte, encodings ...string) echoed {
	t.Helper()
	var got echoed
	h := decodeRequestBody(cfg, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got.length, got.encoding = r.ContentLength, r.Header.Get("Content-Encoding")
		b, err := io.ReadAll(r.Body)
		got.body, got.readErr = string(b), err
	}))
	req := httptest.NewRequest("POST", "/rel", bytes.NewReader(body))
	for _, e := range encodings {
		req.Header.Add("Content-Encoding", e)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	got.code = rec.Code
	if got.code != http.StatusOK {
		got.body = rec.Body.String()
	}
	return got
}

// Each accepted coding reaches the handler decoded, with no Content-Encoding left and an unknown length, and the
// pooled decoders give the same result when reused.
func TestDecodeRequestBody_DecodesEachCoding(t *testing.T) {
	cfg := config.Test()
	plain := []byte(strings.Repeat(`{"relation":"t"}`, 100))
	cases := map[string][]byte{"gzip": gzipped(t, plain), "x-gzip": gzipped(t, plain), "deflate": zlibbed(t, plain), "zstd": zstded(t, plain), "IDENTITY, Gzip": gzipped(t, plain)}
	for coding, body := range cases {
		for round := range 2 {
			got := serveDecoded(t, cfg, body, coding)
			if got.code != http.StatusOK || got.readErr != nil {
				t.Fatalf("%s round %d: status %d, read error %v : %s", coding, round, got.code, got.readErr, got.body)
			}
			if got.body != string(plain) {
				t.Errorf("%s round %d: decoded body differs", coding, round)
			}
			if got.length != -1 || got.encoding != "" {
				t.Errorf("%s round %d: ContentLength %d, Content-Encoding %q ; want -1 and none", coding, round, got.length, got.encoding)
			}
		}
	}
}

func TestDecodeRequestBody_Refusals(t *testing.T) {
	cfg := config.Test()
	plain := []byte(`{"relation":"t"}`)
	for name, c := range map[string]struct {
		body      []byte
		encodings []string
		code      int
	}{
		"br":                   {plain, []string{"br"}, http.StatusUnsupportedMediaType},
		"two codings":          {gzipped(t, gzipped(t, plain)), []string{"gzip, gzip"}, http.StatusUnsupportedMediaType},
		"codings on two lines": {gzipped(t, plain), []string{"gzip", "zstd"}, http.StatusUnsupportedMediaType},
		"not gzip":             {plain, []string{"gzip"}, http.StatusBadRequest},
		"not zlib":             {plain, []string{"deflate"}, http.StatusBadRequest},
	} {
		t.Run(name, func(t *testing.T) {
			if got := serveDecoded(t, cfg, c.body, c.encodings...); got.code != c.code {
				t.Fatalf("status %d, want %d : %s", got.code, c.code, got.body)
			}
		})
	}
}

// A zstd body that is not zstd fails as the handler reads it, not as a too-large body.
func TestDecodeRequestBody_BadZstdFailsTheRead(t *testing.T) {
	got := serveDecoded(t, config.Test(), []byte(`{"relation":"t"}`), "zstd")
	if got.readErr == nil {
		t.Fatalf("expected the read to fail")
	}
	if _, ok := errors.AsType[*http.MaxBytesError](got.readErr); ok {
		t.Errorf("a malformed stream reads as too large : %v", got.readErr)
	}
}

// The encoded bytes are bounded on their own : zstd skippable frames decode to nothing, so only that bound stops
// them. The handler's read must still fail with the *http.MaxBytesError its own 413 tests for.
func TestDecodeRequestBody_EncodedBytesBounded(t *testing.T) {
	cfg := config.Test()
	cfg.Http.MaxBodySize, cfg.Http.Upload.MaxSize = 1024, 1024
	frame := append([]byte{0x50, 0x2a, 0x4d, 0x18, 0xf8, 0x00, 0x00, 0x00}, make([]byte, 248)...) // a skippable frame of 248 bytes
	noise := make([]byte, 4096)
	_, _ = rand.Read(noise)
	for coding, b := range map[string][]byte{"zstd": bytes.Repeat(frame, 10), "gzip": gzipped(t, noise), "deflate": zlibbed(t, noise)} {
		got := serveDecoded(t, cfg, b, coding)
		if _, ok := errors.AsType[*http.MaxBytesError](got.readErr); !ok {
			t.Errorf("%s: read error %v, want a *http.MaxBytesError", coding, got.readErr)
		}
	}
	// a body under the bound decodes, skippable frames included
	if got := serveDecoded(t, cfg, append(frame, zstded(t, []byte("ok"))...), "zstd"); got.readErr != nil || got.body != "ok" {
		t.Errorf("under the bound : body %q, error %v", got.body, got.readErr)
	}
}
