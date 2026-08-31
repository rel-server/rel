// Package websec implements specs/http-content.md's CORS and CSP
// sections : both apply uniformly to /rel, /rpc, AND /static (the spec's
// own explicit statement), so this lives as its own leaf package rather
// than inside rpc or server specifically — both need it, and rpc/encode.go
// (RelHttpRequest.csp_nonce, RelHttpResponse.csp) needs the nonce/CSP
// machinery directly, which rules out putting it in boot (rpc doesn't
// import boot, but boot imports rpc — a boot-side package would be
// unreachable from rpc).
package websec

import (
	"context"
	"crypto/rand"
	"encoding/base64"
)

// nonceContextKey is unexported so no other package can collide with it by
// constructing an equal-by-value context key.
type nonceContextKey struct{}

// NewNonce generates a fresh, cryptographically random nonce — ## CSP
// ### Nonce : "one crypto/rand read", base64-encoded.
func NewNonce() (string, error) {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(buf), nil
}

// WithNonce stashes nonce in ctx, for a downstream handler to read back via
// NonceFromContext (RelHttpRequest.csp_nonce, Jet's Nonce variable).
func WithNonce(ctx context.Context, nonce string) context.Context {
	return context.WithValue(ctx, nonceContextKey{}, nonce)
}

// NonceFromContext reads back what WithNonce stashed — "" if the
// middleware was never in the chain.
func NonceFromContext(ctx context.Context) string {
	n, _ := ctx.Value(nonceContextKey{}).(string)
	return n
}
