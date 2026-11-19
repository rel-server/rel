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
	"compress/gzip"
	"compress/zlib"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"

	"github.com/klauspost/compress/zstd"

	"github.com/rel-server/rel/config"
	"github.com/rel-server/rel/errcode"
	"github.com/rel-server/rel/httperr"
)

// acceptedEncodings is the Accept-Encoding of a 415 for an unsupported Content-Encoding.
const acceptedEncodings = "gzip, deflate, zstd, identity"

// zstdMaxWindow bounds a zstd stream's back-reference window, the memory one decoder holds : the 8 MiB every
// decoder is expected to support.
const zstdMaxWindow = 8 << 20

var (
	gzipReaders sync.Pool
	zlibReaders sync.Pool
	zstdReaders = sync.Pool{New: func() any {
		// a concurrency of 1 decodes in the caller's goroutine, so a pooled decoder holds no goroutine
		d, err := zstd.NewReader(nil, zstd.WithDecoderConcurrency(1), zstd.WithDecoderMaxWindow(zstdMaxWindow))
		if err != nil {
			panic(err) // only for invalid options
		}
		return d
	}}
)

// decodeRequestBody replaces a gzip, deflate (zlib) or zstd body with its decoded bytes, so every limit downstream
// bounds the decoded size. The encoded bytes are bounded too, by the largest body limit : a zstd stream of
// skippable frames would otherwise be read forever for no output. Any other coding, or more than one, is a 415.
func decodeRequestBody(cfg *config.Config, next http.Handler) http.Handler {
	maxEncoded := int64(max(cfg.Http.MaxBodySize, cfg.Http.Upload.MaxSize))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		coding, ok := requestCoding(r)
		if !ok {
			w.Header().Set("Accept-Encoding", acceptedEncodings)
			httperr.WritePlain(w, http.StatusUnsupportedMediaType, errcode.UnsupportedMediaType, "request body must be encoded with at most one of gzip, deflate or zstd : Content-Encoding "+strings.Join(r.Header.Values("Content-Encoding"), ", ")+" is not accepted")
			return
		}
		if coding == "" {
			next.ServeHTTP(w, r)
			return
		}
		body, release, err := decoder(coding, http.MaxBytesReader(w, r.Body, maxEncoded))
		if err != nil {
			if _, tooLarge := errors.AsType[*http.MaxBytesError](err); tooLarge {
				httperr.WritePlain(w, http.StatusRequestEntityTooLarge, errcode.BodyTooLarge, "request body exceeds http.max_body_size")
				return
			}
			httperr.WritePlain(w, http.StatusBadRequest, errcode.MalformedBody, "request body is not valid "+coding+": "+err.Error())
			return
		}
		defer release()
		r.Body = body
		r.ContentLength = -1 // the decoded size is not known before reading
		r.Header.Del("Content-Encoding")
		next.ServeHTTP(w, r)
	})
}

// requestCoding is the request's one content coding, lower-cased, "" for none ; identity counts as none. ok is
// false for an unsupported coding or more than one.
func requestCoding(r *http.Request) (coding string, ok bool) {
	for _, line := range r.Header.Values("Content-Encoding") {
		for c := range strings.SplitSeq(line, ",") {
			c = strings.ToLower(strings.TrimSpace(c))
			switch c {
			case "", "identity":
				continue
			case "x-gzip":
				c = "gzip"
			case "gzip", "deflate", "zstd":
			default:
				return "", false
			}
			if coding != "" {
				return "", false
			}
			coding = c
		}
	}
	return coding, true
}

// decoder reads src through a pooled decoder for coding ; release returns it to its pool once the body is done with.
// gzip and deflate read their header here, so a body that is not of its coding fails before any handler runs.
func decoder(coding string, src io.Reader) (io.ReadCloser, func(), error) {
	switch coding {
	case "gzip":
		zr, _ := gzipReaders.Get().(*gzip.Reader)
		var err error
		if zr == nil {
			zr, err = gzip.NewReader(src)
		} else {
			err = zr.Reset(src)
		}
		if err != nil {
			return nil, nil, err
		}
		return io.NopCloser(zr), func() { gzipReaders.Put(zr) }, nil
	case "deflate":
		var zr io.ReadCloser
		var err error
		if pooled, _ := zlibReaders.Get().(io.ReadCloser); pooled != nil {
			zr, err = pooled, pooled.(zlib.Resetter).Reset(src, nil)
		} else {
			zr, err = zlib.NewReader(src)
		}
		if err != nil {
			return nil, nil, err
		}
		return io.NopCloser(zr), func() { zlibReaders.Put(zr) }, nil
	default: // zstd
		zr := zstdReaders.Get().(*zstd.Decoder)
		if err := zr.Reset(src); err != nil {
			zstdReaders.Put(zr)
			return nil, nil, err
		}
		return io.NopCloser(zr), func() { _ = zr.Reset(nil); zstdReaders.Put(zr) }, nil
	}
}
