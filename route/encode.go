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
	handleResponseJwt(w, cfg, functionIdent, resp)

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
