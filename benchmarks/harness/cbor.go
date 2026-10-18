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

package harness

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"reflect"
	"time"

	"github.com/fxamacker/cbor/v2"
	"github.com/samber/oops"
)

// RelCBOR is rel again, spoken to in CBOR : the same container as Rel, with the request body sent as CBOR and
// `Accept: application/cbor`. Scenarios define their requests as JSON once ; the client converts them.
const RelCBOR Server = "rel-cbor"

// Service is the docker compose service that runs the server.
func (s Server) Service() string {
	if s == RelCBOR {
		return string(Rel)
	}
	return string(s)
}

// NewServerClient is NewClient for the server srv, which may need to speak a different format.
func NewServerClient(srv Server) *Client {
	c := NewClient(URL(srv))
	c.cbor = srv == RelCBOR
	return c
}

// toCBORRequest turns a JSON /rel request into its CBOR form. The conversion is on the load generator's side, which
// has cores of its own ; it costs microseconds against milliseconds of server time.
func toCBORRequest(r Request) (Request, error) {
	h := r.Header.Clone()
	if h == nil {
		h = http.Header{}
	}
	h.Set("Accept", "application/cbor")
	if r.Body == nil {
		r.Header = h
		return r, nil
	}
	dec := json.NewDecoder(bytes.NewReader(r.Body))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return r, oops.Wrapf(err, "request body is not JSON")
	}
	body, err := cbor.Marshal(numbers(v))
	if err != nil {
		return r, oops.Wrapf(err, "encoding the request as CBOR")
	}
	h.Set("Content-Type", "application/cbor")
	r.Header, r.Body = h, body
	return r, nil
}

// numbers replaces json.Number by an integer or a float, so CBOR gets what JSON meant.
func numbers(v any) any {
	switch t := v.(type) {
	case json.Number:
		if i, err := t.Int64(); err == nil {
			return i
		}
		f, _ := t.Float64()
		return f
	case map[string]any:
		for k, x := range t {
			t[k] = numbers(x)
		}
	case []any:
		for i, x := range t {
			t[i] = numbers(x)
		}
	}
	return v
}

var cborDecode = func() cbor.DecMode {
	m, err := cbor.DecOptions{DefaultMapType: reflect.TypeOf(map[string]any{})}.DecMode()
	if err != nil {
		panic(err)
	}
	return m
}()

// CBORToJSON turns a CBOR response into the JSON a normalizer expects : the types JSON lacks become the text or
// number the JSON response gives for them.
func CBORToJSON(body []byte) ([]byte, error) {
	var v any
	if err := cborDecode.Unmarshal(body, &v); err != nil {
		return nil, oops.Wrapf(err, "decoding CBOR response")
	}
	return json.Marshal(jsonable(v))
}

func jsonable(v any) any {
	switch t := v.(type) {
	case map[string]any:
		for k, x := range t {
			t[k] = jsonable(x)
		}
	case []any:
		for i, x := range t {
			t[i] = jsonable(x)
		}
	case cbor.Tag:
		switch t.Number {
		case 4: // [exponent, mantissa]
			if p, ok := t.Content.([]any); ok && len(p) == 2 {
				return json.Number(fmt.Sprintf("%ve%v", p[1], p[0]))
			}
		case 37:
			if b, ok := t.Content.([]byte); ok && len(b) == 16 {
				return fmt.Sprintf("%x-%x-%x-%x-%x", b[:4], b[4:6], b[6:8], b[8:10], b[10:])
			}
		case 100:
			if d, ok := t.Content.(int64); ok {
				return time.Unix(d*86400, 0).UTC().Format("2006-01-02")
			} else if u, ok := t.Content.(uint64); ok {
				return time.Unix(int64(u)*86400, 0).UTC().Format("2006-01-02")
			}
		}
		return jsonable(t.Content)
	case time.Time:
		return t.UTC().Format(time.RFC3339Nano)
	case big.Int:
		return json.Number(t.String())
	case *big.Int:
		return json.Number(t.String())
	case []byte:
		return fmt.Sprintf(`\x%x`, t)
	}
	return v
}
