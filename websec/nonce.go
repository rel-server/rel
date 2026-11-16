// Package websec implements CORS and CSP for /rel, /route and /static.
package websec

import (
	"context"
	"crypto/rand"
	"encoding/base64"

	"github.com/rel-server/rel/logging"
)

var log = logging.For("websec")

type nonceContextKey struct{}

// NewNonce generates a fresh, cryptographically random nonce, base64-encoded.
func NewNonce() (string, error) {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(buf), nil
}

// WithNonce stashes nonce in ctx, for NonceFromContext.
func WithNonce(ctx context.Context, nonce string) context.Context {
	return context.WithValue(ctx, nonceContextKey{}, nonce)
}

// NonceFromContext reads back what WithNonce stashed ; "" if the middleware was not in the chain.
func NonceFromContext(ctx context.Context) string {
	n, _ := ctx.Value(nonceContextKey{}).(string)
	return n
}
