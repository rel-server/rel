package route

import (
	"context"
	"net/http"

	"github.com/rel-server/rel/errcode"
	"github.com/rel-server/rel/logging"
	"github.com/rel-server/rel/pgerr"
)

// writePlainError writes a plain-text error body (## Postgres Exceptions),
// deliberately different from server/response.go's JSON envelope.
func writePlainError(w http.ResponseWriter, status int, code errcode.Code, message string) {
	w.Header().Set(errcode.Header, string(code))
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(status)
	_, _ = w.Write([]byte(message))
}

// writeServerError is writePlainError plus specs/logging.md ## Error
// logging's one "request failed" line — every writePlainError call site
// whose status is a genuine server fault (>= 500) AND has a real err in
// scope (most already reduced it to message, a static string, before
// reaching here) should call this instead, so the message stays exactly
// what the client always saw while the full err also reaches the logs.
func writeServerError(ctx context.Context, w http.ResponseWriter, status int, code errcode.Code, message string, err error) {
	logging.FromContext(ctx).Error("request failed", append([]any{"code", code, "status", status}, logging.Error(err)...)...)
	writePlainError(w, status, code, message)
}

// writeErrorForPgErr classifies err and writes the tiered plain-text
// response, falling back to a generic 500 when it doesn't classify. A 500
// (classified or not) is always logged in full first — specs/logging.md
// ## Error logging — independently of what dev/tier let the client itself see.
func writeErrorForPgErr(ctx context.Context, w http.ResponseWriter, err error, dev bool) {
	status, code, tier, detail, ok := pgerr.Classify(err)
	if !ok {
		logging.FromContext(ctx).Error("request failed", append([]any{"code", errcode.Internal, "status", http.StatusInternalServerError}, logging.Error(err)...)...)
		writePlainError(w, http.StatusInternalServerError, errcode.Internal, "internal error")
		return
	}
	if status >= 500 {
		logAttrs := append([]any{"code", code, "status", status}, logging.Error(err)...)
		if detail != nil {
			logAttrs = append(logAttrs, "pg_error", detail)
		}
		logging.FromContext(ctx).Error("request failed", logAttrs...)
	}
	writePlainError(w, status, code, pgPlainText(code, tier, detail, dev))
}

// pgPlainText shares its tiering with server/response.go's pgResponseText,
// but renders one line with no structured pg_error field.
func pgPlainText(code errcode.Code, tier pgerr.Tier, detail *pgerr.Detail, dev bool) string {
	if pgerr.IsRSCode(code) {
		return detail.Message
	}
	if !pgerr.AllowsDetail(tier, dev) {
		return pgerr.FallbackMessage(tier)
	}
	if tier == pgerr.TierConstraintViolation && detail.Detail != "" {
		return detail.Message + ": " + detail.Detail
	}
	return detail.Message
}
