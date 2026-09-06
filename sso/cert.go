// This file implements specs/oauth-saml.md ## Certificate : load the
// deployment's SAML SP keypair if one exists at the configured paths,
// otherwise generate and persist a self-signed one — silent reuse on
// every later boot, loud WARN only the first time a keypair is generated.
package sso

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"log/slog"
	"math/big"
	"os"
	"path/filepath"
	"time"

	"github.com/rel-server/rel/config"
)

// spKeyPair is the deployment's own SAML SP identity : Key signs outgoing
// AuthnRequests (specs/oauth-saml.md's force_signed_requests) and
// Certificate is what's published at /auth/saml/{name}/metadata for an IdP
// administrator to trust.
type spKeyPair struct {
	Key         *rsa.PrivateKey
	Certificate *x509.Certificate
}

// LoadOrGenerateSPKeyPair implements ## Certificate's three numbered
// steps : try every (certPathList, keyPathList) candidate pair, in order,
// for an existing readable keypair ; generate one if none was found,
// persisting it to the first candidate pair whose directory already
// exists (rel never creates directories — see jwt.secret's own doc
// comment for why) ; fall back to an ephemeral, unpersisted keypair (loudly
// warned, same spirit as $GEN$'s own pass 3) if no candidate directory
// exists at all. hostnames populates the generated certificate's DNSNames.
func LoadOrGenerateSPKeyPair(certPathList, keyPathList string, hostnames []string, logger *slog.Logger) (*spKeyPair, error) {
	certCandidates := config.SplitPathList(certPathList)
	keyCandidates := config.SplitPathList(keyPathList)

	if kp, ok := tryLoadExistingKeyPair(certCandidates, keyCandidates); ok {
		return kp, nil
	}

	kp, certPEM, keyPEM, err := generateSelfSignedKeyPair(hostnames)
	if err != nil {
		return nil, fmt.Errorf("sso: generating SAML SP certificate: %w", err)
	}

	n := min(len(certCandidates), len(keyCandidates))
	for i := 0; i < n; i++ {
		dir := filepath.Dir(certCandidates[i])
		if _, statErr := os.Stat(dir); statErr != nil {
			continue
		}
		if writeErr := os.WriteFile(certCandidates[i], certPEM, 0o600); writeErr != nil {
			return nil, fmt.Errorf("sso: writing generated SAML SP certificate to %s: %w", certCandidates[i], writeErr)
		}
		if writeErr := os.WriteFile(keyCandidates[i], keyPEM, 0o600); writeErr != nil {
			return nil, fmt.Errorf("sso: writing generated SAML SP key to %s: %w", keyCandidates[i], writeErr)
		}
		logWarnf(logger, "sso: generated a new SAML SP certificate at %s (key: %s) — hand its metadata (GET /auth/saml/{name}/metadata) to every configured IdP's administrator", certCandidates[i], keyCandidates[i])
		return kp, nil
	}

	logWarnf(logger, "sso: none of saml.certificate_path's configured directories exist — generated an EPHEMERAL, UNPERSISTED SAML SP certificate for THIS RUN ONLY (tried: %s)", certPathList)
	return kp, nil
}

// tryLoadExistingKeyPair tries each (cert, key) candidate pair BY INDEX —
// never cert candidate i with key candidate j — since they're always
// written together as a pair in the first place.
func tryLoadExistingKeyPair(certCandidates, keyCandidates []string) (*spKeyPair, bool) {
	n := min(len(certCandidates), len(keyCandidates))
	for i := 0; i < n; i++ {
		certPEM, err := os.ReadFile(certCandidates[i])
		if err != nil {
			continue
		}
		keyPEM, err := os.ReadFile(keyCandidates[i])
		if err != nil {
			continue
		}
		kp, err := parseKeyPair(certPEM, keyPEM)
		if err != nil {
			continue
		}
		return kp, true
	}
	return nil, false
}

func parseKeyPair(certPEM, keyPEM []byte) (*spKeyPair, error) {
	certBlock, _ := pem.Decode(certPEM)
	if certBlock == nil {
		return nil, fmt.Errorf("sso: no PEM block found in certificate file")
	}
	cert, err := x509.ParseCertificate(certBlock.Bytes)
	if err != nil {
		return nil, fmt.Errorf("sso: parsing certificate: %w", err)
	}
	keyBlock, _ := pem.Decode(keyPEM)
	if keyBlock == nil {
		return nil, fmt.Errorf("sso: no PEM block found in key file")
	}
	key, err := x509.ParsePKCS1PrivateKey(keyBlock.Bytes)
	if err != nil {
		return nil, fmt.Errorf("sso: parsing private key: %w", err)
	}
	return &spKeyPair{Key: key, Certificate: cert}, nil
}

// generateSelfSignedKeyPair mirrors legacy's own generation shape (RSA
// 2048, self-signed, 2-year validity) — PEM-encoded return values are what
// get persisted to disk, the parsed *spKeyPair is what the caller uses
// immediately without a round trip back through PEM.
func generateSelfSignedKeyPair(hostnames []string) (*spKeyPair, []byte, []byte, error) {
	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return nil, nil, nil, err
	}

	template := x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{Organization: []string{"rel"}},
		NotBefore:    time.Now(),
		NotAfter:     time.Now().Add(2 * 365 * 24 * time.Hour),

		KeyUsage:              x509.KeyUsageKeyEncipherment | x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		DNSNames:              hostnames,
	}

	derBytes, err := x509.CreateCertificate(rand.Reader, &template, &template, &priv.PublicKey, priv)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("creating certificate: %w", err)
	}
	cert, err := x509.ParseCertificate(derBytes)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("parsing generated certificate: %w", err)
	}

	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: derBytes})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(priv)})

	return &spKeyPair{Key: priv, Certificate: cert}, certPEM, keyPEM, nil
}

// logWarnf falls back to slog.Default() when logger is nil (mux-building
// call sites that haven't threaded a *slog.Logger through, mirroring
// route/registry.go's own logging.For(...) fallback style).
func logWarnf(logger *slog.Logger, format string, args ...any) {
	if logger == nil {
		logger = slog.Default()
	}
	logger.Warn(fmt.Sprintf(format, args...))
}
