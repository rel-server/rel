package route

import (
	"net/http"

	"github.com/ceymard/rel/errcode"
	"github.com/ceymard/rel/pgerr"
)

// writePlainError writes a plain-text error body (## Postgres Exceptions),
// deliberately different from server/response.go's JSON envelope.
func writePlainError(w http.ResponseWriter, status int, code errcode.Code, message string) {
	w.Header().Set(errcode.Header, string(code))
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(status)
	_, _ = w.Write([]byte(message))
}

// writeErrorForPgErr classifies err and writes the tiered plain-text
// response, falling back to a generic 500 when it doesn't classify.
func writeErrorForPgErr(w http.ResponseWriter, err error, dev bool) {
	status, code, tier, detail, ok := pgerr.Classify(err)
	if !ok {
		writePlainError(w, http.StatusInternalServerError, errcode.Internal, "internal error")
		return
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
