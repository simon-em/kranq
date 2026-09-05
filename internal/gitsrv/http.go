package gitsrv

import (
	"fmt"
	"net/http"
	"net/http/cgi"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/effetmonstre/forge/internal/token"
)

// git-http-backend is not on PATH on macOS; it lives in git's exec directory.
// Asking git where that is beats guessing at install layouts.
func Backend() (string, error) {
	out, err := exec.Command("git", "--exec-path").Output()
	if err != nil {
		return "", fmt.Errorf("asking git for its exec path: %w", err)
	}
	path := filepath.Join(strings.TrimSpace(string(out)), "git-http-backend")
	if _, err := os.Stat(path); err != nil {
		return "", fmt.Errorf("git-http-backend is not at %s: %w", path, err)
	}
	return path, nil
}

type Server struct {
	Store    *Store
	Backend  string
	Tokens   func() (*token.Set, error)
	OnAuth   func(tokenName, repo string)
	Log      func(format string, args ...any)
	MaxBytes int64
}

const DefaultMaxBytes = 512 << 20

func (s *Server) logf(format string, args ...any) {
	if s.Log != nil {
		s.Log(format, args...)
	}
}

func (s *Server) Handler() http.Handler {
	return http.HandlerFunc(s.serve)
}

func (s *Server) serve(w http.ResponseWriter, r *http.Request) {
	repo, err := RepoName(r.URL.Path)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}

	name, ok := s.authenticate(r)
	if !ok {
		// Git only offers credentials after a challenge, so this header is what
		// makes `git push` send the token rather than fail outright.
		w.Header().Set("WWW-Authenticate", `Basic realm="forge"`)
		http.Error(w, "a forge token is required", http.StatusUnauthorized)
		s.logf("git: rejected %s %s", r.Method, r.URL.Path)
		return
	}
	if s.OnAuth != nil {
		s.OnAuth(name, repo)
	}
	s.logf("git: %s %s as %s", r.Method, r.URL.Path, name)

	dir, err := s.Store.Ensure(r.Context(), repo)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	max := s.MaxBytes
	if max <= 0 {
		max = DefaultMaxBytes
	}
	r.Body = http.MaxBytesReader(w, r.Body, max)

	// The path git-http-backend sees must be relative to the project root, and
	// that root is the single repository rather than the whole store, so a
	// caller cannot reach a repository it did not name.
	proxied := r.Clone(r.Context())
	proxied.URL.Path = strip(r.URL.Path, repo)

	(&cgi.Handler{
		Path: s.Backend,
		Dir:  dir,
		Env: []string{
			"GIT_PROJECT_ROOT=" + dir,
			"GIT_HTTP_EXPORT_ALL=1",
			"REMOTE_USER=" + name,
			"FORGE_TOKEN_NAME=" + name,
		},
		InheritEnv: []string{"PATH", "HOME"},
	}).ServeHTTP(w, proxied)
}

// "/dx.git/info/refs" becomes "/info/refs". A leftover empty segment makes git
// report the path as "aliased" and refuse to serve it.
func strip(path, repo string) string {
	rest := strings.TrimPrefix(path, "/")
	for _, prefix := range []string{repo + ".git", repo} {
		if trimmed, found := strings.CutPrefix(rest, prefix); found {
			rest = trimmed
			break
		}
	}
	return "/" + strings.TrimPrefix(rest, "/")
}

func (s *Server) authenticate(r *http.Request) (string, bool) {
	_, secret, ok := r.BasicAuth()
	if !ok || secret == "" {
		return "", false
	}
	set, err := s.Tokens()
	if err != nil {
		return "", false
	}
	name, valid := set.Verify(secret)
	if !valid {
		return "", false
	}
	return name, true
}

func (s *Server) Touch(home, name string) {
	set, err := token.Load(token.Path(home))
	if err != nil {
		return
	}
	set.Touch(name, time.Now())
	_ = set.Save(token.Path(home))
}
