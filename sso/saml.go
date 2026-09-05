// This file implements specs/oauth-saml.md's SAML half : /auth/saml/{name}/
// login, .../acs, and .../metadata, on crewjam/saml's lower-level
// *saml.ServiceProvider API directly — NOT samlsp.Middleware, which owns
// its own session/cookie/attribute-name opinions ; we want every raw
// assertion attribute handed to the callback function untouched (## Claims
// shape), not samlsp's own guess at "the" username attribute.
package sso

import (
	"context"
	"encoding/xml"
	"net/http"
	"net/url"
	"strings"
	"sync"

	"github.com/crewjam/saml"
	"github.com/crewjam/saml/samlsp"
	"github.com/go-chi/chi/v5"
	dsig "github.com/russellhaering/goxmldsig"

	"github.com/ceymard/rel/config"
	"github.com/ceymard/rel/errcode"
	"github.com/ceymard/rel/pg"
	"github.com/ceymard/rel/route"
)

// samlEndpoint is one configured saml.<name> entry's runtime state — same
// "discover once, retry lazily on next login" shape oidcEndpoint uses,
// applied to fetching the IdP's own metadata instead of OIDC discovery.
type samlEndpoint struct {
	name string
	cfg  config.SamlProvider

	mu    sync.Mutex
	sp    *saml.ServiceProvider
	ready bool
}

func samlLoginPath(name string) string    { return "/auth/saml/" + name + "/login" }
func samlAcsPath(name string) string      { return "/auth/saml/" + name + "/acs" }
func samlMetadataPath(name string) string { return "/auth/saml/" + name + "/metadata" }

// newSamlEndpoint builds the *saml.ServiceProvider immediately — this is
// what makes /auth/saml/{name}/metadata servable right away, independent
// of whether the IdP's own metadata has ever resolved (specs/oauth-saml.md
// ## Certificate's bootstrapping-deadlock rationale) — and attempts the
// IdP metadata fetch once, leaving a failure for the lazy retry.
func newSamlEndpoint(name string, cfg config.SamlProvider, rootURL string, kp *spKeyPair) *samlEndpoint {
	root := strings.TrimRight(rootURL, "/") + "/auth/saml/" + name + "/"
	metadataURL, _ := url.Parse(root + "metadata")
	acsURL, _ := url.Parse(root + "acs")

	sp := &saml.ServiceProvider{
		Key:               kp.Key,
		Certificate:       kp.Certificate,
		MetadataURL:       *metadataURL,
		AcsURL:            *acsURL,
		AllowIDPInitiated: true,
	}
	if cfg.ForceSignedRequests {
		sp.SignatureMethod = dsig.RSASHA256SignatureMethod
	}

	e := &samlEndpoint{name: name, cfg: cfg, sp: sp}
	if err := e.fetchMetadata(context.Background()); err != nil {
		log.Warn("sso: saml idp metadata fetch failed at startup, will retry on next login attempt", "name", name, "idp_metadata_url", cfg.IdpMetadataUrl, "error", err.Error())
	}
	return e
}

// fetchMetadata is the shared body of construction and the lazy retry —
// safe to call more than once ; a success is permanent (## Metadata fetch
// is lazy : "clears the not-ready mark permanently").
func (e *samlEndpoint) fetchMetadata(ctx context.Context) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.ready {
		return nil
	}
	idpURL, err := url.Parse(e.cfg.IdpMetadataUrl)
	if err != nil {
		return err
	}
	metadata, err := samlsp.FetchMetadata(ctx, http.DefaultClient, *idpURL)
	if err != nil {
		return err
	}
	e.sp.IDPMetadata = metadata
	e.ready = true
	return nil
}

// ensureReady is ## Metadata fetch is lazy's own retry, mirroring
// oidcEndpoint.ensureReady exactly.
func (e *samlEndpoint) ensureReady(ctx context.Context) bool {
	e.mu.Lock()
	ready := e.ready
	e.mu.Unlock()
	if ready {
		return true
	}
	if err := e.fetchMetadata(ctx); err != nil {
		log.Warn("sso: saml idp metadata fetch retry failed", "name", e.name, "idp_metadata_url", e.cfg.IdpMetadataUrl, "error", err.Error())
		return false
	}
	return true
}

// mountSaml registers /auth/saml/{name}/{login,acs,metadata} for every
// configured saml.<name> entry. kp is the shared SP identity — one
// keypair across every IdP, per specs/oauth-saml.md ## Certificate. Each
// entry's own root URL is resolveHost(p.PublicHost, cfg.Http.PublicHost) —
// specs/oauth-saml.md ## Configuration — HTTP's per-entry override.
func mountSaml(router chi.Router, db *pg.DbInfos, cfg *config.Config, kp *spKeyPair, templates *route.TemplateSet) {
	if len(cfg.Saml.Providers) == 0 {
		return
	}
	for name, p := range cfg.Saml.Providers {
		if p.IdpMetadataUrl == "" {
			log.Error("sso: saml entry has no idp_metadata_url configured, skipping", "name", name)
			continue
		}
		host := resolveHost(p.PublicHost, cfg.Http.PublicHost)
		if host == "" {
			log.Error("sso: saml entry has no effective public_host (neither its own nor http.public_host is set), skipping", "name", name)
			continue
		}
		endpoint := newSamlEndpoint(name, p, rootURLFor(host), kp)
		router.Get(samlLoginPath(name), samlLoginHandler(endpoint))
		router.Post(samlAcsPath(name), samlAcsHandler(endpoint, db, cfg, templates))
		router.Get(samlMetadataPath(name), samlMetadataHandler(endpoint))
	}
}

// samlLoginHandler is the SP-initiated flow : redirect to the IdP with a
// signed or unsigned AuthnRequest per force_signed_requests.
func samlLoginHandler(e *samlEndpoint) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !e.ensureReady(r.Context()) {
			writePlainError(w, http.StatusServiceUnavailable, errcode.SsoNotReady, "this SAML IdP's metadata has not resolved yet — try again shortly")
			return
		}
		redirectURL, err := e.sp.MakeRedirectAuthenticationRequest("")
		if err != nil {
			writePlainError(w, http.StatusInternalServerError, errcode.SsoInternal, "building SAML AuthnRequest")
			return
		}
		http.Redirect(w, r, redirectURL.String(), http.StatusFound)
	}
}

// samlAcsHandler is the Assertion Consumer Service : parses the IdP's
// response (SP-initiated or IdP-initiated alike, since AllowIDPInitiated
// skips InResponseTo correlation — specs/oauth-saml.md ## Endpoints :
// "nothing distinguishes an IdP-initiated POST... no separate
// configuration"), extracts every attribute as string[], and hands the
// result to ## Callback function.
func samlAcsHandler(e *samlEndpoint, db *pg.DbInfos, cfg *config.Config, templates *route.TemplateSet) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !e.ensureReady(r.Context()) {
			writePlainError(w, http.StatusServiceUnavailable, errcode.SsoNotReady, "this SAML IdP's metadata has not resolved yet")
			return
		}
		if err := r.ParseForm(); err != nil {
			writePlainError(w, http.StatusBadRequest, errcode.SsoBadRequest, "parsing SAML response form")
			return
		}
		assertion, err := e.sp.ParseResponse(r, nil)
		if err != nil {
			writePlainError(w, http.StatusBadRequest, errcode.SsoSamlInvalidResponse, "invalid SAML response")
			return
		}

		claims := extractSamlClaims(assertion)

		invokeCallback(w, r, cfg, db, templates, resolveCallbackFunction(e.cfg.CallbackFunction, cfg), ssoClaims{
			Protocol: "saml",
			Name:     e.name,
			Claims:   claims,
		})
	}
}

// extractSamlClaims is specs/oauth-saml.md ## Claims shape's SAML half :
// "every assertion attribute, keyed by its attribute name", each value
// ALWAYS a string[] even for a single-valued attribute — SAML attributes
// are multi-valued by the spec, and rel does not guess at collapsing a
// single-element array. The assertion's own NameID is folded in under the
// literal key "NameID", matching the example callback function's own
// candidate-key list (specs/oauth-saml.md ## Example).
func extractSamlClaims(assertion *saml.Assertion) map[string]any {
	claims := map[string]any{}
	for _, stmt := range assertion.AttributeStatements {
		for _, attr := range stmt.Attributes {
			values := make([]string, len(attr.Values))
			for i, v := range attr.Values {
				values[i] = v.Value
			}
			claims[attr.Name] = values
		}
	}
	if assertion.Subject != nil && assertion.Subject.NameID != nil {
		claims["NameID"] = []string{assertion.Subject.NameID.Value}
	}
	return claims
}

// samlMetadataHandler always serves this deployment's own SP metadata —
// independent of e.ready, per specs/oauth-saml.md ## Endpoints.
func samlMetadataHandler(e *samlEndpoint) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		buf, err := xml.MarshalIndent(e.sp.Metadata(), "", "  ")
		if err != nil {
			writePlainError(w, http.StatusInternalServerError, errcode.SsoInternal, "encoding SP metadata")
			return
		}
		w.Header().Set("Content-Type", "application/samlmetadata+xml")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(buf)
	}
}
