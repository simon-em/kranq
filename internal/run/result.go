package run

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/simon-em/kranq/internal/gitsrv"
)

// A run's output is a commit, and its parent is the commit that was pushed. So
// `git diff HEAD..FETCH_HEAD` is exactly what the job did: files it changed and
// whatever it left in ci-artifacts/. Artifacts and code changes stop being two
// mechanisms with two transports.
//
// It also takes the git host out of the VM. A task that used to need a token to
// push a branch can commit instead and let whoever pulls it decide where that
// goes, which is the difference between the build machine holding a write
// credential and not.
func ResultRef(taskID string) string { return gitsrv.ResultRefPrefix + taskID }

const (
	guestBundle  = "/tmp/kranq-result.bundle"
	resultBranch = "kranq-result"
)

// commitScript runs in the VM once the task has finished. It is deliberately
// separate from the task script: the task's exit code has already been taken,
// and nothing here may change it.
func commitScript(base string) string {
	return fmt.Sprintf(`set -uo pipefail
cd "$HOME/%s" 2>/dev/null || exit 0
git rev-parse --git-dir >/dev/null 2>&1 || exit 0
git add -A >/dev/null 2>&1
# ci-artifacts/ is usually gitignored, and it is the whole point of the run.
[ -d %s ] && git add -Af %s >/dev/null 2>&1
if git diff --cached --quiet; then exit 3; fi
git -c user.email=kranq@localhost -c user.name=kranq commit -q -m "kranq result" || exit 1
git branch -f %s HEAD >/dev/null 2>&1 || exit 1
rm -f %s
# Only what the job added: the base is already on the other side, and sending
# the history again would cost more than everything else the run does.
git bundle create %s %s --not %s >/dev/null 2>&1 || exit 1
`, WorkDir, ArtifactsDir, ArtifactsDir, resultBranch, guestBundle, guestBundle, resultBranch, base)
}

// commitResult brings the run's commit back to the repository the push landed
// in, where whoever pushed can fetch it. It never fails a run: a job that
// changed nothing is the normal case, and a job that succeeded is not made to
// have failed by trouble collecting after it.
func (e *Engine) commitResult(ctx context.Context, name string, req Request, out io.Writer) string {
	code, err := e.Driver.Shell(ctx, name, commitScript(req.Source.Commit), io.Discard)
	switch {
	case err != nil:
		fmt.Fprintf(out, "warning: could not package the result: %v\n", err)
		return ""
	case code == 3:
		return ""
	case code != 0:
		fmt.Fprintf(out, "warning: packaging the result exited %d\n", code)
		return ""
	}

	dir, err := os.MkdirTemp("", "kranq-result-*")
	if err != nil {
		fmt.Fprintf(out, "warning: %v\n", err)
		return ""
	}
	defer os.RemoveAll(dir)
	bundle := filepath.Join(dir, "result.bundle")
	if err := e.Driver.CopyOut(ctx, name, guestBundle, bundle, false); err != nil {
		fmt.Fprintf(out, "warning: the result could not be copied back: %v\n", err)
		return ""
	}

	ref := ResultRef(req.TaskID)
	if _, err := runGit(ctx, "", "--git-dir", req.ResultRepo,
		"fetch", "--quiet", bundle, resultBranch+":"+ref); err != nil {
		fmt.Fprintf(out, "warning: the result could not be recorded: %v\n", err)
		return ""
	}
	fmt.Fprintf(out, "result: %s\n", ref)
	return ref
}
