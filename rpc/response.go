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

// pgPlainText renders a classified Postgres error as plain text — the
// tiering itself (pgerr.AllowsDetail/FallbackMessage) is shared with
// server/response.go's pgResponseText, so the two can't independently
// drift on WHICH tier shows what ; only the RENDERING differs, since this
// path has no structured pg_error field to fall back on (no JSON body
// here) — the constraint/table/column detail a JSON client gets
// separately is folded into one line instead, still built only from
// pgerr.Detail's allow-listed fields, never err.Error()'s wrapped chain.
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
	// TierConstraintViolation with no extra Detail text, or
	// TierPermissionDenied/TierUnclassified with AllowsDetail true (only
	// reachable under dev).
	return detail.Message
}
