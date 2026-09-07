package gitsrv

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func run(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := try(dir, args...)
	if err != nil {
		t.Fatalf("git %s: %v: %s", strings.Join(args, " "), err, out)
	}
	return out
}

func try(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	for _, kv := range os.Environ() {
		if name, _, _ := strings.Cut(kv, "="); name != "GIT_DIR" && name != "GIT_WORK_TREE" {
			cmd.Env = append(cmd.Env, kv)
		}
	}
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// A ref is a path, so refs/heads/task and refs/heads/task/<run> cannot both
// exist. That is the entire reason the anonymous landing pad is called "tasks"
// and not "task", and it is a property of git rather than of this code, so it
// is checked against git.
func TestGitRefusesARefThatIsAlsoADirectory(t *testing.T) {
	bare, commit := seed(t)
	run(t, "", "--git-dir", bare, "update-ref", TaskRefPrefix+"a-run", commit)

	if out, err := try("", "--git-dir", bare, "update-ref", "refs/heads/task", commit); err == nil {
		t.Fatalf("git allowed refs/heads/task beside %sa-run; the naming here is built on it refusing: %s", TaskRefPrefix, out)
	}
	// The sibling name, which is what kranq actually uses, is fine.
	run(t, "", "--git-dir", bare, "update-ref", AnonRef, commit)
	run(t, "", "--git-dir", bare, "update-ref", TaskRefPrefix+"another", commit)
}

// The pipeline shape, end to end against git: push a run, publish a result onto
// the same ref, pull it back by the short name. The checkout is shallow and
// detached because that is how a CI container arrives.
func TestARunIsPulledBackFromTheNameItWasPushedTo(t *testing.T) {
	bare, _ := seed(t)
	root := t.TempDir()
	ci := filepath.Join(root, "ci")
	run(t, "", "clone", "--quiet", "--depth", "1", "file://"+bare, ci)
	run(t, ci, "checkout", "--quiet", "--detach", "HEAD")

	if got := strings.TrimSpace(run(t, ci, "rev-parse", "--is-shallow-repository")); got != "true" {
		t.Fatalf("the checkout is not shallow (%s), so this proves less than it should", got)
	}

	// What kranq-setup configures: a full refname, because git cannot DWIM a
	// short destination from a detached HEAD -- it refuses with "the
	// destination you provided is not a full refname".
	pushed := TaskRefPrefix + "run-1"
	run(t, ci, "push", "--quiet", bare, "+HEAD:"+pushed)

	head := strings.TrimSpace(run(t, ci, "rev-parse", "HEAD"))
	result := resultOn(t, bare, head)
	run(t, "", "--git-dir", bare, "update-ref", pushed, result)
	run(t, "", "--git-dir", bare, "update-ref", OKRefPrefix+"run-1", result)

	run(t, ci, "pull", "--ff-only", "--quiet", bare, ShortRef(pushed))

	if _, err := os.Stat(filepath.Join(ci, "report", "index.html")); err != nil {
		t.Errorf("what the run wrote did not come back: %v", err)
	}
	body, err := os.ReadFile(filepath.Join(ci, "f"))
	if err != nil || !strings.Contains(string(body), "changed by the run") {
		t.Errorf("a file the run edited did not come back: %q, %v", body, err)
	}

	if _, err := try(ci, "fetch", bare, ShortRef(OKRefPrefix+"run-1")); err != nil {
		t.Errorf("fetching the pass ref of a passing run failed: %v", err)
	}
	if _, err := try(ci, "fetch", bare, ShortRef(OKRefPrefix+"never")); err == nil {
		t.Error("fetching a missing pass ref succeeded, so a failing run would go green")
	}
}

// A short destination cannot be pushed from a detached HEAD: git has no branch
// to infer refs/heads from. CI checks out detached, which is why kranq-setup
// configures a full refname rather than the short form a person types.
func TestAShortDestinationNeedsABranch(t *testing.T) {
	bare, _ := seed(t)
	root := t.TempDir()
	ci := filepath.Join(root, "ci")
	run(t, "", "clone", "--quiet", "file://"+bare, ci)

	run(t, ci, "checkout", "--quiet", "-B", "work")
	if out, err := try(ci, "push", "--quiet", bare, "HEAD:tasks"); err != nil {
		t.Fatalf("the short form failed from a branch: %v: %s", err, out)
	}

	run(t, ci, "checkout", "--quiet", "--detach", "HEAD")
	out, err := try(ci, "push", "--quiet", bare, "HEAD:tasks2")
	if err == nil {
		t.Fatal("the short form worked from a detached HEAD; kranq-setup could stop configuring a full refname")
	}
	if !strings.Contains(out, "full refname") {
		t.Errorf("push failed for an unexpected reason: %s", out)
	}
}

func seed(t *testing.T) (bare, commit string) {
	t.Helper()
	root := t.TempDir()
	bare = filepath.Join(root, "bare.git")
	run(t, "", "init", "--bare", "--quiet", bare)

	work := filepath.Join(root, "work")
	run(t, "", "init", "--quiet", "-b", "main", work)
	if err := os.WriteFile(filepath.Join(work, "f"), []byte("one\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run(t, work, "add", "-A")
	run(t, work, "-c", "user.email=t@t", "-c", "user.name=t", "commit", "-qm", "one")
	run(t, work, "push", "--quiet", bare, "HEAD:refs/heads/main")
	run(t, "", "--git-dir", bare, "symbolic-ref", "HEAD", "refs/heads/main")
	return bare, strings.TrimSpace(run(t, work, "rev-parse", "HEAD"))
}

// Stands in for the commit a job makes in the VM: a file it edited, and a
// report in a directory the repository would normally ignore.
func resultOn(t *testing.T, bare, parent string) string {
	t.Helper()
	tmp := t.TempDir()
	work := filepath.Join(tmp, "w")
	run(t, "", "clone", "--quiet", "--no-checkout", bare, work)
	run(t, work, "checkout", "--quiet", "--detach", parent)
	if err := os.MkdirAll(filepath.Join(work, "report"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(work, "report", "index.html"), []byte("<html>\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(work, "f"), []byte("changed by the run\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run(t, work, "add", "-Af", ".")
	run(t, work, "-c", "user.email=k@k", "-c", "user.name=k", "commit", "-qm", "kranq result")
	sha := strings.TrimSpace(run(t, work, "rev-parse", "HEAD"))
	run(t, work, "push", "--quiet", bare, "HEAD:refs/kranq/tmp-result")
	return sha
}
