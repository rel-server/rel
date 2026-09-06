// This file implements docs/content/http/authentication.md ## Passing
// state through login : /login's own query string, round-tripped through
// whichever protocol-native continuation-data carrier survives that
// protocol's callback request (OIDC's `state` param, composed alongside
// rel's own CSRF token ; SAML's RelayState, otherwise unused), and handed
// back to the callback function verbatim as the payload's "state" field.
package sso

import (
	"encoding/base64"
	"encoding/json"

	"github.com/rel-server/rel/querystring"
)

// decodeLoginState decodes /login's raw query string (r.URL.RawQuery) the
// same structural way GET /rel's own query field is (querystring.
// DecodeQueryField) — dotted keys nest, an empty query string decodes to
// nil ("no state at all", not "an empty object"). This is the ONLY
// validation performed on it : the result is attacker-influenceable, IdP-
// visible data, per ## Passing state through login's security note, never
// something rel itself trusts.
func decodeLoginState(rawQuery string) (any, error) {
	return querystring.DecodeQueryField(rawQuery)
}

// encodeStateForTransit turns a decoded state value into the compact,
// URL-safe string OIDC's `state` param actually carries : base64url
// (unpadded — its alphabet has no '.', which is what lets the callback
// split rel's own CSRF token from this cleanly, see decodeOidcReturnedState)
// of the value's JSON encoding. "" for a nil state — meaning nothing is
// appended to the CSRF token at all, byte-identical to a request that
// never had this feature.
func encodeStateForTransit(state any) (string, error) {
	if state == nil {
		return "", nil
	}
	buf, err := json.Marshal(state)
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

// decodeStateFromTransit reverses encodeStateForTransit ; "" decodes to
// nil, matching "no state was ever appended."
func decodeStateFromTransit(encoded string) (any, error) {
	if encoded == "" {
		return nil, nil
	}
	buf, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		return nil, err
	}
	var v any
	if err := json.Unmarshal(buf, &v); err != nil {
		return nil, err
	}
	return v, nil
}

// encodeRelayState is SAML's own, simpler version of encodeStateForTransit :
// RelayState carries nothing else (no CSRF token sharing its slot — SAML's
// CSRF/replay protection comes from the signed assertion and InResponseTo,
// never from RelayState), so it's just the state value's plain JSON, not
// base64 — "" for a nil state, which MakeRedirectAuthenticationRequest("")
// already treats as "no RelayState at all," byte-identical to before this
// feature existed.
func encodeRelayState(state any) (string, error) {
	if state == nil {
		return "", nil
	}
	buf, err := json.Marshal(state)
	if err != nil {
		return "", err
	}
	return string(buf), nil
}

// decodeRelayState reverses encodeRelayState. A value that fails to
// decode as JSON is treated as "no state" rather than a hard error : an
// IdP-initiated login (specs/oauth-saml.md ## Endpoints) never went
// through /login at all, so there was never a rel-issued RelayState to
// begin with, and some IdPs echo back their own RelayState convention
// regardless — neither is an attack, just absence of this feature's data.
func decodeRelayState(raw string) any {
	if raw == "" {
		return nil
	}
	var v any
	if err := json.Unmarshal([]byte(raw), &v); err != nil {
		log.Debug("sso: RelayState did not decode as JSON, treating as no state", "error", err.Error())
		return nil
	}
	return v
}
