package sso

import (
	"reflect"
	"testing"
)

func TestDecodeLoginState_EmptyQueryIsNil(t *testing.T) {
	got, err := decodeLoginState("")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != nil {
		t.Errorf("expected nil for an empty query string, got %#v", got)
	}
}

func TestDecodeLoginState_DottedKeysNest(t *testing.T) {
	got, err := decodeLoginState("return_to=/dashboard&flags.remember=yes")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	m, ok := got.(map[string]any)
	if !ok {
		t.Fatalf("expected a map, got %#v", got)
	}
	if m["return_to"] != "/dashboard" {
		t.Errorf("return_to = %#v", m["return_to"])
	}
	// The structural layer never coerces scalar types (that's the filter
	// expression grammar's job, not involved here) — a value decodes as a
	// plain string regardless of what it looks like.
	flags, ok := m["flags"].(map[string]any)
	if !ok || flags["remember"] != "yes" {
		t.Errorf("flags.remember = %#v", m["flags"])
	}
}

// TestStateForTransit_OIDC_RoundTrips proves encodeStateForTransit/
// decodeStateFromTransit are exact inverses, and that the encoded form
// never contains a '.' — oidcCallbackHandler's own csrfToken/appState
// split (strings.Cut on the first '.') depends on that.
func TestStateForTransit_OIDC_RoundTrips(t *testing.T) {
	original := map[string]any{"return_to": "/dashboard", "n": float64(3)}

	encoded, err := encodeStateForTransit(original)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	if encoded == "" {
		t.Fatalf("expected a non-empty encoding for a non-nil state")
	}
	for _, r := range encoded {
		if r == '.' {
			t.Fatalf("encoded state must never contain '.', got %q", encoded)
		}
	}

	decoded, err := decodeStateFromTransit(encoded)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !reflect.DeepEqual(decoded, original) {
		t.Errorf("round trip = %#v, want %#v", decoded, original)
	}
}

func TestStateForTransit_OIDC_NilRoundTripsToEmptyString(t *testing.T) {
	encoded, err := encodeStateForTransit(nil)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	if encoded != "" {
		t.Errorf("expected \"\" for a nil state, got %q", encoded)
	}
	decoded, err := decodeStateFromTransit("")
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if decoded != nil {
		t.Errorf("expected nil, got %#v", decoded)
	}
}

func TestDecodeStateFromTransit_MalformedIsAnError(t *testing.T) {
	if _, err := decodeStateFromTransit("not-valid-base64url!!!"); err == nil {
		t.Errorf("expected an error decoding malformed transit state")
	}
}

// TestRelayState_RoundTrips is encodeRelayState/decodeRelayState's own
// version : plain JSON, no base64 (RelayState shares its slot with
// nothing else, unlike OIDC's state param — see state.go).
func TestRelayState_RoundTrips(t *testing.T) {
	original := map[string]any{"return_to": "/dashboard"}
	encoded, err := encodeRelayState(original)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	if encoded == "" {
		t.Fatalf("expected a non-empty encoding for a non-nil state")
	}
	if got := decodeRelayState(encoded); !reflect.DeepEqual(got, original) {
		t.Errorf("round trip = %#v, want %#v", got, original)
	}
}

func TestRelayState_NilRoundTripsToEmptyString(t *testing.T) {
	encoded, err := encodeRelayState(nil)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	if encoded != "" {
		t.Errorf("expected \"\" for a nil state, got %q", encoded)
	}
	if got := decodeRelayState(""); got != nil {
		t.Errorf("expected nil, got %#v", got)
	}
}

// TestDecodeRelayState_MalformedIsTreatedAsNoState : an IdP-initiated
// login (specs/oauth-saml.md ## Endpoints) never went through /login, so
// there's no rel-issued RelayState to decode — malformed/unexpected input
// degrades to "no state" rather than rejecting the login outright.
func TestDecodeRelayState_MalformedIsTreatedAsNoState(t *testing.T) {
	if got := decodeRelayState("not json at all"); got != nil {
		t.Errorf("expected nil for malformed RelayState, got %#v", got)
	}
}
