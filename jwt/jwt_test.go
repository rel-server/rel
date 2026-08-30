package jwt

import (
	"maps"
	"strings"
	"testing"
	"time"

	jwtlib "github.com/golang-jwt/jwt/v5"

	"github.com/ceymard/rel/config"
)

func testCfg() config.Jwt {
	return config.Jwt{
		Secret:        "test-secret-value",
		CookieName:    "accesstoken",
		Algorithm:     "HS256",
		SameSite:      "Lax",
		MaxAge:        1800,
		RenewAfter:    0.5,
		MaxSessionAge: 604800,
	}
}

func TestSignVerify_RoundTrip(t *testing.T) {
	cfg := testCfg()
	now := time.Now().UTC()
	claims := Mint(cfg, "editor", now, cfg.MaxAge, map[string]any{"custom": "value"})

	token, err := Sign(cfg, claims)
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}

	verified, err := Verify(cfg, token)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if Role(verified) != "editor" {
		t.Errorf("expected role=editor, got %q", Role(verified))
	}
	if verified["custom"] != "value" {
		t.Errorf("expected custom claim to survive round-trip, got %v", verified["custom"])
	}
	if AuthTime(verified).Unix() != now.Unix() {
		t.Errorf("expected auth_time to survive round-trip, got %v want %v", AuthTime(verified), now)
	}
}

func TestVerify_WrongSecretFails(t *testing.T) {
	cfg := testCfg()
	claims := Mint(cfg, "editor", time.Now(), cfg.MaxAge, nil)
	token, err := Sign(cfg, claims)
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}

	wrongCfg := cfg
	wrongCfg.Secret = "a-completely-different-secret"
	if _, err := Verify(wrongCfg, token); err == nil {
		t.Fatalf("expected verify to fail with the wrong secret")
	}
}

func TestVerify_ExpiredTokenFails(t *testing.T) {
	cfg := testCfg()
	claims := Mint(cfg, "editor", time.Now(), -1, nil) // maxage -1s : already expired
	token, err := Sign(cfg, claims)
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}
	if _, err := Verify(cfg, token); err == nil {
		t.Fatalf("expected verify to fail on an expired token")
	}
}

// TestVerify_RejectsNoneAlgorithm covers the spec's explicit requirement :
// "rejects any token whose header claims a different one — including
// none — as an invalid signature."
func TestVerify_RejectsNoneAlgorithm(t *testing.T) {
	cfg := testCfg()
	claims := Mint(cfg, "editor", time.Now(), cfg.MaxAge, nil)
	unsigned := jwtlib.NewWithClaims(jwtlib.SigningMethodNone, claims)
	token, err := unsigned.SignedString(jwtlib.UnsafeAllowNoneSignatureType)
	if err != nil {
		t.Fatalf("building a none-alg token: %v", err)
	}
	if _, err := Verify(cfg, token); err == nil {
		t.Fatalf("expected verify to reject an alg=none token")
	}
}

// TestVerify_RejectsWrongHSVariant covers algorithm enforcement across
// different HMAC bit-widths, not just none vs HS256.
func TestVerify_RejectsWrongHSVariant(t *testing.T) {
	cfg := testCfg()
	other := cfg
	other.Algorithm = "HS512"
	claims := Mint(other, "editor", time.Now(), cfg.MaxAge, nil)
	token, err := Sign(other, claims)
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}
	// Verifying with cfg (HS256 configured) against an HS512-signed token.
	if _, err := Verify(cfg, token); err == nil {
		t.Fatalf("expected verify to reject a token signed with a different HS variant")
	}
}

func TestVerify_MaxSessionAgeCeiling(t *testing.T) {
	cfg := testCfg()
	cfg.MaxSessionAge = 10 // 10 seconds
	staleAuthTime := time.Now().Add(-20 * time.Second)
	// A long maxage keeps exp comfortably in the future ; only auth_time
	// (session age) should be what fails verification here.
	claims := Mint(cfg, "editor", staleAuthTime, 3600, nil)
	token, err := Sign(cfg, claims)
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}
	if _, err := Verify(cfg, token); err == nil {
		t.Fatalf("expected verify to fail once auth_time+maxsessionage has passed")
	}
}

func TestShouldRenew_ThresholdBoundary(t *testing.T) {
	cfg := testCfg()
	cfg.RenewAfter = 0.5

	// A token minted "now" with the full maxage window is not due for
	// renewal yet.
	fresh := Mint(cfg, "editor", time.Now(), cfg.MaxAge, nil)
	if ShouldRenew(cfg, fresh) {
		t.Errorf("expected a freshly-minted token to not be due for renewal")
	}

	// A token whose iat was maxage*0.6 seconds ago (well past the 0.5
	// threshold) IS due.
	past := Claims{}
	maps.Copy(past, fresh)
	old := time.Now().Add(-time.Duration(float64(cfg.MaxAge)*0.6) * time.Second)
	past["iat"] = float64(old.Unix())
	past["exp"] = float64(old.Add(time.Duration(cfg.MaxAge) * time.Second).Unix())
	if !ShouldRenew(cfg, past) {
		t.Errorf("expected a token past the renewafter threshold to be due for renewal")
	}
}

// TestRenew_ReusesOriginalWidth covers this session's resolution : a
// jwt_attrs.maxage override from the original mint "self-persists" across
// renewal because Renew reuses the CURRENT token's own exp-iat width, not
// cfg.Jwt.MaxAge.
func TestRenew_ReusesOriginalWidth(t *testing.T) {
	cfg := testCfg()
	customMaxage := 7200 // an override, different from cfg.MaxAge (1800)
	authTime := time.Now().Add(-time.Hour)
	original := Mint(cfg, "editor", authTime, customMaxage, map[string]any{"extra": "kept"})

	renewed := Renew(cfg, original)

	gotWidth := ExpiresAt(renewed).Sub(IssuedAt(renewed))
	wantWidth := time.Duration(customMaxage) * time.Second
	if gotWidth != wantWidth {
		t.Errorf("expected renewal to reuse the original %v width, got %v", wantWidth, gotWidth)
	}
	if AuthTime(renewed).Unix() != authTime.Unix() {
		t.Errorf("expected auth_time unchanged across renewal")
	}
	if Role(renewed) != "editor" {
		t.Errorf("expected role unchanged across renewal")
	}
	if renewed["extra"] != "kept" {
		t.Errorf("expected other claims carried over as-is, got %v", renewed["extra"])
	}
}

func TestCookieValue_MaxAgeMirrorsExpMinusIat(t *testing.T) {
	cfg := testCfg()
	claims := Mint(cfg, "editor", time.Now(), 900, nil)
	token, err := Sign(cfg, claims)
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}
	c := CookieValue(cfg, token, claims, "")
	if c.MaxAge != 900 {
		t.Errorf("expected cookie Max-Age to mirror exp-iat (900), got %d", c.MaxAge)
	}
	if c.SameSite != sameSite(cfg.SameSite) {
		t.Errorf("expected default samesite when no override given")
	}
}

func TestCookieValue_SameSiteOverride(t *testing.T) {
	cfg := testCfg()
	claims := Mint(cfg, "editor", time.Now(), cfg.MaxAge, nil)
	token, _ := Sign(cfg, claims)
	c := CookieValue(cfg, token, claims, "Strict")
	if c.SameSite != sameSite("Strict") {
		t.Errorf("expected the override samesite to apply")
	}
}

func TestClearCookie_ExpiresImmediately(t *testing.T) {
	cfg := testCfg()
	c := ClearCookie(cfg)
	if c.MaxAge >= 0 {
		t.Errorf("expected a negative Max-Age (immediate expiry), got %d", c.MaxAge)
	}
	if c.Value != "" {
		t.Errorf("expected an empty cookie value, got %q", c.Value)
	}
}

func TestSign_UnrecognizedAlgorithmErrors(t *testing.T) {
	cfg := testCfg()
	cfg.Algorithm = "RS256" // not one of the three accepted HMAC variants
	if _, err := Sign(cfg, Claims{}); err == nil {
		t.Fatalf("expected Sign to reject an unrecognized algorithm")
	}
}

func TestVerify_MissingRoleFails(t *testing.T) {
	cfg := testCfg()
	claims := Claims{
		"iat":       float64(time.Now().Unix()),
		"exp":       float64(time.Now().Add(time.Hour).Unix()),
		"auth_time": float64(time.Now().Unix()),
	}
	token, err := Sign(cfg, claims)
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}
	if _, err := Verify(cfg, token); err == nil {
		t.Fatalf("expected verify to fail on a token with no role claim")
	}
}

func TestVerify_TamperedTokenFails(t *testing.T) {
	cfg := testCfg()
	claims := Mint(cfg, "editor", time.Now(), cfg.MaxAge, nil)
	token, err := Sign(cfg, claims)
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		t.Fatalf("expected a 3-part JWT, got %d parts", len(parts))
	}
	tampered := parts[0] + "." + parts[1] + "." + parts[2][:len(parts[2])-2] + "xx"
	if _, err := Verify(cfg, tampered); err == nil {
		t.Fatalf("expected verify to fail on a tampered signature")
	}
}
