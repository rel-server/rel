// Package sso implements specs/oauth-saml.md : the /auth/oidc/{name}/*
// and /auth/saml/{name}/* endpoints. This file is the part both protocols
// converge on — ## Claims shape's SsoClaims JSON, ## Callback function's
// invocation, and writing the RelHttpResponse it returns.
package sso

import (
	"encoding/json"
	"net/http"

	"github.com/ceymard/rel/config"
	"github.com/ceymard/rel/dbauth"
	"github.com/ceymard/rel/errcode"
	"github.com/ceymard/rel/logging"
	"github.com/ceymard/rel/pg"
	"github.com/ceymard/rel/pgerr"
	"github.com/ceymard/rel/route"
)

var log = logging.For("sso")

// ssoClaims is specs/oauth-saml.md ## Claims shape's SsoClaims, passed as
// the callback function's own jsonb argument verbatim.
type ssoClaims struct {
	Protocol     string         `json:"protocol"`
	Name         string         `json:"name"`
	Claims       map[string]any `json:"claims"`
	AccessToken  string         `json:"access_token,omitempty"`
	RefreshToken string         `json:"refresh_token,omitempty"`
}

// resolveCallbackFunction is ## Callback function's fallback rule :
// per-entry callback_function first, http.functions.sso_callback second.
func resolveCallbackFunction(perEntry string, cfg *config.Config) string {
	if perEntry != "" {
		return perEntry
	}
	return cfg.Http.Functions.SsoCallback
}

// invokeCallback calls the resolved function with claims, writing the
// resulting RelHttpResponse (mint/cookies/headers, exactly like an
// ordinary /route function's own response — WriteRelHttpResponse is
// route's shared implementation, see route/encode.go's own doc comment on
// why it no longer needs a discovered Route to do this). No function
// configured at all is a 500, per ## Callback function's own "always 500s
// until a function is configured".
func invokeCallback(w http.ResponseWriter, r *http.Request, cfg *config.Config, db *pg.DbInfos, templates *route.TemplateSet, functionName string, claims ssoClaims) {
	if functionName == "" {
		log.Error("sso: no callback function configured for this endpoint, and http.functions.sso_callback is also unset", "protocol", claims.Protocol, "name", claims.Name)
		writePlainError(w, http.StatusInternalServerError, errcode.Internal, "no SSO callback function configured")
		return
	}

	payload, err := json.Marshal(claims)
	if err != nil {
		writePlainError(w, http.StatusInternalServerError, errcode.Internal, "encoding SSO claims")
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
