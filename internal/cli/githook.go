package cli

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/effetmonstre/forge/internal/exitcode"
	"github.com/effetmonstre/forge/internal/gitsrv"
	"github.com/effetmonstre/forge/internal/ipc"
	"github.com/effetmonstre/forge/internal/task"
)

// forge git-hook is invoked by the hooks forge writes into each pushed-to
// repository. It is split across two phases because git splits them:
//
//   - pre-receive can reject the push, but the pushed objects are quarantined
//     and no separate process can read them, so no job can start here.
//   - post-receive can read the objects, but cannot reject anything.
//
// So validation happens in the first and the run in the second. Both stream
// their output back to whoever ran git push.
func runGitHook(env Env, args []string) int {
	fs := flag.NewFlagSet("git-hook", flag.ContinueOnError)
	fs.SetOutput(env.Stderr)
	repo := fs.String("repo", "", "repository the push landed in")
	socket := fs.String("socket", "", "daemon socket to submit through")
	rest, err := parsePermuted(fs, args)
	if err != nil {
		return exitcode.Usage
	}
	if len(rest) != 1 || *repo == "" {
		fmt.Fprintln(env.Stderr, "usage: forge git-hook <pre-receive|post-receive> --repo NAME --socket PATH")
		return exitcode.Usage
	}

	updates := readUpdates(os.Stdin)
	req, parseErr := gitsrv.ParseOptions(pushOptions())

	switch rest[0] {
	case "pre-receive":
		return hookValidate(env, req, parseErr, updates)
	case "post-receive":
		if parseErr != nil {
			return exitcode.OK
		}
		return hookRun(env, *repo, *socket, req, updates)
	}
	fmt.Fprintf(env.Stderr, "forge: unknown hook phase %q\n", rest[0])
	return exitcode.Usage
}

type update struct {
	Old, New, Ref string
}

func readUpdates(r *os.File) []update {
	var out []update
	scanner := bufio.NewScanner(r)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) == 3 {
			out = append(out, update{Old: fields[0], New: fields[1], Ref: fields[2]})
		}
	}
	return out
}

func pushOptions() []string {
	count, err := strconv.Atoi(os.Getenv("GIT_PUSH_OPTION_COUNT"))
	if err != nil {
		return nil
	}
	out := make([]string, 0, count)
	for i := 0; i < count; i++ {
		out = append(out, os.Getenv(fmt.Sprintf("GIT_PUSH_OPTION_%d", i)))
	}
	return out
}

// A client that names no branch usually pushed to one, so the ref is the best
// guess. A push meant only to carry objects lands outside refs/heads, and its
// ref is a nonce that would be a nonsense branch name.
func branchFromRef(ref string) string {
	if head, ok := strings.CutPrefix(ref, "refs/heads/"); ok {
		return head
	}
	return "forge-push"
}

// Everything printed here reaches the pushing terminal prefixed with "remote:".
func say(env Env, format string, args ...any) {
	fmt.Fprintf(env.Stderr, "forge: "+format+"\n", args...)
}

func hookValidate(env Env, req gitsrv.Request, parseErr error, updates []update) int {
	if parseErr != nil {
		say(env, "%v", parseErr)
		say(env, "for example: git push forge HEAD:refs/forge/run -o task=ci/tasks/spec.yaml")
		return exitcode.InvalidSpec
	}
	live := 0
	for _, u := range updates {
		if !isDeletion(u.New) {
			live++
		}
	}
	if live == 0 {
		say(env, "nothing to run: this push deletes refs and creates none")
		return exitcode.InvalidSpec
	}
	if live > 1 {
		say(env, "push one ref at a time; this push updates %d", live)
		return exitcode.InvalidSpec
	}
	// The pushed objects are quarantined, and no separate process can read
	// them, but this hook can: its environment points at the quarantine. So the
	// spec is checked here, where a bad push can still be refused, rather than
	// after the ref has already moved.
	for _, u := range updates {
		if isDeletion(u.New) {
			continue
		}
		spec, err := showFile(gitDir(), u.New, req.Task)
		if err != nil {
			say(env, "%v", err)
			return exitcode.InvalidSpec
		}
		if _, err := task.Parse(spec); err != nil {
			say(env, "%s: %v", req.Task, err)
			return exitcode.InvalidSpec
		}
	}
	say(env, "accepted, will run %s", req.Task)
	return exitcode.OK
}

func isDeletion(sha string) bool {
	return strings.Trim(sha, "0") == ""
}

func hookRun(env Env, repo, socket string, req gitsrv.Request, updates []update) int {
	var commit, ref string
	for _, u := range updates {
		if !isDeletion(u.New) {
			commit, ref = u.New, u.Ref
			break
		}
	}
	if commit == "" {
		return exitcode.OK
	}

	dir := gitDir()
	spec, err := showFile(dir, commit, req.Task)
	if err != nil {
		say(env, "%v", err)
		return exitcode.InvalidSpec
	}

	branch := req.Branch
	if branch == "" {
		branch = branchFromRef(ref)
	}

	client := ipc.NewClient(socket)
	ctx := context.Background()
	task, err := client.Submit(ctx, ipc.SubmitRequest{
		Spec:         string(spec),
		Repo:         repo,
		Branch:       branch,
		Label:        req.Label,
		Env:          req.Env,
		Keep:         req.Keep,
		SourceCommit: commit,
	})
	if err != nil {
		say(env, "could not submit: %v", err)
		return exitcode.Unreachable
	}
	say(env, "task %s queued from %s", task.ID, commit[:12])

	if req.Detach {
		say(env, "detached; follow it with: forge logs %s -f", task.ID)
		return exitcode.OK
	}
	// The push cannot carry the task's exit code, so the client checks the
	// result afterwards. Streaming here is what makes the run visible at all.
	if err := client.Logs(ctx, task.ID, true, env.Stderr); err != nil {
		say(env, "the log stream ended early: %v", err)
	}
	final, err := client.Get(ctx, task.ID)
	if err != nil {
		say(env, "could not read the result: %v", err)
		return exitcode.OK
	}
	say(env, "task %s %s (exit %d)", task.ID, final.Status, final.ExitCode)
	// A push cannot carry an exit code: post-receive runs after the ref has
	// already been accepted, and nothing it returns reaches git's exit status.
	// This line is the contract instead, and `forge push` exits on it.
	fmt.Fprintf(env.Stderr, "%s id=%s status=%s exit=%d\n",
		gitsrv.ResultMarker, final.ID, final.Status, final.ExitCode)
	return exitcode.OK
}

func gitDir() string {
	if v := os.Getenv("GIT_DIR"); v != "" {
		abs, err := filepath.Abs(v)
		if err == nil {
			return abs
		}
		return v
	}
	wd, _ := os.Getwd()
	return wd
}

func showFile(gitDir, commit, path string) ([]byte, error) {
	out, err := exec.Command("git", "--git-dir", gitDir, "show", commit+":"+path).Output()
	if err != nil {
		return nil, fmt.Errorf("%s is not in the pushed commit", path)
	}
	return out, nil
}
