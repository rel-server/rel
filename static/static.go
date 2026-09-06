// Package static implements specs/http-content.md ## Static files :
// serving http.static.path's colon-separated directory search list, with
// no directory listing and no dotfiles. Access control moved to
// specs/new-routes.md ## Middleware — a database-backed gate over a
// subtree is now an ordinary middleware function declared at that prefix,
// not something this package arranges itself. (### Upload destinations,
// specs/http-content.md ## Static files' second half, lives in route — it's
// a route-function discovery mechanism wired through the registry, not
// something this package serves.)
package static

import (
	"net/http"
	"os"
	"path"
	"strings"
	"time"

	"github.com/rel-server/rel/config"
	"github.com/rel-server/rel/pg"
)

// Server is a built, ready-to-mount static file server : Dirs is the
// existing-only subset of http.static.path's colon-separated search list,
// in order (first match wins). Rebuilt on every boot.BuildMux call (see its
// own doc comment) — startup and every SIGUSR1 reload alike, per
// ## Static files' own "this check re-runs every time the inner mux is
// (re)built" rule.
type Server struct {
	Dirs []string
}

// New builds a *Server from cfg, or nil if EVERY directory in
// http.static.path is missing — ## Static files' "missing directories
// silently skipped... /static/* isn't mounted at ALL only when EVERY
// listed directory is missing" rule ; the caller (boot.BuildMux) simply
// doesn't mount the static fallback at all in that case.
func New(cfg config.Http) *Server {
	var dirs []string
	for _, d := range config.SplitPathList(cfg.Static.Path) {
		if fi, err := os.Stat(d); err == nil && fi.IsDir() {
			dirs = append(dirs, d)
		}
	}
	if len(dirs) == 0 {
		return nil
	}
	return &Server{Dirs: dirs}
}

// WriteDir is ### Upload destinations' "the FIRST listed directory
// specifically" — the one, unambiguous write target ; "" if no directory
// is configured/exists at all (New returns nil in that case, so this is
// only meaningful on a non-nil *Server, kept as a method for callers that
// already have one rather than re-deriving the same rule independently).
func (s *Server) WriteDir() string {
	if s == nil || len(s.Dirs) == 0 {
		return ""
	}
	return s.Dirs[0]
}

// Info is specs/new-routes.md ## Static path masking's request.static
// shape — what would be served at a given request path, before any
// masking route function or middleware runs.
type Info struct {
	Exists     bool
	Size       int64
	ModifiedAt time.Time
}

// Stat reports what would be served at reqPath (relative to
// http.static.path, no leading "/") — Exists false for a nonexistent path,
// a directory with no index.html, or one that fails the same dotfile/
// traversal check Handler itself applies. Safe to call on a nil *Server
// (no http.static.path directories at all).
func (s *Server) Stat(reqPath string) Info {
	if s == nil || hasDotSegment(reqPath) {
		return Info{}
	}
	_, fi, ok := s.openMulti(reqPath)
	if !ok {
		return Info{}
	}
	if fi.IsDir() {
		_, fi, ok = s.openMulti(path.Join(reqPath, "index.html"))
		if !ok {
			return Info{}
		}
	}
	return Info{Exists: true, Size: fi.Size(), ModifiedAt: fi.ModTime()}
}

// hasDotSegment implements ## Static files' "Dotfiles are never served"
// rule, checked before any filesystem call.
func hasDotSegment(p string) bool {
	for _, seg := range strings.Split(p, "/") {
		if strings.HasPrefix(seg, ".") && seg != "" {
			return true
		}
	}
	return false
}

// openMulti tries name across every directory, first success wins ; a
// plain function since the caller also needs *os.FileInfo.
func (s *Server) openMulti(name string) (dir string, fi os.FileInfo, ok bool) {
	for _, d := range s.Dirs {
		full := path.Join(d, name)
		info, err := os.Stat(full)
		if err == nil {
			return d, info, true
		}
	}
	return "", nil, false
}

// Handler is the static fallback mount — specs/new-routes.md
// ## Static path masking : rel os.Stats/serves whatever the request path
// resolves to, relative to http.static.path ; a database-backed gate over
// a subtree is now a middleware function (## Middleware), not something
// this handler arranges. db/cfg are unused now that access control moved
// out, kept for call-site compatibility (boot.BuildMux mounts this the same
// way it mounts every other handler here).
func (s *Server) Handler(db *pg.DbInfos, cfg *config.Config) http.Handler {
	fileServer := http.FileServer(multiDirFS(s.Dirs))

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upath := strings.TrimPrefix(r.URL.Path, "/")

		// Before any filesystem call — closes off serving a partial upload
		// mid-stream or a dotfile like .env.
		if hasDotSegment(upath) {
			http.NotFound(w, r)
			return
		}

		s.serve(w, r, fileServer, upath)
	})
}

// serve applies ## Static files' no-directory-listing rule and otherwise
// delegates to http.FileServer for the actual bytes.
func (s *Server) serve(w http.ResponseWriter, r *http.Request, fileServer http.Handler, upath string) {
	_, fi, exists := s.openMulti(upath)
	if !exists {
		http.NotFound(w, r)
		return
	}
	if fi.IsDir() && !s.hasIndex(upath) {
		http.NotFound(w, r)
		return
	}
	fileServer.ServeHTTP(w, r)
}

// ServeFile serves relPath (relative to http.static.path, no leading "/")
// through the same multi-directory search/no-directory-listing/dotfile
// rules Handler itself applies — specs/new-routes.md ## Static path
// masking's static_file : a full-control route or middleware response can
// defer to a specific file on disk, not necessarily the one at the
// request's own URL path. Returns false (writes nothing) when relPath
// doesn't resolve to anything servable, leaving 404 rendering to the
// caller — route's own error format, not this package's.
func (s *Server) ServeFile(w http.ResponseWriter, r *http.Request, relPath string) bool {
	if s == nil || hasDotSegment(relPath) {
		return false
	}
	_, fi, exists := s.openMulti(relPath)
	if !exists {
		return false
	}
	if fi.IsDir() && !s.hasIndex(relPath) {
		return false
	}
	fileServer := http.FileServer(multiDirFS(s.Dirs))
	r2 := r.Clone(r.Context())
	r2.URL.Path = "/" + relPath
	fileServer.ServeHTTP(w, r2)
	return true
}

func (s *Server) hasIndex(dirPath string) bool {
	_, _, ok := s.openMulti(path.Join(dirPath, "index.html"))
	return ok
}

// multiDirFS backs http.FileServer directly, trying each directory's own
// http.Dir in order, first success wins.
type multiDirFS []string

func (m multiDirFS) Open(name string) (http.File, error) {
	var firstErr error
	for _, d := range m {
		f, err := http.Dir(d).Open(name)
		if err == nil {
			return f, nil
		}
		if firstErr == nil {
			firstErr = err
		}
	}
	return nil, firstErr
}
