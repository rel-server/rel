package rpc

import (
	"net/http"

	"github.com/ceymard/rel/errcode"
	"github.com/ceymard/rel/pgerr"
)

// writePlainError writes a plain-text (NOT JSON) error body — ## Postgres
// Exceptions' own framing : "gives the response status xxx, with whatever
// text was raised as the body." This is deliberately different from
// server/response.go's JSON envelope : /rel and /rpc have separate error
// conventions per their respective spec sections. code is always set on the
// X-Rel-Errorcode header regardless of body framing — specs/error-handling.md
// ## Delivery.
func writePlainError(w http.ResponseWriter, status int, code errcode.Code, message string) {
	w.Header().Set(errcode.Header, string(code))
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(status)
	_, _ = w.Write([]byte(message))
}

// writeErrorForPgErr classifies err via pgerr.Classify and writes the
// appropriate plain-text response, tiered exactly like server/response.go's
// pgResponseText — RSxxx and a classified constraint violation are always
// shown in full ; permission-denied and anything unclassified are
// dev-gated. Falls back to a generic 500 when err doesn't unwrap to a
// *pgconn.PgError at all.
func writeErrorForPgErr(w http.ResponseWriter, err error, dev bool) {
	status, code, tier, detail, ok := pgerr.Classify(err)
	if !ok {
		writePlainError(w, http.StatusInternalServerError, errcode.Internal, "internal error")
		return
	}
	writePlainError(w, status, code, pgPlainText(code, tier, detail, dev))
}

// pgPlainText renders a classified Postgres error as plain text, mirroring
// server/response.go's pgResponseText tier-by-tier but without a
// structured pg_error field to fall back on (there's no JSON body on this
// path) — the constraint/table/column detail a JSON client gets separately
// is folded into one line here instead, still built only from pgerr.Detail's
// allow-listed fields, never err.Error()'s wrapped chain.
func pgPlainText(code errcode.Code, tier pgerr.Tier, detail *pgerr.Detail, dev bool) string {
	switch {
	case pgerr.IsRSCode(code):
		return detail.Message
	case tier == pgerr.TierConstraintViolation:
		if detail.Detail != "" {
			return detail.Message + ": " + detail.Detail
		}
		return detail.Message
	case tier == pgerr.TierPermissionDenied:
		if dev {
			return detail.Message
		}
		return "insufficient permissions for this operation"
	default: // TierUnclassified, not RSxxx.
		if dev {
			return detail.Message
		}
		return "internal error"
	}
}
