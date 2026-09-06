package run

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func gitIn(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v: %s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

// A bare repository holding a commit under refs/forge/*, which is what a push
// to forge actually leaves behind.
func pushedRepo(t *testing.T) (bare, commit string) {
	t.Helper()
	root := t.TempDir()
	bare = filepath.Join(root, "dx.git")
	gitIn(t, root, "init", "--bare", "--quiet", bare)
	work := filepath.Join(root, "work")
	gitIn(t, root, "init", "--quiet", work)
	if err := os.WriteFile(filepath.Join(work, "Forgefile"), []byte("RUN true\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(work, "MESSAGE"), []byte("only in the push\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitIn(t, work, "add", "-A")
	gitIn(t, work, "commit", "--quiet", "-m", "pushed")
	commit = gitIn(t, work, "rev-parse", "HEAD")
	gitIn(t, work, "push", "--quiet", bare, "HEAD:refs/forge/run")
	return bare, commit
}

// A plain clone only looks at refs/heads, so a commit sitting under refs/forge
// clones into an empty tree without erroring. Staging has to fix that.
func TestAPushedCommitIsInvisibleToAPlainCloneUntilStaged(t *testing.T) {
	bare, _ := pushedRepo(t)
	dest := filepath.Join(t.TempDir(), "naive")
	gitIn(t, t.TempDir(), "clone", "--quiet", bare, dest)
	if _, err := os.Stat(filepath.Join(dest, "MESSAGE")); err == nil {
		t.Skip("this git clones refs/forge by default; staging is then belt and braces")
	}
}

func TestStageProducesARepoTheVMCanClone(t *testing.T) {
	bare, commit := pushedRepo(t)
	staged := filepath.Join(t.TempDir(), "src.git")

	branch, err := Stage(context.Background(), Source{Bare: bare, Commit: commit}, "20260904T150405-abc", staged, nil)
	if err != nil {
		t.Fatal(err)
	}
	if branch != RunBranch("20260904T150405-abc") {
		t.Fatalf("branch = %q", branch)
	}

	dest := filepath.Join(t.TempDir(), "guest")
	gitIn(t, t.TempDir(), "clone", "--quiet", "--branch", branch, staged, dest)
	body, err := os.ReadFile(filepath.Join(dest, "MESSAGE"))
	if err != nil {
		t.Fatalf("the staged repo did not carry the tree: %v", err)
	}
	if !strings.Contains(string(body), "only in the push") {
		t.Fatalf("wrong content: %q", body)
	}
	if got := gitIn(t, dest, "rev-parse", "HEAD"); got != commit {
		t.Fatalf("checked out %s, want %s", got, commit)
	}
}

// What gets copied into the VM should be the one commit, not every run's.
func TestStagingIsSmallerThanTheSharedRepo(t *testing.T) {
	bare, commit := pushedRepo(t)
	for i := 0; i < 5; i++ {
		gitIn(t, t.TempDir(), "--git-dir", bare, "update-ref", "refs/forge/extra"+string(rune('a'+i)), commit)
	}
	staged := filepath.Join(t.TempDir(), "src.git")
	if _, err := Stage(context.Background(), Source{Bare: bare, Commit: commit}, "t-1", staged, nil); err != nil {
		t.Fatal(err)
	}
	refs := gitIn(t, t.TempDir(), "--git-dir", staged, "for-each-ref", "--format=%(refname)")
	if strings.Contains(refs, "extra") {
		t.Fatalf("the staged repo carried other runs' refs:\n%s", refs)
	}
}

func TestCheckoutPushedNeedsNoNetwork(t *testing.T) {
	bare, commit := pushedRepo(t)
	staged := filepath.Join(t.TempDir(), "src.git")
	branch, err := Stage(context.Background(), Source{Bare: bare, Commit: commit}, "t-1", staged, nil)
	if err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(t.TempDir(), "host")
	if err := CheckoutPushed(context.Background(), bare, branch, dest, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dest, "Forgefile")); err != nil {
		t.Fatalf("the host cannot read the project config: %v", err)
	}
}

// Only the source came from forge. A task with effects pushes its branch and
// opens a pull request at the real remote, and the fence lives there too.
func TestTheGuestCloneRepointsOriginAtTheRealRemote(t *testing.T) {
	src := Source{Bare: "/tmp/x", Commit: "abc", Branch: "forge/t-1"}
	cmd := src.CloneCommand(`"$HOME/work"`, Remote{Base: "git@bitbucket.org:effetmonstre", Repo: "dx"})
	if !strings.Contains(cmd, GuestSource) {
		t.Fatalf("the guest does not clone the copied source:\n%s", cmd)
	}
	if !strings.Contains(cmd, "remote set-url origin 'git@bitbucket.org:effetmonstre/dx.git'") {
		t.Fatalf("origin was not repointed at the real remote:\n%s", cmd)
	}
	// git warns and ignores --depth on a local clone, and the staged repo is
	// already a single commit.
	if strings.Contains(cmd, "--depth") {
		t.Fatalf("--depth is meaningless here and only produces a warning:\n%s", cmd)
	}
}

func TestPushedIsOnlyTrueWhenBothPartsArePresent(t *testing.T) {
	if (Source{}).Pushed() {
		t.Fatal("an empty source claimed to be pushed")
	}
	if (Source{Bare: "/tmp/x"}).Pushed() {
		t.Fatal("a source with no commit claimed to be pushed")
	}
	if (Source{Commit: "abc"}).Pushed() {
		t.Fatal("a source with no repository claimed to be pushed")
	}
	if !(Source{Bare: "/tmp/x", Commit: "abc"}).Pushed() {
		t.Fatal("a complete source was not recognised")
	}
}
