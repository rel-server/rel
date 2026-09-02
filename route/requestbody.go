package route

// This file implements specs/route.md's "## Request bodies" :
// the multipart/form-data and single-raw-binary-POST support for a route
// function declaring the extra "files bytea[]" (and optionally
// "parts_headers jsonb") parameter beyond "req RelHttpRequest". It also
// implements the 415 mismatch rules between a route's declared shape and
// the actual request, and http.max_body_size/http.max_part_count.

import (
	"encoding/json"
	"errors"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"strings"

	"github.com/bytedance/sonic"

	"github.com/ceymard/rel/errcode"
)

// requestPart is ## Request bodies' RequestPart TypeScript interface :
// parts_headers[i] describes files[i].
type requestPart struct {
	// Name is the Content-Disposition name= (the multipart form field
	// key) ; nil for a real multipart part with no name= at all (rare,
	// technically non-conforming multipart/form-data), and always nil for
	// the synthesized pseudo-part a single raw binary POST produces.
	Name *string `json:"name"`
	// Filename is Content-Disposition filename= ; nil when this part
	// isn't a file (e.g. a plain form field), and always nil for the
	// synthesized pseudo-part.
	Filename *string `json:"filename"`
	// ContentType is this part's own Content-Type header ; nil if the
	// part had no Content-Type header at all — NOT the empty string.
	ContentType *string `json:"content_type"`
	// Headers is ALL of this part's own headers, verbatim — nil (JSON
	// null) is never produced here ; an empty map marshals as "{}".
	Headers map[string][]string `json:"headers"`
}

// requestBodyError is a request-body-shape error that resolves directly to
// an HTTP status : 400 (malformed multipart envelope), 413
// (http.max_body_size/http.max_part_count exceeded), or 415 (## Request
// bodies' declared-shape-vs-actual-request mismatch). Kept distinct from
// badQueryError/badBodyError (always 400) since this path can also produce
// 413/415.
type requestBodyError struct {
	status  int
	code    errcode.Code
	message string
}

func (e *requestBodyError) Error() string { return e.message }

func badRequestBody(msg string) error {
	return &requestBodyError{http.StatusBadRequest, errcode.MalformedMultipart, msg}
}
func tooLargeBody(msg string) error {
	return &requestBodyError{http.StatusRequestEntityTooLarge, errcode.BodyTooLarge, msg}
}
func unsupportedMediaType(msg string) error {
	return &requestBodyError{http.StatusUnsupportedMediaType, errcode.UnsupportedMediaType, msg}
}

// tooLargeIfContentLengthExceeds is http.max_body_size's own "before any
// part is buffered in memory, not after" rule : a declared Content-Length
// already over the limit is rejected outright, without reading anything —
// http.MaxBytesReader alone only catches this AFTER reading limit+1 bytes
// (the chunked, no declared Content-Length case, or a lying Content-
// Length), so both checks are needed together. resolveRequestBody and
// upload_handler.go's own body-size enforcement both start with this exact
// check, immediately before wrapping r.Body in http.MaxBytesReader.
func tooLargeIfContentLengthExceeds(r *http.Request, maxBodySize int64) error {
	if r.ContentLength > maxBodySize {
		return tooLargeBody("request body exceeds http.max_body_size")
	}
	return nil
}

// writeRequestBodyError renders any error resolveRequestBody (or
// upload_handler.go's own body-size precheck, which reuses
// tooLargeIfContentLengthExceeds above and so can return the same
// *requestBodyError) can produce : a *requestBodyError carries its own
// status/code/message ; a *badBodyError is always a 400 malformed body ;
// anything else is a generic 500. The three-way dispatch handleRoute used to
// restate inline at its own resolveRequestBody call site.
func writeRequestBodyError(w http.ResponseWriter, err error) {
	if rbe, ok := errors.AsType[*requestBodyError](err); ok {
		writePlainError(w, rbe.status, rbe.code, rbe.message)
		return
	}
	if bbe, ok := errors.AsType[*badBodyError](err); ok {
		writePlainError(w, http.StatusBadRequest, errcode.MalformedBody, bbe.Error())
		return
	}
	writePlainError(w, http.StatusInternalServerError, errcode.Internal, "reading request body")
}

// resolvedRequestBody is everything handleRoute needs, both to build
// RelHttpRequest.body and to invoke a files/parts_headers-aware route :
// Files and PartsHeadersRaw are always non-nil (an empty array, never a SQL
// NULL/JSON null, when there's nothing to report — ## Request bodies is
// explicit that an empty upload is not the same as "no files parameter").
type resolvedRequestBody struct {
	BodyJSON        json.RawMessage
	Files           [][]byte
	PartsHeadersRaw []byte
}

// resolveRequestBody reads r.Body (bounded by maxBodySize) and builds
// everything ## Request / ## Request bodies describe, dispatching on
// route.AcceptsFiles and the request's own Content-Type. w is passed only
// because http.MaxBytesReader's signature requires a ResponseWriter (to set
// Connection: close on overflow) — nothing here writes through it directly.
func resolveRequestBody(w http.ResponseWriter, r *http.Request, route Route, maxBodySize int64, maxPartCount int) (resolvedRequestBody, error) {
	contentTypeHeader := r.Header.Get("Content-Type")

	if err := tooLargeIfContentLengthExceeds(r, maxBodySize); err != nil {
		return resolvedRequestBody{}, err
	}

	limited := http.MaxBytesReader(w, r.Body, maxBodySize)

	mt, params, mtErr := mime.ParseMediaType(contentTypeHeader)
	if mtErr != nil && strings.HasPrefix(strings.ToLower(strings.TrimSpace(contentTypeHeader)), "multipart/") {
		// Looks like it was meant to be multipart (e.g. a missing/malformed
		// boundary param) — a genuinely malformed request, same class as a
		// malformed JSON/form body, not a shape mismatch.
		return resolvedRequestBody{}, badRequestBody("malformed multipart Content-Type: " + mtErr.Error())
	}
	isMultipart := mtErr == nil && strings.HasPrefix(mt, "multipart/")

	if isMultipart {
		files, parts, err := parseMultipart(multipart.NewReader(limited, params["boundary"]), maxPartCount)
		if err != nil {
			return resolvedRequestBody{}, err
		}
		return finishMultipartBody(route, files, parts)
	}

	body, err := io.ReadAll(limited)
	if err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			return resolvedRequestBody{}, tooLargeBody("request body exceeds http.max_body_size")
		}
		return resolvedRequestBody{}, badRequestBody("reading request body: " + err.Error())
	}
	return finishSingleBody(route, r, contentTypeHeader, body)
}

// finishMultipartBody applies ## Request bodies' multipart mismatch rules,
// once files/parts have already been parsed (possibly empty).
func finishMultipartBody(route Route, files [][]byte, parts []requestPart) (resolvedRequestBody, error) {
	if route.AcceptsFiles {
		return resolvedRequestBody{
			BodyJSON:        json.RawMessage("null"),
			Files:           files,
			PartsHeadersRaw: mustMarshalParts(parts),
		}, nil
	}
	// A route NOT declaring files, receiving actual multipart data (>=1
	// part), has nowhere to put it — 415 ; multipart is never silently
	// base64-encoded whole into body as a fallback. Zero parts is treated
	// the same as no body at all, on ANY route (## Request bodies' own
	// explicit carve-out) — falls through to the empty-body return below.
	if len(files) > 0 {
		return resolvedRequestBody{}, unsupportedMediaType("route does not accept file uploads (no files bytea[] parameter declared)")
	}
	return resolvedRequestBody{
		BodyJSON:        json.RawMessage("null"),
		Files:           [][]byte{},
		PartsHeadersRaw: []byte("[]"),
	}, nil
}

// finishSingleBody applies ## Request's body content-type dispatch (for a
// route not declaring files) or ## Request bodies' single-raw-binary-POST
// rule (for a route declaring files) to a non-multipart request body.
func finishSingleBody(route Route, r *http.Request, contentTypeHeader string, body []byte) (resolvedRequestBody, error) {
	if !route.AcceptsFiles {
		bodyJSON, err := encodeBody(contentTypeHeader, body, false)
		if err != nil {
			return resolvedRequestBody{}, err // *badBodyError -> handler maps to 400
		}
		return resolvedRequestBody{BodyJSON: bodyJSON, Files: [][]byte{}, PartsHeadersRaw: []byte("[]")}, nil
	}

	// route.AcceptsFiles : body is ALWAYS null. A request with no body at
	// all (any Content-Type, zero bytes) yields empty files/parts_headers
	// — NOT a 415 ; a route requiring at least one file checks
	// array_length itself.
	if len(body) == 0 {
		return resolvedRequestBody{BodyJSON: json.RawMessage("null"), Files: [][]byte{}, PartsHeadersRaw: []byte("[]")}, nil
	}

	mt := mediaTypeOf(contentTypeHeader)
	if mt == "application/json" || strings.HasSuffix(mt, "+json") ||
		strings.HasPrefix(mt, "text/") || mt == "application/x-www-form-urlencoded" {
		// "body would otherwise have been populated" — no byte payload left
		// over to hand over separately as a file.
		return resolvedRequestBody{}, unsupportedMediaType("route only accepts file uploads, but content_type " + contentTypeHeader + " has no separate byte payload to deliver as files")
	}

	// Anything else : a single raw binary POST, treated as a ONE-ELEMENT
	// files array holding the whole body — with a synthesized pseudo-part
	// if parts_headers was declared.
	partsRaw := []byte("[]")
	if route.AcceptsPartsHeaders {
		partsRaw = mustMarshalParts([]requestPart{synthesizedPseudoPart(r, contentTypeHeader)})
	}
	return resolvedRequestBody{
		BodyJSON:        json.RawMessage("null"),
		Files:           [][]byte{body},
		PartsHeadersRaw: partsRaw,
	}, nil
}

// synthesizedPseudoPart is ## Request bodies' rule for a single,
// non-multipart, raw binary POST : name/filename are always null (there's
// no Content-Disposition to read them from), content_type/headers are the
// REQUEST's own — there's no separate "part" envelope to have its own when
// there was no multipart wrapper to begin with. content_type is nil (not
// "") when the request itself had no Content-Type header, matching
// RequestPart.content_type's own "null, NOT the empty string" contract for
// an absent header.
func synthesizedPseudoPart(r *http.Request, contentTypeHeader string) requestPart {
	var ct *string
	if contentTypeHeader != "" {
		ct = &contentTypeHeader
	}
	return requestPart{
		Name:        nil,
		Filename:    nil,
		ContentType: ct,
		Headers:     map[string][]string(r.Header),
	}
}

// parseMultipart reads every part of mr, enforcing maxPartCount
// incrementally (rejected as soon as the count is exceeded, not after
// fully parsing an oversized part set — ## Request bodies ### Limits).
// Total byte size is already bounded by mr's own underlying reader, which
// the caller wraps in http.MaxBytesReader before constructing mr. Returns
// (always non-nil, possibly empty) files/parts on success.
func parseMultipart(mr *multipart.Reader, maxPartCount int) ([][]byte, []requestPart, error) {
	files := [][]byte{}
	parts := []requestPart{}
	count := 0
	for {
		p, err := mr.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			var maxErr *http.MaxBytesError
			if errors.As(err, &maxErr) {
				return nil, nil, tooLargeBody("request body exceeds http.max_body_size")
			}
			return nil, nil, badRequestBody("malformed multipart body: " + err.Error())
		}
		count++
		if count > maxPartCount {
			return nil, nil, tooLargeBody("request exceeds http.max_part_count")
		}
		data, rerr := io.ReadAll(p)
		_ = p.Close()
		if rerr != nil {
			var maxErr *http.MaxBytesError
			if errors.As(rerr, &maxErr) {
				return nil, nil, tooLargeBody("request body exceeds http.max_body_size")
			}
			return nil, nil, badRequestBody("reading multipart part: " + rerr.Error())
		}
		files = append(files, data)
		parts = append(parts, requestPartFrom(p))
	}
	return files, parts, nil
}

// requestPartFrom builds a requestPart from an already-fully-read
// multipart.Part (p.Header stays populated after Close/EOF — only the body
// reading itself is affected).
func requestPartFrom(p *multipart.Part) requestPart {
	var name, filename, ct *string
	if fn := p.FormName(); fn != "" {
		name = &fn
	}
	if fn := p.FileName(); fn != "" {
		filename = &fn
	}
	if vals, ok := p.Header["Content-Type"]; ok && len(vals) > 0 {
		v := vals[0]
		ct = &v
	}
	return requestPart{
		Name:        name,
		Filename:    filename,
		ContentType: ct,
		Headers:     map[string][]string(p.Header),
	}
}

// mustMarshalParts marshals parts as a JSON array, always "[]" (never
// "null") for an empty/nil slice — ## Request bodies' parts_headers is
// always a JSON array, empty when there's nothing to report.
func mustMarshalParts(parts []requestPart) []byte {
	if len(parts) == 0 {
		return []byte("[]")
	}
	b, err := sonic.Marshal(parts)
	if err != nil {
		// requestPart is a plain, fully JSON-marshalable struct — this
		// cannot fail in practice ; fall back to an empty array rather than
		// panicking on a response-shape guarantee.
		return []byte("[]")
	}
	return b
}
