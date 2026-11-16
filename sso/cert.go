// Loads the SAML SP keypair from the configured paths, or generates and persists a self-signed one (warned once).
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

// spKeyPair is the SAML SP identity : Key signs AuthnRequests, Certificate is published in the metadata.
type spKeyPair struct {
	Key         *rsa.PrivateKey
	Certificate *x509.Certificate
}

// LoadOrGenerateSPKeyPair loads the first readable (certPathList, keyPathList) candidate pair, or generates
// one and persists it to the first pair whose directory exists (rel never creates directories). With no such
// directory the keypair is ephemeral, with a warning. hostnames fills the generated certificate's DNSNames.
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

// tryLoadExistingKeyPair tries each (cert, key) candidate pair by index, never cert i with key j.
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

// generateSelfSignedKeyPair makes an RSA 2048, 2-year keypair, returned both parsed and as PEM to persist.
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

// logWarnf falls back to slog.Default() when logger is nil.
func logWarnf(logger *slog.Logger, format string, args ...any) {
	if logger == nil {
		logger = slog.Default()
	}
	logger.Warn(fmt.Sprintf(format, args...))
}
