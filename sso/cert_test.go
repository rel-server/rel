package sso

import (
	"bytes"
	"encoding/pem"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func mustParseCertPEM(t *testing.T, data []byte) []byte {
	t.Helper()
	block, _ := pem.Decode(data)
	if block == nil {
		t.Fatalf("no PEM block found")
	}
	return block.Bytes
}

// captureWarnLogs installs a JSON slog handler for the duration of the
// test, same pattern logging/logging_test.go's own capture tests use.
func captureWarnLogs(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	original := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn})))
	t.Cleanup(func() { slog.SetDefault(original) })
	return &buf
}

// TestLoadOrGenerateSPKeyPair_GeneratesThenReusesSilently covers specs/oauth-saml.md
// ## Certificate's three numbered steps end to end : first call has no
// keypair on disk, generates and persists one with a loud WARN ; second
// call against the SAME paths loads the persisted keypair back, silently
// (no WARN), and gets byte-identical certificate/key material.
func TestLoadOrGenerateSPKeyPair_GeneratesThenReusesSilently(t *testing.T) {
	dir := t.TempDir()
	certPath := filepath.Join(dir, "cert.pem")
	keyPath := filepath.Join(dir, "cert.key")

	buf := captureWarnLogs(t)
	kp1, err := LoadOrGenerateSPKeyPair(certPath, keyPath, []string{"example.com"}, nil)
	if err != nil {
		t.Fatalf("first LoadOrGenerateSPKeyPair: %v", err)
	}
	if !strings.Contains(buf.String(), "generated a new SAML SP certificate") {
		t.Errorf("expected a WARN log on generation, got: %s", buf.String())
	}
	if _, err := os.Stat(certPath); err != nil {
		t.Errorf("expected certificate persisted at %s: %v", certPath, err)
	}
	if _, err := os.Stat(keyPath); err != nil {
		t.Errorf("expected key persisted at %s: %v", keyPath, err)
	}

	buf2 := captureWarnLogs(t)
	kp2, err := LoadOrGenerateSPKeyPair(certPath, keyPath, []string{"example.com"}, nil)
	if err != nil {
		t.Fatalf("second LoadOrGenerateSPKeyPair: %v", err)
	}
	if buf2.Len() != 0 {
		t.Errorf("expected silent reuse (no WARN) on second load, got: %s", buf2.String())
	}
	if !kp1.Certificate.Equal(kp2.Certificate) {
		t.Errorf("expected the same certificate to be reloaded, got a different one")
	}
	if kp1.Key.D.Cmp(kp2.Key.D) != 0 {
		t.Errorf("expected the same private key to be reloaded, got a different one")
	}
}

// TestLoadOrGenerateSPKeyPair_NoWritableDirectoryIsEphemeral covers ##
// Certificate's fallback : no candidate directory exists at all still
// returns a usable (ephemeral, unpersisted) keypair, loudly warned, rather
// than failing outright.
func TestLoadOrGenerateSPKeyPair_NoWritableDirectoryIsEphemeral(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "no-such-dir", "cert.pem")
	missingKey := filepath.Join(t.TempDir(), "no-such-dir", "cert.key")

	buf := captureWarnLogs(t)
	kp, err := LoadOrGenerateSPKeyPair(missing, missingKey, nil, nil)
	if err != nil {
		t.Fatalf("LoadOrGenerateSPKeyPair: %v", err)
	}
	if kp == nil || kp.Certificate == nil {
		t.Fatalf("expected a usable ephemeral keypair, got %v", kp)
	}
	if !strings.Contains(buf.String(), "EPHEMERAL") {
		t.Errorf("expected an EPHEMERAL warning, got: %s", buf.String())
	}
}

// TestLoadOrGenerateSPKeyPair_BringYourOwnCertificate covers the
// bring-your-own-certificate path : a keypair already present at the
// configured location is used unchanged, never regenerated.
func TestLoadOrGenerateSPKeyPair_BringYourOwnCertificate(t *testing.T) {
	dir := t.TempDir()
	certPath := filepath.Join(dir, "own-cert.pem")
	keyPath := filepath.Join(dir, "own-cert.key")

	// Seed the "bring your own" files by generating once into a
	// different pair of paths, then copying them into place.
	seedDir := t.TempDir()
	seedCert := filepath.Join(seedDir, "cert.pem")
	seedKey := filepath.Join(seedDir, "cert.key")
	if _, err := LoadOrGenerateSPKeyPair(seedCert, seedKey, nil, nil); err != nil {
		t.Fatalf("seeding: %v", err)
	}
	certBytes, err := os.ReadFile(seedCert)
	if err != nil {
		t.Fatalf("reading seeded cert: %v", err)
	}
	keyBytes, err := os.ReadFile(seedKey)
	if err != nil {
		t.Fatalf("reading seeded key: %v", err)
	}
	if err := os.WriteFile(certPath, certBytes, 0o600); err != nil {
		t.Fatalf("writing bring-your-own cert: %v", err)
	}
	if err := os.WriteFile(keyPath, keyBytes, 0o600); err != nil {
		t.Fatalf("writing bring-your-own key: %v", err)
	}

	buf := captureWarnLogs(t)
	kp, err := LoadOrGenerateSPKeyPair(certPath, keyPath, nil, nil)
	if err != nil {
		t.Fatalf("LoadOrGenerateSPKeyPair: %v", err)
	}
	if buf.Len() != 0 {
		t.Errorf("expected no WARN when a keypair is already present, got: %s", buf.String())
	}
	if kp.Certificate.SerialNumber.Cmp(kp.Certificate.SerialNumber) != 0 {
		t.Fatalf("sanity check failed")
	}
}

// TestLoadOrGenerateSPKeyPair_MultiPathSearchList_FirstExistingWins covers
// the colon-separated candidate search list — the SECOND candidate holds
// an existing keypair, so it must win over generating a fresh one at the
// first (missing) candidate.
func TestLoadOrGenerateSPKeyPair_MultiPathSearchList_FirstExistingWins(t *testing.T) {
	dir := t.TempDir()
	secondCert := filepath.Join(dir, "second-cert.pem")
	secondKey := filepath.Join(dir, "second-cert.key")
	if _, err := LoadOrGenerateSPKeyPair(secondCert, secondKey, nil, nil); err != nil {
		t.Fatalf("seeding second candidate: %v", err)
	}

	firstCert := filepath.Join(t.TempDir(), "no-such-dir", "first-cert.pem")
	firstKey := filepath.Join(t.TempDir(), "no-such-dir", "first-cert.key")

	buf := captureWarnLogs(t)
	kp, err := LoadOrGenerateSPKeyPair(firstCert+":"+secondCert, firstKey+":"+secondKey, nil, nil)
	if err != nil {
		t.Fatalf("LoadOrGenerateSPKeyPair: %v", err)
	}
	if buf.Len() != 0 {
		t.Errorf("expected the existing second candidate to be found silently, got: %s", buf.String())
	}
	want, err := os.ReadFile(secondCert)
	if err != nil {
		t.Fatalf("reading second candidate: %v", err)
	}
	if !bytes.Equal(kp.Certificate.Raw, mustParseCertPEM(t, want)) {
		t.Errorf("expected the second candidate's certificate to be loaded")
	}
}
