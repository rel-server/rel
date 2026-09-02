package route

import (
	"context"
	"encoding/json"
)

// requestJSONContextKey is unexported so no other package can collide with
// it by constructing an equal-by-value context key.
type requestJSONContextKey struct{}

// withRequestJSON stashes the exact RelHttpRequest JSON bytes handleRoute
// built and sent to the route function — templates.go's "Req" VarMap (##
// Templates step 2) reads this back rather than independently re-deriving
// it, so it can never drift from what the route function itself actually
// received.
func withRequestJSON(ctx context.Context, raw json.RawMessage) context.Context {
	return context.WithValue(ctx, requestJSONContextKey{}, raw)
}

// requestJSONFromContext reads back what withRequestJSON stashed — (nil,
// false) if it was never set (a route function that doesn't take a
// RelHttpRequest argument at all, or a response written without going
// through handleRoute's normal flow).
func requestJSONFromContext(ctx context.Context) (json.RawMessage, bool) {
	raw, ok := ctx.Value(requestJSONContextKey{}).(json.RawMessage)
	return raw, ok
}
