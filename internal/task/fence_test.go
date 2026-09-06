package task

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

type fenceRepo struct {
	t        *testing.T
	origin   string
	checkout string
}

func (f fenceRepo) git(dir string, args ...string) string {
	f.t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	// The guard hook is installed in this checkout, so the test's own
	// out-of-band pushes have to say so.
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t",
		"KRANQ_FENCE_BYPASS=1")
	out, err := cmd.CombinedOutput()
	if err != nil {
		f.t.Fatalf("git %s: %v: %s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

func newFenceRepo(t *testing.T) fenceRepo {
	t.Helper()
	root := t.TempDir()
	f := fenceRepo{t: t, origin: filepath.Join(root, "origin.git"), checkout: filepath.Join(root, "work")}
	f.git(root, "init", "--bare", "--quiet", f.origin)
	f.git(root, "init", "--quiet", f.checkout)
	if err := os.WriteFile(filepath.Join(f.checkout, "f"), []byte("one\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	f.git(f.checkout, "add", "f")
	f.git(f.checkout, "commit", "--quiet", "-m", "one")
	f.git(f.checkout, "remote", "add", "origin", f.origin)
	f.git(f.checkout, "push", "--quiet", "origin", "HEAD:refs/heads/main")
	return f
}

const testFenceRef = "refs/kranq/fence/abc123"

// setFence puts the fence at a record naming task, the way the host's Claim does.
func (f fenceRepo) setFence(task string) {
	f.t.Helper()
	tree := f.git(f.checkout, "hash-object", "-t", "tree", "-w", "--stdin")
	oid := f.git(f.checkout, "commit-tree", tree, "-m", fmt.Sprintf(`{"task":"%s","node":"host"}`, task))
	f.git(f.checkout, "push", "--quiet", "--force", "origin", oid+":"+testFenceRef)
}

func (f fenceRepo) remoteRefs() string {
	return f.git(f.checkout, "ls-remote", f.origin)
}

func (f fenceRepo) runScript(body string, env ...string) (string, error) {
	f.t.Helper()
	script := "#!/usr/bin/env bash\nset -euo pipefail\n" + fencePreamble + "\n" + body + "\n"
	path := filepath.Join(f.t.TempDir(), "run.sh")
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
		f.t.Fatal(err)
	}
	cmd := exec.Command("bash", path)
	cmd.Dir = f.checkout
	cmd.Env = append(os.Environ(), append([]string{
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t",
		"KRANQ_FENCE_REF=" + testFenceRef,
	}, env...)...)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func fenceEnv(task string) []string {
	return []string{
		"KRANQ_FENCE_TASK=" + task,
		"KRANQ_FENCE_RECORD=" + fmt.Sprintf(`{"task":"%s","node":"host","phase":"pushed"}`, task),
	}
}

func TestKranqPushLandsWhileTheFenceIsHeld(t *testing.T) {
	f := newFenceRepo(t)
	f.setFence("task-a")
	out, err := f.runScript("kranq_push HEAD:refs/heads/pr-one", fenceEnv("task-a")...)
	if err != nil {
		t.Fatalf("kranq_push failed while holding the fence: %v\n%s", err, out)
	}
	if !strings.Contains(f.remoteRefs(), "refs/heads/pr-one") {
		t.Fatalf("the branch did not land:\n%s", f.remoteRefs())
	}
}

func TestKranqPushAdvancesTheFenceSoTheHostCanStillFindIt(t *testing.T) {
	f := newFenceRepo(t)
	f.setFence("task-a")
	before := f.git(f.checkout, "ls-remote", f.origin, testFenceRef)
	if out, err := f.runScript("kranq_push HEAD:refs/heads/pr-one", fenceEnv("task-a")...); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	after := f.git(f.checkout, "ls-remote", f.origin, testFenceRef)
	if before == after {
		t.Fatal("the fence did not move, so a push left no trace")
	}
	f.git(f.checkout, "fetch", "--quiet", "--force", f.origin, testFenceRef+":refs/read")
	if body := f.git(f.checkout, "log", "-1", "--format=%B", "refs/read"); !strings.Contains(body, `"task":"task-a"`) {
		t.Fatalf("the advanced fence no longer names its holder: %s", body)
	}
}

func TestRawGitPushIsBlocked(t *testing.T) {
	f := newFenceRepo(t)
	f.setFence("task-a")
	out, err := f.runScript("git push origin HEAD:refs/heads/sneaky", fenceEnv("task-a")...)
	if err == nil {
		t.Fatalf("a raw git push was allowed past the fence:\n%s", out)
	}
	if strings.Contains(f.remoteRefs(), "refs/heads/sneaky") {
		t.Fatal("the unfenced branch landed")
	}
	if !strings.Contains(out, "kranq_push") {
		t.Fatalf("the refusal does not say what to do instead:\n%s", out)
	}
}

func TestKranqPushRefusesWhenAnotherRunHoldsTheFence(t *testing.T) {
	f := newFenceRepo(t)
	f.setFence("task-b")
	out, err := f.runScript("kranq_push HEAD:refs/heads/pr-one", fenceEnv("task-a")...)
	if err == nil {
		t.Fatalf("pushed while another run held the fence:\n%s", out)
	}
	if strings.Contains(f.remoteRefs(), "refs/heads/pr-one") {
		t.Fatal("the branch landed despite a foreign fence")
	}
}

func TestKranqPushRefusesWhenTheFenceIsGone(t *testing.T) {
	f := newFenceRepo(t)
	out, err := f.runScript("kranq_push HEAD:refs/heads/pr-one", fenceEnv("task-a")...)
	if err == nil {
		t.Fatalf("pushed with no fence at all:\n%s", out)
	}
	if strings.Contains(f.remoteRefs(), "refs/heads/pr-one") {
		t.Fatal("the branch landed with no fence")
	}
}

// The window the fence exists to close: the fence is broken mid-run and the
// original attempt, still alive, tries to push.
func TestKranqPushRefusesAfterTheFenceIsBrokenMidRun(t *testing.T) {
	f := newFenceRepo(t)
	f.setFence("task-a")
	if out, err := f.runScript("kranq_push HEAD:refs/heads/pr-one", fenceEnv("task-a")...); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	f.git(f.checkout, "push", "--quiet", "--delete", f.origin, testFenceRef)
	f.setFence("task-b")

	f.git(f.checkout, "commit", "--quiet", "--allow-empty", "-m", "more")
	out, err := f.runScript("kranq_push HEAD:refs/heads/pr-two", fenceEnv("task-a")...)
	if err == nil {
		t.Fatalf("the original attempt pushed again after being fenced out:\n%s", out)
	}
	if strings.Contains(f.remoteRefs(), "refs/heads/pr-two") {
		t.Fatal("a second branch landed; this is the duplicate-PR bug")
	}
}

func TestFencePreambleOnlyAppearsForEffectfulTasks(t *testing.T) {
	plain := Spec{Name: "t", Steps: []Step{{Run: "true"}}}
	if strings.Contains(BuildScript(plain, nil), "kranq_push") {
		t.Fatal("a task with no declared effects got the fence helpers")
	}
	effectful := Spec{Name: "t", Effects: Effects{Push: true}, Steps: []Step{{Run: "true"}}}
	if !strings.Contains(BuildScript(effectful, nil), "kranq_push") {
		t.Fatal("a task declaring effects.push did not get the fence helpers")
	}
}
