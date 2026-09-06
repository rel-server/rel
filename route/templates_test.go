package route

import "testing"

// TestTemplateDataFromSingleReturn_TextIsWrapped is a regression test : a
// text/plain single-return route's raw bytes are not valid JSON on their
// own (e.g. hello, not "hello"), so templateDataValue silently unmarshaled
// them into nil before this wrapping existed.
func TestTemplateDataFromSingleReturn_TextIsWrapped(t *testing.T) {
	raw := templateDataFromSingleReturn([]byte("hello"), "text/plain")
	v := templateDataValue(raw)
	s, ok := v.(string)
	if !ok || s != "hello" {
		t.Fatalf("expected the string %q, got %#v (raw=%s)", "hello", v, raw)
	}
}

// TestTemplateDataFromSingleReturn_JSONPassesThrough covers jsonb/json
// returns, which are already valid JSON and must not be re-wrapped.
func TestTemplateDataFromSingleReturn_JSONPassesThrough(t *testing.T) {
	raw := templateDataFromSingleReturn([]byte(`{"a":1}`), "application/json")
	v := templateDataValue(raw)
	m, ok := v.(map[string]any)
	if !ok || m["a"] != float64(1) {
		t.Fatalf("expected the decoded object, got %#v (raw=%s)", v, raw)
	}
}

// TestTemplateDataFromSingleReturn_MimeDomainOverTextIsWrapped covers a
// mimetype domain over a text underlying (e.g. a "text/csv" domain) —
// classifySingleReturnType returns the domain name as contentType, not
// "text/plain" or "application/json", so the wrap must key off "is this
// actually JSON", not an exact "text/plain" match.
func TestTemplateDataFromSingleReturn_MimeDomainOverTextIsWrapped(t *testing.T) {
	raw := templateDataFromSingleReturn([]byte("a,b,c"), "text/csv")
	v := templateDataValue(raw)
	s, ok := v.(string)
	if !ok || s != "a,b,c" {
		t.Fatalf("expected the string %q, got %#v (raw=%s)", "a,b,c", v, raw)
	}
}
