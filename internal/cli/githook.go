package cli

import (
	"bufio"
	"context"
	"crypto/rand"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/simon-em/kranq/internal/exitcode"
	"github.com/simon-em/kranq/internal/gitsrv"
	"github.com/simon-em/kranq/internal/ipc"
	"github.com/simon-em/kranq/internal/state"
	"github.com/simon-em/kranq/internal/task"
)

// kranq git-hook is invoked by the hooks kranq writes into each pushed-to
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
		fmt.Fprintln(env.Stderr, "usage: kranq git-hook <pre-receive|post-receive> --repo NAME --socket PATH")
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
	fmt.Fprintf(env.Stderr, "kranq: unknown hook phase %q\n", rest[0])
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
// guess. A push that only carries objects lands on a run ref, whose name is a
// nonce that would be a nonsense branch name.
func branchFromRef(ref string) string {
	if ref == gitsrv.AnonRef || strings.HasPrefix(ref, gitsrv.TaskRefPrefix) {
		return "kranq-push"
	}
	if head, ok := strings.CutPrefix(ref, "refs/heads/"); ok {
		return head
	}
	return "kranq-push"
}

// runName is what the run will be called, which is also what the pusher pulls
// from. A push that named one keeps it; a push to the anonymous ref gets one
// minted here, and is told what it was.
func runName(ref string) string {
	if ref == gitsrv.AnonRef {
		return mintRun()
	}
	return gitsrv.RunName(ref)
}

// The timestamp prefix is not decoration: retention reads it, because the name
// is the only record of when a run happened that a ref carries.
func mintRun() string {
	var nonce [5]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return time.Now().UTC().Format("20060102T150405") + "-" + strconv.Itoa(os.Getpid())
	}
	return fmt.Sprintf("%s-%x", time.Now().UTC().Format("20060102T150405"), nonce)
}

// Everything printed here reaches the pushing terminal prefixed with "remote:".
func say(env Env, format string, args ...any) {
	fmt.Fprintf(env.Stderr, "kranq: "+format+"\n", args...)
}

func hookValidate(env Env, req gitsrv.Request, parseErr error, updates []update) int {
	if parseErr != nil {
		say(env, "%v", parseErr)
		say(env, "for example: git push kranq HEAD:tasks -o task_file=ci/tasks/spec.yaml")
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
		spec, err := specFor(req, u.New)
		if err != nil {
			say(env, "%v", err)
			return exitcode.InvalidSpec
		}
		if _, err := task.Parse(spec); err != nil {
			say(env, "%s: %v", taskName(req), err)
			return exitcode.InvalidSpec
		}
	}
	say(env, "accepted, will run %s", taskName(req))
	return exitcode.OK
}

// A spec sent inline wins over a path, because a caller only sends one when the
// path could not have worked: a shared task in a submodule is a gitlink, so its
// contents are not in the commit at all.
func specFor(req gitsrv.Request, commit string) ([]byte, error) {
	if len(req.Spec) > 0 {
		return req.Spec, nil
	}
	return showFile(gitDir(), commit, req.Task)
}

func taskName(req gitsrv.Request) string {
	if req.Task != "" {
		return req.Task
	}
	return "the task sent with this push"
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

	// A refused push leaves the landing pad holding the commit, and the next
	// push of it says "Everything up-to-date": no hook, no run, and no word
	// about why. By the time this runs, release has either moved it or nothing
	// has, and deleting a ref that is already gone is not an error.
	defer clearAnon(ref)

	spec, err := specFor(req, commit)
	if err != nil {
		say(env, "%v", err)
		return refused(env, "the spec could not be read")
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
		return refused(env, err.Error())
	}
	say(env, "task %s queued from %s", task.ID, commit[:12])
	run := runName(ref)
	release(env, ref, run, commit, task.ID)
	say(env, "pull it back with: git pull kranq %s", gitsrv.ShortRef(gitsrv.TaskRef(run)))
	sweep(env)

	if req.Detach {
		say(env, "detached; follow it with: kranq logs %s -f", task.ID)
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
	publish(env, run, final)
	say(env, "task %s %s (exit %d)", task.ID, final.Status, final.ExitCode)
	// A push cannot carry an exit code: post-receive runs after the ref has
	// already been accepted, and nothing it returns reaches git's exit status.
	// This line is the contract instead, and `kranq push` exits on it.
	fmt.Fprintf(env.Stderr, "%s id=%s status=%s exit=%d%s\n",
		gitsrv.ResultMarker, final.ID, final.Status, final.ExitCode, resultField(final.ResultRef))
	return exitcode.OK
}

// release makes the run pullable and keeps its objects alive.
//
// The run ref is the deliverable, not a place the objects were parked: whoever
// pushed pulls it back from the same name, and until the job finishes it points
// at what they pushed. Any other ref a push landed on is a landing pad, and is
// cleared once the run has been published under its own name.
//
// The source is retained under its task id as well, because that is the name
// the runner clones from, and because it is what the next push negotiates
// against: without it git resends the whole history every time.
func release(env Env, ref, run, commit, taskID string) {
	dir := gitDir()
	if err := git(dir, "update-ref", "refs/kranq/src/"+taskID, commit); err != nil {
		say(env, "could not retain %s: %v", short(commit), err)
		return
	}
	// Anything that is not already the run ref is a landing pad, including a
	// plain `HEAD:main`, and has to be cleared: a second push of the same
	// commit to a ref that still holds it says "Everything up-to-date", runs no
	// hook, and the job silently never happens. Retrying a failed pipeline step
	// is the ordinary way to reach that.
	task := gitsrv.TaskRef(run)
	if task == ref {
		return
	}
	if err := git(dir, "update-ref", task, commit); err != nil {
		say(env, "could not publish the run as %s: %v", run, err)
		return
	}
	if err := git(dir, "update-ref", "-d", ref, commit); err != nil {
		say(env, "could not clear %s: %v", ref, err)
	}
}

// A push is the moment this repository is known to be in use, which makes it
// the cheapest place to drop what has aged out. It never fails a run: a
// retention sweep that cannot run is a disk problem, not this job's problem.
func sweep(env Env) {
	ttl := gitsrv.DefaultTTL
	if days := settingInt(settings(kranqHome()), "KRANQ_RESULT_TTL_DAYS", 0); days > 0 {
		ttl = time.Duration(days) * 24 * time.Hour
	}
	dropped, err := gitsrv.Sweep(context.Background(), gitDir(), time.Now(), ttl)
	if err != nil {
		say(env, "could not sweep old run refs: %v", err)
		return
	}
	if len(dropped) > 0 {
		say(env, "dropped %d run ref(s) older than %s", len(dropped), ttl)
	}
}

func git(dir string, args ...string) error {
	cmd := exec.Command("git", append([]string{"--git-dir", dir}, args...)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func clearAnon(ref string) {
	if ref == gitsrv.AnonRef {
		_ = git(gitDir(), "update-ref", "-d", ref)
	}
}

func short(sha string) string {
	if len(sha) > 12 {
		return sha[:12]
	}
	return sha
}

// A run that never started still owes the client a verdict. Without one it
// cannot tell "refused" from "still going", and reports the wrong thing after
// waiting for a result that is never coming.
func refused(env Env, reason string) int {
	fmt.Fprintf(env.Stderr, "%s id= status=%s exit=%d reason=%s\n",
		gitsrv.ResultMarker, gitsrv.StatusRefused, exitcode.InvalidSpec, oneLine(reason))
	return exitcode.OK
}

func oneLine(v string) string {
	return strings.Join(strings.Fields(v), " ")
}

// publish moves the run ref onto what the job produced, so that the name the
// pusher already pulled from now carries the result: files the task changed and
// whatever it left in its artifacts directory, in the places it wrote them.
// Artifacts and code changes stop being two mechanisms with two transports, and
// a pipeline needs no archive step at all.
//
// The verdict is a second ref, because one fetch cannot both deliver a payload
// and fail. Measured: `git pull <remote> task/<run> ok/<run>` with the second
// ref missing exits 1 and merges nothing, so a failing run would take its own
// report down with it -- which is exactly the report worth having.
func publish(env Env, run string, final state.Task) {
	if run == "" {
		return
	}
	dir := gitDir()
	if final.ResultRef != "" {
		if err := git(dir, "update-ref", gitsrv.TaskRef(run), final.ResultRef); err != nil {
			say(env, "could not publish the result as %s: %v", run, err)
		}
	}
	if final.Status != state.StatusSucceeded {
		return
	}
	if err := git(dir, "update-ref", gitsrv.OKRef(run), gitsrv.TaskRef(run)); err != nil {
		say(env, "could not publish the pass marker: %v", err)
	}
}

func resultField(ref string) string {
	if ref == "" {
		return ""
	}
	return " result=" + ref
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
