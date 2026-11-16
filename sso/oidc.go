// The OIDC half : /auth/oidc/{name}/login and .../callback.
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
	"github.com/rel-server/rel/httperr"
	"github.com/rel-server/rel/pg"
	"github.com/rel-server/rel/route"
)

// oidcStateCookiePrefix names the short-lived cookie carrying the OAuth2 state and OIDC nonce from /login to
// /callback.
const oidcStateCookiePrefix = "rel_oidc_state_"

// oidcEndpoint is one openid.<name> entry's runtime state : discovery runs at construction, and again on the
// next /login if it failed. mu guards provider/oauthCfg/verifier together.
type oidcEndpoint struct {
	name string
	cfg  config.OpenidProvider

	mu       sync.Mutex
	provider *oidc.Provider
	verifier *oidc.IDTokenVerifier
	oauthCfg oauth2.Config
	ready    bool
}

// newOidcEndpoint attempts discovery once ; a failure is logged and left for the retry.
func newOidcEndpoint(name string, cfg config.OpenidProvider, redirectURL string) *oidcEndpoint {
	e := &oidcEndpoint{name: name, cfg: cfg}
	if err := e.discover(context.Background(), redirectURL); err != nil {
		log.Warn("sso: oidc discovery failed at startup, will retry on next login attempt", "name", name, "issuer", cfg.Issuer, "error", err.Error())
	}
	return e
}

// discover runs OIDC discovery and builds the oauth2.Config/verifier ; called under mu.
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

// ensureReady retries discovery once, synchronously, per request while the endpoint is not ready.
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

// mergeUserinfoClaims merges the userinfo claims over the ID token's, into a new map.
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

// mountOidc registers /auth/oidc/{name}/login and .../callback for every openid.<name> entry.
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

// oidcLoginHandler redirects to the issuer's authorization endpoint, with a fresh csrfToken and nonce kept in a
// short-lived cookie. /login's query string travels in the state param after csrfToken.
func oidcLoginHandler(e *oidcEndpoint, redirectURL string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !e.ensureReady(r.Context(), redirectURL) {
			httperr.WritePlain(w, http.StatusServiceUnavailable, errcode.SsoNotReady, "this OIDC provider's discovery has not succeeded yet — try again shortly")
			return
		}
		oauthCfg, _, _ := e.snapshot()

		loginState, err := decodeLoginState(r.URL.RawQuery)
		if err != nil {
			httperr.WritePlain(w, http.StatusBadRequest, errcode.SsoBadRequest, "decoding login state")
			return
		}
		encodedState, err := encodeStateForTransit(loginState)
		if err != nil {
			httperr.WritePlain(w, http.StatusInternalServerError, errcode.SsoInternal, "encoding login state")
			return
		}

		csrfToken, err := randomHex(16)
		if err != nil {
			httperr.WritePlain(w, http.StatusInternalServerError, errcode.SsoInternal, "generating state")
			return
		}
		nonce, err := randomHex(16)
		if err != nil {
			httperr.WritePlain(w, http.StatusInternalServerError, errcode.SsoInternal, "generating nonce")
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

// oidcCallbackHandler exchanges the code, verifies the ID token and its nonce, optionally merges userinfo, and
// hands the result to the callback function.
func oidcCallbackHandler(e *oidcEndpoint, db *pg.DbInfos, cfg *config.Config, templates *route.TemplateSet) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		_, provider, verifier := e.snapshot()
		if provider == nil || verifier == nil {
			httperr.WritePlain(w, http.StatusServiceUnavailable, errcode.SsoNotReady, "this OIDC provider's discovery has not succeeded yet")
			return
		}
		oauthCfg, _, _ := e.snapshot()

		cookie, err := r.Cookie(oidcStateCookiePrefix + e.name)
		if err != nil {
			httperr.WritePlain(w, http.StatusBadRequest, errcode.SsoBadState, "missing state cookie")
			return
		}
		http.SetCookie(w, &http.Cookie{Name: oidcStateCookiePrefix + e.name, Value: "", Path: callbackPath(e.name), MaxAge: -1})
		wantCsrf, wantNonce, ok := strings.Cut(cookie.Value, ".")
		// The state param is csrfToken, then "."+encodedAppState when /login had a query string.
		gotCsrf, encodedAppState, _ := strings.Cut(r.URL.Query().Get("state"), ".")
		if !ok || gotCsrf != wantCsrf {
			httperr.WritePlain(w, http.StatusBadRequest, errcode.SsoBadState, "state mismatch")
			return
		}
		loginState, err := decodeStateFromTransit(encodedAppState)
		if err != nil {
			httperr.WritePlain(w, http.StatusBadRequest, errcode.SsoBadState, "decoding returned state")
			return
		}

		code := r.URL.Query().Get("code")
		if code == "" {
			httperr.WritePlain(w, http.StatusBadRequest, errcode.SsoBadRequest, "missing code")
			return
		}

		ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
		defer cancel()

		token, err := oauthCfg.Exchange(ctx, code)
		if err != nil {
			httperr.WritePlain(w, http.StatusBadGateway, errcode.SsoTokenExchangeFailed, "exchanging authorization code")
			return
		}

		rawIDToken, ok := token.Extra("id_token").(string)
		if !ok {
			httperr.WritePlain(w, http.StatusBadGateway, errcode.SsoNoIdToken, "token response had no id_token")
			return
		}
		idToken, err := verifier.Verify(ctx, rawIDToken)
		if err != nil {
			httperr.WritePlain(w, http.StatusBadGateway, errcode.SsoInvalidIdToken, "verifying id_token")
			return
		}
		if idToken.Nonce != wantNonce {
			httperr.WritePlain(w, http.StatusBadRequest, errcode.SsoBadNonce, "nonce mismatch")
			return
		}

		claims := map[string]any{}
		if err := idToken.Claims(&claims); err != nil {
			httperr.WritePlain(w, http.StatusInternalServerError, errcode.SsoInternal, "decoding id_token claims")
			return
		}

		if e.cfg.FetchUserinfo {
			userInfo, err := provider.UserInfo(ctx, oauthCfg.TokenSource(ctx, token))
			if err != nil {
				httperr.WritePlain(w, http.StatusBadGateway, errcode.SsoUserinfoFailed, "fetching userinfo")
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
