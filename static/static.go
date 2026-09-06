// Package static implements specs/http-content.md ## Static files and
// ### Access control : serving http.static.path's colon-separated
// directory search list at the fixed /static/ URL prefix, with no
// directory listing, no dotfiles, and an opt-in, named, prefix-scoped
// database access-control mechanism. (### Upload destinations, the same
// section's second half, lives in route — it's a route-function discovery
// mechanism wired through the registry, not something /static/ itself
// serves.)
package static

import (
	"context"
	"net/http"
	"os"
	"path"
	"sort"
	"strings"

	"github.com/bytedance/sonic"

	"github.com/rel-server/rel/config"
	"github.com/rel-server/rel/dbauth"
	"github.com/rel-server/rel/errcode"
	jwtpkg "github.com/rel-server/rel/jwt"
	"github.com/rel-server/rel/logging"
	"github.com/rel-server/rel/pg"
	"github.com/rel-server/rel/pgerr"
)

// Server is a built, ready-to-mount static file server : Dirs is the
// existing-only subset of http.static.path's colon-separated search list,
// in order (first match wins) ; Access is the named access-control rule
// set, sorted longest-prefix-first so an overlapping pair of rules always
// matches the more specific one. Rebuilt on every boot.BuildMux call (see
// its own doc comment) — startup and every SIGUSR1 reload alike, per
// ## Static files' own "this check re-runs every time the inner mux is
// (re)built" rule.
type Server struct {
	Dirs   []string
	Access []accessRule
}

type accessRule struct {
	name     string
	prefix   string
	function string
}

// New builds a *Server from cfg, or nil if EVERY directory in
// http.static.path is missing — ## Static files' "missing directories
// silently skipped... /static/* isn't mounted at ALL only when EVERY
// listed directory is missing" rule ; the caller (boot.BuildMux) simply
// doesn't mount "/static/" at all in that case.
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

	rules := make([]accessRule, 0, len(cfg.Static.Access))
	for name, r := range cfg.Static.Access {
		if r.Prefix == "" || r.Function == "" {
			continue
		}
		rules = append(rules, accessRule{name: name, prefix: r.Prefix, function: r.Function})
	}
	// Longest-prefix-first : deterministic, most-specific-wins matching,
	// independent of the map's own (unspecified) iteration order.
	sort.Slice(rules, func(i, j int) bool { return len(rules[i].prefix) > len(rules[j].prefix) })

	return &Server{Dirs: dirs, Access: rules}
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

// matchRule returns the most specific accessRule whose prefix reqPath
// falls under, ("", false) if none.
func (s *Server) matchRule(reqPath string) (accessRule, bool) {
	for _, r := range s.Access {
		if strings.HasPrefix(reqPath, r.prefix) {
			return r, true
		}
	}
	return accessRule{}, false
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

// Handler is the actual /static/ mount, per net/http's convention : callers
// mount this at "/static/" wrapped in http.StripPrefix("/static/", ...),
// so upath below is already relative to http.static.path.
func (s *Server) Handler(db *pg.DbInfos, cfg *config.Config) http.Handler {
	fs := multiDirFS(s.Dirs)
	fileServer := http.FileServer(fs)

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upath := strings.TrimPrefix(r.URL.Path, "/")

		// Before any filesystem call or access-control gating — closes off
		// serving a partial upload mid-stream or a dotfile like .env either way.
		if hasDotSegment(upath) {
			http.NotFound(w, r)
			return
		}

		rule, gated := s.matchRule(upath)
		if !gated {
			s.serve(w, r, fileServer, upath)
			return
		}

		claims, verified := jwtpkg.VerifyRequest(cfg.Jwt, r)

		// ### Access control : 401 before the existence check (same
		// ordering as /rel/route) — avoids leaking existence to the gate.
		if !verified && !db.AnonymousRoleExists {
			// Unlike /rel and /route, no X-Rel-Errorcode header here — this
			// package's writePlainError never carried one ; flagged, not fixed.
			writePlainError(w, http.StatusUnauthorized, errcode.AnonymousDisabledMessage)
			return
		}

		// Cheap stat before any DB round trip, for a request that was never
		// going to succeed either way.
		_, fi, exists := s.openMulti(upath)
		if !exists {
			http.NotFound(w, r)
			return
		}
		if fi.IsDir() && !s.hasIndex(upath) {
			http.NotFound(w, r)
			return
		}

		if err := checkStaticAccess(r.Context(), db, rule.function, upath, verified, claims); err != nil {
			if status, message, ok := pgerr.RSStatus(err); ok {
				writePlainError(w, status, message)
				return
			}
			logging.FromContext(r.Context()).With("module", "static").Error("check_static_access", "function", rule.function, "path", upath, "error", err.Error())
			writePlainError(w, http.StatusInternalServerError, "internal error")
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

func (s *Server) hasIndex(dirPath string) bool {
	_, _, ok := s.openMulti(path.Join(dirPath, "index.html"))
	return ok
}

// checkStaticAccessPayload is ### Access control's own documented shape :
// {"path": "<request path, relative to http.static.path>", "jwt": JWT | null}.
type checkStaticAccessPayload struct {
	Path string        `json:"path"`
	Jwt  jwtpkg.Claims `json:"jwt"`
}

// checkStaticAccess acquires one connection for the check_static_access
// call — no transaction, no SET LOCAL ROLE (### Access control).
func checkStaticAccess(ctx context.Context, db *pg.DbInfos, qualifiedName, reqPath string, verified bool, claims jwtpkg.Claims) error {
	var jwtVal jwtpkg.Claims
	if verified {
		jwtVal = claims
	}
	payload, err := sonic.Marshal(checkStaticAccessPayload{Path: reqPath, Jwt: jwtVal})
	if err != nil {
		return err
	}
	conn, err := db.Pool.Acquire(ctx)
	if err != nil {
		return err
	}
	defer conn.Release()
	return dbauth.CallJSONBFunction(ctx, conn, qualifiedName, payload)
}

func writePlainError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(status)
	_, _ = w.Write([]byte(message))
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
