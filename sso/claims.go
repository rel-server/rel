// Package sso implements the /auth/oidc/{name}/* and /auth/saml/{name}/* endpoints. This file is the part both
// protocols share : calling the callback function and writing its RelHttpResponse.
package sso

import (
	"encoding/json"
	"net/http"

	"github.com/rel-server/rel/config"
	"github.com/rel-server/rel/dbauth"
	"github.com/rel-server/rel/errcode"
	"github.com/rel-server/rel/httperr"
	jwtpkg "github.com/rel-server/rel/jwt"
	"github.com/rel-server/rel/logging"
	"github.com/rel-server/rel/pg"
	"github.com/rel-server/rel/route"
)

var log = logging.For("sso")

// ssoIdentity is what the SSO round trip verified, under the callback payload's "identity" key.
type ssoIdentity struct {
	Protocol     string         `json:"protocol"`
	Name         string         `json:"name"`
	Claims       map[string]any `json:"claims"`
	AccessToken  string         `json:"access_token,omitempty"`
	RefreshToken string         `json:"refresh_token,omitempty"`
}

// ssoCallbackPayload is the jsonb argument of the callback function :
//
//   - Jwt is the browser's current session, read off the callback request, never passed through the IdP ;
//     nil for none. Under jwt.same_site=Lax a SAML /acs POST is cross-site, so it is always nil there.
//   - Identity is what the SSO round trip verified.
//   - State is /login's decoded query string : attacker-controlled and IdP-visible, never trusted.
type ssoCallbackPayload struct {
	Jwt      jwtpkg.Claims `json:"jwt"`
	Identity ssoIdentity   `json:"identity"`
	State    any           `json:"state"`
}

// resolveCallbackFunction picks the entry's callback_function, else http.functions.sso_callback.
func resolveCallbackFunction(perEntry string, cfg *config.Config) string {
	if perEntry != "" {
		return perEntry
	}
	return cfg.Http.Functions.SsoCallback
}

// invokeCallback calls the resolved function with identity, state and the session read from r, and writes the
// RelHttpResponse it returns like a /route response. No function configured is a 500.
func invokeCallback(w http.ResponseWriter, r *http.Request, cfg *config.Config, db *pg.DbInfos, templates *route.TemplateSet, functionName string, identity ssoIdentity, state any) {
	if functionName == "" {
		log.Error("sso: no callback function configured for this endpoint, and http.functions.sso_callback is also unset", "protocol", identity.Protocol, "name", identity.Name)
		httperr.WritePlain(w, http.StatusInternalServerError, errcode.Internal, "no SSO callback function configured")
		return
	}

	claims, _ := jwtpkg.VerifyRequest(cfg.Jwt, r)
	payload, err := json.Marshal(ssoCallbackPayload{Jwt: claims, Identity: identity, State: state})
	if err != nil {
		httperr.WritePlain(w, http.StatusInternalServerError, errcode.Internal, "encoding SSO callback payload")
		return
	}

	// Not reqauth's role-scoped transaction : the user is not signed in yet, so the callback runs as the
	// connection's own role, and is "security definer" to read what it needs.
	raw, err := dbauth.CallJSONBFunctionReturningJSON(r.Context(), db.Pool, functionName, payload)
	if err != nil {
		httperr.WritePostgres(r.Context(), w, err, cfg.Dev)
		return
	}

	route.WriteRelHttpResponse(w, r, cfg, functionName, raw, templates)
}
