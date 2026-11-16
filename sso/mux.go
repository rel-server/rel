package sso

import (
	"github.com/go-chi/chi/v5"

	"github.com/rel-server/rel/config"
	"github.com/rel-server/rel/pg"
	"github.com/rel-server/rel/route"
	"github.com/rel-server/rel/wellknown"
)

// resolveHost applies the per-entry override rule : an entry's own public_host wins when set, otherwise
// http.public_host ; "" means neither is configured for this entry.
func resolveHost(perEntry, global string) string {
	if perEntry != "" {
		return perEntry
	}
	return global
}

// rootURLFor builds the "https://<host>" root of OIDC redirect_uri and SAML URLs ; always https, since rel
// sits behind a TLS-terminating proxy.
func rootURLFor(host string) string {
	return "https://" + host
}

// Mount registers every configured openid.<name>/saml.<name> endpoint onto router ; with no entry, nothing is
// registered and no certificate generated. An entry with no host is skipped and logged.
func Mount(router chi.Router, db *pg.DbInfos, cfg *config.Config, wkReg *wellknown.Registry) {
	if len(cfg.Openid) == 0 && len(cfg.Saml.Providers) == 0 {
		return
	}
	if cfg.Http.PublicHost == "" && !anyPerEntryHostConfigured(cfg) {
		log.Warn("sso: no http.public_host configured and no per-entry public_host override set either — every openid/saml entry will be skipped")
	}
	if cfg.Http.Functions.SsoCallback == "" && !anyPerEntryCallbackConfigured(cfg) {
		log.Warn("sso: no http.functions.sso_callback configured and no per-entry callback_function set either — every /auth/{oidc,saml}/*/{callback,acs} will 500 until one is configured")
	}

	templates := route.NewTemplateSet(cfg, nil, db, wkReg)

	mountOidc(router, db, cfg, templates)

	if len(cfg.Saml.Providers) > 0 {
		kp, err := LoadOrGenerateSPKeyPair(cfg.Saml.CertificatePath, cfg.Saml.PrivateKeyPath, samlHostnames(cfg), nil)
		if err != nil {
			log.Error("sso: could not load or generate the SAML SP certificate — no /auth/saml/* routes mounted", "error", err.Error())
			return
		}
		mountSaml(router, db, cfg, kp, templates)
	}
}

func anyPerEntryCallbackConfigured(cfg *config.Config) bool {
	for _, p := range cfg.Openid {
		if p.CallbackFunction != "" {
			return true
		}
	}
	for _, p := range cfg.Saml.Providers {
		if p.CallbackFunction != "" {
			return true
		}
	}
	return false
}

func anyPerEntryHostConfigured(cfg *config.Config) bool {
	for _, p := range cfg.Openid {
		if p.PublicHost != "" {
			return true
		}
	}
	for _, p := range cfg.Saml.Providers {
		if p.PublicHost != "" {
			return true
		}
	}
	return false
}

// samlHostnames collects the distinct hosts of the saml.<name> entries, for the generated certificate's DNSNames.
func samlHostnames(cfg *config.Config) []string {
	seen := map[string]bool{}
	var hosts []string
	for _, p := range cfg.Saml.Providers {
		h := resolveHost(p.PublicHost, cfg.Http.PublicHost)
		if h != "" && !seen[h] {
			seen[h] = true
			hosts = append(hosts, h)
		}
	}
	return hosts
}
