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

	"github.com/rel-server/rel/config"
	"github.com/rel-server/rel/errcode"
	jwtpkg "github.com/rel-server/rel/jwt"
	"github.com/rel-server/rel/querystring"
	"github.com/rel-server/rel/static"
	"github.com/rel-server/rel/websec"
	"github.com/samber/oops"
)

// relHttpRequestPayload is docs/content/http/requests-responses.md's
// RelHttpRequest, with one deviation : Cookies is {[name]: string}, a
// browser only sends name=value.
type relHttpRequestPayload struct {
	Method      string              `json:"method"`
	URI         string              `json:"uri"`
	Headers     map[string][]string `json:"headers"`
	ContentType string              `json:"content_type"`
	// SniffedContentType is ## Content-type sniffing : rel's own detection
	// of the body's actual bytes, alongside the client-claimed ContentType
	// above — omitted for a body-less request or a multipart one (whose
	// sniffing lives per-Part instead).
	SniffedContentType string `json:"sniffed_content_type,omitempty"`
	// Typed by ContentType (see encodeBody) ; RawMessage since the shape is
	// genuinely polymorphic (JSON value / string / form object / base64 / null).
	Body    json.RawMessage   `json:"body"`
	Cookies map[string]string `json:"cookies"`
	Jwt     jwtpkg.Claims     `json:"jwt"`
	// Query is specs/query-json.md ## /route's query field, decoded
	// through the structural layer only (querystring.DecodeQueryField).
	Query any `json:"query"`
	// CspNonce is docs/content/http/cors-csp.md ## CSP ### Nonce's csp_nonce.
	CspNonce string `json:"csp_nonce"`
	// Parts is specs/new-routes.md ## Function prototype's multipart part
	// metadata, always filled for a multipart request regardless of
	// whether the route declares bytea[] — nil for a non-multipart request.
	Parts []requestPart `json:"parts,omitempty"`
	// Static is specs/new-routes.md ## Static path masking's request.static
	// : what would be served at this exact request path, computed once per
	// request regardless of whether anything ends up masking it.
	Static *staticInfoPayload `json:"static,omitempty"`
	// Context is specs/new-routes.md ## Middleware's request.context :
	// shallow-merged in by any middleware that ran first ; nil if none did.
	Context json.RawMessage `json:"context,omitempty"`
	// Upload is specs/new-routes.md ## `stream_upload`'s per-call upload
	// metadata ({part} on the first call, {part, size} on the second) ;
	// nil for any route that isn't stream_upload.
	Upload json.RawMessage `json:"upload,omitempty"`
}

// staticInfoPayload is static.Info's own HTTP-facing shape.
type staticInfoPayload struct {
	Exists     bool   `json:"exists"`
	Size       int64  `json:"size,omitempty"`
	ModifiedAt string `json:"modified_at,omitempty"`
}

// badBodyError marks a malformed request body (specs/route.md ## Request)
// — a 400, same class as badQueryError.
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

// encodeBody implements docs/content/http/requests-responses.md's body
// dispatch (see specs/route.md ## Request for the malformed-body/charset
// edge cases) ; hasFiles forces JSON null since the payload goes through
// files/parts_headers instead.
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

// badQueryError marks a request whose query string failed to decode — a
// 400 ; handleRoute type-switches on this to pick the right status.
type badQueryError struct{ err error }

func (e *badQueryError) Error() string { return e.err.Error() }
func (e *badQueryError) Unwrap() error { return e.err }

// buildRelHttpRequest encodes r as docs/content/http/requests-responses.md's RelHttpRequest ; bodyJSON is
// pre-encoded (see encodeBody) since the handler needs the route's hasFiles.
// static/parts/reqContext are specs/new-routes.md additions — static is nil
// when there's no static.Server at all ; parts is nil for a non-multipart
// request ; reqContext is nil until a middleware has actually merged
// something in (## Middleware).
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

// relHttpResponsePayload is docs/content/http/requests-responses.md's RelHttpResponse.
// Content/Headers/Cookies/Jwt are json.RawMessage since each is polymorphic.
type relHttpResponsePayload struct {
	Status      int                        `json:"status"`
	ContentType string                     `json:"content_type"`
	Content     json.RawMessage            `json:"content"`
	Headers     map[string]json.RawMessage `json:"headers"`
	Cookies     map[string]json.RawMessage `json:"cookies"`
	Jwt         json.RawMessage            `json:"jwt"`
	JwtAttrs    *jwtAttrsPayload           `json:"jwt_attrs"`
	// Template/TemplateData are specs/http-content.md ## Templates : a
	// non-empty Template renders in place of Content as the response body.
	Template     string          `json:"template"`
	TemplateData json.RawMessage `json:"template_data"`
	// Csp is docs/content/http/cors-csp.md ## CSP ### Per-response override.
	Csp string `json:"csp"`
	// StaticFile is specs/new-routes.md ## Static path masking's
	// static_file : served instead of Content when set.
	StaticFile string `json:"static_file"`
}

type jwtAttrsPayload struct {
	SameSite string `json:"samesite"`
	MaxAge   *int   `json:"maxage"`
}

// WriteRelHttpResponse decodes raw and writes the response ; headers/
// cookies are set before status/body, since WriteHeader freezes headers.
// functionIdent is the fully qualified, escaped identifier of the
// Postgres function that produced raw — only used to gate
// http.functions.allowed_auth (authFunctionAllowed) — so any caller
// invoking an arbitrary function this way (route/upload_handler.go's own
// route dispatch, or sso's SSO-callback dispatch) can reuse this
// unchanged, not just a discovered Route.
func WriteRelHttpResponse(w http.ResponseWriter, r *http.Request, cfg *config.Config, functionIdent string, raw []byte, templates *TemplateSet) {
	var resp relHttpResponsePayload
	if err := sonic.Unmarshal(raw, &resp); err != nil {
		writeServerError(r.Context(), w, http.StatusInternalServerError, errcode.Internal, "decoding function response", err)
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

// applyResponseSideEffects applies headers/cookies/jwt/csp — every
// RelHttpResponse field that ISN'T the response body itself — and returns
// the resolved status. Shared between the single-jsonb-envelope case above
// (also sso's own callback response, via WriteRelHttpResponse) and a
// full-control route's envelope (writeFullControlResponse), since both
// carry the exact same side-effecting fields.
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

// writeSingleReturnResponse writes a non-full-control route's single return
// value directly as the body — no envelope, no cookies/headers/jwt/status
// override possible without the two-OUT-column shape, EXCEPT whatever
// accumulated already carries in from a pass-through middleware ahead of
// this route (## Middleware : a middleware's side-effecting fields apply
// regardless of the route's own shape) — a zero-value accumulated when no
// middleware ran leaves this response exactly as before middleware
// existed. raw is exactly what Postgres returned : already the real
// bytes/text/JSON, never itself JSON-encoded the way a single-jsonb-
// envelope's "content" key is, so it's written verbatim rather than
// through contentBytes. route.Template (the declared default) renders it
// as Data when set — specs/new-routes.md ## Templates : "the other return
// type is then used as the Data" applies here too, there being no
// per-response override without full control.
func writeSingleReturnResponse(w http.ResponseWriter, r *http.Request, cfg *config.Config, functionIdent string, route Route, accumulated relHttpResponsePayload, raw []byte, templates *TemplateSet) {
	applyResponseSideEffects(w, r, cfg, functionIdent, accumulated)

	if route.Template != "" {
		writeTemplateResponse(w, r, cfg, templates, route.Template, templateDataFromSingleReturn(raw, route.ContentType), route.ContentType, http.StatusOK)
		return
	}
	if route.ContentType != "" {
		w.Header().Set("Content-Type", route.ContentType)
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(raw)
}

// writeFullControlResponse decodes envelopeRaw (the first OUT column) and
// renders contentRaw (the second) as the body, per specs/new-routes.md
// ## Function prototype's two-OUT-column shape. accumulated carries any
// cookies/headers/jwt/jwt_attrs/csp already merged in from middleware that
// ran ahead of this response (## Middleware : "on a per-key basis, a later
// middleware's or the route function's own value ... wins") — pass a
// zero-value relHttpResponsePayload{} when no middleware ran, which leaves
// this response's own values as the only ones present, unchanged from
// before middleware existed.
func writeFullControlResponse(w http.ResponseWriter, r *http.Request, cfg *config.Config, functionIdent string, route Route, accumulated relHttpResponsePayload, envelopeRaw, contentRaw []byte, templates *TemplateSet, staticSrv *static.Server) {
	var resp relHttpResponsePayload
	// A genuine SQL NULL envelope (as opposed to the JSON literal "null")
	// scans as zero-length raw ; treated the same as an empty {} envelope
	// rather than a decode error.
	if len(envelopeRaw) > 0 {
		if err := sonic.Unmarshal(envelopeRaw, &resp); err != nil {
			writeServerError(r.Context(), w, http.StatusInternalServerError, errcode.Internal, "decoding function response", err)
			return
		}
	}
	renderFullControlResponse(w, r, cfg, functionIdent, route, accumulated, resp, contentRaw, templates, staticSrv)
}

// renderFullControlResponse is writeFullControlResponse's already-decoded
// half, shared with a terminating middleware's own response (## Middleware),
// which has its envelope decoded once already rather than needing a second
// round trip through JSON.
func renderFullControlResponse(w http.ResponseWriter, r *http.Request, cfg *config.Config, functionIdent string, route Route, accumulated, resp relHttpResponsePayload, contentRaw []byte, templates *TemplateSet, staticSrv *static.Server) {
	mergeSideEffects(&accumulated, resp)
	accumulated.Status = resp.Status
	accumulated.ContentType = resp.ContentType
	accumulated.Template = resp.Template
	accumulated.StaticFile = resp.StaticFile

	status := applyResponseSideEffects(w, r, cfg, functionIdent, accumulated)

	// ## Static path masking : static_file serves that file instead of
	// content ; checked before the null-content 404 rule below, since
	// static_file is the explicit way to defer to disk.
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

	// ## Static path masking : "returning neither [content nor static_file]
	// serves a 404" — scoped to the masking case specifically : a response
	// that didn't even bother setting its own status either has made no
	// decision at all, so there's nothing to send. A response with an
	// explicit status (401, 402, 204, ...) and deliberately no content is a
	// normal, real answer, not a masking no-op — its null/empty content is
	// sent as-is (a middleware terminating with only {status: 401} is the
	// concrete case this guards).
	if accumulated.Status == 0 && (len(bytes.TrimSpace(contentRaw)) == 0 || string(bytes.TrimSpace(contentRaw)) == "null") {
		http.NotFound(w, r)
		return
	}

	if contentType != "" {
		w.Header().Set("Content-Type", contentType)
	}
	w.WriteHeader(status)
	_, _ = w.Write(contentRaw)
}

// headerValues decodes a RelHttpResponse.headers value : a single JSON
// string, or an array of strings.
func headerValues(raw json.RawMessage) []string {
	var single string
	if err := sonic.Unmarshal(raw, &single); err == nil {
		return []string{single}
	}
	var multi []string
	_ = sonic.Unmarshal(raw, &multi)
	return multi
}

// outboundCookiePayload is the object form of RelHttpResponse.cookies'
// Cookie|string value.
type outboundCookiePayload struct {
	Value    string  `json:"value"`
	HttpOnly *bool   `json:"httponly"`
	Secure   *bool   `json:"secure"`
	SameSite *string `json:"samesite"`
	MaxAge   *int    `json:"maxage"`
}

// decodeOutboundCookie applies docs/content/http/requests-responses.md's stated cookie defaults
// on top of either shorthand form.
func decodeOutboundCookie(cfg *config.Config, name string, raw json.RawMessage) *http.Cookie {
	c := &http.Cookie{
		Name:     name,
		Path:     "/",
		Secure:   true,
		HttpOnly: true,
		SameSite: routeSameSite("Lax"),
		MaxAge:   cfg.Http.CookiesMaxAge,
	}

	// specs/new-routes.md ## Cookie clearing : checked before the plain-
	// string unmarshal below, which would otherwise silently treat JSON
	// null the same as an empty string and produce a persistent, not
	// cleared, cookie.
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

// routeSameSite mirrors jwt's own unexported sameSite mapping — a small
// copy rather than exporting an internal helper for one call site.
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

// authFunctionAllowed is http.functions.auth's gate ; empty regexp = unrestricted.
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

// handleResponseJwt is Lifecycle step 1 (Mint)/logout : JSON null clears
// the session, {role, ...} mints one ; ignored outside http.functions.auth.
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
