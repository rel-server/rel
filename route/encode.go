package route

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"mime"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/bytedance/sonic"

	"github.com/rel-server/rel/cborenc"
	"github.com/rel-server/rel/config"
	"github.com/rel-server/rel/errcode"
	"github.com/rel-server/rel/httperr"
	jwtpkg "github.com/rel-server/rel/jwt"
	"github.com/rel-server/rel/logging"
	"github.com/rel-server/rel/query"
	"github.com/rel-server/rel/querystring"
	"github.com/rel-server/rel/static"
	"github.com/rel-server/rel/websec"
	"github.com/samber/oops"
)

// relHttpRequestPayload is the request object a route function receives. Cookies is {[name]: string}, since a
// browser only sends name=value.
type relHttpRequestPayload struct {
	Method      string              `json:"method"`
	URI         string              `json:"uri"`
	Headers     map[string][]string `json:"headers"`
	ContentType string              `json:"content_type"`
	// SniffedContentType is detected from the body's bytes ; omitted for an empty or multipart body.
	SniffedContentType string `json:"sniffed_content_type,omitempty"`
	// Body's shape follows ContentType (see encodeBody).
	Body    json.RawMessage   `json:"body"`
	Cookies map[string]string `json:"cookies"`
	Jwt     jwtpkg.Claims     `json:"jwt"`
	// Query is the query string decoded through the structural layer only.
	Query any `json:"query"`
	// CspNonce is the nonce injected into the response's CSP.
	CspNonce string `json:"csp_nonce"`
	// Parts is the multipart part metadata ; nil for a non-multipart request.
	Parts []requestPart `json:"parts,omitempty"`
	// Static is what would be served at this request path.
	Static *staticInfoPayload `json:"static,omitempty"`
	// Context is merged in by middleware ; nil if none set one.
	Context json.RawMessage `json:"context,omitempty"`
	// Upload is {part} on stream_upload's first call, {part, size} on the second ; nil for other routes.
	Upload json.RawMessage `json:"upload,omitempty"`
}

// staticInfoPayload is request.static.
type staticInfoPayload struct {
	Exists     bool   `json:"exists"`
	Size       int64  `json:"size,omitempty"`
	ModifiedAt string `json:"modified_at,omitempty"`
}

// badBodyError marks a malformed request body : a 400.
type badBodyError struct{ err error }

func (e *badBodyError) Error() string { return e.err.Error() }
func (e *badBodyError) Unwrap() error { return e.err }

// mediaTypeOf extracts the lower-cased bare media type, falling back to a
// manual split on ';' when mime.ParseMediaType rejects it.
func mediaTypeOf(contentType string) string {
	mt, _, err := mime.ParseMediaType(contentType)
	if err == nil {
		return mt
	}
	if idx := strings.IndexByte(contentType, ';'); idx >= 0 {
		contentType = contentType[:idx]
	}
	return strings.ToLower(strings.TrimSpace(contentType))
}

// encodeBody decodes body according to its content type ; hasFiles forces JSON null, the payload going through
// the files argument instead.
func encodeBody(contentType string, body []byte, hasFiles bool) (json.RawMessage, error) {
	if hasFiles || len(body) == 0 {
		return json.RawMessage("null"), nil
	}

	mt := mediaTypeOf(contentType)
	switch {
	case mt == "application/json" || strings.HasSuffix(mt, "+json"):
		if !sonic.Valid(body) {
			return nil, &badBodyError{err: oops.With("content_type", contentType).Errorf("request body is not valid JSON")}
		}
		return json.RawMessage(body), nil
	case mt == "application/cbor" || strings.HasSuffix(mt, "+cbor"):
		j, err := query.CBORBodyJSON(body)
		if err != nil {
			return nil, &badBodyError{err: oops.With("content_type", contentType).Wrapf(err, "request body is not CBOR rel can read")}
		}
		return json.RawMessage(j), nil
	case strings.HasPrefix(mt, "text/"):
		return sonic.Marshal(string(body))
	case mt == "application/x-www-form-urlencoded":
		decoded, err := querystring.DecodeStructural(string(body))
		if err != nil {
			return nil, &badBodyError{err: err}
		}
		return sonic.Marshal(decoded)
	default:
		return sonic.Marshal(base64.StdEncoding.EncodeToString(body))
	}
}

// badQueryError marks a request whose query string failed to decode : a 400.
type badQueryError struct{ err error }

func (e *badQueryError) Error() string { return e.err.Error() }
func (e *badQueryError) Unwrap() error { return e.err }

// buildRelHttpRequest encodes r as the request object ; bodyJSON is already encoded by encodeBody. Returns a
// *badQueryError for an undecodable query string.
func buildRelHttpRequest(r *http.Request, bodyJSON json.RawMessage, verified bool, claims jwtpkg.Claims, static *staticInfoPayload, parts []requestPart, reqContext, upload json.RawMessage, sniffedContentType string) ([]byte, error) {
	cookies := map[string]string{}
	for _, c := range r.Cookies() {
		cookies[c.Name] = c.Value
	}
	var jwtVal jwtpkg.Claims
	if verified {
		jwtVal = claims
	}
	queryVal, err := querystring.DecodeQueryField(r.URL.RawQuery)
	if err != nil {
		return nil, &badQueryError{err}
	}
	payload := relHttpRequestPayload{
		Method:             r.Method,
		URI:                r.URL.String(),
		Headers:            map[string][]string(r.Header),
		ContentType:        r.Header.Get("Content-Type"),
		Body:               bodyJSON,
		Cookies:            cookies,
		Jwt:                jwtVal,
		Query:              queryVal,
		CspNonce:           websec.NonceFromContext(r.Context()),
		Parts:              parts,
		Static:             static,
		Context:            reqContext,
		Upload:             upload,
		SniffedContentType: sniffedContentType,
	}
	return sonic.Marshal(payload)
}

// relHttpResponsePayload is the response object a function returns.
type relHttpResponsePayload struct {
	Status      int                        `json:"status"`
	ContentType string                     `json:"content_type"`
	Content     json.RawMessage            `json:"content"`
	Headers     map[string]json.RawMessage `json:"headers"`
	Cookies     map[string]json.RawMessage `json:"cookies"`
	Jwt         json.RawMessage            `json:"jwt"`
	JwtAttrs    *jwtAttrsPayload           `json:"jwt_attrs"`
	// A non-empty Template renders in place of Content.
	Template     string          `json:"template"`
	TemplateData json.RawMessage `json:"template_data"`
	// Csp overrides the configured CSP for this response.
	Csp string `json:"csp"`
	// StaticFile (static_file) is served instead of Content when set.
	StaticFile string `json:"static_file"`
}

type jwtAttrsPayload struct {
	SameSite string `json:"samesite"`
	MaxAge   *int   `json:"maxage"`
}

// WriteRelHttpResponse decodes raw, a function's response object, and writes it. functionIdent is that function's
// qualified name, matched against http.functions.allowed_auth before honouring its jwt.
func WriteRelHttpResponse(w http.ResponseWriter, r *http.Request, cfg *config.Config, functionIdent string, raw []byte, templates *TemplateSet) {
	var resp relHttpResponsePayload
	if err := sonic.Unmarshal(raw, &resp); err != nil {
		httperr.WriteServer(r.Context(), w, http.StatusInternalServerError, errcode.Internal, "decoding function response", err)
		return
	}
	status := applyResponseSideEffects(w, r, cfg, functionIdent, resp)

	if resp.Template != "" {
		writeTemplateResponse(w, r, cfg, templates, resp.Template, resp.TemplateData, resp.ContentType, status)
		return
	}

	if resp.ContentType != "" {
		w.Header().Set("Content-Type", resp.ContentType)
	}
	w.WriteHeader(status)
	_, _ = w.Write(contentBytes(resp.Content, resp.ContentType))
}

// applyResponseSideEffects applies headers/cookies/jwt/csp and returns the resolved status.
func applyResponseSideEffects(w http.ResponseWriter, r *http.Request, cfg *config.Config, functionIdent string, resp relHttpResponsePayload) int {
	for key, rawVal := range resp.Headers {
		for _, v := range headerValues(rawVal) {
			w.Header().Add(key, v)
		}
	}
	for name, rawVal := range resp.Cookies {
		http.SetCookie(w, decodeOutboundCookie(cfg, name, rawVal))
	}
	handleResponseJwt(w, cfg, functionIdent, resp)

	if resp.Csp != "" {
		nonce := websec.NonceFromContext(r.Context())
		w.Header().Set("Content-Security-Policy", websec.Policy(cfg.Http.Csp, resp.Csp, nonce))
	}

	if resp.Status == 0 {
		return http.StatusOK
	}
	return resp.Status
}

// writeSingleReturnResponse writes a non-full-control route's value as the body, or as Data of route.Template.
// Only accumulated, from pass-through middleware, can add side effects.
func writeSingleReturnResponse(w http.ResponseWriter, r *http.Request, cfg *config.Config, functionIdent string, route Route, accumulated relHttpResponsePayload, raw []byte, isCBOR bool, templates *TemplateSet) {
	applyResponseSideEffects(w, r, cfg, functionIdent, accumulated)

	if route.Template != "" {
		writeTemplateResponse(w, r, cfg, templates, route.Template, templateDataFromSingleReturn(raw, route.ContentType), route.ContentType, http.StatusOK)
		return
	}
	contentType := route.ContentType
	if isCBOR { // already CBOR, in the value's own types
		w.Header().Add("Vary", "Accept")
		contentType = cborenc.MediaType
	} else {
		raw, contentType = negotiateBody(w, r, contentType, raw)
	}
	if contentType != "" {
		w.Header().Set("Content-Type", contentType)
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(raw)
}

// negotiateBody answers a JSON body in CBOR when the request Accepts it more than JSON.
// The body is converted as a whole : a route function returns its value at once, there is nothing to stream.
// Whatever cannot be converted stays JSON ; any other media type is left alone.
func negotiateBody(w http.ResponseWriter, r *http.Request, contentType string, raw []byte) ([]byte, string) {
	if mediaTypeOf(contentType) != "application/json" {
		return raw, contentType
	}
	w.Header().Add("Vary", "Accept")
	if len(raw) == 0 || !cborenc.Accepts(r.Header.Get("Accept")) {
		return raw, contentType
	}
	out, err := cborenc.AppendJSON(nil, raw)
	if err != nil {
		logging.FromContext(r.Context()).Warn("route: a JSON body has no CBOR form, sent as JSON", "error", err.Error())
		return raw, contentType
	}
	return out, cborenc.MediaType
}

// writeFullControlResponse decodes envelopeRaw (the first OUT column) and renders contentRaw (the second).
// accumulated holds middleware side effects, which the envelope's own values override per key.
func writeFullControlResponse(w http.ResponseWriter, r *http.Request, cfg *config.Config, functionIdent string, route Route, accumulated relHttpResponsePayload, envelopeRaw, contentRaw []byte, templates *TemplateSet, staticSrv *static.Server) {
	var resp relHttpResponsePayload
	// A SQL NULL envelope is treated like {}.
	if len(envelopeRaw) > 0 {
		if err := sonic.Unmarshal(envelopeRaw, &resp); err != nil {
			httperr.WriteServer(r.Context(), w, http.StatusInternalServerError, errcode.Internal, "decoding function response", err)
			return
		}
	}
	renderFullControlResponse(w, r, cfg, functionIdent, route, accumulated, resp, contentRaw, templates, staticSrv)
}

// renderFullControlResponse is writeFullControlResponse's already-decoded half, shared with a terminating middleware.
func renderFullControlResponse(w http.ResponseWriter, r *http.Request, cfg *config.Config, functionIdent string, route Route, accumulated, resp relHttpResponsePayload, contentRaw []byte, templates *TemplateSet, staticSrv *static.Server) {
	mergeSideEffects(&accumulated, resp)
	accumulated.Status = resp.Status
	accumulated.ContentType = resp.ContentType
	accumulated.Template = resp.Template
	accumulated.StaticFile = resp.StaticFile

	status := applyResponseSideEffects(w, r, cfg, functionIdent, accumulated)

	// static_file serves that file instead of content, before the null-content 404 below.
	if accumulated.StaticFile != "" {
		relPath := strings.TrimPrefix(accumulated.StaticFile, "/")
		if serveStaticFileOrJet(w, r, cfg, staticSrv, templates, relPath) {
			return
		}
		http.NotFound(w, r)
		return
	}

	contentType := accumulated.ContentType
	if contentType == "" {
		contentType = route.ContentType
	}

	template := accumulated.Template
	if template == "" {
		template = route.Template
	}
	if template != "" {
		writeTemplateResponse(w, r, cfg, templates, template, templateDataFromSingleReturn(contentRaw, contentType), contentType, status)
		return
	}

	// Neither content, static_file nor status is a 404. With an explicit status (a middleware's {status: 401}),
	// empty content is sent as is.
	if accumulated.Status == 0 && (len(bytes.TrimSpace(contentRaw)) == 0 || string(bytes.TrimSpace(contentRaw)) == "null") {
		http.NotFound(w, r)
		return
	}

	contentRaw, contentType = negotiateBody(w, r, contentType, contentRaw)
	if contentType != "" {
		w.Header().Set("Content-Type", contentType)
	}
	w.WriteHeader(status)
	_, _ = w.Write(contentRaw)
}

// headerValues decodes a response headers value : a single string, or an array of strings.
func headerValues(raw json.RawMessage) []string {
	var single string
	if err := sonic.Unmarshal(raw, &single); err == nil {
		return []string{single}
	}
	var multi []string
	_ = sonic.Unmarshal(raw, &multi)
	return multi
}

// outboundCookiePayload is the object form of a response cookie.
type outboundCookiePayload struct {
	Value    string  `json:"value"`
	HttpOnly *bool   `json:"httponly"`
	Secure   *bool   `json:"secure"`
	SameSite *string `json:"samesite"`
	MaxAge   *int    `json:"maxage"`
}

// decodeOutboundCookie applies the cookie defaults under a string or object cookie ; null clears it.
func decodeOutboundCookie(cfg *config.Config, name string, raw json.RawMessage) *http.Cookie {
	c := &http.Cookie{
		Name:     name,
		Path:     "/",
		Secure:   true,
		HttpOnly: true,
		SameSite: routeSameSite("Lax"),
		MaxAge:   cfg.Http.CookiesMaxAge,
	}

	// Checked before the string unmarshal, which would take null as "".
	if string(bytes.TrimSpace(raw)) == "null" {
		c.Value = ""
		c.MaxAge = -1
		return c
	}

	var asString string
	if err := sonic.Unmarshal(raw, &asString); err == nil {
		c.Value = asString
		return c
	}

	var obj outboundCookiePayload
	if err := sonic.Unmarshal(raw, &obj); err != nil {
		return c
	}
	c.Value = obj.Value
	if obj.Secure != nil {
		c.Secure = *obj.Secure
	}
	if obj.HttpOnly != nil {
		c.HttpOnly = *obj.HttpOnly
	}
	if obj.SameSite != nil {
		c.SameSite = routeSameSite(*obj.SameSite)
	}
	if obj.MaxAge != nil {
		c.MaxAge = *obj.MaxAge
	}
	return c
}

// routeSameSite mirrors jwt's unexported sameSite mapping.
func routeSameSite(s string) http.SameSite {
	switch s {
	case "Strict":
		return http.SameSiteStrictMode
	case "None":
		return http.SameSiteNoneMode
	default:
		return http.SameSiteLaxMode
	}
}

// contentBytes writes a JSON string raw/unquoted when content_type isn't
// JSON-flavored ; anything else is written as its raw JSON representation.
func contentBytes(content json.RawMessage, contentType string) []byte {
	trimmed := bytes.TrimSpace(content)
	if len(trimmed) >= 2 && trimmed[0] == '"' && !strings.Contains(contentType, "json") {
		var s string
		if err := sonic.Unmarshal(trimmed, &s); err == nil {
			return []byte(s)
		}
	}
	return trimmed
}

// authFunctionAllowed is http.functions.allowed_auth's gate ; empty regexp = unrestricted.
func authFunctionAllowed(cfg *config.Config, functionIdent string) bool {
	if cfg.Http.Functions.AllowedAuth == "" {
		return true
	}
	re, err := regexp.Compile(cfg.Http.Functions.AllowedAuth)
	if err != nil {
		return false
	}
	return re.MatchString(functionIdent)
}

// handleResponseJwt : JSON null clears the session, {role, ...} mints one ; ignored for functions outside
// http.functions.allowed_auth.
func handleResponseJwt(w http.ResponseWriter, cfg *config.Config, functionIdent string, resp relHttpResponsePayload) {
	if len(resp.Jwt) == 0 {
		return
	}
	if !authFunctionAllowed(cfg, functionIdent) {
		return
	}
	if string(bytes.TrimSpace(resp.Jwt)) == "null" {
		http.SetCookie(w, jwtpkg.ClearCookie(cfg.Jwt))
		return
	}

	var obj map[string]any
	if err := sonic.Unmarshal(resp.Jwt, &obj); err != nil {
		return
	}
	role, _ := obj["role"].(string)
	if role == "" {
		return
	}
	delete(obj, "role")

	maxage := cfg.Jwt.MaxAge
	samesite := ""
	if resp.JwtAttrs != nil {
		if resp.JwtAttrs.MaxAge != nil {
			maxage = *resp.JwtAttrs.MaxAge
		}
		samesite = resp.JwtAttrs.SameSite
	}

	claims := jwtpkg.Mint(cfg.Jwt, role, time.Now(), maxage, obj)
	token, err := jwtpkg.Sign(cfg.Jwt, claims)
	if err != nil {
		return
	}
	http.SetCookie(w, jwtpkg.CookieValue(cfg.Jwt, token, claims, samesite))
}
