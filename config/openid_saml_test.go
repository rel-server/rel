package config

import (
	"testing"
)

// TestLoad_OpenidClientCredentials_DefaultFilePath covers specs/oauth-saml.md
// ## Configuration — OIDC's per-name $FILE$ default : client_id/client_secret
// need no explicit config line at all when the corresponding flat file
// exists at the deployment's own cwd (the default's second, local-dev
// candidate — /secrets/openid-<name>.id is assumed absent on the machine
// running this test, same assumption TestLoad_JwtSecretDefault_GenAndResolve
// makes about jwt.secret's own first candidate).
func TestLoad_OpenidClientCredentials_DefaultFilePath(t *testing.T) {
	t.Chdir(t.TempDir())
	writeFile(t, ".", "openid-myprovider.id", "the-client-id")
	writeFile(t, ".", "openid-myprovider.secret", "the-client-secret")

	cfgPath := writeFile(t, t.TempDir(), "rel.toml", `
[openid.myprovider]
issuer = "https://issuer.example.com"
`)
	cfg, err := Load([]string{"--config=" + cfgPath})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	p, ok := cfg.Openid["myprovider"]
	if !ok {
		t.Fatalf("expected an openid.myprovider entry, got %v", cfg.Openid)
	}
	if p.Issuer != "https://issuer.example.com" {
		t.Errorf("issuer = %q", p.Issuer)
	}
	if p.ClientID != "the-client-id" {
		t.Errorf("client_id = %q, want %q", p.ClientID, "the-client-id")
	}
	if p.ClientSecret != "the-client-secret" {
		t.Errorf("client_secret = %q, want %q", p.ClientSecret, "the-client-secret")
	}
}

// TestLoad_OpenidClientCredentials_ExplicitOverridesFile proves an
// explicitly configured client_id/client_secret wins over the $FILE$
// default entirely — never even attempts to read the file.
func TestLoad_OpenidClientCredentials_ExplicitOverridesFile(t *testing.T) {
	t.Chdir(t.TempDir())
	cfgPath := writeFile(t, t.TempDir(), "rel.toml", `
[openid.myprovider]
issuer = "https://issuer.example.com"
client_id = "explicit-id"
client_secret = "explicit-secret"
`)
	cfg, err := Load([]string{"--config=" + cfgPath})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	p := cfg.Openid["myprovider"]
	if p.ClientID != "explicit-id" || p.ClientSecret != "explicit-secret" {
		t.Errorf("got client_id=%q client_secret=%q, want the explicit values unresolved-through-$FILE$", p.ClientID, p.ClientSecret)
	}
}

// TestLoad_OpenidClientCredentials_MissingEverywhereIsAnError : no $GEN$
// fallback for these (specs/oauth-saml.md : "rel has no business
// fabricating one") — absent from config AND absent at both candidate
// paths must fail Load, not silently produce an empty ClientID/ClientSecret.
func TestLoad_OpenidClientCredentials_MissingEverywhereIsAnError(t *testing.T) {
	t.Chdir(t.TempDir())
	cfgPath := writeFile(t, t.TempDir(), "rel.toml", `
[openid.myprovider]
issuer = "https://issuer.example.com"
`)
	_, err := Load([]string{"--config=" + cfgPath})
	if err == nil {
		t.Fatalf("expected Load to fail when openid.myprovider.client_id/client_secret resolve nowhere")
	}
}

// TestLoad_OpenidDefaults_ScopesAndFetchUserinfo covers
// openid.<name>.scopes' default and fetch_userinfo's default false.
func TestLoad_OpenidDefaults_ScopesAndFetchUserinfo(t *testing.T) {
	t.Chdir(t.TempDir())
	writeFile(t, ".", "openid-myprovider.id", "id")
	writeFile(t, ".", "openid-myprovider.secret", "secret")
	cfgPath := writeFile(t, t.TempDir(), "rel.toml", `
[openid.myprovider]
issuer = "https://issuer.example.com"
`)
	cfg, err := Load([]string{"--config=" + cfgPath})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	p := cfg.Openid["myprovider"]
	wantScopes := []string{"openid", "email", "profile"}
	if len(p.Scopes) != len(wantScopes) {
		t.Fatalf("scopes = %v, want %v", p.Scopes, wantScopes)
	}
	for i, s := range wantScopes {
		if p.Scopes[i] != s {
			t.Errorf("scopes[%d] = %q, want %q", i, p.Scopes[i], s)
		}
	}
	if p.FetchUserinfo {
		t.Errorf("fetch_userinfo default should be false")
	}
}

// TestLoad_SamlCertPaths_DefaultFlatUnderSecrets covers specs/oauth-saml.md
// ## Configuration — SAML's flat-file default (no subdirectory).
func TestLoad_SamlCertPaths_DefaultFlatUnderSecrets(t *testing.T) {
	cfg, err := Load([]string{"--config=" + writeFile(t, t.TempDir(), "rel.toml", "")})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Saml.CertificatePath != "/secrets/saml-cert.pem:./saml-cert.pem" {
		t.Errorf("saml.certificate_path default = %q", cfg.Saml.CertificatePath)
	}
	if cfg.Saml.PrivateKeyPath != "/secrets/saml-cert.key:./saml-cert.key" {
		t.Errorf("saml.private_key_path default = %q", cfg.Saml.PrivateKeyPath)
	}
}

// TestLoad_SamlForceSignedRequests_DefaultsTrue covers specs/oauth-saml.md's
// "Why default true" : the SP always has a key, so signing costs nothing
// and is strictly more secure — an operator must opt OUT, not in.
func TestLoad_SamlForceSignedRequests_DefaultsTrue(t *testing.T) {
	cfgPath := writeFile(t, t.TempDir(), "rel.toml", `
[saml.myidp]
idp_metadata_url = "https://idp.example.com/metadata"
`)
	cfg, err := Load([]string{"--config=" + cfgPath})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	p, ok := cfg.Saml.Providers["myidp"]
	if !ok {
		t.Fatalf("expected a saml.myidp entry, got %v", cfg.Saml.Providers)
	}
	if !p.ForceSignedRequests {
		t.Errorf("force_signed_requests should default to true")
	}
	if p.IdpMetadataUrl != "https://idp.example.com/metadata" {
		t.Errorf("idp_metadata_url = %q", p.IdpMetadataUrl)
	}
}

// TestLoad_SamlForceSignedRequests_ExplicitFalse proves the default can
// still be opted out of for the rare IdP that can't accept signed requests.
func TestLoad_SamlForceSignedRequests_ExplicitFalse(t *testing.T) {
	cfgPath := writeFile(t, t.TempDir(), "rel.toml", `
[saml.myidp]
idp_metadata_url = "https://idp.example.com/metadata"
force_signed_requests = false
`)
	cfg, err := Load([]string{"--config=" + cfgPath})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Saml.Providers["myidp"].ForceSignedRequests {
		t.Errorf("expected force_signed_requests=false to stick")
	}
}

// TestLoad_SamlProviders_CertPathKeysAreNotMistakenForProviders proves
// saml.certificate_path/saml.private_key_path (the shared SP identity,
// not per-IdP) never leak into cfg.Saml.Providers as bogus named entries.
func TestLoad_SamlProviders_CertPathKeysAreNotMistakenForProviders(t *testing.T) {
	cfgPath := writeFile(t, t.TempDir(), "rel.toml", `
[saml]
certificate_path = "/custom/cert.pem"
private_key_path = "/custom/cert.key"

[saml.myidp]
idp_metadata_url = "https://idp.example.com/metadata"
`)
	cfg, err := Load([]string{"--config=" + cfgPath})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if _, ok := cfg.Saml.Providers["certificate_path"]; ok {
		t.Errorf("certificate_path leaked into Providers: %v", cfg.Saml.Providers)
	}
	if _, ok := cfg.Saml.Providers["private_key_path"]; ok {
		t.Errorf("private_key_path leaked into Providers: %v", cfg.Saml.Providers)
	}
	if len(cfg.Saml.Providers) != 1 {
		t.Fatalf("expected exactly one provider, got %v", cfg.Saml.Providers)
	}
	if cfg.Saml.CertificatePath != "/custom/cert.pem" {
		t.Errorf("certificate_path = %q", cfg.Saml.CertificatePath)
	}
	if cfg.Saml.PrivateKeyPath != "/custom/cert.key" {
		t.Errorf("private_key_path = %q", cfg.Saml.PrivateKeyPath)
	}
}

// TestLoad_SamlProviders_PerEntryCertOverride covers saml.<name>.
// certificate_path/private_key_path : an optional, per-entry override of
// the shared saml.certificate_path/private_key_path, empty (inheriting the
// shared pair) when unset.
func TestLoad_SamlProviders_PerEntryCertOverride(t *testing.T) {
	cfgPath := writeFile(t, t.TempDir(), "rel.toml", `
[saml]
certificate_path = "/shared/cert.pem"
private_key_path = "/shared/cert.key"

[saml.inherits]
idp_metadata_url = "https://a.example.com/metadata"

[saml.overrides]
idp_metadata_url = "https://b.example.com/metadata"
certificate_path = "/custom/b-cert.pem"
private_key_path = "/custom/b-cert.key"
`)
	cfg, err := Load([]string{"--config=" + cfgPath})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := cfg.Saml.Providers["inherits"].CertificatePath; got != "" {
		t.Errorf("saml.inherits.certificate_path should be empty (inherits the shared pair), got %q", got)
	}
	if got := cfg.Saml.Providers["inherits"].PrivateKeyPath; got != "" {
		t.Errorf("saml.inherits.private_key_path should be empty (inherits the shared pair), got %q", got)
	}
	if got := cfg.Saml.Providers["overrides"].CertificatePath; got != "/custom/b-cert.pem" {
		t.Errorf("saml.overrides.certificate_path = %q", got)
	}
	if got := cfg.Saml.Providers["overrides"].PrivateKeyPath; got != "/custom/b-cert.key" {
		t.Errorf("saml.overrides.private_key_path = %q", got)
	}
}

// TestLoad_SsoCallbackFallback_DefaultEmpty covers http.functions.sso_callback's
// default (disabled) and explicit-set path.
func TestLoad_SsoCallbackFallback_DefaultEmpty(t *testing.T) {
	cfg, err := Load([]string{"--config=" + writeFile(t, t.TempDir(), "rel.toml", "")})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Http.Functions.SsoCallback != "" {
		t.Errorf("expected empty default, got %q", cfg.Http.Functions.SsoCallback)
	}

	cfgPath := writeFile(t, t.TempDir(), "rel.toml", `
[http.functions]
sso_callback = "auth.sso_callback"
`)
	cfg, err = Load([]string{"--config=" + cfgPath})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Http.Functions.SsoCallback != "auth.sso_callback" {
		t.Errorf("sso_callback = %q", cfg.Http.Functions.SsoCallback)
	}
}

// TestLoad_PublicHost_GlobalDefaultAndPerEntryOverride covers
// docs/content/http/authentication.md ## OpenID Connect and SAML :
// http.public_host is a bare host with no default ; specs/oauth-saml.md
// ## Configuration — HTTP's openid.<name>.public_host/saml.<name>.public_host
// override it per entry when set.
func TestLoad_PublicHost_GlobalDefaultAndPerEntryOverride(t *testing.T) {
	cfg, err := Load([]string{"--config=" + writeFile(t, t.TempDir(), "rel.toml", "")})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Http.PublicHost != "" {
		t.Errorf("expected empty default, got %q", cfg.Http.PublicHost)
	}

	cfgPath := writeFile(t, t.TempDir(), "rel.toml", `
[http]
public_host = "app.example.com"

[openid.a]
issuer = "https://issuer.example.com"
client_id = "a-id"
client_secret = "a-secret"

[openid.b]
issuer = "https://issuer.example.com"
client_id = "b-id"
client_secret = "b-secret"
public_host = "b.example.com"

[saml.c]
idp_metadata_url = "https://idp.example.com/metadata"
public_host = "c.example.com"
`)
	cfg, err = Load([]string{"--config=" + cfgPath})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Http.PublicHost != "app.example.com" {
		t.Errorf("http.public_host = %q", cfg.Http.PublicHost)
	}
	if got := cfg.Openid["a"].PublicHost; got != "" {
		t.Errorf("openid.a.public_host should be empty (inherits the global default), got %q", got)
	}
	if got := cfg.Openid["b"].PublicHost; got != "b.example.com" {
		t.Errorf("openid.b.public_host = %q", got)
	}
	if got := cfg.Saml.Providers["c"].PublicHost; got != "c.example.com" {
		t.Errorf("saml.c.public_host = %q", got)
	}
}
