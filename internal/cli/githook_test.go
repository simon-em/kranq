package cli

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/simon-em/kranq/internal/gitsrv"
	"github.com/simon-em/kranq/internal/state"
)

// A bare repository with one commit in it, standing in for the repository a
// push lands in. GIT_DIR is how the hook finds it, exactly as git sets it.
func hookRepo(t *testing.T) (dir, commit string) {
	t.Helper()
	root := t.TempDir()
	dir = filepath.Join(root, "bare.git")
	mustGit(t, "", "init", "--bare", "--quiet", dir)

	work := filepath.Join(root, "work")
	mustGit(t, "", "init", "--quiet", "-b", "main", work)
	if err := os.WriteFile(filepath.Join(work, "f"), []byte("one\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustGit(t, work, "add", "-A")
	mustGit(t, work, "-c", "user.email=t@t", "-c", "user.name=t", "commit", "-qm", "one")
	mustGit(t, work, "push", "--quiet", dir, "HEAD:refs/heads/seed")
	commit = strings.TrimSpace(mustGit(t, work, "rev-parse", "HEAD"))

	t.Setenv("GIT_DIR", dir)
	return dir, commit
}

func mustGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	// Unset rather than emptied: git reads GIT_WORK_TREE="" as set, and refuses.
	for _, kv := range os.Environ() {
		if name, _, _ := strings.Cut(kv, "="); name != "GIT_DIR" && name != "GIT_WORK_TREE" {
			cmd.Env = append(cmd.Env, kv)
		}
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v: %s", strings.Join(args, " "), err, out)
	}
	return string(out)
}

func refs(t *testing.T, dir string) []string {
	t.Helper()
	out := mustGit(t, "", "--git-dir", dir, "for-each-ref", "--format=%(refname)")
	var got []string
	for _, l := range strings.Split(strings.TrimSpace(out), "\n") {
		if l != "" {
			got = append(got, l)
		}
	}
	return got
}

func has(refs []string, want string) bool {
	for _, r := range refs {
		if r == want {
			return true
		}
	}
	return false
}

func discard() (Env, *bytes.Buffer) {
	var buf bytes.Buffer
	return Env{Stdout: &buf, Stderr: &buf}, &buf
}

// A push that names its run keeps that name, because the name is the contract:
// it is what the pusher pulls from, and it chose it before pushing.
func TestANamedPushKeepsItsName(t *testing.T) {
	if got := runName(gitsrv.TaskRefPrefix + "20260907T000000-abc"); got != "20260907T000000-abc" {
		t.Errorf("runName = %q, want the name that was pushed", got)
	}
}

// A push to the anonymous ref has no name to keep, so one is minted -- with a
// timestamp in front, because retention reads the name and nothing else records
// when a run happened.
func TestAnAnonymousPushIsGivenATimestampedName(t *testing.T) {
	first, second := runName(gitsrv.AnonRef), runName(gitsrv.AnonRef)
	if first == second {
		t.Fatalf("two anonymous pushes were both called %q", first)
	}
	for _, name := range []string{first, second} {
		if len(name) < len("20060102T150405") {
			t.Fatalf("run %q is too short to carry a timestamp, so it would never expire", name)
		}
		if !strings.Contains(name, "T") {
			t.Errorf("run %q does not look like a timestamp", name)
		}
	}
}

// The run ref is the deliverable, not a parking space: whoever pushed pulls it
// back from the same name, so it has to survive the push that created it.
func TestANamedRunRefSurvivesTheHook(t *testing.T) {
	dir, commit := hookRepo(t)
	env, _ := discard()
	ref := gitsrv.TaskRefPrefix + "myrun"
	mustGit(t, "", "--git-dir", dir, "update-ref", ref, commit)

	release(env, ref, "myrun", commit, "task-1")

	if !has(refs(t, dir), ref) {
		t.Fatalf("%s is gone, so there is nothing to pull", ref)
	}
	if !has(refs(t, dir), "refs/kranq/src/task-1") {
		t.Error("the source was not retained, so the runner has nothing to clone")
	}
}

// The anonymous ref is a landing pad. Leaving it in place makes the next push
// to it a no-op -- git says "Everything up-to-date", runs no hook, and the job
// silently never happens.
func TestTheAnonymousRefIsClearedAndTheRunPublished(t *testing.T) {
	dir, commit := hookRepo(t)
	env, _ := discard()
	mustGit(t, "", "--git-dir", dir, "update-ref", gitsrv.AnonRef, commit)

	release(env, gitsrv.AnonRef, "minted", commit, "task-2")

	got := refs(t, dir)
	if has(got, gitsrv.AnonRef) {
		t.Errorf("%s was left in place; the next push to it would run nothing", gitsrv.AnonRef)
	}
	if !has(got, gitsrv.TaskRefPrefix+"minted") {
		t.Errorf("the run was not published under its minted name; refs = %v", got)
	}
}

// Same reasoning for an ordinary branch: `git push kranq HEAD:main` twice must
// run the task twice.
func TestAPushToAnOrdinaryBranchIsAlsoCleared(t *testing.T) {
	dir, commit := hookRepo(t)
	env, _ := discard()
	mustGit(t, "", "--git-dir", dir, "update-ref", "refs/heads/main", commit)

	release(env, "refs/heads/main", "main", commit, "task-3")

	got := refs(t, dir)
	if has(got, "refs/heads/main") {
		t.Error("refs/heads/main was left where it was pushed, so a second push would be a no-op")
	}
	if !has(got, gitsrv.TaskRefPrefix+"main") {
		t.Errorf("the run is not pullable; refs = %v", got)
	}
}

// The point of the whole arrangement: the ref the pusher already knows about
// ends up holding what the run produced, so `git pull kranq task/<run>` brings
// back code changes and artifacts together, with no archive step.
func TestAPassingRunPublishesItsResultAndItsVerdict(t *testing.T) {
	dir, commit := hookRepo(t)
	env, _ := discard()
	result := resultCommit(t, dir, commit)
	mustGit(t, "", "--git-dir", dir, "update-ref", gitsrv.TaskRef("r1"), commit)

	publish(env, "r1", state.Task{ID: "t", Status: state.StatusSucceeded, ResultRef: result})

	if got := revParse(t, dir, gitsrv.TaskRef("r1")); got != result {
		t.Errorf("task ref points at %s, want the result commit %s", short(got), short(result))
	}
	if !has(refs(t, dir), gitsrv.OKRef("r1")) {
		t.Error("no pass ref, so a passing run would turn the step red")
	}
}

// The decisive case. A failing run still publishes what it produced, because a
// report is worth having precisely when the tests failed -- only the verdict is
// withheld.
func TestAFailingRunStillHandsBackWhatItProduced(t *testing.T) {
	dir, commit := hookRepo(t)
	env, _ := discard()
	result := resultCommit(t, dir, commit)
	mustGit(t, "", "--git-dir", dir, "update-ref", gitsrv.TaskRef("r2"), commit)

	publish(env, "r2", state.Task{ID: "t", Status: state.StatusFailed, ExitCode: 12, ResultRef: result})

	if got := revParse(t, dir, gitsrv.TaskRef("r2")); got != result {
		t.Error("a failing run did not publish its result, so its report is unreachable")
	}
	if has(refs(t, dir), gitsrv.OKRef("r2")) {
		t.Error("a failing run published a pass ref, so the step would go green")
	}
}

// A run that changed nothing has no result commit. The run ref must still be
// there, pointing at what was pushed, so the pull is a no-op rather than a
// failure.
func TestARunThatProducedNothingLeavesThePushedCommitInPlace(t *testing.T) {
	dir, commit := hookRepo(t)
	env, _ := discard()
	mustGit(t, "", "--git-dir", dir, "update-ref", gitsrv.TaskRef("r3"), commit)

	publish(env, "r3", state.Task{ID: "t", Status: state.StatusSucceeded})

	if got := revParse(t, dir, gitsrv.TaskRef("r3")); got != commit {
		t.Errorf("task ref = %s, want the pushed commit", short(got))
	}
	if !has(refs(t, dir), gitsrv.OKRef("r3")) {
		t.Error("a run that passed without changing anything must still say so")
	}
}

func resultCommit(t *testing.T, dir, parent string) string {
	t.Helper()
	tree := strings.TrimSpace(mustGit(t, "", "--git-dir", dir, "rev-parse", parent+"^{tree}"))
	out := mustGit(t, "", "--git-dir", dir,
		"-c", "user.email=k@k", "-c", "user.name=k",
		"commit-tree", tree, "-p", parent, "-m", "kranq result")
	sha := strings.TrimSpace(out)
	mustGit(t, "", "--git-dir", dir, "update-ref", "refs/kranq/result/t", sha)
	return sha
}

func revParse(t *testing.T, dir, ref string) string {
	t.Helper()
	return strings.TrimSpace(mustGit(t, "", "--git-dir", dir, "rev-parse", ref))
}

// A push that is refused after the ref has moved still has to clear the landing
// pad. Otherwise pushing the same commit again says "Everything up-to-date",
// runs no hook, and reports nothing at all -- which looks like success.
func TestARefusedAnonymousPushStillClearsTheLandingPad(t *testing.T) {
	dir, commit := hookRepo(t)
	mustGit(t, "", "--git-dir", dir, "update-ref", gitsrv.AnonRef, commit)

	clearAnon(gitsrv.AnonRef)

	if has(refs(t, dir), gitsrv.AnonRef) {
		t.Fatalf("%s survived, so the next push to it would run nothing", gitsrv.AnonRef)
	}
	// Twice, because release clears it on the ordinary path and this runs after.
	clearAnon(gitsrv.AnonRef)
}

// A named run ref is the deliverable and must never be swept up by this.
func TestClearingTheLandingPadLeavesRunRefsAlone(t *testing.T) {
	dir, commit := hookRepo(t)
	ref := gitsrv.TaskRefPrefix + "keep-me"
	mustGit(t, "", "--git-dir", dir, "update-ref", ref, commit)

	clearAnon(ref)

	if !has(refs(t, dir), ref) {
		t.Fatal("the run ref was deleted; there would be nothing to pull")
	}
}
