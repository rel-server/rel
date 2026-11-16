// Package static serves http.static.path's colon-separated directory list, with no directory listing and no
// dotfiles, through a plain file / index.html / .ext.jet lookup chain. Access control is a middleware function.
//
// It only resolves what to serve and serves plain files ; package route renders .jet candidates.
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

// Server is a static file server, rebuilt on every boot and reload. Dirs is the existing directories of
// http.static.path, in order (first match wins). UploadDir is the absolute http.upload.dir under WriteDir()
// ("" when uploads are disabled), under which nothing is rendered as jet.
type Server struct {
	Dirs      []string
	UploadDir string
}

// New builds a *Server from cfg, or nil if every directory in http.static.path is missing (then nothing is
// mounted).
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

// WriteDir is the first existing directory, where uploads go.
func (s *Server) WriteDir() string {
	if s == nil || len(s.Dirs) == 0 {
		return ""
	}
	return s.Dirs[0]
}

// isUpload reports whether abs, resolved under one of Dirs, falls under UploadDir.
func (s *Server) isUpload(abs string) bool {
	if s.UploadDir == "" {
		return false
	}
	return abs == s.UploadDir || strings.HasPrefix(abs, s.UploadDir+string(filepath.Separator))
}

// resolveUnderStaticDir rejects a rel that escapes dir, rather than letting Clean rewrite it to somewhere under
// dir.
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

// Info reports what would be served at a request path, for request.static in masking. A jet-backed path reports
// the .jet source file's stats, not the render's.
type Info struct {
	Exists     bool
	Size       int64
	ModifiedAt time.Time
}

// JetCandidate is a .ext.jet source Resolve found for a request path with no plain file ; the caller renders it.
type JetCandidate struct {
	// AbsPath is the .ext.jet source file's absolute path on disk.
	AbsPath string
	// Dir is the Dirs[] entry AbsPath was found under.
	Dir string
	// DirIndex is Dir's index within Dirs, to namespace the template name in the shared jet.Set.
	DirIndex int
	// RelPath is AbsPath relative to Dir, slash-separated.
	RelPath string
	// Ext is the extension before ".jet", which sets the content type ; never empty.
	Ext string
	// Excluded is true when AbsPath falls under UploadDir : the caller must not render it.
	Excluded bool
}

// hasDotSegment reports whether p has a dot-prefixed segment ; dotfiles are never served.
func hasDotSegment(p string) bool {
	for _, seg := range strings.Split(p, "/") {
		if strings.HasPrefix(seg, ".") && seg != "" {
			return true
		}
	}
	return false
}

// openMulti tries name across every directory, first success wins.
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

// jetCandidateAt stats dir/jetRelPath in that one directory only.
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

// NeedsTrailingSlash reports whether reqPath (no leading "/") names a directory but lacks the trailing "/" ; the
// caller then redirects before calling Resolve.
func (s *Server) NeedsTrailingSlash(reqPath string) bool {
	if s == nil || reqPath == "" || strings.HasSuffix(reqPath, "/") || hasDotSegment(reqPath) {
		return false
	}
	_, fi, ok := s.openMulti(reqPath)
	return ok && fi.IsDir()
}

// Resolve reports how reqPath is handled : a plain file's absolute path (an extensionless path tries ".html"),
// a .jet candidate for the caller to render, or neither (ok=false, 404). A plain file in any directory wins over
// a .jet. Safe on a nil *Server.
func (s *Server) Resolve(reqPath string) (servablePath string, jet *JetCandidate, ok bool) {
	if s == nil || hasDotSegment(reqPath) {
		return "", nil, false
	}

	if dir, fi, found := s.openMulti(reqPath); found {
		if !fi.IsDir() {
			return path.Join(dir, reqPath), nil, true
		}
		// Directory : index.html, then index.html.jet.
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

// Stat is Resolve reduced to Info, for request.static ; reqPath has no leading "/". An excluded jet candidate
// reports as nonexistent.
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

// redirectToTrailingSlash redirects to r.URL.Path+"/", keeping the query string.
func redirectToTrailingSlash(w http.ResponseWriter, r *http.Request) {
	to := path.Base(r.URL.Path) + "/"
	if q := r.URL.RawQuery; q != "" {
		to += "?" + q
	}
	http.Redirect(w, r, to, http.StatusMovedPermanently)
}

// Handler serves the plain file the request path resolves to ; a .jet candidate 404s (route.NewStaticHandler
// renders them). db/cfg are unused.
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

// ServeFile serves relPath (no leading "/") through Resolve, for masking's static_file. Returns false, writing
// nothing, when relPath is not a plain file (a .jet candidate included).
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
