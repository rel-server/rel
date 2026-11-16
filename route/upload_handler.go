// stream_upload : the same full-control function is called twice, first with only the upload's metadata to decide
// the destination, then with request.upload.size once the bytes are on disk.
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
	"github.com/samber/oops"

	"github.com/rel-server/rel/config"
	"github.com/rel-server/rel/errcode"
	"github.com/rel-server/rel/httperr"
	"github.com/rel-server/rel/logging"
	"github.com/rel-server/rel/pg"
	"github.com/rel-server/rel/reqauth"
	"github.com/rel-server/rel/static"
)

// requestUploadPayload is request.upload as sent to a stream_upload function.
type requestUploadPayload struct {
	Part json.RawMessage `json:"part,omitempty"`
	Size *int64          `json:"size,omitempty"`
}

// responseUploadPayload is the first call's response upload : its destination decision.
type responseUploadPayload struct {
	Path      *string `json:"path"`
	Mkdir     bool    `json:"mkdir"`
	Overwrite string  `json:"overwrite"`
	MaxSize   *int64  `json:"max_size"`
}

// firstCallEnvelope reads only "upload" : the first call's response is never sent, so its other fields are ignored.
type firstCallEnvelope struct {
	Upload responseUploadPayload `json:"upload"`
}

// handleUploadRoute runs stream_upload's two calls.
func handleUploadRoute(w http.ResponseWriter, r *http.Request, db *pg.DbInfos, cfg *config.Config, reg *Registry, route Route, staticSrv *static.Server, templates *TemplateSet, sess *reqauth.Session) {
	ctx := r.Context()
	rlog := logging.FromContext(ctx).With("module", "route")

	staticInfo := staticInfoForRequest(staticSrv, r)

	// A JSON/text/form body is a 415, checked from Content-Type before either call.
	contentTypeHeader := r.Header.Get("Content-Type")
	mt := mediaTypeOf(contentTypeHeader)
	if mt == "application/json" || strings.HasSuffix(mt, "+json") ||
		strings.HasPrefix(mt, "text/") || mt == "application/x-www-form-urlencoded" {
		httperr.WritePlain(w, http.StatusUnsupportedMediaType, errcode.UnsupportedMediaType, "route accepts a single opaque upload, not a JSON/text/form body")
		return
	}

	if err := tooLargeIfContentLengthExceeds(r, int64(cfg.Http.Upload.MaxSize), "http.upload.max_size"); err != nil {
		writeRequestBodyError(ctx, w, err)
		return
	}
	limitedBody := http.MaxBytesReader(w, r.Body, int64(cfg.Http.Upload.MaxSize))

	isMultipart := false
	var boundary string
	if pmt, params, perr := mime.ParseMediaType(contentTypeHeader); perr == nil && strings.HasPrefix(pmt, "multipart/") {
		isMultipart = true
		boundary = params["boundary"]
	}

	// Read only the upload's headers ; its bytes stay unread until the first call decides where they go.
	partJSON := json.RawMessage("null")
	hasUpload := false
	var mr *multipart.Reader
	var currentPart *multipart.Part

	if isMultipart {
		mr = multipart.NewReader(limitedBody, boundary)
		p, perr := mr.NextPart()
		if perr != nil {
			if perr != io.EOF {
				httperr.WritePlain(w, http.StatusBadRequest, errcode.MalformedMultipart, "malformed multipart body: "+perr.Error())
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
		// Only an explicit zero Content-Length means no upload ; unknown (-1, chunked) might have one.
		hasUpload = true
		if b, merr := sonic.Marshal(synthesizedPseudoPart(r, contentTypeHeader)); merr == nil {
			partJSON = b
		}
	}

	firstUpload, err := sonic.Marshal(requestUploadPayload{Part: partJSON})
	if err != nil {
		httperr.WriteServer(ctx, w, http.StatusInternalServerError, errcode.Internal, "internal error", err)
		return
	}
	// buildFirstReq derives the first call's request JSON with a given request.context, which only middleware sets.
	buildFirstReq := func(reqContext json.RawMessage) ([]byte, error) {
		return buildRelHttpRequest(r, json.RawMessage("null"), sess.Verified, sess.Claims, staticInfo, nil, reqContext, firstUpload, "")
	}
	reqJSON1, err := buildFirstReq(nil)
	if err != nil {
		httperr.WriteServer(ctx, w, http.StatusInternalServerError, errcode.Internal, "encoding request", err)
		return
	}
	r = r.WithContext(withRequestJSON(ctx, reqJSON1))
	ctx = r.Context()

	// Middleware runs once, before the first call only, in the first call's transaction.
	tx1, err := sess.Open(ctx, db.Pool)
	if err != nil {
		httperr.WritePlainError(ctx, w, reqauth.HTTPError(err), cfg.Dev)
		return
	}

	handled, mergedContext, accumulated, err := runMiddlewareChain(ctx, tx1, w, r, cfg, reg, route.AnonPath, buildFirstReq, templates, staticSrv)
	if err != nil {
		_ = tx1.Rollback(ctx)
		tx1.Release()
		return
	}
	if handled {
		if cerr := tx1.Commit(ctx); cerr != nil {
			httperr.WriteServer(ctx, w, http.StatusInternalServerError, errcode.TransactionError, "committing transaction", cerr)
		}
		tx1.Release()
		return
	}
	if mergedContext != nil {
		reqJSON1, err = buildFirstReq(mergedContext)
		if err != nil {
			_ = tx1.Rollback(ctx)
			tx1.Release()
			httperr.WriteServer(ctx, w, http.StatusInternalServerError, errcode.Internal, "encoding request", err)
			return
		}
		r = r.WithContext(withRequestJSON(ctx, reqJSON1))
	}

	// Renew now that the chain has let the request through. Both calls see
	// the claims as received : a renewal only changes the cookie.
	sess.Renew(w)

	ident := route.Function.Identifier.EscapedString()
	var firstEnvelope, firstContent []byte
	frow := tx1.QueryRow(ctx, "select * from "+ident+"($1::jsonb)", reqJSON1)
	if err := frow.Scan(&firstEnvelope, &firstContent); err != nil {
		_ = tx1.Rollback(ctx)
		tx1.Release()
		httperr.WritePostgres(ctx, w, err, cfg.Dev)
		return
	}
	if err := tx1.Commit(ctx); err != nil {
		tx1.Release()
		httperr.WriteServer(ctx, w, http.StatusInternalServerError, errcode.TransactionError, "committing transaction", err)
		return
	}
	tx1.Release()

	var first firstCallEnvelope
	if err := sonic.Unmarshal(firstEnvelope, &first); err != nil {
		httperr.WriteServer(ctx, w, http.StatusInternalServerError, errcode.Internal, "internal error", err)
		return
	}
	upload := first.Upload

	writeDir := ""
	if staticSrv != nil {
		writeDir = staticSrv.UploadDir
	}

	// Without an upload, the first call's path/mkdir/overwrite are ignored.
	var finalPath, tempPath, overwrite string
	// A no-op once swapUploadIntoPlace has moved tempPath.
	defer func() {
		if tempPath != "" {
			_ = os.Remove(tempPath)
		}
	}()
	if hasUpload {
		if upload.Path != nil && *upload.Path != "" {
			if writeDir == "" {
				httperr.WriteServer(ctx, w, http.StatusInternalServerError, errcode.Internal, "internal error",
					oops.With("path", *upload.Path).Errorf("upload route resolved a path but http.upload.dir is unset or no http.static.path directory is configured/exists"))
				return
			}
			cleaned, ok := resolveUnderDir(writeDir, *upload.Path)
			if !ok {
				httperr.WriteServer(ctx, w, http.StatusInternalServerError, errcode.Internal, "internal error",
					oops.With("path", *upload.Path).Errorf("stream_upload's first call returned a path escaping http.upload.dir"))
				return
			}
			finalPath = cleaned

			overwrite = upload.Overwrite
			if overwrite == "" {
				overwrite = "disallow"
			}
			if overwrite == "disallow" {
				if _, statErr := os.Stat(finalPath); statErr == nil {
					httperr.WritePlain(w, http.StatusConflict, errcode.UploadConflict, "a file already exists at the resolved path")
					return
				}
			}
			if upload.Mkdir {
				if err := os.MkdirAll(filepath.Dir(finalPath), 0o755); err != nil {
					httperr.WriteServer(ctx, w, http.StatusInternalServerError, errcode.UploadIOError, "internal error",
						oops.With("path", finalPath).Wrapf(err, "upload mkdir"))
					return
				}
			}
			tempPath = filepath.Join(filepath.Dir(finalPath), ".upload-"+randomToken())
		} else if writeDir != "" {
			// Discard case : still streamed to a staging location, so the
			// second call gets an accurate size.
			tempPath = filepath.Join(writeDir, ".upload-"+randomToken())
		} else {
			httperr.WriteServer(ctx, w, http.StatusInternalServerError, errcode.Internal, "internal error",
				oops.Errorf("upload route has http.upload.dir unset or no http.static.path directory configured/exists to stage the discarded upload into"))
			return
		}
	}

	// Stream to the temp file ; for multipart, one more NextPart() catches
	// a second part.
	var size int64
	if hasUpload {
		f, ferr := os.OpenFile(tempPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
		if ferr != nil {
			httperr.WriteServer(ctx, w, http.StatusInternalServerError, errcode.UploadIOError, "internal error",
				oops.With("path", tempPath).Wrapf(ferr, "creating temp upload file"))
			return
		}
		var src io.ReadCloser = limitedBody
		if isMultipart {
			src = currentPart
		}
		// The first call's max_size can lower http.upload.max_size, never raise it.
		if upload.MaxSize != nil && *upload.MaxSize >= 0 && *upload.MaxSize < int64(cfg.Http.Upload.MaxSize) {
			src = http.MaxBytesReader(w, src, *upload.MaxSize)
		}
		n, cerr := io.Copy(f, src)
		_ = f.Close()
		size = n
		if cerr != nil {
			var maxErr *http.MaxBytesError
			if errors.As(cerr, &maxErr) {
				msg := "request body exceeds http.upload.max_size"
				if upload.MaxSize != nil && *upload.MaxSize < int64(cfg.Http.Upload.MaxSize) {
					msg = "request body exceeds the upload size limit set by " + route.Function.Identifier.String()
				}
				httperr.WritePlain(w, http.StatusRequestEntityTooLarge, errcode.BodyTooLarge, msg)
				return
			}
			httperr.WritePlain(w, http.StatusBadRequest, errcode.MalformedBody, "reading upload body: "+cerr.Error())
			return
		}
		if isMultipart {
			_ = currentPart.Close()
			if _, nerr := mr.NextPart(); nerr != io.EOF {
				if nerr == nil {
					httperr.WritePlain(w, http.StatusUnsupportedMediaType, errcode.UnsupportedMediaType, "route accepts exactly one upload part, request carried more than one")
				} else {
					httperr.WritePlain(w, http.StatusBadRequest, errcode.MalformedMultipart, "malformed multipart body: "+nerr.Error())
				}
				return
			}
		}
	}

	secondUploadPayload := requestUploadPayload{Part: partJSON}
	if hasUpload {
		secondUploadPayload.Size = &size
		// Only the second call gets sniffed_content_type, read from the temp file's head.
		if sniffed, ok := sniffFileHead(tempPath); ok {
			secondUploadPayload.Part = withSniffedContentType(partJSON, sniffed)
		}
	} else {
		secondUploadPayload.Part = json.RawMessage("null")
	}
	secondUpload, err := sonic.Marshal(secondUploadPayload)
	if err != nil {
		httperr.WriteServer(ctx, w, http.StatusInternalServerError, errcode.Internal, "internal error", err)
		return
	}
	reqJSON2, err := buildRelHttpRequest(r, json.RawMessage("null"), sess.Verified, sess.Claims, staticInfo, nil, nil, secondUpload, "")
	if err != nil {
		httperr.WriteServer(ctx, w, http.StatusInternalServerError, errcode.Internal, "encoding request", err)
		return
	}
	r = r.WithContext(withRequestJSON(ctx, reqJSON2))

	// A second connection/transaction, as the same user ; the middleware chain and
	// renew already ran before the first call and aren't repeated.
	tx2, err := sess.Open(ctx, db.Pool)
	if err != nil {
		httperr.WritePlainError(ctx, w, reqauth.HTTPError(err), cfg.Dev)
		return
	}
	defer tx2.Release()

	var secondEnvelope, secondContent []byte
	mrow := tx2.QueryRow(ctx, "select * from "+ident+"($1::jsonb)", reqJSON2)
	if err := mrow.Scan(&secondEnvelope, &secondContent); err != nil {
		_ = tx2.Rollback(ctx)
		// No commit : the temp file is deleted by the deferred cleanup above.
		httperr.WritePostgres(ctx, w, err, cfg.Dev)
		return
	}
	if err := tx2.Commit(ctx); err != nil {
		httperr.WriteServer(ctx, w, http.StatusInternalServerError, errcode.TransactionError, "committing transaction", err)
		return
	}

	// The disk swap finalizes only on commit AND a set path ; path omitted
	// means the deferred cleanup deletes the temp file instead.
	if hasUpload && finalPath != "" {
		if err := swapUploadIntoPlace(rlog, tempPath, finalPath, overwrite); err != nil {
			httperr.WriteServer(ctx, w, http.StatusInternalServerError, errcode.UploadIOError, "internal error",
				oops.With("temp", tempPath, "final", finalPath).Wrapf(err, "swapping upload into place"))
			return
		}
	}

	writeFullControlResponse(w, r, cfg, route.Function.Identifier.String(), route, accumulated, secondEnvelope, secondContent, templates, staticSrv)
}

// sniffFileHead detects path's content type from its head ; ok is false for an empty file or a read failure.
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

// withSniffedContentType sets sniffed_content_type on an already-encoded part.
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

// randomToken names a temp/backup file's suffix ; callers dot-prefix it so the file is never served mid-stream.
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
