package sso

import (
	"encoding/json"
	"html"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/crewjam/saml"
	dsig "github.com/russellhaering/goxmldsig"

	"github.com/ceymard/rel/config"
	jwtpkg "github.com/ceymard/rel/jwt"
)

// staticSessionProvider/staticSPProvider are the minimal saml.IdentityProvider
// plumbing needed to drive a real, fully-signed IdP-initiated response
// through samlAcsHandler — a legitimate assertion built by the same
// library our own SP code verifies with, rather than a hand-rolled XML
// fixture that would drift from what a real IdP actually sends.
type staticSessionProvider struct{ session *saml.Session }

func (p staticSessionProvider) GetSession(w http.ResponseWriter, r *http.Request, req *saml.IdpAuthnRequest) *saml.Session {
	return p.session
}

type staticSPProvider struct{ metadata *saml.EntityDescriptor }

func (p staticSPProvider) GetServiceProvider(r *http.Request, id string) (*saml.EntityDescriptor, error) {
	return p.metadata, nil
}

var samlResponseValueRe = regexp.MustCompile(`name="SAMLResponse" value="([^"]*)"`)

// samlTestFixture bundles the SP-side samlEndpoint under test with an
// in-process IdP that trusts it, sharing one SP metadata document between
// them so the IdP-minted assertion is one our own SP code will actually
// accept.
type samlTestFixture struct {
	endpoint *samlEndpoint
	idp      *saml.IdentityProvider
}

// newSamlTestFixture builds the SP (samlEndpoint, with a freshly generated
// keypair) and an IdP (its own freshly generated keypair) that already
// trusts this exact SP's metadata — IDPMetadata is set directly rather
// than fetched over HTTP, since the test already holds the *EntityDescriptor
// in-process ; this is equivalent to ## Metadata fetch is lazy having
// already succeeded once.
func newSamlTestFixture(t *testing.T, rootURL string, forceSigned bool) *samlTestFixture {
	t.Helper()

	dir := t.TempDir()
	spKp, err := LoadOrGenerateSPKeyPair(dir+"/sp-cert.pem", dir+"/sp-cert.key", nil, nil)
	if err != nil {
		t.Fatalf("generating SP keypair: %v", err)
	}
	idpKp, _, _, err := generateSelfSignedKeyPair([]string{"idp.example.com"})
	if err != nil {
		t.Fatalf("generating IdP keypair: %v", err)
	}

	root := strings.TrimRight(rootURL, "/") + "/auth/saml/test/"
	metadataURL, _ := url.Parse(root + "metadata")
	acsURL, _ := url.Parse(root + "acs")

	sp := &saml.ServiceProvider{
		Key:               spKp.Key,
		Certificate:       spKp.Certificate,
		MetadataURL:       *metadataURL,
		AcsURL:            *acsURL,
		AllowIDPInitiated: true,
	}
	if forceSigned {
		sp.SignatureMethod = dsig.RSASHA256SignatureMethod
	}

	endpoint := &samlEndpoint{
		name:  "test",
		cfg:   config.SamlProvider{ForceSignedRequests: forceSigned},
		sp:    sp,
		ready: true,
	}

	idp := &saml.IdentityProvider{
		Key:                     idpKp.Key,
		Signer:                  idpKp.Key,
		Certificate:             idpKp.Certificate,
		MetadataURL:             url.URL{Scheme: "https", Host: "idp.example.com", Path: "/metadata"},
		SSOURL:                  url.URL{Scheme: "https", Host: "idp.example.com", Path: "/sso"},
		ServiceProviderProvider: staticSPProvider{metadata: sp.Metadata()},
	}
	sp.IDPMetadata = idp.Metadata()

	return &samlTestFixture{endpoint: endpoint, idp: idp}
}

// idpInitiatedPost drives f.idp's ServeIDPInitiated for session, extracts
// the resulting SAMLResponse form field, and builds the POST request our
// own /acs handler expects.
func idpInitiatedPost(t *testing.T, f *samlTestFixture, session *saml.Session, acsPath string) *http.Request {
	t.Helper()
	f.idp.SessionProvider = staticSessionProvider{session: session}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/idp-initiated", nil)
	f.idp.ServeIDPInitiated(rec, req, f.endpoint.sp.MetadataURL.String(), "")

	if rec.Code != 0 && rec.Code != http.StatusOK {
		t.Fatalf("ServeIDPInitiated: unexpected status %d: %s", rec.Code, rec.Body.String())
	}
	m := samlResponseValueRe.FindStringSubmatch(rec.Body.String())
	if m == nil {
		t.Fatalf("could not find SAMLResponse in IdP response body: %s", rec.Body.String())
	}

	form := url.Values{"SAMLResponse": {html.UnescapeString(m[1])}}
	acsReq := httptest.NewRequest(http.MethodPost, acsPath, strings.NewReader(form.Encode()))
	acsReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	return acsReq
}

// idpInitiatedPostWithRelayState is idpInitiatedPost plus a RelayState form
// field — AllowIDPInitiated:true means samlAcsHandler doesn't correlate
// against a real prior AuthnRequest, so this is enough to exercise the ACS
// side of state passthrough without simulating a full SP-initiated
// InResponseTo round trip.
func idpInitiatedPostWithRelayState(t *testing.T, f *samlTestFixture, session *saml.Session, acsPath, relayState string) *http.Request {
	t.Helper()
	req := idpInitiatedPost(t, f, session, acsPath)
	if err := req.ParseForm(); err != nil {
		t.Fatalf("ParseForm: %v", err)
	}
	form := url.Values{"SAMLResponse": {req.PostForm.Get("SAMLResponse")}, "RelayState": {relayState}}
	out := httptest.NewRequest(http.MethodPost, acsPath, strings.NewReader(form.Encode()))
	out.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	return out
}

// loginRelayState drives f.endpoint's own /login handler for real and
// extracts the RelayState it put on the redirect to the IdP — proving
// samlLoginHandler's own encodeRelayState call, not just decodeRelayState
// in isolation.
func loginRelayState(t *testing.T, f *samlTestFixture, rawQuery string) string {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/auth/saml/test/login?"+rawQuery, nil)
	rec := httptest.NewRecorder()
	samlLoginHandler(f.endpoint)(rec, req)
	if rec.Code != http.StatusFound {
		t.Fatalf("expected 302 from /login, got %d: %s", rec.Code, rec.Body.String())
	}
	loc, err := url.Parse(rec.Header().Get("Location"))
	if err != nil {
		t.Fatalf("parsing /login redirect: %v", err)
	}
	return loc.Query().Get("RelayState")
}

// TestSaml_MetadataAlwaysServed proves /metadata is servable regardless of
// e.ready — specs/oauth-saml.md ## Endpoints' bootstrapping-deadlock
// rationale : a developer must be able to hand this document to an IdP
// administrator before the IdP side is configured at all.
func TestSaml_MetadataAlwaysServed(t *testing.T) {
	f := newSamlTestFixture(t, "https://app.example.com", true)
	f.endpoint.ready = false // simulate "IdP metadata never resolved"

	rec := httptest.NewRecorder()
	samlMetadataHandler(f.endpoint)(rec, httptest.NewRequest(http.MethodGet, "/auth/saml/test/metadata", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "EntityDescriptor") {
		t.Errorf("expected SP metadata XML, got: %s", rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/samlmetadata+xml" {
		t.Errorf("Content-Type = %q", ct)
	}
}

// TestSaml_AcsHappyPath drives a real, signed IdP-initiated assertion
// through samlAcsHandler end to end, asserting the configured callback
// function actually mints app_user for alice@example.com
// (testdata/schema.sql).
func TestSaml_AcsHappyPath(t *testing.T) {
	f := newSamlTestFixture(t, "https://app.example.com", true)
	session := &saml.Session{
		ID:     "session-1",
		NameID: "alice@example.com",
		CustomAttributes: []saml.Attribute{
			{Name: "email", Values: []saml.AttributeValue{{Value: "alice@example.com"}}},
			{Name: "groups", Values: []saml.AttributeValue{{Value: "admins"}, {Value: "users"}}},
		},
	}
	acsReq := idpInitiatedPost(t, f, session, "/auth/saml/test/acs")

	rec := httptest.NewRecorder()
	samlAcsHandler(f.endpoint, testDb, testCfg, nil)(rec, acsReq)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var cookie *http.Cookie
	for _, c := range rec.Result().Cookies() {
		if c.Name == testCfg.Jwt.CookieName {
			cookie = c
		}
	}
	if cookie == nil {
		t.Fatalf("expected the JWT cookie to be set on a successful login")
	}
}

// TestSaml_AcsRejectsUnknownIdentity mirrors the OIDC RSxxx-rejection
// coverage : a validly signed assertion for an identity the callback
// function's users table has no row for must fail, no cookie set.
func TestSaml_AcsRejectsUnknownIdentity(t *testing.T) {
	f := newSamlTestFixture(t, "https://app.example.com", true)
	session := &saml.Session{
		ID:     "session-2",
		NameID: "nobody@example.com",
		CustomAttributes: []saml.Attribute{
			{Name: "email", Values: []saml.AttributeValue{{Value: "nobody@example.com"}}},
		},
	}
	acsReq := idpInitiatedPost(t, f, session, "/auth/saml/test/acs")

	rec := httptest.NewRecorder()
	samlAcsHandler(f.endpoint, testDb, testCfg, nil)(rec, acsReq)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 (RS401), got %d: %s", rec.Code, rec.Body.String())
	}
	for _, c := range rec.Result().Cookies() {
		if c.Name == testCfg.Jwt.CookieName && c.Value != "" {
			t.Errorf("expected no JWT cookie set on rejection, got %q", c.Value)
		}
	}
}

// TestSaml_AcsRejectsTamperedAssertion proves the signature is actually
// verified : a response whose signed XML has been altered post-signing
// must be rejected, not silently accepted.
func TestSaml_AcsRejectsTamperedAssertion(t *testing.T) {
	f := newSamlTestFixture(t, "https://app.example.com", true)
	session := &saml.Session{
		ID:     "session-3",
		NameID: "alice@example.com",
		CustomAttributes: []saml.Attribute{
			{Name: "email", Values: []saml.AttributeValue{{Value: "alice@example.com"}}},
		},
	}
	acsReq := idpInitiatedPost(t, f, session, "/auth/saml/test/acs")

	// Tamper with the base64 SAMLResponse body : flip a character deep
	// enough in to land inside the signed XML payload, not just padding.
	if err := acsReq.ParseForm(); err != nil {
		t.Fatalf("ParseForm: %v", err)
	}
	original := acsReq.PostForm.Get("SAMLResponse")
	tampered := tamperBase64(original)
	form := url.Values{"SAMLResponse": {tampered}}
	tamperedReq := httptest.NewRequest(http.MethodPost, "/auth/saml/test/acs", strings.NewReader(form.Encode()))
	tamperedReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	rec := httptest.NewRecorder()
	samlAcsHandler(f.endpoint, testDb, testCfg, nil)(rec, tamperedReq)

	if rec.Code == http.StatusOK {
		t.Fatalf("expected a tampered assertion to be rejected, got 200: %s", rec.Body.String())
	}
}

// tamperBase64 flips one character in the middle of a base64 string,
// staying within the base64 alphabet so it still decodes (into different
// bytes), which is what actually exercises signature validation rather
// than a base64-decode failure.
func tamperBase64(s string) string {
	mid := len(s) / 2
	b := []byte(s)
	if b[mid] == 'A' {
		b[mid] = 'B'
	} else {
		b[mid] = 'A'
	}
	return string(b)
}

// TestSaml_AcsForwardsLoginStateAndSession is TestOidc_CallbackForwardsLoginStateAndSession's
// SAML counterpart : /login's query string round-trips through RelayState
// (previously hardcoded empty), and the browser's own current session
// reaches the callback payload as "jwt" — docs/content/http/authentication.md
// ## Passing state through login. httptest.NewRequest doesn't model a
// browser's SameSite cookie policy, so attaching the session cookie here
// proves the Go-side wiring works ; it does NOT prove a real browser would
// deliver this cookie on /acs's cross-site POST under jwt.same_site=Lax
// (the default) — see that same doc section for why it wouldn't.
func TestSaml_AcsForwardsLoginStateAndSession(t *testing.T) {
	f := newSamlTestFixture(t, "https://app.example.com", true)
	f.endpoint.cfg.CallbackFunction = "auth.echo_payload"

	relayState := loginRelayState(t, f, "return_to=%2Fdashboard")
	if relayState == "" {
		t.Fatalf("expected /login to set a non-empty RelayState")
	}

	session := &saml.Session{
		ID:     "session-4",
		NameID: "alice@example.com",
		CustomAttributes: []saml.Attribute{
			{Name: "email", Values: []saml.AttributeValue{{Value: "alice@example.com"}}},
		},
	}
	acsReq := idpInitiatedPostWithRelayState(t, f, session, "/auth/saml/test/acs", relayState)

	sessionClaims := jwtpkg.Mint(testCfg.Jwt, "app_user", time.Now(), testCfg.Jwt.MaxAge, nil)
	token, err := jwtpkg.Sign(testCfg.Jwt, sessionClaims)
	if err != nil {
		t.Fatalf("signing test session: %v", err)
	}
	acsReq.AddCookie(jwtpkg.CookieValue(testCfg.Jwt, token, sessionClaims, ""))

	rec := httptest.NewRecorder()
	samlAcsHandler(f.endpoint, testDb, testCfg, nil)(rec, acsReq)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var payload struct {
		Jwt   map[string]any `json:"jwt"`
		State map[string]any `json:"state"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decoding echoed payload: %v", err)
	}
	if payload.State["return_to"] != "/dashboard" {
		t.Errorf("state.return_to = %#v, want \"/dashboard\" — got payload %s", payload.State["return_to"], rec.Body.String())
	}
	if payload.Jwt["role"] != "app_user" {
		t.Errorf("jwt.role = %#v, want \"app_user\" — got payload %s", payload.Jwt["role"], rec.Body.String())
	}
}

// TestSaml_AcsIdpInitiatedHasNilState : an IdP-initiated login never went
// through /login, so there's no RelayState rel itself issued — the
// callback payload's state must be nil, not an error.
func TestSaml_AcsIdpInitiatedHasNilState(t *testing.T) {
	f := newSamlTestFixture(t, "https://app.example.com", true)
	f.endpoint.cfg.CallbackFunction = "auth.echo_payload"

	session := &saml.Session{
		ID:     "session-5",
		NameID: "alice@example.com",
		CustomAttributes: []saml.Attribute{
			{Name: "email", Values: []saml.AttributeValue{{Value: "alice@example.com"}}},
		},
	}
	acsReq := idpInitiatedPost(t, f, session, "/auth/saml/test/acs")

	rec := httptest.NewRecorder()
	samlAcsHandler(f.endpoint, testDb, testCfg, nil)(rec, acsReq)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var payload struct {
		State any `json:"state"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decoding echoed payload: %v", err)
	}
	if payload.State != nil {
		t.Errorf("state = %#v, want nil for an IdP-initiated login", payload.State)
	}
}
