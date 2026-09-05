package gitsrv

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/effetmonstre/forge/internal/token"
)

type harness struct {
	t       *testing.T
	server  *httptest.Server
	store   *Store
	secret  string
	shallow string
	hookLog string
}

func git(t *testing.T, dir string, args ...string) (string, error) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t",
		"GIT_TERMINAL_PROMPT=0")
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func mustGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := git(t, dir, args...)
	if err != nil {
		t.Fatalf("git %s: %v: %s", strings.Join(args, " "), err, out)
	}
	return out
}

// A hook that records what it saw, standing in for `forge git-hook`.
const recordingHook = `#!/bin/sh
{
  echo "PHASE $2"
  echo "COUNT ${GIT_PUSH_OPTION_COUNT:-0}"
  i=0
  while [ "$i" -lt "${GIT_PUSH_OPTION_COUNT:-0}" ]; do
    eval "v=\$GIT_PUSH_OPTION_$i"
    echo "OPT $v"
    i=$((i+1))
  done
  echo "USER ${REMOTE_USER:-none}"
  while read -r old new ref; do echo "REF $ref $new"; done
} >> HOOKLOG
echo "forge: hook ran" >&2
exit 0
`

func newHarness(t *testing.T) *harness {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not available")
	}
	backend, err := Backend()
	if err != nil {
		t.Skip("git-http-backend is not available: " + err.Error())
	}

	root := t.TempDir()
	h := &harness{t: t, hookLog: filepath.Join(root, "hooks.log")}

	stub := filepath.Join(root, "forge-stub")
	body := strings.Replace(recordingHook, "HOOKLOG", h.hookLog, 1)
	if err := os.WriteFile(stub, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}

	h.store = &Store{Root: filepath.Join(root, "repos"), ForgeBin: stub, SocketPath: "/tmp/forge.sock"}

	set := &token.Set{}
	secret, err := set.Create("ci-dx", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	h.secret = secret

	h.server = httptest.NewServer((&Server{
		Store:   h.store,
		Backend: backend,
		Tokens:  func() (*token.Set, error) { return set, nil },
	}).Handler())
	t.Cleanup(h.server.Close)

	// An upstream with history, then the shallow clone a CI container gets.
	origin := filepath.Join(root, "origin.git")
	mustGit(t, root, "init", "--bare", "--quiet", origin)
	work := filepath.Join(root, "work")
	mustGit(t, root, "init", "--quiet", work)
	for i := 0; i < 5; i++ {
		if err := os.WriteFile(filepath.Join(work, "f"), []byte(strings.Repeat("x", i+1)), 0o644); err != nil {
			t.Fatal(err)
		}
		mustGit(t, work, "add", "f")
		mustGit(t, work, "commit", "--quiet", "-m", "c")
	}
	mustGit(t, work, "push", "--quiet", origin, "HEAD:refs/heads/main")

	h.shallow = filepath.Join(root, "shallow")
	mustGit(t, root, "clone", "--quiet", "--depth", "1", "file://"+origin, h.shallow)
	return h
}

func (h *harness) url() string {
	return strings.Replace(h.server.URL, "http://", "http://forge:"+h.secret+"@", 1) + "/dx.git"
}

func (h *harness) push(args ...string) (string, error) {
	return git(h.t, h.shallow, append([]string{"push", h.url()}, args...)...)
}

func (h *harness) hooks() string {
	body, err := os.ReadFile(h.hookLog)
	if err != nil {
		return ""
	}
	return string(body)
}

// The whole idea rests on this: a CI container has a shallow clone, and git
// rejects a shallow push unless the receiving repo opts in.
func TestAShallowCloneCanPush(t *testing.T) {
	h := newHarness(t)
	if got := mustGit(t, h.shallow, "rev-parse", "--is-shallow-repository"); strings.TrimSpace(got) != "true" {
		t.Fatalf("the fixture is not shallow: %q", got)
	}
	out, err := h.push("-o", "task=ci/tasks/spec.yaml", "HEAD:refs/forge/run")
	if err != nil {
		t.Fatalf("a shallow push was refused: %v\n%s", err, out)
	}
	if strings.Contains(out, "shallow update not allowed") {
		t.Fatal("receive.shallowUpdate is not taking effect")
	}
}

func TestPushOptionsReachTheHook(t *testing.T) {
	h := newHarness(t)
	out, err := h.push(
		"-o", "task=ci/tasks/spec.yaml",
		"-o", "label=spec",
		"-o", "env.BITBUCKET_TOKEN=s3cr3t",
		"HEAD:refs/forge/run")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	log := h.hooks()
	for _, want := range []string{
		"OPT task=ci/tasks/spec.yaml",
		"OPT label=spec",
		"OPT env.BITBUCKET_TOKEN=s3cr3t",
		"PHASE pre-receive",
		"PHASE post-receive",
	} {
		if !strings.Contains(log, want) {
			t.Fatalf("%q missing from the hook log:\n%s", want, log)
		}
	}
}

func TestTheHookLearnsWhichTokenPushed(t *testing.T) {
	h := newHarness(t)
	if out, err := h.push("-o", "task=t.yaml", "HEAD:refs/forge/run"); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if !strings.Contains(h.hooks(), "USER ci-dx") {
		t.Fatalf("the token name did not reach the hook:\n%s", h.hooks())
	}
}

func TestTheHookSeesTheCommitThatWasPushed(t *testing.T) {
	h := newHarness(t)
	if out, err := h.push("-o", "task=t.yaml", "HEAD:refs/forge/run"); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	sha := strings.TrimSpace(mustGit(t, h.shallow, "rev-parse", "HEAD"))
	if !strings.Contains(h.hooks(), "REF refs/forge/run "+sha) {
		t.Fatalf("the pushed sha did not reach the hook:\n%s", h.hooks())
	}
}

func TestHookOutputReachesThePusher(t *testing.T) {
	h := newHarness(t)
	out, err := h.push("-o", "task=t.yaml", "HEAD:refs/forge/run")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	// This is what makes a build log visible during `git push`.
	if !strings.Contains(out, "forge: hook ran") {
		t.Fatalf("the hook's output did not stream back:\n%s", out)
	}
}

func TestPushingWithNoTokenIsRefused(t *testing.T) {
	h := newHarness(t)
	out, err := git(t, h.shallow, "push", h.server.URL+"/dx.git", "HEAD:refs/forge/run")
	if err == nil {
		t.Fatalf("an unauthenticated push succeeded:\n%s", out)
	}
	if strings.Contains(h.hooks(), "PHASE") {
		t.Fatal("an unauthenticated push reached the hooks")
	}
}

func TestPushingWithAWrongTokenIsRefused(t *testing.T) {
	h := newHarness(t)
	bad := strings.Replace(h.server.URL, "http://", "http://forge:forge_deadbeef@", 1) + "/dx.git"
	out, err := git(t, h.shallow, "push", bad, "HEAD:refs/forge/run")
	if err == nil {
		t.Fatalf("a wrong token was accepted:\n%s", out)
	}
	if strings.Contains(h.hooks(), "PHASE") {
		t.Fatal("a wrong token reached the hooks")
	}
}

func TestARevokedTokenStopsWorking(t *testing.T) {
	h := newHarness(t)
	set := &token.Set{}
	secret, _ := set.Create("ci-dx", time.Now())
	srv := httptest.NewServer((&Server{
		Store: h.store, Backend: mustBackend(t),
		Tokens: func() (*token.Set, error) { return set, nil },
	}).Handler())
	defer srv.Close()

	url := strings.Replace(srv.URL, "http://", "http://forge:"+secret+"@", 1) + "/dx.git"
	if out, err := git(t, h.shallow, "push", url, "HEAD:refs/forge/a"); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if err := set.Revoke("ci-dx"); err != nil {
		t.Fatal(err)
	}
	if out, err := git(t, h.shallow, "push", url, "HEAD:refs/forge/b"); err == nil {
		t.Fatalf("a revoked token still pushed:\n%s", out)
	}
}

// The repository name comes out of a URL supplied by the caller.
func TestATraversingRepoNameCannotReachAnything(t *testing.T) {
	h := newHarness(t)
	for _, path := range []string{"/../../../etc/passwd", "/..%2f..%2fetc"} {
		out, err := git(t, h.shallow, "push",
			strings.Replace(h.server.URL, "http://", "http://forge:"+h.secret+"@", 1)+path,
			"HEAD:refs/forge/run")
		if err == nil {
			t.Fatalf("%q was served:\n%s", path, out)
		}
	}
}

func TestTheRepoIsCreatedOnFirstPush(t *testing.T) {
	h := newHarness(t)
	if _, err := os.Stat(h.store.Dir("dx")); err == nil {
		t.Fatal("the repository existed before anything was pushed")
	}
	if out, err := h.push("-o", "task=t.yaml", "HEAD:refs/forge/run"); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if _, err := os.Stat(filepath.Join(h.store.Dir("dx"), "HEAD")); err != nil {
		t.Fatalf("the repository was not created: %v", err)
	}
}

// What forge does with the push: the tree at that commit has to be complete
// even though the history behind it is not.
func TestThePushedTreeIsCompleteAndClonable(t *testing.T) {
	h := newHarness(t)
	if err := os.MkdirAll(filepath.Join(h.shallow, "ci"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(h.shallow, "ci", "setup.yaml"), []byte("memory: 2GiB\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustGit(t, h.shallow, "add", "-A")
	mustGit(t, h.shallow, "commit", "--quiet", "-m", "add ci config")

	if out, err := h.push("-o", "task=t.yaml", "HEAD:refs/heads/forge-run-1"); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}

	dest := filepath.Join(t.TempDir(), "vm")
	out, err := git(t, t.TempDir(), "clone", "--quiet", "--depth", "1",
		"--branch", "forge-run-1", "file://"+h.store.Dir("dx"), dest)
	if err != nil {
		t.Fatalf("a VM could not clone the pushed source: %v\n%s", err, out)
	}
	body, err := os.ReadFile(filepath.Join(dest, "ci", "setup.yaml"))
	if err != nil {
		t.Fatalf("the tree is incomplete: %v", err)
	}
	if !strings.Contains(string(body), "2GiB") {
		t.Fatalf("wrong content: %q", body)
	}
}

func mustBackend(t *testing.T) string {
	t.Helper()
	b, err := Backend()
	if err != nil {
		t.Skip(err.Error())
	}
	return b
}

func TestStripLeavesAPathGitWillServe(t *testing.T) {
	cases := map[string]string{
		"/dx.git/info/refs":        "/info/refs",
		"/dx/info/refs":            "/info/refs",
		"/dx.git/git-receive-pack": "/git-receive-pack",
		"/dx.git":                  "/",
		"/dx.git/":                 "/",
	}
	for in, want := range cases {
		if got := strip(in, "dx"); got != want {
			t.Fatalf("strip(%q) = %q, want %q", in, got, want)
		}
	}
	// A doubled slash makes git report the path as "aliased" and refuse it.
	for in := range cases {
		if strings.Contains(strip(in, "dx"), "//") {
			t.Fatalf("strip(%q) produced a doubled slash", in)
		}
	}
}

// git goes through curl, which normalises ".." out of a URL before it is sent,
// so pushing is the wrong way to test this. These go straight at the handler
// with the path a hostile client would actually put on the wire.
func TestTheHandlerItselfRefusesPathsThatEscapeTheStore(t *testing.T) {
	h := newHarness(t)
	client := &http.Client{}
	for _, raw := range []string{
		"/../../../etc/passwd/info/refs",
		"/..%2F..%2Fetc/info/refs",
		"/.ssh/info/refs",
		"//info/refs",
		"/a%00b.git/info/refs",
	} {
		req, err := http.NewRequest("GET", h.server.URL, nil)
		if err != nil {
			t.Fatal(err)
		}
		req.URL.Opaque = raw
		req.SetBasicAuth("forge", h.secret)
		resp, err := client.Do(req)
		if err != nil {
			continue
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode == http.StatusOK {
			t.Fatalf("%q was served: %s", raw, body)
		}
	}
	// Nothing may have been created outside the store root.
	entries, err := os.ReadDir(h.store.Root)
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.Contains(e.Name(), "..") || strings.HasPrefix(e.Name(), ".") {
			t.Fatalf("a suspicious directory was created: %s", e.Name())
		}
	}
}

// The cap is checked against a trivial backend rather than git-http-backend:
// the real one leaves the connection open when a body is cut off mid-pack,
// which hangs the test rather than telling us anything.
func TestABodyPastTheCapNeverReachesTheBackend(t *testing.T) {
	h := newHarness(t)
	counter := filepath.Join(t.TempDir(), "count.cgi")
	seen := filepath.Join(t.TempDir(), "seen")
	body := "#!/bin/sh\nprintf 'Content-Type: text/plain\\n\\n'\nwc -c > " + seen + "\n"
	if err := os.WriteFile(counter, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}

	set := &token.Set{}
	secret, _ := set.Create("ci-dx", time.Now())
	srv := httptest.NewServer((&Server{
		Store: h.store, Backend: counter, MaxBytes: 1024,
		Tokens: func() (*token.Set, error) { return set, nil },
	}).Handler())
	defer srv.Close()

	req, err := http.NewRequest("POST", srv.URL+"/dx.git/git-receive-pack",
		bytes.NewReader(make([]byte, 64<<10)))
	if err != nil {
		t.Fatal(err)
	}
	req.SetBasicAuth("forge", secret)
	resp, err := (&http.Client{Timeout: 20 * time.Second}).Do(req)
	if err == nil {
		resp.Body.Close()
	}
	if counted, err := os.ReadFile(seen); err == nil {
		n := strings.TrimSpace(string(counted))
		if n != "" && n != "0" && len(n) > 4 {
			t.Fatalf("the backend read %s bytes, past the 1024 cap", n)
		}
	}
}

// A push runs the task with the connection open, and the point of that is
// watching the build happen. Without flushing each write, net/http holds a
// couple of kilobytes back and the output arrives in clumps.
func TestOutputIsFlushedAsItIsWritten(t *testing.T) {
	h := newHarness(t)
	slow := filepath.Join(t.TempDir(), "slow.cgi")
	body := "#!/bin/sh\nprintf 'Content-Type: text/plain\\n\\n'\n" +
		"for i in 1 2 3; do printf 'tick %s\\n' \"$i\"; sleep 0.4; done\n"
	if err := os.WriteFile(slow, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	set := &token.Set{}
	secret, _ := set.Create("ci-dx", time.Now())
	srv := httptest.NewServer((&Server{
		Store: h.store, Backend: slow,
		Tokens: func() (*token.Set, error) { return set, nil },
	}).Handler())
	defer srv.Close()

	req, err := http.NewRequest("GET", srv.URL+"/dx.git/info/refs", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.SetBasicAuth("forge", secret)
	resp, err := (&http.Client{Timeout: 20 * time.Second}).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	start := time.Now()
	var gaps []time.Duration
	buf := make([]byte, 64)
	for len(gaps) < 3 {
		n, err := resp.Body.Read(buf)
		if n > 0 {
			gaps = append(gaps, time.Since(start))
		}
		if err != nil {
			break
		}
	}
	if len(gaps) < 3 {
		t.Fatalf("only %d chunk(s) arrived separately; the output was buffered into one", len(gaps))
	}
	// The last line cannot have arrived at the same moment as the first.
	if gaps[len(gaps)-1]-gaps[0] < 300*time.Millisecond {
		t.Fatalf("all output arrived within %s; it is not being flushed as written",
			gaps[len(gaps)-1]-gaps[0])
	}
}

func TestStreamingIsANoOpWhenThereIsNothingToFlush(t *testing.T) {
	plain := notAFlusher{}
	if got := streaming(plain); got != http.ResponseWriter(plain) {
		t.Fatal("a writer that cannot flush was wrapped anyway")
	}
}

type notAFlusher struct{}

func (notAFlusher) Header() http.Header       { return http.Header{} }
func (notAFlusher) Write([]byte) (int, error) { return 0, nil }
func (notAFlusher) WriteHeader(int)           {}
