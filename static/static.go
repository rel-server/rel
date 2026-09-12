// Package static implements specs/http-content.md ## Static files and
// specs/templating-2.md's generalized fallback : serving http.static.path's
// colon-separated directory search list, with no directory listing and no
// dotfiles, plus a plain-file/index.html/.ext.jet lookup chain. Access
// control moved to specs/new-routes.md ## Middleware — a database-backed
// gate over a subtree is now an ordinary middleware function declared at
// that prefix, not something this package arranges itself.
//
// This package only ever reports what should be served (Resolve) and
// serves a plain file it already found ; it never executes a .jet
// candidate itself. Jet execution needs the shared jet.Set, rel(), and
// nonce/JWT/role resolution, all of which live in package route (which
// already imports this package, so keeping jet execution there avoids an
// import cycle) — route.NewStaticHandler wraps this package's Resolve for
// the root-level static-fallback mount, and route's own masking helper does
// the same for request.static_file.
package static

import (
	"net/http"
	"os"
	"path"
	"path/filepath"
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
// (re)built" rule. UploadDir is the resolved absolute path of
// http.upload.dir under WriteDir() ("" when uploads are disabled) —
// specs/templating-2.md's subtree no file is ever jet-eligible under.
type Server struct {
	Dirs      []string
	UploadDir string
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
	s := &Server{Dirs: dirs}
	if cfg.Upload.Dir != "" {
		if resolved, ok := resolveUnderStaticDir(dirs[0], cfg.Upload.Dir); ok {
			s.UploadDir = resolved
		}
	}
	return s
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

// isUpload reports whether abs (an absolute path already resolved under one
// of Dirs) falls under UploadDir — specs/templating-2.md's jet exclusion,
// checked against the one absolute path it resolves to, not re-derived per
// http.static.path search-list entry.
func (s *Server) isUpload(abs string) bool {
	if s.UploadDir == "" {
		return false
	}
	return abs == s.UploadDir || strings.HasPrefix(abs, s.UploadDir+string(filepath.Separator))
}

// resolveUnderStaticDir rejects rel escaping dir outright — plain
// filepath.Join+Clean alone would silently rewrite it elsewhere under dir.
// A package-local twin of route/upload_handler.go's resolveUnderDir (same
// contract, different package — that one is private to route and used for
// the shared jet loader's own bounding, not reused from here to avoid a
// route<->static import in either direction).
func resolveUnderStaticDir(dir, rel string) (string, bool) {
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

// Info reports what would be served at a given request path, before any
// masking route function or middleware runs — specs/new-routes.md
// ## Static path masking's request.static shape. A jet-backed path reports
// the .jet SOURCE file's own stats (specs/templating-2.md) : the caller's
// own choice of a .jet file already means a request there renders, and a
// route function relying on this for a jet-backed path gets that source
// file's size/mtime, not anything about the eventual render.
type Info struct {
	Exists     bool
	Size       int64
	ModifiedAt time.Time
}

// JetCandidate is a .ext.jet source Resolve found for a request path with
// no reachable plain file of its own — the caller (package route) is
// responsible for actually rendering it ; this package never does.
type JetCandidate struct {
	// AbsPath is the .ext.jet source file's absolute path on disk.
	AbsPath string
	// Dir is the Dirs[] entry AbsPath was found under.
	Dir string
	// DirIndex is Dir's index within Dirs, for namespacing this candidate's
	// template name against the shared jet.Set (route.staticTemplateName).
	DirIndex int
	// RelPath is AbsPath relative to Dir, slash-separated.
	RelPath string
	// Ext is the extension immediately before ".jet", determining content
	// type ("html", "svg", "json", ... — always non-empty, since the
	// extensionless case always resolves through its ".html" form).
	Ext string
	// Excluded is true when AbsPath falls under UploadDir — the caller must
	// refuse to render it (specs/templating-2.md's upload/jet separation).
	Excluded bool
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

// jetCandidateAt stats dir/jetRelPath directly (not searched across every
// directory — a .jet fallback only ever applies to the specific directory
// being considered at that point in Resolve's own search order).
func (s *Server) jetCandidateAt(dir string, dirIndex int, jetRelPath, ext string) (*JetCandidate, bool) {
	full := path.Join(dir, jetRelPath)
	fi, err := os.Stat(full)
	if err != nil || fi.IsDir() {
		return nil, false
	}
	return &JetCandidate{
		AbsPath: full, Dir: dir, DirIndex: dirIndex, RelPath: jetRelPath, Ext: ext,
		Excluded: s.isUpload(full),
	}, true
}

// NeedsTrailingSlash reports whether reqPath (no leading "/") names an
// existing directory but doesn't already end in "/" — the caller should
// redirect to the slashed URL before calling Resolve, the same trailing-
// slash-on-directory behavior http.FileServer provides natively (lost here
// since Resolve always hands http.ServeFile a concrete file, never a
// directory, so http.ServeFile's own version of this check never fires).
func (s *Server) NeedsTrailingSlash(reqPath string) bool {
	if s == nil || reqPath == "" || strings.HasSuffix(reqPath, "/") || hasDotSegment(reqPath) {
		return false
	}
	_, fi, ok := s.openMulti(reqPath)
	return ok && fi.IsDir()
}

// Resolve reports how reqPath should be handled, per specs/templating-2.md's
// generalized fallback chain : a plain servable file's absolute path
// (existing behavior, extended with the extensionless -> ".html" fallback),
// a .jet candidate for the caller to render, or neither (ok=false, 404).
// A reachable file always wins over its own .jet fallback, checked
// directory-by-directory in http.static.path's own search order — .jet is
// the last resort, never tried ahead of a file that already exists at the
// requested name anywhere in the search list. Safe to call on a nil
// *Server (no http.static.path directories at all).
func (s *Server) Resolve(reqPath string) (servablePath string, jet *JetCandidate, ok bool) {
	if s == nil || hasDotSegment(reqPath) {
		return "", nil, false
	}

	if dir, fi, found := s.openMulti(reqPath); found {
		if !fi.IsDir() {
			return path.Join(dir, reqPath), nil, true
		}
		// Directory : index.html, then index.html.jet — same precedence as
		// any other extensionless request.
		indexPath := path.Join(reqPath, "index.html")
		if _, fi2, ok2 := s.openMulti(indexPath); ok2 && !fi2.IsDir() {
			return path.Join(dir, indexPath), nil, true
		}
		if jc, ok2 := s.jetCandidateAt(dir, s.dirIndex(dir), path.Join(reqPath, "index.html.jet"), "html"); ok2 {
			return "", jc, true
		}
		return "", nil, false
	}

	ext := strings.TrimPrefix(path.Ext(reqPath), ".")
	if ext == "" {
		htmlPath := reqPath + ".html"
		if dir, fi, found := s.openMulti(htmlPath); found && !fi.IsDir() {
			return path.Join(dir, htmlPath), nil, true
		}
		for i, d := range s.Dirs {
			if jc, ok2 := s.jetCandidateAt(d, i, htmlPath+".jet", "html"); ok2 {
				return "", jc, true
			}
		}
		return "", nil, false
	}

	jetPath := reqPath + ".jet"
	for i, d := range s.Dirs {
		if jc, ok2 := s.jetCandidateAt(d, i, jetPath, ext); ok2 {
			return "", jc, true
		}
	}
	return "", nil, false
}

func (s *Server) dirIndex(dir string) int {
	for i, d := range s.Dirs {
		if d == dir {
			return i
		}
	}
	return -1
}

// Stat is Resolve, reduced to the Info shape masking's request.static needs
// (specs/new-routes.md ## Static path masking) — reqPath is relative to
// http.static.path, no leading "/". An excluded jet candidate reports as
// nonexistent, same as any other refused path.
func (s *Server) Stat(reqPath string) Info {
	if s == nil {
		return Info{}
	}
	servable, jc, ok := s.Resolve(reqPath)
	if !ok {
		return Info{}
	}
	full := servable
	if jc != nil {
		if jc.Excluded {
			return Info{}
		}
		full = jc.AbsPath
	}
	fi, err := os.Stat(full)
	if err != nil {
		return Info{}
	}
	return Info{Exists: true, Size: fi.Size(), ModifiedAt: fi.ModTime()}
}

// redirectToTrailingSlash redirects to r.URL.Path+"/", preserving the query
// string — http.FileServer's own behavior for a directory request missing
// its trailing slash, reproduced here since Resolve always hands
// http.ServeFile a concrete file, never a directory.
func redirectToTrailingSlash(w http.ResponseWriter, r *http.Request) {
	to := path.Base(r.URL.Path) + "/"
	if q := r.URL.RawQuery; q != "" {
		to += "?" + q
	}
	http.Redirect(w, r, to, http.StatusMovedPermanently)
}

// Handler is the static fallback mount for a plain file — specs/new-routes.md
// ## Static path masking : rel os.Stats/serves whatever the request path
// resolves to, relative to http.static.path ; a database-backed gate over a
// subtree is a middleware function (## Middleware), not something this
// handler arranges. A .jet candidate 404s here — this handler has no
// rendering capability (see the package doc comment) ; boot.BuildMux mounts
// route.NewStaticHandler instead, which wraps Resolve with jet execution
// on top of the same plain-file serving this method does. db/cfg are
// unused, kept for call-site compatibility (boot.BuildMux mounts every
// handler here the same way).
func (s *Server) Handler(db *pg.DbInfos, cfg *config.Config) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upath := strings.TrimPrefix(r.URL.Path, "/")
		if s.NeedsTrailingSlash(upath) {
			redirectToTrailingSlash(w, r)
			return
		}
		servable, _, ok := s.Resolve(upath)
		if !ok || servable == "" {
			http.NotFound(w, r)
			return
		}
		http.ServeFile(w, r, servable)
	})
}

// ServeFile serves relPath (relative to http.static.path, no leading "/")
// through the same Resolve-driven lookup Handler itself applies —
// specs/new-routes.md ## Static path masking's static_file : a full-control
// route or middleware response can defer to a specific file on disk, not
// necessarily the one at the request's own URL path. Returns false (writes
// nothing) when relPath doesn't resolve to a plain servable file — a .jet
// candidate included, since this method can't render one either ; route's
// own masking helper wraps this with jet execution the same way Handler's
// replacement does for the root-level mount.
func (s *Server) ServeFile(w http.ResponseWriter, r *http.Request, relPath string) bool {
	if s == nil {
		return false
	}
	servable, _, ok := s.Resolve(relPath)
	if !ok || servable == "" {
		return false
	}
	http.ServeFile(w, r, servable)
	return true
}
