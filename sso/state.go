// /login's query string, carried through OIDC's `state` param (after the CSRF token) or SAML's RelayState, and
// handed to the callback function as the payload's "state".
package sso

import (
	"encoding/base64"
	"encoding/json"

	"github.com/rel-server/rel/querystring"
)

// decodeLoginState decodes /login's raw query string like GET /rel's query field : dotted keys nest, empty is nil.
// The result is attacker-controlled, never trusted.
func decodeLoginState(rawQuery string) (any, error) {
	return querystring.DecodeQueryField(rawQuery)
}

// encodeStateForTransit encodes state for OIDC's `state` param as unpadded base64url JSON, whose alphabet has no
// '.' to clash with the CSRF token separator. "" for a nil state.
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

// decodeStateFromTransit reverses encodeStateForTransit ; "" decodes to nil.
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

// encodeRelayState encodes state as plain JSON for SAML's RelayState, which carries nothing else ; "" for a nil
// state, meaning no RelayState.
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

// decodeRelayState reverses encodeRelayState. A value that is not JSON is no state : an IdP-initiated login
// carries the IdP's own RelayState, if any.
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
