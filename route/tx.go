package route

import (
	"context"
	"errors"
	"net/http"

	"github.com/rel-server/rel/errcode"
	"github.com/rel-server/rel/reqauth"
)

// writeAuthError renders a reqauth failure as this package's plain-text response : a 4xx is the
// message alone, a 5xx is logged in full first, and a Postgres error (a role the login role cannot
// become) is classified like any other.
func writeAuthError(ctx context.Context, w http.ResponseWriter, err error, dev bool) {
	e, ok := errors.AsType[*reqauth.Error](err)
	switch {
	case !ok:
		writeServerError(ctx, w, http.StatusInternalServerError, errcode.Internal, "internal error", err)
	case e.Postgres:
		writeErrorForPgErr(ctx, w, e.Err, dev)
	case e.Status < http.StatusInternalServerError:
		writePlainError(w, e.Status, e.Code, e.Message)
	default:
		writeServerError(ctx, w, e.Status, e.Code, e.Message, e.Err)
	}
}
