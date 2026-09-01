// This file implements specs/http-content.md ### Upload destinations'
// numbered "Ordering" subsection : a genuinely different request flow from
// handleRpc's own single-transaction one — the body is resolved (streamed
// straight to a temp file on disk) AFTER __prepare's placement decision,
// and the file only changes on disk after the mandatory function's own
// transaction has actually committed.
package rpc

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/bytedance/sonic"

	"github.com/jackc/pgx/v5"

	"github.com/ceymard/rel/config"
	"github.com/ceymard/rel/dbauth"
	"github.com/ceymard/rel/errcode"
	jwtpkg "github.com/ceymard/rel/jwt"
	"github.com/ceymard/rel/pg"
	"github.com/ceymard/rel/static"
)

// relUploadPayload mirrors ### Upload destinations' RelUpload domain —
// Path/Mkdir/Overwrite are __prepare's own decision ; Part/Size are always
// rel-filled, never read from __prepare's own response.
type relUploadPayload struct {
	Path      *string         `json:"path"`
	Mkdir     bool            `json:"mkdir"`
	Overwrite string          `json:"overwrite"`
	Part      json.RawMessage `json:"part"`
	Size      *int64          `json:"size"`
}

// handleUploadRoute implements every numbered step of ### Upload
// destinations' "Ordering" subsection. reqJSON isn't precomputed by the
// caller (unlike the ordinary path) — RelHttpRequest.body is always JSON
// null for this family (the payload never goes through body), so it's
// built directly here.
func handleUploadRoute(w http.ResponseWriter, r *http.Request, db *pg.DbInfos, cfg *config.Config, route Route, staticSrv *static.Server, templates *TemplateSet, verified bool, claims jwtpkg.Claims) {
	ctx := r.Context()

	reqJSON, err := buildRelHttpRequest(r, json.RawMessage("null"), verified, claims)
	if err != nil {
		writePlainError(w, http.StatusInternalServerError, errcode.Internal, "encoding request")
		return
	}
	r = r.WithContext(withRequestJSON(ctx, reqJSON))
	ctx = r.Context()

	// Step 2's own 415 rule : "A request that IS JSON/text/form-urlencoded
	// is a 415... checked BEFORE invoking either function, from the
	// request's own Content-Type alone."
	contentTypeHeader := r.Header.Get("Content-Type")
	mt := mediaTypeOf(contentTypeHeader)
	if mt == "application/json" || strings.HasSuffix(mt, "+json") ||
		strings.HasPrefix(mt, "text/") || mt == "application/x-www-form-urlencoded" {
		writePlainError(w, http.StatusUnsupportedMediaType, errcode.UnsupportedMediaType, "route accepts a single opaque upload, not a JSON/text/form body")
		return
	}

	if err := tooLargeIfContentLengthExceeds(r, int64(cfg.Http.MaxBodySize)); err != nil {
		writeRequestBodyError(w, err)
		return
	}
	limitedBody := http.MaxBytesReader(w, r.Body, int64(cfg.Http.MaxBodySize))

	isMultipart := false
	var boundary string
	if pmt, params, perr := mime.ParseMediaType(contentTypeHeader); perr == nil && strings.HasPrefix(pmt, "multipart/") {
		isMultipart = true
		boundary = params["boundary"]
	}

	// Step 2 : read ONLY the incoming upload's headers — part is built
	// without touching a single byte of the actual payload.
	partJSON := json.RawMessage("null")
	hasUpload := false
	var mr *multipart.Reader
	var currentPart *multipart.Part

	if isMultipart {
		mr = multipart.NewReader(limitedBody, boundary)
		p, perr := mr.NextPart()
		if perr != nil {
			if perr != io.EOF {
				writePlainError(w, http.StatusBadRequest, errcode.MalformedMultipart, "malformed multipart body: "+perr.Error())
				return
			}
			// Zero parts : "no upload at all", same as no body.
		} else {
			hasUpload = true
			currentPart = p
			if b, merr := sonic.Marshal(requestPartFrom(p)); merr == nil {
				partJSON = b
			}
		}
	} else if r.ContentLength != 0 {
		// A non-multipart request is a potential single raw upload regardless
		// of whether Content-Type was sent at all — matching ## Request
		// bodies' own general dispatch rule elsewhere ("anything else... a
		// single raw binary POST"), where a MISSING Content-Type is treated
		// the same as an unrecognized one, not as "nothing to read." Gating
		// this on contentTypeHeader != "" (the previous, narrower condition)
		// meant a genuine binary upload sent with no Content-Type header at
		// all was silently never streamed/consumed. r.ContentLength == 0 is
		// still excluded (a definitely-empty body, same as no upload at all)
		// ; -1 (unknown/chunked length) is treated as "might have a body,"
		// same as everywhere else that can't know length ahead of reading.
		hasUpload = true
		if b, merr := sonic.Marshal(synthesizedPseudoPart(r, contentTypeHeader)); merr == nil {
			partJSON = b
		}
	}

	// Step 3 : connection acquire ; check_session as a PLAIN STATEMENT,
	// before BEGIN READ ONLY opens ; renew ; BEGIN READ ONLY ; SET LOCAL
	// ROLE ; invoke __prepare(req, part) ; COMMIT (trivial for a read-only
	// transaction).
	conn, err := db.Pool.Acquire(ctx)
	if err != nil {
		writePlainError(w, http.StatusInternalServerError, errcode.DBUnavailable, "acquiring connection")
		return
	}

	if err := dbauth.CheckSessionIfConfigured(ctx, conn, cfg.Http.Functions.CheckSession, claims, verified); err != nil {
		conn.Release()
		jwtpkg.ClearSessionCookie(cfg.Jwt, w)
		writeErrorForPgErr(w, err, cfg.Dev)
		return
	}
	if verified {
		claims = jwtpkg.RenewIfDue(cfg.Jwt, w, claims)
	}

	role := jwtpkg.ResolveRole(cfg.Pg.Query.AnonymousRole, claims, verified)
	if role == "" {
		conn.Release()
		writePlainError(w, http.StatusInternalServerError, errcode.NoRoleConfigured, dbauth.NoRoleConfiguredMessage)
		return
	}

	tx1, err := conn.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly})
	if err != nil {
		conn.Release()
		writePlainError(w, http.StatusInternalServerError, errcode.TransactionError, "starting read-only transaction")
		return
	}
	if err := dbauth.SetLocalRole(ctx, tx1, role); err != nil {
		_ = tx1.Rollback(ctx)
		conn.Release()
		writeErrorForPgErr(w, err, cfg.Dev)
		return
	}

	prepIdent := route.PrepareFunction.Identifier.EscapedString()
	var uploadRaw []byte
	prow := tx1.QueryRow(ctx, "select "+prepIdent+"($1::jsonb, $2::jsonb)", reqJSON, []byte(partJSON))
	if err := prow.Scan(&uploadRaw); err != nil {
		_ = tx1.Rollback(ctx)
		conn.Release()
		writeErrorForPgErr(w, err, cfg.Dev)
		return
	}
	if err := tx1.Commit(ctx); err != nil {
		conn.Release()
		writePlainError(w, http.StatusInternalServerError, errcode.TransactionError, "committing read-only transaction")
		return
	}
	conn.Release()

	var upload relUploadPayload
	if err := sonic.Unmarshal(uploadRaw, &upload); err != nil {
		log.Error("rpc: decoding __prepare's RelUpload response", "function", route.PrepareFunction.Identifier.String(), "error", err.Error())
		writePlainError(w, http.StatusInternalServerError, errcode.Internal, "internal error")
		return
	}

	writeDir := ""
	if staticSrv != nil {
		writeDir = staticSrv.WriteDir()
	}

	// Immediately after __prepare returns, still before any bytes are
	// read : traversal validation (500), the overwrite/409 fast-fail check,
	// and mkdir — all skipped entirely (not executed-but-vacuous) when
	// !hasUpload, per step 2's own "part: null... whatever path/mkdir/
	// overwrite it returned is simply never acted on" rule.
	var finalPath, tempPath, overwrite string
	// Every error return past this point used to repeat its own
	// "if tempPath != \"\" { os.Remove(tempPath) }" by hand — a future
	// error return added without that line would silently leak a temp
	// file. One unconditional defer instead : safe even past a successful
	// swap (swapUploadIntoPlace's own os.Rename has already moved tempPath
	// away by then, so this becomes a no-op on an already-gone path) or
	// the discard-case's own removal below.
	defer func() {
		if tempPath != "" {
			_ = os.Remove(tempPath)
		}
	}()
	if hasUpload {
		if upload.Path != nil && *upload.Path != "" {
			if writeDir == "" {
				log.Error("rpc: upload route resolved a path but no http.static.path directory is configured/exists", "path", *upload.Path)
				writePlainError(w, http.StatusInternalServerError, errcode.Internal, "internal error")
				return
			}
			cleaned, ok := resolveUnderDir(writeDir, *upload.Path)
			if !ok {
				log.Error("rpc: upload __prepare returned a path escaping http.static.path", "path", *upload.Path)
				writePlainError(w, http.StatusInternalServerError, errcode.Internal, "internal error")
				return
			}
			finalPath = cleaned

			overwrite = upload.Overwrite
			if overwrite == "" {
				overwrite = "disallow"
			}
			if overwrite == "disallow" {
				if _, statErr := os.Stat(finalPath); statErr == nil {
					writePlainError(w, http.StatusConflict, errcode.UploadConflict, "a file already exists at the resolved path")
					return
				}
			}
			if upload.Mkdir {
				if err := os.MkdirAll(filepath.Dir(finalPath), 0o755); err != nil {
					log.Error("rpc: upload mkdir", "path", finalPath, "error", err.Error())
					writePlainError(w, http.StatusInternalServerError, errcode.UploadIOError, "internal error")
					return
				}
			}
			tempPath = filepath.Join(filepath.Dir(finalPath), ".upload-"+randomToken())
		} else if writeDir != "" {
			// path omitted (discard case) : still streamed, into a generic
			// staging location, so the mandatory function still gets an
			// accurate size.
			tempPath = filepath.Join(writeDir, ".upload-"+randomToken())
		} else {
			log.Error("rpc: upload route has no http.static.path directory configured/exists to stage the discarded upload into")
			writePlainError(w, http.StatusInternalServerError, errcode.Internal, "internal error")
			return
		}
	}

	// Step 4-5 : stream bytes to the temp file ; for multipart, one more
	// NextPart() call after streaming ends catches a second part.
	var size int64
	if hasUpload {
		f, ferr := os.OpenFile(tempPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
		if ferr != nil {
			log.Error("rpc: creating temp upload file", "path", tempPath, "error", ferr.Error())
			writePlainError(w, http.StatusInternalServerError, errcode.UploadIOError, "internal error")
			return
		}
		var src io.Reader = limitedBody
		if isMultipart {
			src = currentPart
		}
		n, cerr := io.Copy(f, src)
		_ = f.Close()
		size = n
		if cerr != nil {
			var maxErr *http.MaxBytesError
			if errors.As(cerr, &maxErr) {
				writePlainError(w, http.StatusRequestEntityTooLarge, errcode.BodyTooLarge, "request body exceeds http.max_body_size")
				return
			}
			writePlainError(w, http.StatusBadRequest, errcode.MalformedBody, "reading upload body: "+cerr.Error())
			return
		}
		if isMultipart {
			_ = currentPart.Close()
			if _, nerr := mr.NextPart(); nerr != io.EOF {
				if nerr == nil {
					writePlainError(w, http.StatusUnsupportedMediaType, errcode.UnsupportedMediaType, "route accepts exactly one upload part, request carried more than one")
				} else {
					writePlainError(w, http.StatusBadRequest, errcode.MalformedMultipart, "malformed multipart body: "+nerr.Error())
				}
				return
			}
		}
	}

	uploadForMandatory, err := buildUploadForMandatory(uploadRaw, partJSON, hasUpload, size)
	if err != nil {
		writePlainError(w, http.StatusInternalServerError, errcode.Internal, "internal error")
		return
	}

	// Steps 6-8 : a SECOND connection/transaction — check_session/renew are
	// NOT repeated here, they already ran exactly once, in step 3.
	conn2, err := db.Pool.Acquire(ctx)
	if err != nil {
		writePlainError(w, http.StatusInternalServerError, errcode.DBUnavailable, "acquiring connection")
		return
	}
	defer conn2.Release()

	tx2, err := conn2.Begin(ctx)
	if err != nil {
		writePlainError(w, http.StatusInternalServerError, errcode.TransactionError, "starting transaction")
		return
	}
	if err := dbauth.SetLocalRole(ctx, tx2, role); err != nil {
		_ = tx2.Rollback(ctx)
		writeErrorForPgErr(w, err, cfg.Dev)
		return
	}

	mandIdent := route.Function.Identifier.EscapedString()
	var respRaw []byte
	mrow := tx2.QueryRow(ctx, "select "+mandIdent+"($1::jsonb, $2::jsonb)", reqJSON, uploadForMandatory)
	if err := mrow.Scan(&respRaw); err != nil {
		_ = tx2.Rollback(ctx)
		// "If the mandatory function raises (no commit) : the temp file is
		// deleted, nothing further happens." — the deferred cleanup above.
		writeErrorForPgErr(w, err, cfg.Dev)
		return
	}
	if err := tx2.Commit(ctx); err != nil {
		writePlainError(w, http.StatusInternalServerError, errcode.TransactionError, "committing transaction")
		return
	}

	// Step 9 : ONLY on a successful commit, and ONLY if path was set, the
	// disk swap finalizes. Path omitted : the deferred cleanup above deletes
	// the temp file instead — not an error.
	if hasUpload && finalPath != "" {
		if err := swapUploadIntoPlace(tempPath, finalPath, overwrite); err != nil {
			log.Error("rpc: swapping upload into place", "temp", tempPath, "final", finalPath, "error", err.Error())
			writePlainError(w, http.StatusInternalServerError, errcode.UploadIOError, "internal error")
			return
		}
	}

	writeRelHttpResponse(w, r, cfg, route, respRaw, templates)
}

// resolveUnderDir cleans rel (a path relative to dir) and confirms the
// result stays under dir — ## Static files' own traversal/escaping rules,
// reused here per ### Upload destinations' "Placement" paragraph : a `path`
// that escapes dir is a hard rejection, never silently rewritten into some
// OTHER location still under dir. filepath.Join(dir, filepath.Clean(sep+rel))
// alone does NOT achieve this — Clean roots ".." components at "/" before
// Join ever runs, so "../../etc/passwd" (or an absolute "/etc/passwd")
// resolves to a real, if unintended, path under dir instead of failing ; an
// attacker-influenced path is caught explicitly, BEFORE that rewriting can
// happen, not after.
func resolveUnderDir(dir, rel string) (string, bool) {
	if rel == "" || filepath.IsAbs(rel) {
		return "", false
	}
	cleaned := filepath.Clean(rel)
	if cleaned == ".." || strings.HasPrefix(cleaned, ".."+string(filepath.Separator)) {
		return "", false
	}
	full := filepath.Join(dir, cleaned)
	base := filepath.Clean(dir)
	if full != base && !strings.HasPrefix(full, base+string(filepath.Separator)) {
		return "", false
	}
	return full, true
}

// randomToken is the ".upload-<random>" temp/backup file naming scheme —
// always dot-prefixed regardless of directory (## Static files' own
// dotfile rule then makes it unservable mid-stream, no separate mechanism
// needed).
func randomToken() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// swapUploadIntoPlace is step 9's own rename sequence : rename any
// existing file at finalPath aside to a dot-prefixed backup, rename temp
// into place, delete the backup on success. On any failure partway,
// restore the backup (if one was made) and clean up the temp file — the
// caller has already committed the mandatory function's own DB write by
// the time this runs, so this failure window never rolls that back, only
// logs/500s per the spec's own documented limitation.
func swapUploadIntoPlace(tempPath, finalPath, overwrite string) error {
	dir := filepath.Dir(finalPath)
	backupPath := filepath.Join(dir, ".upload-backup-"+randomToken())
	hadExisting := false
	if _, err := os.Stat(finalPath); err == nil {
		// overwrite:'disallow' was already enforced once, early (before any
		// bytes were read) — deliberately NOT re-enforced here (step 9's own
		// "last-write-wins on a genuine race" rule : the mandatory function's
		// transaction has already committed by this point, so refusing the
		// swap now would leave the database referencing a file that was
		// never actually written). A file existing here despite that early
		// check passing means exactly such a race happened — worth a warning
		// even though it isn't an error.
		if overwrite == "disallow" {
			log.Warn("rpc: upload overwrote an existing file at commit time despite overwrite:'disallow' — a concurrent request won the race after the early existence check", "path", finalPath)
		}
		if err := os.Rename(finalPath, backupPath); err != nil {
			_ = os.Remove(tempPath)
			return err
		}
		hadExisting = true
	}
	if err := os.Rename(tempPath, finalPath); err != nil {
		if hadExisting {
			_ = os.Rename(backupPath, finalPath)
		}
		_ = os.Remove(tempPath)
		return err
	}
	if hadExisting {
		_ = os.Remove(backupPath)
	}
	return nil
}

// buildUploadForMandatory decodes __prepare's own raw RelUpload response,
// overwrites ONLY the rel-filled part/size fields (never touching
// path/mkdir/overwrite, which __prepare alone controls), and re-marshals —
// preserves any field __prepare's own response happened to carry rather
// than narrowing through relUploadPayload's own Go shape.
func buildUploadForMandatory(prepareRaw []byte, partJSON json.RawMessage, hasUpload bool, size int64) ([]byte, error) {
	var m map[string]any
	if err := sonic.Unmarshal(prepareRaw, &m); err != nil {
		return nil, err
	}
	if m == nil {
		m = map[string]any{}
	}
	if !hasUpload {
		m["part"] = nil
		m["size"] = nil
		return sonic.Marshal(m)
	}
	var partVal any
	if len(partJSON) > 0 && string(partJSON) != "null" {
		_ = sonic.Unmarshal(partJSON, &partVal)
	}
	m["part"] = partVal
	m["size"] = size
	return sonic.Marshal(m)
}
