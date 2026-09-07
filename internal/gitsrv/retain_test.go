package gitsrv

import (
	"context"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestExpiredGoesByTaskIdNotCommitDate(t *testing.T) {
	now := time.Date(2026, 9, 6, 18, 0, 0, 0, time.UTC)
	refs := []string{
		"refs/kranq/result/20260906T170000-aaa", // an hour ago
		"refs/kranq/src/20260905T170000-bbb",    // yesterday
		"refs/kranq/result/20260903T170000-ccc", // three days ago
		"refs/kranq/src/20260901T000000-ddd",    // five days ago
	}
	got := Expired(refs, now, DefaultTTL)
	want := []string{"refs/kranq/result/20260903T170000-ccc", "refs/kranq/src/20260901T000000-ddd"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("expired = %v, want %v", got, want)
	}
}

// A ref whose last segment is not a task id must be left alone rather than
// guessed at: deleting somebody's branch to reclaim disk is not a trade anyone
// agreed to.
func TestExpiredLeavesUnrecognisedRefsAlone(t *testing.T) {
	now := time.Date(2026, 9, 6, 18, 0, 0, 0, time.UTC)
	refs := []string{"refs/kranq/src/main", "refs/kranq/result/x", "refs/heads/main"}
	if got := Expired(refs, now, DefaultTTL); len(got) != 0 {
		t.Errorf("expired = %v, want nothing", got)
	}
}

func TestALongerTTLKeepsMore(t *testing.T) {
	now := time.Date(2026, 9, 6, 18, 0, 0, 0, time.UTC)
	refs := []string{"refs/kranq/result/20260903T170000-ccc"}
	if got := Expired(refs, now, 30*24*time.Hour); len(got) != 0 {
		t.Errorf("a thirty day ttl dropped %v", got)
	}
}

func TestSweepDropsOnlyTheOldRefs(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "repo.git")
	run := func(args ...string) {
		t.Helper()
		if out, err := exec.Command("git", append([]string{"--git-dir", dir}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	if out, err := exec.Command("git", "init", "-q", "--bare", dir).CombinedOutput(); err != nil {
		t.Fatalf("init: %v: %s", err, out)
	}
	empty := commitInto(t, dir)
	fresh := "refs/kranq/result/20260906T170000-fresh"
	stale := "refs/kranq/result/20260901T000000-stale"
	keep := "refs/heads/main"
	for _, ref := range []string{fresh, stale, keep} {
		run("update-ref", ref, empty)
	}

	now := time.Date(2026, 9, 6, 18, 0, 0, 0, time.UTC)
	dropped, err := Sweep(context.Background(), dir, now, DefaultTTL)
	if err != nil {
		t.Fatal(err)
	}
	if len(dropped) != 1 || dropped[0] != stale {
		t.Fatalf("dropped %v, want just the stale one", dropped)
	}
	out, err := exec.Command("git", "--git-dir", dir, "for-each-ref", "--format=%(refname)").Output()
	if err != nil {
		t.Fatal(err)
	}
	got := string(out)
	if !strings.Contains(got, fresh) || !strings.Contains(got, keep) {
		t.Errorf("sweep removed too much: %q", got)
	}
	if strings.Contains(got, stale) {
		t.Errorf("the stale ref survived: %q", got)
	}
}

func commitInto(t *testing.T, dir string) string {
	t.Helper()
	tree, err := exec.Command("git", "--git-dir", dir, "hash-object", "-t", "tree", "-w", "--stdin").Output()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("git", "--git-dir", dir, "commit-tree", strings.TrimSpace(string(tree)), "-m", "x")
	cmd.Env = append(cmd.Environ(),
		"GIT_AUTHOR_NAME=k", "GIT_AUTHOR_EMAIL=k@x", "GIT_COMMITTER_NAME=k", "GIT_COMMITTER_EMAIL=k@x")
	out, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(out))
}

// A pipeline needs the name before it pushes, and the only name it has is the
// one it chose for the ref.
func TestRunNameIsTheLastSegmentOfThePushedRef(t *testing.T) {
	cases := map[string]string{
		"refs/kranq/push/20260906T180307-19242-4885": "20260906T180307-19242-4885",
		"refs/heads/main": "main",
		"refs/heads/":     "",
		"nope":            "",
	}
	for ref, want := range cases {
		if got := RunName(ref); got != want {
			t.Errorf("RunName(%q) = %q, want %q", ref, got, want)
		}
	}
}

// Every namespace a run writes to has to age out, or a busy machine accumulates
// a ref per run forever -- and these hold whole working trees, artifacts
// included.
func TestEveryRunNamespaceIsSwept(t *testing.T) {
	now := time.Date(2026, 9, 6, 18, 0, 0, 0, time.UTC)
	for _, prefix := range []string{TaskRefPrefix, OKRefPrefix, "refs/kranq/src/"} {
		refs := []string{prefix + "20260901T000000-old", prefix + "20260906T175000-new"}
		got := Expired(refs, now, DefaultTTL)
		if len(got) != 1 || got[0] != prefix+"20260901T000000-old" {
			t.Errorf("%s expired = %v, want just the old one", prefix, got)
		}
		if !slices.Contains(Kept, prefix) {
			t.Errorf("Sweep does not look at %s, so those refs would never be dropped", prefix)
		}
	}
}

// The refs the previous naming left on disk still have to age out; nothing
// writes them any more, so this is the only thing that will ever remove them.
func TestTheOldNamespacesStillAgeOut(t *testing.T) {
	for _, prefix := range []string{"refs/kranq/result/", "refs/kranq/passed/"} {
		if !slices.Contains(Kept, prefix) {
			t.Errorf("%s is not swept, so what is already on disk lives forever", prefix)
		}
	}
}

// refs/heads/task/<run> and refs/heads/task cannot coexist: a ref is a path.
// Tested against git, which refuses the second with "cannot lock ref", in
// either order. The anonymous landing pad is a sibling for that reason, and the
// day someone "tidies" it to refs/heads/task this fails.
func TestTheAnonymousRefIsNotInsideTheRunNamespace(t *testing.T) {
	if strings.HasPrefix(AnonRef+"/", TaskRefPrefix) {
		t.Fatalf("%s is inside %s; git would refuse every push to one while the other exists", AnonRef, TaskRefPrefix)
	}
	if !strings.HasPrefix(TaskRefPrefix, "refs/heads/") || !strings.HasPrefix(OKRefPrefix, "refs/heads/") {
		t.Fatal("a run outside refs/heads cannot be pulled back by its short name")
	}
}
