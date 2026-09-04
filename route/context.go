package route

import (
	"context"
	"encoding/json"
)

// requestJSONContextKey is unexported so no other package can collide with
// it by constructing an equal-by-value context key.
type requestJSONContextKey struct{}

// withRequestJSON stashes the exact request JSON sent to the route
// function ; templates.go's "Req" var reads it back rather than re-deriving.
func withRequestJSON(ctx context.Context, raw json.RawMessage) context.Context {
	return context.WithValue(ctx, requestJSONContextKey{}, raw)
}

// requestJSONFromContext reads back what withRequestJSON stashed — (nil,
// false) if it was never set.
func requestJSONFromContext(ctx context.Context) (json.RawMessage, bool) {
	raw, ok := ctx.Value(requestJSONContextKey{}).(json.RawMessage)
	return raw, ok
}
