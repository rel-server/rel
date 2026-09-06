// Package sso implements specs/oauth-saml.md : the /auth/oidc/{name}/*
// and /auth/saml/{name}/* endpoints. This file is the part both protocols
// converge on — ## Callback function's payload shape and invocation, and
// writing the RelHttpResponse it returns.
package sso

import (
	"encoding/json"
	"net/http"

	"github.com/rel-server/rel/config"
	"github.com/rel-server/rel/dbauth"
	"github.com/rel-server/rel/errcode"
	jwtpkg "github.com/rel-server/rel/jwt"
	"github.com/rel-server/rel/logging"
	"github.com/rel-server/rel/pg"
	"github.com/rel-server/rel/pgerr"
	"github.com/rel-server/rel/route"
)

var log = logging.For("sso")

// ssoIdentity is specs/oauth-saml.md ## Callback payload shape's per-protocol
// answer — what the SSO round trip itself actually verified — nested
// under the callback payload's "identity" key, never flattened into it
// directly : keeping "claims" meaning exactly one thing (the protocol-
// supplied identity claims) regardless of nesting depth is worth the
// extra level, rather than a payload.claims.claims stutter.
type ssoIdentity struct {
	Protocol     string         `json:"protocol"`
	Name         string         `json:"name"`
	Claims       map[string]any `json:"claims"`
	AccessToken  string         `json:"access_token,omitempty"`
	RefreshToken string         `json:"refresh_token,omitempty"`
}

// ssoCallbackPayload is the jsonb argument handed to the configured
// callback function, docs/content/http/authentication.md ## OpenID
// Connect and SAML's SsoCallback shape :
//
//   - Jwt is the browser's OWN current session, read directly off the
//     callback request the same way jwt.VerifyRequest reads any other
//     request's — NEVER threaded through the IdP (that would leak a live
//     session credential to a third party). nil (encodes as JSON null)
//     for no session, which is the expected case for SAML's callback
//     under rel's default jwt.same_site=Lax : that cookie isn't sent on a
//     cross-site POST, which /acs always is, so a callback function can't
//     assume Jwt reflects reality there unless the deployment has set
//     jwt.same_site=None.
//   - Identity is what the SSO round trip verified — see ssoIdentity.
//   - State is whatever /login's own query string decoded to (see
//     state.go) — attacker-influenceable and IdP-visible, same trust
//     level as any other query string ; never something rel itself
//     trusts or acts on.
type ssoCallbackPayload struct {
	Jwt      jwtpkg.Claims `json:"jwt"`
	Identity ssoIdentity   `json:"identity"`
	State    any           `json:"state"`
}

// resolveCallbackFunction is ## Callback function's fallback rule :
// per-entry callback_function first, http.functions.sso_callback second.
func resolveCallbackFunction(perEntry string, cfg *config.Config) string {
	if perEntry != "" {
		return perEntry
	}
	return cfg.Http.Functions.SsoCallback
}

// invokeCallback calls the resolved function with the assembled
// ssoCallbackPayload (Jwt filled in here, from r itself — callers only
// ever supply identity/state), writing the resulting RelHttpResponse
// (mint/cookies/headers, exactly like an ordinary /route function's own
// response — WriteRelHttpResponse is route's shared implementation, see
// route/encode.go's own doc comment on why it no longer needs a
// discovered Route to do this). No function configured at all is a 500,
// per ## Callback function's own "always 500s until a function is
// configured".
func invokeCallback(w http.ResponseWriter, r *http.Request, cfg *config.Config, db *pg.DbInfos, templates *route.TemplateSet, functionName string, identity ssoIdentity, state any) {
	if functionName == "" {
		log.Error("sso: no callback function configured for this endpoint, and http.functions.sso_callback is also unset", "protocol", identity.Protocol, "name", identity.Name)
		writePlainError(w, http.StatusInternalServerError, errcode.Internal, "no SSO callback function configured")
		return
	}

	claims, _ := jwtpkg.VerifyRequest(cfg.Jwt, r)
	payload, err := json.Marshal(ssoCallbackPayload{Jwt: claims, Identity: identity, State: state})
	if err != nil {
		writePlainError(w, http.StatusInternalServerError, errcode.Internal, "encoding SSO callback payload")
		return
	}

	raw, err := dbauth.CallJSONBFunctionReturningJSON(r.Context(), db.Pool, functionName, payload)
	if err != nil {
		writeErrorForPgErr(w, err, cfg.Dev)
		return
	}

	route.WriteRelHttpResponse(w, r, cfg, functionName, raw, templates)
}

// writePlainError/writeErrorForPgErr mirror route/requestbody.go's own
// unexported helpers of the same name — deliberately duplicated rather
// than exported across packages, same "different response envelope per
// package" precedent route/encode.go's writePlainError doc comment
// already sets against server/response.go's own JSON envelope.
func writePlainError(w http.ResponseWriter, status int, code errcode.Code, message string) {
	w.Header().Set(errcode.Header, string(code))
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(status)
	_, _ = w.Write([]byte(message))
}

func writeErrorForPgErr(w http.ResponseWriter, err error, dev bool) {
	status, code, tier, detail, ok := pgerr.Classify(err)
	if !ok {
		writePlainError(w, http.StatusInternalServerError, errcode.Internal, "internal error")
		return
	}
	message := detail.Message
	if pgerr.IsRSCode(code) {
		writePlainError(w, status, code, message)
		return
	}
	if !pgerr.AllowsDetail(tier, dev) {
		message = pgerr.FallbackMessage(tier)
	} else if tier == pgerr.TierConstraintViolation && detail.Detail != "" {
		message = detail.Message + ": " + detail.Detail
	}
	writePlainError(w, status, code, message)
}
