package gitsrv

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func store(t *testing.T) *Store {
	t.Helper()
	return &Store{Root: t.TempDir(), KranqBin: "/usr/local/bin/kranq", SocketPath: "/home/x/.kranq/kranq.sock"}
}

// The repo name arrives in a URL from a caller and becomes a directory path.
func TestRepoNamesThatEscapeAreRefused(t *testing.T) {
	for _, bad := range []string{
		"", ".", "..", "../etc", "a/b", "/etc/passwd", "..%2Fetc",
		".hidden", "-flag", strings.Repeat("x", 65), "a b", "a;rm -rf /",
	} {
		if err := ValidRepo(bad); err == nil {
			t.Fatalf("%q was accepted as a repository name", bad)
		}
	}
	for _, good := range []string{"dx", "dx.git", "my-repo", "my_repo", "repo.name", "a"} {
		if err := ValidRepo(good); err != nil {
			t.Fatalf("%q rejected: %v", good, err)
		}
	}
}

func TestRepoNameFromURLPath(t *testing.T) {
	cases := map[string]string{
		"/dx.git/info/refs":        "dx",
		"/dx.git/git-receive-pack": "dx",
		"dx.git":                   "dx",
		"/my-repo.git/info/refs":   "my-repo",
	}
	for path, want := range cases {
		got, err := RepoName(path)
		if err != nil {
			t.Fatalf("%q: %v", path, err)
		}
		if got != want {
			t.Fatalf("%q -> %q, want %q", path, got, want)
		}
	}
	for _, bad := range []string{"/../../etc/passwd", "//", "/"} {
		if _, err := RepoName(bad); err == nil {
			t.Fatalf("%q was accepted", bad)
		}
	}
}

func gitConfig(t *testing.T, dir, key string) string {
	t.Helper()
	out, err := exec.Command("git", "--git-dir", dir, "config", "--get", key).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// Each of these was proven necessary by pushing without it.
func TestEnsureSetsTheConfigAPushActuallyNeeds(t *testing.T) {
	s := store(t)
	dir, err := s.Ensure(context.Background(), "dx")
	if err != nil {
		t.Fatal(err)
	}
	// Without this, a push from the shallow clone a CI container starts with is
	// rejected with "shallow update not allowed".
	if got := gitConfig(t, dir, "receive.shallowUpdate"); got != "true" {
		t.Fatalf("receive.shallowUpdate = %q", got)
	}
	// Without this, push options never reach the hook and every run would use
	// defaults nobody asked for.
	if got := gitConfig(t, dir, "receive.advertisePushOptions"); got != "true" {
		t.Fatalf("receive.advertisePushOptions = %q", got)
	}
	// Without this, pushing over http is refused outright.
	if got := gitConfig(t, dir, "http.receivepack"); got != "true" {
		t.Fatalf("http.receivepack = %q", got)
	}
}

func TestEnsureIsIdempotent(t *testing.T) {
	s := store(t)
	first, err := s.Ensure(context.Background(), "dx")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(first, "marker"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	second, err := s.Ensure(context.Background(), "dx")
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Fatalf("%s then %s", first, second)
	}
	if _, err := os.Stat(filepath.Join(second, "marker")); err != nil {
		t.Fatal("the repository was recreated rather than reused")
	}
}

func TestHooksAreInstalledAndExecutable(t *testing.T) {
	s := store(t)
	dir, err := s.Ensure(context.Background(), "dx")
	if err != nil {
		t.Fatal(err)
	}
	for _, phase := range []string{"pre-receive", "post-receive"} {
		path := filepath.Join(dir, "hooks", phase)
		info, err := os.Stat(path)
		if err != nil {
			t.Fatalf("%s: %v", phase, err)
		}
		if info.Mode().Perm()&0o100 == 0 {
			t.Fatalf("%s is %04o and will never run", phase, info.Mode().Perm())
		}
		body, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(body), "git-hook "+phase) {
			t.Fatalf("%s does not invoke kranq: %s", phase, body)
		}
		if !strings.Contains(string(body), s.SocketPath) {
			t.Fatalf("%s cannot reach the daemon: %s", phase, body)
		}
	}
}

func TestHookPathsWithSpacesSurvive(t *testing.T) {
	s := store(t)
	s.KranqBin = "/Users/some one/.local/bin/kranq"
	s.SocketPath = "/Users/some one/.kranq/kranq.sock"
	dir, err := s.Ensure(context.Background(), "dx")
	if err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(filepath.Join(dir, "hooks", "pre-receive"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), `'/Users/some one/.local/bin/kranq'`) {
		t.Fatalf("a path with a space was not quoted: %s", body)
	}
}

func TestEnsureRefusesABadName(t *testing.T) {
	s := store(t)
	if _, err := s.Ensure(context.Background(), "../escape"); err == nil {
		t.Fatal("a traversing name created a repository")
	}
	if entries, _ := os.ReadDir(s.Root); len(entries) != 0 {
		t.Fatalf("something was created anyway: %v", entries)
	}
}
