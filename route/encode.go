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

	"github.com/ceymard/rel/config"
	"github.com/ceymard/rel/errcode"
	jwtpkg "github.com/ceymard/rel/jwt"
	"github.com/ceymard/rel/querystring"
	"github.com/ceymard/rel/websec"
	"github.com/samber/oops"
)

// relHttpRequestPayload is specs/route.md ## Request's
// RelHttpRequest — with ONE deliberate deviation, matching the spec file's
// own ## Request note : Cookies is {[name]: string} (value only), not the
// full Cookie shape (value/httponly/secure/samesite/maxage) the spec's
// literal TypeScript reuses from the RESPONSE side. A browser's Cookie
// header only ever sends name=value ; the other four attributes are
// response-only and can never be known for an inbound cookie, so reusing
// that shape here would just mean four fields that are always zero-valued.
type relHttpRequestPayload struct {
	Method      string              `json:"method"`
	URI         string              `json:"uri"`
	Headers     map[string][]string `json:"headers"`
	ContentType string              `json:"content_type"`
	// Body is ## Request's RelHttpRequest.body (renamed from an earlier,
	// always-a-raw-string "content" field), typed by ContentType — see
	// encodeBody. Kept as json.RawMessage since its shape is genuinely
	// polymorphic (JSON value / plain string / decoded form object /
	// base64 string / null), already fully encoded by the time it reaches
	// this struct.
	Body    json.RawMessage   `json:"body"`
	Cookies map[string]string `json:"cookies"`
	Jwt     jwtpkg.Claims     `json:"jwt"`
	// Query is specs/query-json.md's ## /route's query field : r.URL.RawQuery
	// decoded through the STRUCTURAL layer only (querystring.DecodeQueryField
	// — no filter expression grammar involvement, that's specific to
	// Relation's where/select/order_by), handed to the Postgres function
	// verbatim. nil (-> JSON null) when the request has no query string at
	// all.
	Query any `json:"query"`
	// CspNonce is specs/http-content.md ## CSP ### Nonce's
	// RelHttpRequest.csp_nonce — generated fresh by websec.Middleware for
	// every request, unconditionally, before the route function runs.
	CspNonce string `json:"csp_nonce"`
}

// badBodyError marks a request whose body failed to decode per ## Request's
// body content-type rules (malformed application/json, or malformed
// application/x-www-form-urlencoded) — a 400, same class as badQueryError.
type badBodyError struct{ err error }

func (e *badBodyError) Error() string { return e.err.Error() }
func (e *badBodyError) Unwrap() error { return e.err }

// mediaTypeOf extracts the bare media type from a Content-Type header value
// (params like charset= stripped), lower-cased. Falls back to a best-effort
// manual split on the first ';' when mime.ParseMediaType rejects the header
// outright (a malformed Content-Type is treated the same as an unrecognized
// one — ## Request's "anything else (binary)" branch — not its own error
// class, since only the BODY's own malformed-ness is a 400 per spec).
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

// encodeBody implements ## Request's RelHttpRequest.body content-type
// dispatch : hasFiles is true for a route declaring ## Request bodies'
// "files bytea[]" parameter, in which case body is ALWAYS JSON null
// regardless of content_type — the payload goes through files/parts_headers
// instead (built separately by the handler's multipart/binary-body path).
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

// badQueryError marks a request whose query string failed to decode
// (specs/query-json.md's own structural layer, used here for /route's
// `query` field) — a 400, same as every other malformed-request case, not
// a 500 ; handleRoute type-switches on this to pick the right status.
type badQueryError struct{ err error }

func (e *badQueryError) Error() string { return e.err.Error() }
func (e *badQueryError) Unwrap() error { return e.err }

// buildRelHttpRequest encodes r as ## Request's RelHttpRequest. bodyJSON is
// the already-encoded RelHttpRequest.body value (see encodeBody, called by
// the handler beforehand — it needs to know hasFiles, which depends on the
// matched route, so it isn't computed in here). jwt is nil (encodes as JSON
// null) for an anonymous request — symmetric with "jwt: null clears the
// session" on the response side, a documented judgment call since the spec
// doesn't pin down the anonymous case explicitly.
func buildRelHttpRequest(r *http.Request, bodyJSON json.RawMessage, verified bool, claims jwtpkg.Claims) ([]byte, error) {
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
		Method:      r.Method,
		URI:         r.URL.String(),
		Headers:     map[string][]string(r.Header),
		ContentType: r.Header.Get("Content-Type"),
		Body:        bodyJSON,
		Cookies:     cookies,
		Jwt:         jwtVal,
		Query:       queryVal,
		CspNonce:    websec.NonceFromContext(r.Context()),
	}
	return sonic.Marshal(payload)
}

// relHttpResponsePayload is ## Responses' RelHttpResponse. Content/Headers/
// Cookies/Jwt are kept as json.RawMessage : their shapes are each
// polymorphic in ways a single Go type can't represent directly (Content is
// TS `unknown` ; a header value is string|string[] ; a cookie value is
// Cookie|string ; jwt is JWT|null, distinguishable from "absent" only via
// RawMessage's own nil-vs-non-nil).
type relHttpResponsePayload struct {
	Status      int                        `json:"status"`
	ContentType string                     `json:"content_type"`
	Content     json.RawMessage            `json:"content"`
	Headers     map[string]json.RawMessage `json:"headers"`
	Cookies     map[string]json.RawMessage `json:"cookies"`
	Jwt         json.RawMessage            `json:"jwt"`
	JwtAttrs    *jwtAttrsPayload           `json:"jwt_attrs"`
	// Template/TemplateData are specs/http-content.md ## Templates :
	// when Template is a non-empty string, it names a Jet template path
	// (relative to http.templates.path) rendered in place of Content as the
	// response body ; TemplateData is that template's Data variable (JSON
	// null when unset).
	Template     string          `json:"template"`
	TemplateData json.RawMessage `json:"template_data"`
	// Csp is specs/http-content.md ## CSP ### Per-response override : a
	// raw policy string that replaces the process-wide default CSP header
	// for this one response only, "" meaning "use the default".
	Csp string `json:"csp"`
}

type jwtAttrsPayload struct {
	SameSite string `json:"samesite"`
	MaxAge   *int   `json:"maxage"`
}

// writeRelHttpResponse decodes raw as a RelHttpResponse and writes the
// actual HTTP response : headers and cookies (including a jwt mint/logout)
// are all set BEFORE status/body, since http.ResponseWriter silently drops
// header changes made after WriteHeader. r is needed for the request's own
// CSP nonce (## CSP ### Per-response override re-injects it into resp.csp
// exactly as it was injected into the process-wide default) and, once a
// Jet template set is wired in (## Templates), for Req/Nonce template vars.
func writeRelHttpResponse(w http.ResponseWriter, r *http.Request, cfg *config.Config, route Route, raw []byte, templates *TemplateSet) {
	var resp relHttpResponsePayload
	if err := sonic.Unmarshal(raw, &resp); err != nil {
		writePlainError(w, http.StatusInternalServerError, errcode.Internal, "decoding function response")
		return
	}

	for key, rawVal := range resp.Headers {
		for _, v := range headerValues(rawVal) {
			w.Header().Add(key, v)
		}
	}
	for name, rawVal := range resp.Cookies {
		http.SetCookie(w, decodeOutboundCookie(cfg, name, rawVal))
	}
	handleResponseJwt(w, cfg, route, resp)

	if resp.Csp != "" {
		nonce := websec.NonceFromContext(r.Context())
		w.Header().Set("Content-Security-Policy", websec.Policy(cfg.Http.Csp, resp.Csp, nonce))
	}

	status := resp.Status
	if status == 0 {
		status = http.StatusOK
	}

	if resp.Template != "" {
		writeTemplateResponse(w, r, templates, resp, status)
		return
	}

	if resp.ContentType != "" {
		w.Header().Set("Content-Type", resp.ContentType)
	}
	w.WriteHeader(status)
	_, _ = w.Write(contentBytes(resp.Content, resp.ContentType))
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

// decodeOutboundCookie applies ## Cookies' stated defaults ("Unless a
// response overrides them, rel sets secure: true, httponly: true,
// samesite: Lax, and a max-age of http.cookiesmaxage") on top of either
// shorthand form.
func decodeOutboundCookie(cfg *config.Config, name string, raw json.RawMessage) *http.Cookie {
	c := &http.Cookie{
		Name:     name,
		Path:     "/",
		Secure:   true,
		HttpOnly: true,
		SameSite: routeSameSite("Lax"),
		MaxAge:   cfg.Http.CookiesMaxAge,
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

// routeSameSite mirrors jwt package's own unexported sameSite mapping — kept
// as its own small copy rather than exporting an internal helper from jwt
// for this one call site.
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

// contentBytes serializes RelHttpResponse.content (TS `unknown`) per this
// session's documented judgment call : a JSON string whose content_type
// isn't itself JSON-flavored is written raw/unquoted (the common case —
// "text/plain"/"text/html" content authored as a plain string) ; anything
// else (an object/array, or content_type is itself a JSON mimetype) is
// written as content's own raw JSON representation.
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

// authFunctionAllowed is http.functions.auth's own gate : "regexp
// restricting which functions' responses rel will honor a jwt field from".
// Empty regexp = unrestricted.
func authFunctionAllowed(cfg *config.Config, route Route) bool {
	if cfg.Http.Functions.AllowedAuth == "" {
		return true
	}
	re, err := regexp.Compile(cfg.Http.Functions.AllowedAuth)
	if err != nil {
		return false
	}
	return re.MatchString(route.Function.Identifier.String())
}

// handleResponseJwt is Lifecycle step 1 (Mint) / logout, driven by a route
// function's own RelHttpResponse.jwt : JSON null clears the session ;
// {role, ...} mints a fresh one ; a route function outside
// http.functions.auth setting jwt is silently NOT honored (a documented
// judgment call — "restricting which functions' responses rel will honor a
// jwt field from" reads as "ignore it", not "error").
func handleResponseJwt(w http.ResponseWriter, cfg *config.Config, route Route, resp relHttpResponsePayload) {
	if len(resp.Jwt) == 0 {
		return // key not present at all — nothing to do
	}
	if !authFunctionAllowed(cfg, route) {
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
