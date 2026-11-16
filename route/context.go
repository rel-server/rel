package route

import (
	"context"
	"encoding/json"
)

type requestJSONContextKey struct{}

// withRequestJSON stashes the exact request JSON sent to the route function, read back by the templates' "Req" var.
func withRequestJSON(ctx context.Context, raw json.RawMessage) context.Context {
	return context.WithValue(ctx, requestJSONContextKey{}, raw)
}

// requestJSONFromContext reads back what withRequestJSON stashed — (nil,
// false) if it was never set.
func requestJSONFromContext(ctx context.Context) (json.RawMessage, bool) {
	raw, ok := ctx.Value(requestJSONContextKey{}).(json.RawMessage)
	return raw, ok
}
