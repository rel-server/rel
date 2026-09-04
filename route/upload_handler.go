// This file implements specs/http-content.md ### Upload destinations'
// "Ordering" subsection : the body streams to a temp file only after
// __prepare's placement decision, and disk only changes after commit.
package route

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
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
	"github.com/ceymard/rel/logging"
	"github.com/ceymard/rel/pg"
	"github.com/ceymard/rel/static"
)

// relUploadPayload mirrors the RelUpload domain. Path/Mkdir/Overwrite are
// __prepare's decision ; Part/Size are always rel-filled.
type relUploadPayload struct {
	Path      *string         `json:"path"`
	Mkdir     bool            `json:"mkdir"`
	Overwrite string          `json:"overwrite"`
	Part      json.RawMessage `json:"part"`
	Size      *int64          `json:"size"`
}

// handleUploadRoute implements ### Upload destinations' "Ordering" ; body
// is always JSON null here, so reqJSON is built directly, not by the caller.
func handleUploadRoute(w http.ResponseWriter, r *http.Request, db *pg.DbInfos, cfg *config.Config, route Route, staticSrv *static.Server, templates *TemplateSet, verified bool, claims jwtpkg.Claims) {
	ctx := r.Context()
	rlog := logging.FromContext(ctx).With("module", "route")

	reqJSON, err := buildRelHttpRequest(r, json.RawMessage("null"), verified, claims)
	if err != nil {
		writePlainError(w, http.StatusInternalServerError, errcode.Internal, "encoding request")
		return
	}
	r = r.WithContext(withRequestJSON(ctx, reqJSON))
	ctx = r.Context()

	// Step 2 : a JSON/text/form body is a 415, checked before either
	// function runs, from Content-Type alone.
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
			// Zero parts : no upload at all, same as no body.
		} else {
			hasUpload = true
			currentPart = p
			if b, merr := sonic.Marshal(requestPartFrom(p)); merr == nil {
				partJSON = b
			}
		}
	} else if r.ContentLength != 0 {
		// A missing Content-Type is treated as "might have a body" (like
		// -1/chunked), not as "nothing to read" — only 0 is excluded.
		hasUpload = true
		if b, merr := sonic.Marshal(synthesizedPseudoPart(r, contentTypeHeader)); merr == nil {
			partJSON = b
		}
	}

	// Step 3 : check_session as a plain statement before BEGIN READ ONLY,
	// then renew, SET LOCAL ROLE, invoke __prepare, commit.
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
		rlog.Error("route: decoding __prepare's RelUpload response", "function", route.PrepareFunction.Identifier.String(), "error", err.Error())
		writePlainError(w, http.StatusInternalServerError, errcode.Internal, "internal error")
		return
	}

	writeDir := ""
	if staticSrv != nil {
		writeDir = staticSrv.WriteDir()
	}

	// Traversal validation/409 check/mkdir are skipped entirely when
	// !hasUpload — __prepare's path/mkdir/overwrite are never acted on.
	var finalPath, tempPath, overwrite string
	// One unconditional defer rather than repeating cleanup at every error
	// return ; a no-op once swapUploadIntoPlace has already moved tempPath.
	defer func() {
		if tempPath != "" {
			_ = os.Remove(tempPath)
		}
	}()
	if hasUpload {
		if upload.Path != nil && *upload.Path != "" {
			if writeDir == "" {
				rlog.Error("route: upload route resolved a path but no http.static.path directory is configured/exists", "path", *upload.Path)
				writePlainError(w, http.StatusInternalServerError, errcode.Internal, "internal error")
				return
			}
			cleaned, ok := resolveUnderDir(writeDir, *upload.Path)
			if !ok {
				rlog.Error("route: upload __prepare returned a path escaping http.static.path", "path", *upload.Path)
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
					rlog.Error("route: upload mkdir", "path", finalPath, "error", err.Error())
					writePlainError(w, http.StatusInternalServerError, errcode.UploadIOError, "internal error")
					return
				}
			}
			tempPath = filepath.Join(filepath.Dir(finalPath), ".upload-"+randomToken())
		} else if writeDir != "" {
			// Discard case : still streamed to a staging location, so the
			// mandatory function gets an accurate size.
			tempPath = filepath.Join(writeDir, ".upload-"+randomToken())
		} else {
			rlog.Error("route: upload route has no http.static.path directory configured/exists to stage the discarded upload into")
			writePlainError(w, http.StatusInternalServerError, errcode.Internal, "internal error")
			return
		}
	}

	// Steps 4-5 : stream to the temp file ; for multipart, one more
	// NextPart() catches a second part.
	var size int64
	if hasUpload {
		f, ferr := os.OpenFile(tempPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
		if ferr != nil {
			rlog.Error("route: creating temp upload file", "path", tempPath, "error", ferr.Error())
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

	// Steps 6-8 : a second connection/transaction ; check_session/renew
	// already ran once, in step 3, and aren't repeated.
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
		// No commit : the temp file is deleted by the deferred cleanup above.
		writeErrorForPgErr(w, err, cfg.Dev)
		return
	}
	if err := tx2.Commit(ctx); err != nil {
		writePlainError(w, http.StatusInternalServerError, errcode.TransactionError, "committing transaction")
		return
	}

	// Step 9 : the disk swap finalizes only on commit AND a set path ;
	// path omitted means the deferred cleanup deletes the temp file instead.
	if hasUpload && finalPath != "" {
		if err := swapUploadIntoPlace(rlog, tempPath, finalPath, overwrite); err != nil {
			rlog.Error("route: swapping upload into place", "temp", tempPath, "final", finalPath, "error", err.Error())
			writePlainError(w, http.StatusInternalServerError, errcode.UploadIOError, "internal error")
			return
		}
	}

	writeRelHttpResponse(w, r, cfg, route, respRaw, templates)
}

// resolveUnderDir rejects a path escaping dir outright ; plain
// filepath.Join+Clean alone would silently rewrite it elsewhere under dir.
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

// randomToken names a temp/backup file's suffix ; always dot-prefixed by
// the caller, so ## Static files' dotfile rule keeps it unservable mid-stream.
func randomToken() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// swapUploadIntoPlace is step 9's rename sequence ; the DB write already
// committed by this point, so a failure here can only log/500, never roll back.
func swapUploadIntoPlace(rlog *slog.Logger, tempPath, finalPath, overwrite string) error {
	dir := filepath.Dir(finalPath)
	backupPath := filepath.Join(dir, ".upload-backup-"+randomToken())
	hadExisting := false
	if _, err := os.Stat(finalPath); err == nil {
		// Not re-enforced here — refusing now would leave the DB referencing
		// a file that was never actually written (step 9 : last-write-wins).
		if overwrite == "disallow" {
			rlog.Warn("route: upload overwrote an existing file at commit time despite overwrite:'disallow' — a concurrent request won the race after the early existence check", "path", finalPath)
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

// buildUploadForMandatory decodes __prepare's raw response, overwrites
// only the rel-filled part/size fields, and re-marshals unnarrowed.
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
