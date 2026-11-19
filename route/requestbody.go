package route

// Route request bodies : multipart/form-data, raw binary POST, and the 415 shape-mismatch rules.

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"strings"

	"github.com/rel-server/rel/cborenc"
	"github.com/rel-server/rel/errcode"
	"github.com/rel-server/rel/httperr"
	"github.com/rel-server/rel/query"
	"github.com/samber/oops"
)

// requestPart describes one multipart part : parts[i] describes files[i].
type requestPart struct {
	// Name is Content-Disposition name= ; nil when absent, including for a raw binary POST's pseudo-part.
	Name *string `json:"name"`
	// Filename is Content-Disposition filename= ; nil when this part
	// isn't a file.
	Filename *string `json:"filename"`
	// ContentType is nil, not "", when the part had no header at all.
	ContentType *string `json:"content_type"`
	// SniffedContentType is detected from the part's bytes ; omitted until they are read, so absent in
	// stream_upload's first call.
	SniffedContentType *string `json:"sniffed_content_type,omitempty"`
	// Headers is never nil — an empty map marshals as "{}".
	Headers map[string][]string `json:"headers"`
}

// requestBodyError carries its own status (400/413/415).
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

// tooLargeIfContentLengthExceeds rejects an over-limit Content-Length
// before reading ; MaxBytesReader alone only catches it after limit+1 bytes.
// configKey names the limit in the error message (e.g. "http.max_body_size").
func tooLargeIfContentLengthExceeds(r *http.Request, maxBodySize int64, configKey string) error {
	if r.ContentLength > maxBodySize {
		return tooLargeBody("request body exceeds " + configKey)
	}
	return nil
}

// writeRequestBodyError : *requestBodyError carries its own status, *badBodyError is 400, anything else is 500.
func writeRequestBodyError(ctx context.Context, w http.ResponseWriter, err error) {
	if rbe, ok := errors.AsType[*requestBodyError](err); ok {
		httperr.WritePlain(w, rbe.status, rbe.code, rbe.message)
		return
	}
	if bbe, ok := errors.AsType[*badBodyError](err); ok {
		httperr.WritePlain(w, http.StatusBadRequest, errcode.MalformedBody, bbe.Error())
		return
	}
	httperr.WriteServer(ctx, w, http.StatusInternalServerError, errcode.Internal, "reading request body", err)
}

// resolvedRequestBody is what builds the request's body/parts. SingleBytes is only set for an AcceptsBytes route.
type resolvedRequestBody struct {
	BodyJSON    json.RawMessage
	SingleBytes []byte
	Files       [][]byte
	Parts       []requestPart
	// SniffedContentType is "" for an empty or multipart body ; multipart sniffing is per part.
	SniffedContentType string
}

// resolveRequestBody reads r.Body, bounded by maxBodySize, according to the route's bytes argument and Content-Type.
func resolveRequestBody(w http.ResponseWriter, r *http.Request, route Route, maxBodySize int64, maxPartCount int) (resolvedRequestBody, error) {
	contentTypeHeader := r.Header.Get("Content-Type")

	if err := tooLargeIfContentLengthExceeds(r, maxBodySize, "http.max_body_size"); err != nil {
		return resolvedRequestBody{}, err
	}

	limited := http.MaxBytesReader(w, r.Body, maxBodySize)

	mt, params, mtErr := mime.ParseMediaType(contentTypeHeader)
	if mtErr != nil && strings.HasPrefix(strings.ToLower(strings.TrimSpace(contentTypeHeader)), "multipart/") {
		// Looks meant to be multipart (bad boundary param, say) : a
		// malformed request, not a shape mismatch.
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
	return finishSingleBody(route, contentTypeHeader, body)
}

// finishMultipartBody : multipart can only fill a bytea[] argument ; files sent to any other route are a 415.
func finishMultipartBody(route Route, files [][]byte, parts []requestPart) (resolvedRequestBody, error) {
	if !route.AcceptsBytesArray {
		if len(files) > 0 {
			return resolvedRequestBody{}, unsupportedMediaType("route does not accept file uploads (no bytea[] parameter declared)")
		}
		return resolvedRequestBody{BodyJSON: json.RawMessage("null"), Files: [][]byte{}, Parts: nil}, nil
	}
	return resolvedRequestBody{
		BodyJSON: json.RawMessage("null"),
		Files:    files,
		Parts:    parts,
	}, nil
}

// finishSingleBody decodes a non-multipart body into body, or into the bytea argument. A bytea[] route only takes
// bytes through multipart.
func finishSingleBody(route Route, contentTypeHeader string, body []byte) (resolvedRequestBody, error) {
	if route.CBORArg != "" {
		return finishCBORBody(contentTypeHeader, body)
	}
	if !route.AcceptsBytes && !route.AcceptsBytesArray {
		bodyJSON, err := encodeBody(contentTypeHeader, body, false)
		if err != nil {
			return resolvedRequestBody{}, err
		}
		return resolvedRequestBody{BodyJSON: bodyJSON, Files: [][]byte{}, Parts: nil, SniffedContentType: sniffedContentTypeOf(body)}, nil
	}

	// A zero-byte body yields an empty/absent payload, not a 415 — a route
	// requiring bytes checks for that itself.
	if len(body) == 0 {
		if route.AcceptsBytes {
			return resolvedRequestBody{BodyJSON: json.RawMessage("null"), SingleBytes: []byte{}, Parts: nil}, nil
		}
		return resolvedRequestBody{BodyJSON: json.RawMessage("null"), Files: [][]byte{}, Parts: nil}, nil
	}

	if route.AcceptsBytesArray {
		return resolvedRequestBody{}, unsupportedMediaType("route only accepts multipart file uploads (bytea[] parameter)")
	}

	mt := mediaTypeOf(contentTypeHeader)
	if mt == "application/json" || strings.HasSuffix(mt, "+json") ||
		strings.HasPrefix(mt, "text/") || mt == "application/x-www-form-urlencoded" {
		// This content-type would fill body, leaving nothing for the bytea argument.
		return resolvedRequestBody{}, unsupportedMediaType("route only accepts a raw byte body, but content_type " + contentTypeHeader + " has no separate byte payload to deliver")
	}

	return resolvedRequestBody{
		BodyJSON:           json.RawMessage("null"),
		SingleBytes:        body,
		Parts:              nil,
		SniffedContentType: sniffedContentTypeOf(body),
	}, nil
}

// finishCBORBody gives a CBOR argument the whole body, one CBOR item without structures. An empty body is NULL ; a
// body of any other media type has no CBOR item to give, JSON included.
func finishCBORBody(contentTypeHeader string, body []byte) (resolvedRequestBody, error) {
	none := resolvedRequestBody{BodyJSON: json.RawMessage("null"), Files: [][]byte{}}
	if len(body) == 0 {
		return none, nil
	}
	if mt := mediaTypeOf(contentTypeHeader); mt != cborenc.MediaType && !strings.HasSuffix(mt, "+cbor") {
		return resolvedRequestBody{}, unsupportedMediaType("route only accepts a CBOR body (application/cbor), not " + contentTypeHeader)
	}
	item, err := query.SpliceCBOR(body)
	if err != nil {
		return resolvedRequestBody{}, &badBodyError{err: oops.Wrapf(err, "request body is not CBOR rel can read")}
	}
	none.SingleBytes = item
	return none, nil
}

// sniffedContentTypeOf is "" for an empty body.
func sniffedContentTypeOf(body []byte) string {
	if len(body) == 0 {
		return ""
	}
	return http.DetectContentType(body)
}

// synthesizedPseudoPart : name/filename are always null for a raw binary
// POST ; content_type/headers are the request's own.
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

// parseMultipart enforces maxPartCount as it reads ; byte size is bounded by the caller's MaxBytesReader.
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
		part := requestPartFrom(p)
		sniffed := http.DetectContentType(data)
		part.SniffedContentType = &sniffed
		parts = append(parts, part)
	}
	return files, parts, nil
}

// requestPartFrom builds a requestPart from an already-read multipart.Part
// (p.Header stays populated after Close/EOF).
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
