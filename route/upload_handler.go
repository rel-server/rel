// This file implements specs/new-routes.md ## `stream_upload` : the same
// full-control function is called twice — once with request.upload
// metadata-only (no bytes received yet, deciding the destination), once
// with request.upload.size filled in (bytes already landed on disk). The
// actual disk-streaming/atomic-swap mechanics are unchanged from the old
// __prepare/mandatory-pair mechanism they replace.
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

	"github.com/rel-server/rel/config"
	"github.com/rel-server/rel/dbauth"
	"github.com/rel-server/rel/errcode"
	jwtpkg "github.com/rel-server/rel/jwt"
	"github.com/rel-server/rel/logging"
	"github.com/rel-server/rel/pg"
	"github.com/rel-server/rel/static"
)

// requestUploadPayload is HttpRequest.upload's shape on the way IN to a
// stream_upload function : only Part/Size are ever meaningful here — the
// destination fields (path/mkdir/overwrite/max_size) are the function's own
// decision, read back from its RESPONSE instead (responseUploadPayload).
type requestUploadPayload struct {
	Part json.RawMessage `json:"part,omitempty"`
	Size *int64          `json:"size,omitempty"`
}

// responseUploadPayload is HttpResponse.upload's shape on the way OUT of a
// stream_upload function's first call : its destination decision.
type responseUploadPayload struct {
	Path      *string `json:"path"`
	Mkdir     bool    `json:"mkdir"`
	Overwrite string  `json:"overwrite"`
	MaxSize   *int64  `json:"max_size"`
}

// firstCallEnvelope is only ever consulted for its "upload" field ; any
// other field a first-call response sets (cookies, say) is meaningless and
// ignored, since the first call's own response is never written to the
// client — only the second call's is.
type firstCallEnvelope struct {
	Upload responseUploadPayload `json:"upload"`
}

// handleUploadRoute implements ## `stream_upload`'s two-call flow.
func handleUploadRoute(w http.ResponseWriter, r *http.Request, db *pg.DbInfos, cfg *config.Config, reg *Registry, route Route, staticSrv *static.Server, templates *TemplateSet, verified bool, claims jwtpkg.Claims) {
	ctx := r.Context()
	rlog := logging.FromContext(ctx).With("module", "route")

	staticInfo := staticInfoForRequest(staticSrv, r)

	// Step : a JSON/text/form body is a 415, checked before either call
	// runs, from Content-Type alone.
	contentTypeHeader := r.Header.Get("Content-Type")
	mt := mediaTypeOf(contentTypeHeader)
	if mt == "application/json" || strings.HasSuffix(mt, "+json") ||
		strings.HasPrefix(mt, "text/") || mt == "application/x-www-form-urlencoded" {
		writePlainError(w, http.StatusUnsupportedMediaType, errcode.UnsupportedMediaType, "route accepts a single opaque upload, not a JSON/text/form body")
		return
	}

	if err := tooLargeIfContentLengthExceeds(r, int64(cfg.Http.MaxUploadSize), "http.max_upload_size"); err != nil {
		writeRequestBodyError(w, err)
		return
	}
	limitedBody := http.MaxBytesReader(w, r.Body, int64(cfg.Http.MaxUploadSize))

	isMultipart := false
	var boundary string
	if pmt, params, perr := mime.ParseMediaType(contentTypeHeader); perr == nil && strings.HasPrefix(pmt, "multipart/") {
		isMultipart = true
		boundary = params["boundary"]
	}

	// Read ONLY the incoming upload's headers — part is built without
	// touching a single byte of the actual payload.
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

	firstUpload, err := sonic.Marshal(requestUploadPayload{Part: partJSON})
	if err != nil {
		writePlainError(w, http.StatusInternalServerError, errcode.Internal, "internal error")
		return
	}
	// buildFirstReq re-derives the first call's request JSON for a given
	// request.context — nil unless middleware ran and set one.
	buildFirstReq := func(reqContext json.RawMessage) ([]byte, error) {
		return buildRelHttpRequest(r, json.RawMessage("null"), verified, claims, staticInfo, nil, reqContext, firstUpload, "")
	}
	reqJSON1, err := buildFirstReq(nil)
	if err != nil {
		writePlainError(w, http.StatusInternalServerError, errcode.Internal, "encoding request")
		return
	}
	r = r.WithContext(withRequestJSON(ctx, reqJSON1))
	ctx = r.Context()

	// ## Middleware : "for a stream_upload route, the middleware chain runs
	// once, ahead of the first call only" — same transaction as the first
	// call itself, after the role switch, same as any other middleware.
	conn, err := db.Pool.Acquire(ctx)
	if err != nil {
		writePlainError(w, http.StatusInternalServerError, errcode.DBUnavailable, "acquiring connection")
		return
	}

	tx1, err := conn.Begin(ctx)
	if err != nil {
		conn.Release()
		writePlainError(w, http.StatusInternalServerError, errcode.TransactionError, "starting transaction")
		return
	}
	if err := dbauth.SetLocalClaims(ctx, tx1, claims); err != nil {
		_ = tx1.Rollback(ctx)
		conn.Release()
		writePlainError(w, http.StatusInternalServerError, errcode.Internal, "setting jwt claims")
		return
	}

	// Renew (Lifecycle step 4) waits until after the middleware chain below
	// decides not to reject — see route/handler.go's identical note. Role
	// resolution reads claims' role only, unaffected by renewal.
	role := jwtpkg.ResolveRole(cfg.Pg.Query.AnonymousRole, claims, verified)
	if role == "" {
		_ = tx1.Rollback(ctx)
		conn.Release()
		writePlainError(w, http.StatusInternalServerError, errcode.NoRoleConfigured, dbauth.NoRoleConfiguredMessage)
		return
	}
	if err := dbauth.SetLocalRole(ctx, tx1, role); err != nil {
		_ = tx1.Rollback(ctx)
		conn.Release()
		writeErrorForPgErr(w, err, cfg.Dev)
		return
	}

	handled, mergedContext, accumulated, err := runMiddlewareChain(ctx, tx1, w, r, cfg, reg, route.AnonPath, buildFirstReq, templates, staticSrv)
	if err != nil {
		_ = tx1.Rollback(ctx)
		conn.Release()
		return
	}
	if handled {
		if cerr := tx1.Commit(ctx); cerr != nil {
			writePlainError(w, http.StatusInternalServerError, errcode.TransactionError, "committing transaction")
		}
		conn.Release()
		return
	}
	if mergedContext != nil {
		reqJSON1, err = buildFirstReq(mergedContext)
		if err != nil {
			_ = tx1.Rollback(ctx)
			conn.Release()
			writePlainError(w, http.StatusInternalServerError, errcode.Internal, "encoding request")
			return
		}
		r = r.WithContext(withRequestJSON(ctx, reqJSON1))
	}

	// Renew now that the chain has let the request through — mutates
	// claims (unlike route/handler.go's discard), since the second call
	// below builds its OWN request fresh and should see the renewed
	// session, same as it did before middleware existed (the first call's
	// reqJSON1 above is already built/used by this point, so it keeps
	// showing the pre-renewal claims either way).
	if verified {
		claims = jwtpkg.RenewIfDue(cfg.Jwt, w, claims)
	}

	ident := route.Function.Identifier.EscapedString()
	var firstEnvelope, firstContent []byte
	frow := tx1.QueryRow(ctx, "select * from "+ident+"($1::jsonb)", reqJSON1)
	if err := frow.Scan(&firstEnvelope, &firstContent); err != nil {
		_ = tx1.Rollback(ctx)
		conn.Release()
		writeErrorForPgErr(w, err, cfg.Dev)
		return
	}
	if err := tx1.Commit(ctx); err != nil {
		conn.Release()
		writePlainError(w, http.StatusInternalServerError, errcode.TransactionError, "committing transaction")
		return
	}
	conn.Release()

	var first firstCallEnvelope
	if err := sonic.Unmarshal(firstEnvelope, &first); err != nil {
		rlog.Error("route: decoding stream_upload's first-call response", "function", route.Function.Identifier.String(), "error", err.Error())
		writePlainError(w, http.StatusInternalServerError, errcode.Internal, "internal error")
		return
	}
	upload := first.Upload

	writeDir := ""
	if staticSrv != nil {
		writeDir = staticSrv.WriteDir()
	}

	// Traversal validation/409 check/mkdir are skipped entirely when
	// !hasUpload — the first call's path/mkdir/overwrite are never acted on.
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
				rlog.Error("route: stream_upload's first call returned a path escaping http.static.path", "path", *upload.Path)
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
			// second call gets an accurate size.
			tempPath = filepath.Join(writeDir, ".upload-"+randomToken())
		} else {
			rlog.Error("route: upload route has no http.static.path directory configured/exists to stage the discarded upload into")
			writePlainError(w, http.StatusInternalServerError, errcode.Internal, "internal error")
			return
		}
	}

	// Stream to the temp file ; for multipart, one more NextPart() catches
	// a second part.
	var size int64
	if hasUpload {
		f, ferr := os.OpenFile(tempPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
		if ferr != nil {
			rlog.Error("route: creating temp upload file", "path", tempPath, "error", ferr.Error())
			writePlainError(w, http.StatusInternalServerError, errcode.UploadIOError, "internal error")
			return
		}
		var src io.ReadCloser = limitedBody
		if isMultipart {
			src = currentPart
		}
		// The first call may tighten the upload cap further (e.g. a
		// per-user quota) via Upload.max_size — it can only lower
		// cfg.Http.MaxUploadSize, never raise it, since limitedBody is
		// already bounded to that ceiling upstream.
		if upload.MaxSize != nil && *upload.MaxSize >= 0 && *upload.MaxSize < int64(cfg.Http.MaxUploadSize) {
			src = http.MaxBytesReader(w, src, *upload.MaxSize)
		}
		n, cerr := io.Copy(f, src)
		_ = f.Close()
		size = n
		if cerr != nil {
			var maxErr *http.MaxBytesError
			if errors.As(cerr, &maxErr) {
				msg := "request body exceeds http.max_upload_size"
				if upload.MaxSize != nil && *upload.MaxSize < int64(cfg.Http.MaxUploadSize) {
					msg = "request body exceeds the upload size limit set by " + route.Function.Identifier.String()
				}
				writePlainError(w, http.StatusRequestEntityTooLarge, errcode.BodyTooLarge, msg)
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

	secondUploadPayload := requestUploadPayload{Part: partJSON}
	if hasUpload {
		secondUploadPayload.Size = &size
		// ## Content-type sniffing : the only place stream_upload's own
		// Part.sniffed_content_type gets set — the first call has no bytes
		// yet, so it never sniffs at all ; here the bytes already landed on
		// disk, so sniff from the temp file's head rather than re-reading
		// the (already-consumed) request body.
		if sniffed, ok := sniffFileHead(tempPath); ok {
			secondUploadPayload.Part = withSniffedContentType(partJSON, sniffed)
		}
	} else {
		secondUploadPayload.Part = json.RawMessage("null")
	}
	secondUpload, err := sonic.Marshal(secondUploadPayload)
	if err != nil {
		writePlainError(w, http.StatusInternalServerError, errcode.Internal, "internal error")
		return
	}
	reqJSON2, err := buildRelHttpRequest(r, json.RawMessage("null"), verified, claims, staticInfo, nil, nil, secondUpload, "")
	if err != nil {
		writePlainError(w, http.StatusInternalServerError, errcode.Internal, "encoding request")
		return
	}
	r = r.WithContext(withRequestJSON(ctx, reqJSON2))

	// A second connection/transaction ; check_session/renew already ran
	// once, before the first call, and aren't repeated.
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
	if err := dbauth.SetLocalClaims(ctx, tx2, claims); err != nil {
		_ = tx2.Rollback(ctx)
		writePlainError(w, http.StatusInternalServerError, errcode.Internal, "setting jwt claims")
		return
	}
	if err := dbauth.SetLocalRole(ctx, tx2, role); err != nil {
		_ = tx2.Rollback(ctx)
		writeErrorForPgErr(w, err, cfg.Dev)
		return
	}

	var secondEnvelope, secondContent []byte
	mrow := tx2.QueryRow(ctx, "select * from "+ident+"($1::jsonb)", reqJSON2)
	if err := mrow.Scan(&secondEnvelope, &secondContent); err != nil {
		_ = tx2.Rollback(ctx)
		// No commit : the temp file is deleted by the deferred cleanup above.
		writeErrorForPgErr(w, err, cfg.Dev)
		return
	}
	if err := tx2.Commit(ctx); err != nil {
		writePlainError(w, http.StatusInternalServerError, errcode.TransactionError, "committing transaction")
		return
	}

	// The disk swap finalizes only on commit AND a set path ; path omitted
	// means the deferred cleanup deletes the temp file instead.
	if hasUpload && finalPath != "" {
		if err := swapUploadIntoPlace(rlog, tempPath, finalPath, overwrite); err != nil {
			rlog.Error("route: swapping upload into place", "temp", tempPath, "final", finalPath, "error", err.Error())
			writePlainError(w, http.StatusInternalServerError, errcode.UploadIOError, "internal error")
			return
		}
	}

	writeFullControlResponse(w, r, cfg, route.Function.Identifier.String(), route, accumulated, secondEnvelope, secondContent, templates, staticSrv)
}

// sniffFileHead reads enough of path's head for net/http.DetectContentType
// (512 bytes is its own documented ceiling — more is never useful) ; ok is
// false for an empty file (nothing to sniff) or a read failure, in which
// case the caller leaves Part.sniffed_content_type absent rather than
// guessing.
func sniffFileHead(path string) (sniffed string, ok bool) {
	f, err := os.Open(path)
	if err != nil {
		return "", false
	}
	defer f.Close()
	buf := make([]byte, 512)
	n, _ := io.ReadFull(f, buf)
	if n == 0 {
		return "", false
	}
	return http.DetectContentType(buf[:n]), true
}

// withSniffedContentType decodes partJSON, sets SniffedContentType, and
// re-marshals — partJSON was already built once (requestPartFrom/
// synthesizedPseudoPart) before any bytes were available, so sniffing
// patches it in after the fact rather than threading sniffed-ness through
// the earlier construction.
func withSniffedContentType(partJSON json.RawMessage, sniffed string) json.RawMessage {
	var part requestPart
	if err := sonic.Unmarshal(partJSON, &part); err != nil {
		return partJSON
	}
	part.SniffedContentType = &sniffed
	encoded, err := sonic.Marshal(part)
	if err != nil {
		return partJSON
	}
	return encoded
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

// swapUploadIntoPlace is the final rename sequence ; the DB write already
// committed by this point, so a failure here can only log/500, never roll back.
func swapUploadIntoPlace(rlog *slog.Logger, tempPath, finalPath, overwrite string) error {
	dir := filepath.Dir(finalPath)
	backupPath := filepath.Join(dir, ".upload-backup-"+randomToken())
	hadExisting := false
	if _, err := os.Stat(finalPath); err == nil {
		// Not re-enforced here — refusing now would leave the DB referencing
		// a file that was never actually written (last-write-wins).
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
