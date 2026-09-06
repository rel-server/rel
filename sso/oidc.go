// This file implements specs/oauth-saml.md's OIDC half : /auth/oidc/{name}/
// login and .../callback, on golang.org/x/oauth2 (code exchange) +
// coreos/go-oidc/v3 (discovery + ID-token verification) — see
// specs/authentication.md's "# Authentication" for why, over goth/gothic.
package sso

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/go-chi/chi/v5"
	"golang.org/x/oauth2"

	"github.com/rel-server/rel/config"
	"github.com/rel-server/rel/errcode"
	"github.com/rel-server/rel/pg"
	"github.com/rel-server/rel/route"
)

// oidcStateCookiePrefix names the short-lived cookie /login sets and
// /callback consumes to carry the OAuth2 state + OIDC nonce across the
// redirect round trip — CSRF protection for the callback (state) plus
// replay protection for the ID token (nonce), the same two values every
// OIDC RP is expected to carry itself.
const oidcStateCookiePrefix = "rel_oidc_state_"

// oidcEndpoint is one configured openid.<name> entry's mutable runtime
// state : discovery is attempted once at construction, and again, lazily,
// synchronously, on the next /login request if it failed — specs/oauth-saml.md
// ## Metadata fetch is lazy. mu guards provider/oauthCfg/verifier together,
// since a concurrent lazy-retry and a read must never observe a half-built set.
type oidcEndpoint struct {
	name string
	cfg  config.OpenidProvider

	mu       sync.Mutex
	provider *oidc.Provider
	verifier *oidc.IDTokenVerifier
	oauthCfg oauth2.Config
	ready    bool
}

// newOidcEndpoint attempts discovery once, immediately — a failure here is
// logged and left for the lazy retry, never fatal to construction.
func newOidcEndpoint(name string, cfg config.OpenidProvider, redirectURL string) *oidcEndpoint {
	e := &oidcEndpoint{name: name, cfg: cfg}
	if err := e.discover(context.Background(), redirectURL); err != nil {
		log.Warn("sso: oidc discovery failed at startup, will retry on next login attempt", "name", name, "issuer", cfg.Issuer, "error", err.Error())
	}
	return e
}

// discover runs OIDC discovery and builds the oauth2.Config/verifier ; safe
// to call more than once (e.g. the lazy retry), always under mu.
func (e *oidcEndpoint) discover(ctx context.Context, redirectURL string) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.ready {
		return nil
	}
	provider, err := oidc.NewProvider(ctx, e.cfg.Issuer)
	if err != nil {
		return err
	}
	e.provider = provider
	e.verifier = provider.Verifier(&oidc.Config{ClientID: e.cfg.ClientID})
	e.oauthCfg = oauth2.Config{
		ClientID:     e.cfg.ClientID,
		ClientSecret: e.cfg.ClientSecret,
		Endpoint:     provider.Endpoint(),
		RedirectURL:  redirectURL,
		Scopes:       e.cfg.Scopes,
	}
	e.ready = true
	return nil
}

// ensureReady is ## Metadata fetch is lazy's own retry : a not-yet-ready
// endpoint gets exactly one synchronous retry per request, never a
// background loop.
func (e *oidcEndpoint) ensureReady(ctx context.Context, redirectURL string) bool {
	e.mu.Lock()
	ready := e.ready
	e.mu.Unlock()
	if ready {
		return true
	}
	if err := e.discover(ctx, redirectURL); err != nil {
		log.Warn("sso: oidc discovery retry failed", "name", e.name, "issuer", e.cfg.Issuer, "error", err.Error())
		return false
	}
	return true
}

func (e *oidcEndpoint) snapshot() (oauth2.Config, *oidc.Provider, *oidc.IDTokenVerifier) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.oauthCfg, e.provider, e.verifier
}

func randomHex(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// mergeUserinfoClaims implements ## Callback payload shape's merge precedence :
// "the userinfo endpoint's claims are merged over the ID token's own
// (userinfo wins on key collision)" — idTokenClaims is never mutated in
// place, since it may still be logged/inspected by the caller on error.
func mergeUserinfoClaims(idTokenClaims, userinfoClaims map[string]any) map[string]any {
	merged := make(map[string]any, len(idTokenClaims)+len(userinfoClaims))
	for k, v := range idTokenClaims {
		merged[k] = v
	}
	for k, v := range userinfoClaims {
		merged[k] = v
	}
	return merged
}

func loginPath(name string) string    { return "/auth/oidc/" + name + "/login" }
func callbackPath(name string) string { return "/auth/oidc/" + name + "/callback" }

// mountOidc registers /auth/oidc/{name}/login and .../callback for every
// configured openid.<name> entry onto router. Each entry's own root URL is
// resolveHost(p.PublicHost, cfg.Http.PublicHost) — specs/oauth-saml.md
// ## Configuration — HTTP's per-entry override, used to build redirect_uri.
func mountOidc(router chi.Router, db *pg.DbInfos, cfg *config.Config, templates *route.TemplateSet) {
	if len(cfg.Openid) == 0 {
		return
	}
	for name, p := range cfg.Openid {
		if p.Issuer == "" {
			log.Error("sso: openid entry has no issuer configured, skipping", "name", name)
			continue
		}
		host := resolveHost(p.PublicHost, cfg.Http.PublicHost)
		if host == "" {
			log.Error("sso: openid entry has no effective public_host (neither its own nor http.public_host is set), skipping", "name", name)
			continue
		}
		redirectURL := rootURLFor(host) + callbackPath(name)
		endpoint := newOidcEndpoint(name, p, redirectURL)
		router.Get(loginPath(name), oidcLoginHandler(endpoint, redirectURL))
		router.Get(callbackPath(name), oidcCallbackHandler(endpoint, db, cfg, templates))
	}
}

// oidcLoginHandler is specs/oauth-saml.md's "redirects to the issuer's
// authorization endpoint" — csrfToken+nonce are generated fresh per
// request and carried in a short-lived cookie, never server-side session
// state. Any query string /login itself received travels ALONGSIDE
// csrfToken in the outgoing state param (## Passing state through login)
// — a distinct concern from csrfToken/nonce, composed rather than
// conflated with them, since only csrfToken is ever validated on return.
func oidcLoginHandler(e *oidcEndpoint, redirectURL string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !e.ensureReady(r.Context(), redirectURL) {
			writePlainError(w, http.StatusServiceUnavailable, errcode.SsoNotReady, "this OIDC provider's discovery has not succeeded yet — try again shortly")
			return
		}
		oauthCfg, _, _ := e.snapshot()

		loginState, err := decodeLoginState(r.URL.RawQuery)
		if err != nil {
			writePlainError(w, http.StatusBadRequest, errcode.SsoBadRequest, "decoding login state")
			return
		}
		encodedState, err := encodeStateForTransit(loginState)
		if err != nil {
			writePlainError(w, http.StatusInternalServerError, errcode.SsoInternal, "encoding login state")
			return
		}

		csrfToken, err := randomHex(16)
		if err != nil {
			writePlainError(w, http.StatusInternalServerError, errcode.SsoInternal, "generating state")
			return
		}
		nonce, err := randomHex(16)
		if err != nil {
			writePlainError(w, http.StatusInternalServerError, errcode.SsoInternal, "generating nonce")
			return
		}

		http.SetCookie(w, &http.Cookie{
			Name:     oidcStateCookiePrefix + e.name,
			Value:    csrfToken + "." + nonce,
			Path:     callbackPath(e.name),
			MaxAge:   300,
			HttpOnly: true,
			Secure:   true,
			SameSite: http.SameSiteLaxMode,
		})

		outgoingState := csrfToken
		if encodedState != "" {
			outgoingState = csrfToken + "." + encodedState
		}

		http.Redirect(w, r, oauthCfg.AuthCodeURL(outgoingState, oidc.Nonce(nonce)), http.StatusFound)
	}
}

// oidcCallbackHandler is the OAuth2 redirect target : exchanges the code,
// verifies the ID token (including the nonce this same flow minted),
// optionally merges userinfo, and hands the result to ## Callback function.
func oidcCallbackHandler(e *oidcEndpoint, db *pg.DbInfos, cfg *config.Config, templates *route.TemplateSet) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		_, provider, verifier := e.snapshot()
		if provider == nil || verifier == nil {
			writePlainError(w, http.StatusServiceUnavailable, errcode.SsoNotReady, "this OIDC provider's discovery has not succeeded yet")
			return
		}
		oauthCfg, _, _ := e.snapshot()

		cookie, err := r.Cookie(oidcStateCookiePrefix + e.name)
		if err != nil {
			writePlainError(w, http.StatusBadRequest, errcode.SsoBadState, "missing state cookie")
			return
		}
		http.SetCookie(w, &http.Cookie{Name: oidcStateCookiePrefix + e.name, Value: "", Path: callbackPath(e.name), MaxAge: -1})
		wantCsrf, wantNonce, ok := strings.Cut(cookie.Value, ".")
		// The returned state param is csrfToken alone, or csrfToken+"."+
		// encodedAppState when /login received a query string (## Passing
		// state through login) — only the csrfToken half is ever validated;
		// strings.Cut's own no-separator case (encodedAppState == "") means
		// "no app state was ever appended," same as before this feature existed.
		gotCsrf, encodedAppState, _ := strings.Cut(r.URL.Query().Get("state"), ".")
		if !ok || gotCsrf != wantCsrf {
			writePlainError(w, http.StatusBadRequest, errcode.SsoBadState, "state mismatch")
			return
		}
		loginState, err := decodeStateFromTransit(encodedAppState)
		if err != nil {
			writePlainError(w, http.StatusBadRequest, errcode.SsoBadState, "decoding returned state")
			return
		}

		code := r.URL.Query().Get("code")
		if code == "" {
			writePlainError(w, http.StatusBadRequest, errcode.SsoBadRequest, "missing code")
			return
		}

		ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
		defer cancel()

		token, err := oauthCfg.Exchange(ctx, code)
		if err != nil {
			writePlainError(w, http.StatusBadGateway, errcode.SsoTokenExchangeFailed, "exchanging authorization code")
			return
		}

		rawIDToken, ok := token.Extra("id_token").(string)
		if !ok {
			writePlainError(w, http.StatusBadGateway, errcode.SsoNoIdToken, "token response had no id_token")
			return
		}
		idToken, err := verifier.Verify(ctx, rawIDToken)
		if err != nil {
			writePlainError(w, http.StatusBadGateway, errcode.SsoInvalidIdToken, "verifying id_token")
			return
		}
		if idToken.Nonce != wantNonce {
			writePlainError(w, http.StatusBadRequest, errcode.SsoBadNonce, "nonce mismatch")
			return
		}

		claims := map[string]any{}
		if err := idToken.Claims(&claims); err != nil {
			writePlainError(w, http.StatusInternalServerError, errcode.SsoInternal, "decoding id_token claims")
			return
		}

		if e.cfg.FetchUserinfo {
			userInfo, err := provider.UserInfo(ctx, oauthCfg.TokenSource(ctx, token))
			if err != nil {
				writePlainError(w, http.StatusBadGateway, errcode.SsoUserinfoFailed, "fetching userinfo")
				return
			}
			var uiClaims map[string]any
			if err := userInfo.Claims(&uiClaims); err == nil {
				claims = mergeUserinfoClaims(claims, uiClaims)
			}
		}

		invokeCallback(w, r, cfg, db, templates, resolveCallbackFunction(e.cfg.CallbackFunction, cfg), ssoIdentity{
			Protocol:     "oidc",
			Name:         e.name,
			Claims:       claims,
			AccessToken:  token.AccessToken,
			RefreshToken: token.RefreshToken,
		}, loginState)
	}
}
