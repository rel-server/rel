// Response framing : the manual "["/","/"]" streaming specs/query-engine.md's
// ## Response Shape asks for (no json_agg, constant memory, first byte
// before the query finishes), plus the error envelope specs/error-handling.md
// specifies in full.
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

// errorResponse is the RelErrorResponse envelope (error-handling.md).
// Status is always "error" ; the numeric HTTP status is never repeated here.
type errorResponse struct {
	Error      string        `json:"error"`
	Status     string        `json:"status"`
	Code       errcode.Code  `json:"code"`
	PgError    *pgerr.Detail `json:"pg_error,omitempty"`
	Stacktrace []string      `json:"stacktrace,omitempty"`
}

// writeError writes err as the RelErrorResponse envelope (error-handling.md) ;
// only clean if nothing has been written to w yet, invalid JSON otherwise.
// Takes ctx solely to log a server-side (5xx) failure in full — specs/
// logging.md ## Error logging — independently of what dev/tier let the
// client itself see. An err that is not an *httperr.Error is a 500.
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
		// Safe-by-default, full detail only under dev : a wrapped chain
		// can embed a file path or connection detail.
		if dev {
			resp.Error = err.Error()
		} else {
			resp.Error = "internal error"
		}
	default:
		// A 4xx always gets the full message — client input, never
		// server-internal state.
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

// formatStackFrames renders oopsErr's frames as ## Stack traces'
// RelErrorResponse.stacktrace : one "file:line function" string per frame.
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
