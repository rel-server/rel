// The error envelope.
package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"github.com/rel-server/rel/errcode"
	"github.com/rel-server/rel/httperr"
	"github.com/rel-server/rel/pgerr"
	"github.com/samber/oops"
)

// errorResponse is the RelErrorResponse envelope ; Status is always "error".
type errorResponse struct {
	Error      string        `json:"error"`
	Status     string        `json:"status"`
	Code       errcode.Code  `json:"code"`
	PgError    *pgerr.Detail `json:"pg_error,omitempty"`
	Stacktrace []string      `json:"stacktrace,omitempty"`
}

// writeError writes err as the RelErrorResponse envelope ; only valid if nothing was written to w yet. ctx is for
// logging a 5xx in full, whatever the client sees. An err that is not an *httperr.Error is a 500.
func writeError(ctx context.Context, w http.ResponseWriter, err error, dev bool) {
	e, ok := errors.AsType[*httperr.Error](err)
	if !ok {
		e = httperr.Server(errcode.Internal, err)
	}
	e.Log(ctx)

	resp := errorResponse{Status: "error", Code: e.Code}

	switch {
	case e.Detail != nil:
		resp.Error, resp.PgError = e.JSONText(dev)
	case e.Status >= 500:
		// Full detail only under dev : a wrapped chain can embed a file path or connection detail.
		if dev {
			resp.Error = err.Error()
		} else {
			resp.Error = "internal error"
		}
	default:
		// A 4xx always gets the full message : it describes the request, not server internals.
		resp.Error = err.Error()
	}

	if e.Status >= 500 && dev {
		if oopsErr, ok := oops.AsOops(err); ok {
			resp.Stacktrace = formatStackFrames(oopsErr)
		}
	}

	w.Header().Set(errcode.Header, string(e.Code))
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(e.Status)
	_ = json.NewEncoder(w).Encode(resp)
}

// formatStackFrames renders oopsErr's frames as one "file:line function" string each.
func formatStackFrames(oopsErr oops.OopsError) []string {
	frames := oopsErr.StackFrames()
	if len(frames) == 0 {
		return nil
	}
	out := make([]string, len(frames))
	for i, f := range frames {
		out[i] = fmt.Sprintf("%s:%d %s", f.File, f.Line, f.Function)
	}
	return out
}
