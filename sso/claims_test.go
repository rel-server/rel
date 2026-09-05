package sso

import (
	"reflect"
	"testing"

	"github.com/crewjam/saml"

	"github.com/ceymard/rel/config"
)

func TestMergeUserinfoClaims_UserinfoWinsOnCollision(t *testing.T) {
	idToken := map[string]any{"email": "idtoken@example.com", "sub": "123"}
	userinfo := map[string]any{"email": "userinfo@example.com", "name": "Alice"}

	got := mergeUserinfoClaims(idToken, userinfo)

	want := map[string]any{"email": "userinfo@example.com", "sub": "123", "name": "Alice"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("mergeUserinfoClaims = %v, want %v", got, want)
	}
	// idToken itself must be untouched.
	if idToken["email"] != "idtoken@example.com" {
		t.Errorf("mergeUserinfoClaims mutated its idTokenClaims argument in place")
	}
}

func TestExtractSamlClaims_AttributesAreAlwaysStringArrays(t *testing.T) {
	assertion := &saml.Assertion{
		Subject: &saml.Subject{NameID: &saml.NameID{Value: "alice@example.com"}},
		AttributeStatements: []saml.AttributeStatement{
			{Attributes: []saml.Attribute{
				{Name: "email", Values: []saml.AttributeValue{{Value: "alice@example.com"}}},
				{Name: "groups", Values: []saml.AttributeValue{{Value: "admins"}, {Value: "users"}}},
			}},
		},
	}

	claims := extractSamlClaims(assertion)

	email, ok := claims["email"].([]string)
	if !ok || len(email) != 1 || email[0] != "alice@example.com" {
		t.Errorf("email = %#v, want single-element string[]", claims["email"])
	}
	groups, ok := claims["groups"].([]string)
	if !ok || len(groups) != 2 || groups[0] != "admins" || groups[1] != "users" {
		t.Errorf("groups = %#v, want [admins users]", claims["groups"])
	}
	nameID, ok := claims["NameID"].([]string)
	if !ok || len(nameID) != 1 || nameID[0] != "alice@example.com" {
		t.Errorf("NameID = %#v, want [alice@example.com]", claims["NameID"])
	}
}

func TestExtractSamlClaims_NoSubjectNoNameID(t *testing.T) {
	assertion := &saml.Assertion{}
	claims := extractSamlClaims(assertion)
	if _, ok := claims["NameID"]; ok {
		t.Errorf("expected no NameID key when Subject is nil, got %v", claims)
	}
}

func TestResolveCallbackFunction_PerEntryWinsOverFallback(t *testing.T) {
	cfg := &config.Config{Http: config.Http{Functions: config.HttpFunctions{SsoCallback: "auth.fallback"}}}
	if got := resolveCallbackFunction("auth.specific", cfg); got != "auth.specific" {
		t.Errorf("got %q, want auth.specific", got)
	}
	if got := resolveCallbackFunction("", cfg); got != "auth.fallback" {
		t.Errorf("got %q, want auth.fallback (the fallback)", got)
	}
}
