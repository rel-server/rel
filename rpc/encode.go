package rpc

import (
	"bytes"
	"encoding/json"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/ceymard/rel/config"
	jwtpkg "github.com/ceymard/rel/jwt"
)

// relHttpRequestPayload is specs/jwt-roles-and-http.md ## Request's
// RelHttpRequest — with ONE deliberate deviation, per this session's
// resolution : Cookies is {[name]: string} (value only), not the full
// Cookie shape (value/httponly/secure/samesite/maxage) the spec's literal
// TypeScript reuses from the RESPONSE side. A browser's Cookie header only
// ever sends name=value ; the other four attributes are response-only and
// can never be known for an inbound cookie, so reusing that shape here
// would just mean four fields that are always zero-valued. jwt-roles-and-
// http.md needs a matching one-line fix (not yet applied to the spec file
// itself in this pass).
type relHttpRequestPayload struct {
	Method      string              `json:"method"`
	URI         string              `json:"uri"`
	Headers     map[string][]string `json:"headers"`
	ContentType string              `json:"content_type"`
	Content     string              `json:"content"`
	Cookies     map[string]string   `json:"cookies"`
	Jwt         jwtpkg.Claims       `json:"jwt"`
}

// buildRelHttpRequest encodes r (already read into body) as ##
// Request's RelHttpRequest. jwt is nil (encodes as JSON null) for an
// anonymous request — symmetric with "jwt: null clears the session" on the
// response side, a documented judgment call since the spec doesn't pin
// down the anonymous case explicitly.
func buildRelHttpRequest(r *http.Request, body []byte, verified bool, claims jwtpkg.Claims) ([]byte, error) {
	cookies := map[string]string{}
	for _, c := range r.Cookies() {
		cookies[c.Name] = c.Value
	}
	var jwtVal jwtpkg.Claims
	if verified {
		jwtVal = claims
	}
	payload := relHttpRequestPayload{
		Method:      r.Method,
		URI:         r.URL.String(),
		Headers:     map[string][]string(r.Header),
		ContentType: r.Header.Get("Content-Type"),
		Content:     string(body),
		Cookies:     cookies,
		Jwt:         jwtVal,
	}
	return json.Marshal(payload)
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
}

type jwtAttrsPayload struct {
	SameSite string `json:"samesite"`
	MaxAge   *int   `json:"maxage"`
}

// writeRelHttpResponse decodes raw as a RelHttpResponse and writes the
// actual HTTP response : headers and cookies (including a jwt mint/logout)
// are all set BEFORE status/body, since http.ResponseWriter silently drops
// header changes made after WriteHeader.
func writeRelHttpResponse(w http.ResponseWriter, cfg *config.Config, route Route, raw []byte) {
	var resp relHttpResponsePayload
	if err := json.Unmarshal(raw, &resp); err != nil {
		writePlainError(w, http.StatusInternalServerError, "decoding function response")
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

	status := resp.Status
	if status == 0 {
		status = http.StatusOK
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
	if err := json.Unmarshal(raw, &single); err == nil {
		return []string{single}
	}
	var multi []string
	_ = json.Unmarshal(raw, &multi)
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
		SameSite: rpcSameSite("Lax"),
		MaxAge:   cfg.Http.CookiesMaxAge,
	}

	var asString string
	if err := json.Unmarshal(raw, &asString); err == nil {
		c.Value = asString
		return c
	}

	var obj outboundCookiePayload
	if err := json.Unmarshal(raw, &obj); err != nil {
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
		c.SameSite = rpcSameSite(*obj.SameSite)
	}
	if obj.MaxAge != nil {
		c.MaxAge = *obj.MaxAge
	}
	return c
}

// rpcSameSite mirrors jwt package's own unexported sameSite mapping — kept
// as its own small copy rather than exporting an internal helper from jwt
// for this one call site.
func rpcSameSite(s string) http.SameSite {
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
		if err := json.Unmarshal(trimmed, &s); err == nil {
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
	if err := json.Unmarshal(resp.Jwt, &obj); err != nil {
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
