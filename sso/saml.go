// The SAML half : /auth/saml/{name}/login, .../acs and .../metadata. Every assertion attribute is handed to
// the callback function untouched.
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

	"github.com/rel-server/rel/config"
	"github.com/rel-server/rel/errcode"
	"github.com/rel-server/rel/httperr"
	"github.com/rel-server/rel/pg"
	"github.com/rel-server/rel/route"
)

// samlEndpoint is one saml.<name> entry's runtime state ; the IdP metadata is retried on login like
// oidcEndpoint's discovery.
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

// newSamlEndpoint builds the *saml.ServiceProvider at once, so the metadata is served even before the IdP's
// resolves, and attempts the IdP metadata fetch once.
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

// fetchMetadata fetches the IdP metadata ; a success is permanent.
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

// ensureReady is the metadata retry, like oidcEndpoint.ensureReady.
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

// mountSaml registers /auth/saml/{name}/{login,acs,metadata} for every saml.<name> entry. sharedKP is the
// keypair of every entry that does not set its own.
func mountSaml(router chi.Router, db *pg.DbInfos, cfg *config.Config, sharedKP *spKeyPair, templates *route.TemplateSet) {
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
		kp, err := resolveSamlKeyPair(name, p, cfg, host, sharedKP)
		if err != nil {
			log.Error("sso: could not load or generate this saml entry's own SAML SP certificate, skipping", "name", name, "error", err.Error())
			continue
		}
		endpoint := newSamlEndpoint(name, p, rootURLFor(host), kp)
		router.Get(samlLoginPath(name), samlLoginHandler(endpoint))
		router.Post(samlAcsPath(name), samlAcsHandler(endpoint, db, cfg, templates))
		router.Get(samlMetadataPath(name), samlMetadataHandler(endpoint))
	}
}

// resolveSamlKeyPair returns sharedKP for an entry setting neither certificate_path nor private_key_path ;
// otherwise the entry gets its own keypair, the unset path falling back to the shared one.
func resolveSamlKeyPair(name string, p config.SamlProvider, cfg *config.Config, host string, sharedKP *spKeyPair) (*spKeyPair, error) {
	if p.CertificatePath == "" && p.PrivateKeyPath == "" {
		return sharedKP, nil
	}
	certPath := p.CertificatePath
	if certPath == "" {
		certPath = cfg.Saml.CertificatePath
	}
	keyPath := p.PrivateKeyPath
	if keyPath == "" {
		keyPath = cfg.Saml.PrivateKeyPath
	}
	return LoadOrGenerateSPKeyPair(certPath, keyPath, []string{host}, nil)
}

// samlLoginHandler redirects to the IdP with an AuthnRequest, signed per force_signed_requests. /login's query
// string travels as RelayState, which SAML does not use for CSRF protection.
func samlLoginHandler(e *samlEndpoint) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !e.ensureReady(r.Context()) {
			httperr.WritePlain(w, http.StatusServiceUnavailable, errcode.SsoNotReady, "this SAML IdP's metadata has not resolved yet — try again shortly")
			return
		}
		loginState, err := decodeLoginState(r.URL.RawQuery)
		if err != nil {
			httperr.WritePlain(w, http.StatusBadRequest, errcode.SsoBadRequest, "decoding login state")
			return
		}
		relayState, err := encodeRelayState(loginState)
		if err != nil {
			httperr.WritePlain(w, http.StatusInternalServerError, errcode.SsoInternal, "encoding login state")
			return
		}
		redirectURL, err := e.sp.MakeRedirectAuthenticationRequest(relayState)
		if err != nil {
			httperr.WritePlain(w, http.StatusInternalServerError, errcode.SsoInternal, "building SAML AuthnRequest")
			return
		}
		http.Redirect(w, r, redirectURL.String(), http.StatusFound)
	}
}

// samlAcsHandler parses the IdP's response, SP- or IdP-initiated alike, and hands every attribute to the
// callback function.
func samlAcsHandler(e *samlEndpoint, db *pg.DbInfos, cfg *config.Config, templates *route.TemplateSet) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !e.ensureReady(r.Context()) {
			httperr.WritePlain(w, http.StatusServiceUnavailable, errcode.SsoNotReady, "this SAML IdP's metadata has not resolved yet")
			return
		}
		if err := r.ParseForm(); err != nil {
			httperr.WritePlain(w, http.StatusBadRequest, errcode.SsoBadRequest, "parsing SAML response form")
			return
		}
		assertion, err := e.sp.ParseResponse(r, nil)
		if err != nil {
			httperr.WritePlain(w, http.StatusBadRequest, errcode.SsoSamlInvalidResponse, "invalid SAML response")
			return
		}

		claims := extractSamlClaims(assertion)
		loginState := decodeRelayState(r.FormValue("RelayState"))

		invokeCallback(w, r, cfg, db, templates, resolveCallbackFunction(e.cfg.CallbackFunction, cfg), ssoIdentity{
			Protocol: "saml",
			Name:     e.name,
			Claims:   claims,
		}, loginState)
	}
}

// extractSamlClaims maps every assertion attribute by name, each value always a string[] since SAML attributes
// are multi-valued. The NameID goes under the key "NameID".
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

// samlMetadataHandler serves the SP metadata, even when e is not ready.
func samlMetadataHandler(e *samlEndpoint) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		buf, err := xml.MarshalIndent(e.sp.Metadata(), "", "  ")
		if err != nil {
			httperr.WritePlain(w, http.StatusInternalServerError, errcode.SsoInternal, "encoding SP metadata")
			return
		}
		w.Header().Set("Content-Type", "application/samlmetadata+xml")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(buf)
	}
}
