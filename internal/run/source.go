package run

import (
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
)

// Source says where a job's code comes from. Pushed sources name an exact
// commit rather than a branch, because a branch tip can move while the task
// waits in the queue and the run would then build something nobody asked for.
type Source struct {
	Bare   string
	Commit string
	Branch string
}

func (s Source) Pushed() bool { return s.Bare != "" && s.Commit != "" }

// RunBranch is the ref kranq creates for one run. A plain clone only looks at
// refs/heads, so a commit sitting under refs/kranq/* is invisible to it: the
// clone succeeds and produces an empty working tree.
func RunBranch(taskID string) string { return "kranq/" + taskID }

type gitRunner func(ctx context.Context, dir string, args ...string) (string, error)

func runGit(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	if dir != "" {
		cmd.Dir = dir
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		return string(out), fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return string(out), nil
}

// Stage names the commit under refs/heads so both the host checkout and the
// VM's clone can find it, then reduces it to a bare repository holding that one
// commit. What gets copied into the VM is the reduced one: the shared
// repository accumulates a ref per run and is much larger.
func Stage(ctx context.Context, src Source, taskID, dest string, git gitRunner) (string, error) {
	if git == nil {
		git = runGit
	}
	branch := RunBranch(taskID)
	if _, err := git(ctx, "", "--git-dir", src.Bare, "update-ref", "refs/heads/"+branch, src.Commit); err != nil {
		return "", err
	}
	// file:// rather than a path, or git treats it as a local clone, ignores
	// --depth and hardlinks the whole object store in.
	if _, err := git(ctx, "", "clone", "--quiet", "--bare", "--depth", "1",
		"--branch", branch, "file://"+src.Bare, dest); err != nil {
		return "", err
	}
	return branch, nil
}

// CheckoutPushed produces the working tree the host needs to read the Kranqfile,
// without contacting the git host at all.
func CheckoutPushed(ctx context.Context, bare, branch, dest string, git gitRunner) error {
	if git == nil {
		git = runGit
	}
	_, err := git(ctx, "", "clone", "--quiet", "--depth", "1", "--branch", branch, "file://"+bare, dest)
	return err
}

const GuestSource = "/tmp/kranq-src.git"

// CloneCommand for a pushed source. origin is set to the real remote afterwards
// because a task with effects pushes its branch and opens a pull request there,
// and the fence lives there too; only the source came from kranq.
func (s Source) CloneCommand(dest string, origin Remote) string {
	// No --depth: the staged repository already holds exactly one commit, and
	// git warns that the flag is ignored for a local clone anyway.
	clone := fmt.Sprintf("git clone --quiet --branch %s %s %s",
		shellQuote(s.Branch), shellQuote(GuestSource), dest)
	url := origin.URL()
	if url == "" {
		return clone
	}
	return clone + fmt.Sprintf("\ngit -C %s remote set-url origin %s", dest, shellQuote(url))
}

func StageDir(work string) string { return filepath.Join(work, "src.git") }
