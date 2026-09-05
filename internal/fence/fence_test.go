package fence

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func bareOrigin(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "origin.git")
	if out, err := exec.Command("git", "init", "--bare", "--quiet", dir).CombinedOutput(); err != nil {
		t.Fatalf("git init --bare: %v: %s", err, out)
	}
	return dir
}

func client(t *testing.T, origin, node string) *Client {
	t.Helper()
	return &Client{Dir: filepath.Join(t.TempDir(), "fence"), URL: origin, Node: node}
}

func scope() Scope { return Scope{Kind: "maintenance", Repo: "dx", Branch: "main"} }

func remoteRefs(t *testing.T, origin string) string {
	t.Helper()
	out, err := exec.Command("git", "--git-dir", origin, "for-each-ref", "--format=%(refname)").CombinedOutput()
	if err != nil {
		t.Fatalf("for-each-ref: %v: %s", err, out)
	}
	return string(out)
}

func commitIn(t *testing.T, c *Client, message string) string {
	t.Helper()
	oid, err := c.commit(context.Background(), Holder{Task: message}, "")
	if err != nil {
		t.Fatalf("commit: %v", err)
	}
	return oid
}

func TestClaimIsExclusive(t *testing.T) {
	origin := bareOrigin(t)
	a, b := client(t, origin, "a"), client(t, origin, "b")
	ctx := context.Background()

	if _, _, err := a.Claim(ctx, scope(), "task-a"); err != nil {
		t.Fatalf("A could not claim an unheld fence: %v", err)
	}
	_, _, err := b.Claim(ctx, scope(), "task-b")
	if !errors.Is(err, ErrHeld) {
		t.Fatalf("B claimed a fence A already holds: %v", err)
	}
}

func TestScopesDoNotCollide(t *testing.T) {
	origin := bareOrigin(t)
	a := client(t, origin, "a")
	ctx := context.Background()

	if _, _, err := a.Claim(ctx, scope(), "task-a"); err != nil {
		t.Fatal(err)
	}
	other := Scope{Kind: "maintenance", Repo: "dx", Branch: "develop"}
	if _, _, err := a.Claim(ctx, other, "task-b"); err != nil {
		t.Fatalf("a different branch should be a different fence: %v", err)
	}
	if scope().Ref() == other.Ref() {
		t.Fatal("two scopes hashed to one ref")
	}
}

// The interleaving the whole at-most-once argument rests on:
// claim as A, claim as B (must fail), advance as A, break the fence,
// advance as A again (must fail AND must not push the branch), claim as B (must succeed).
func TestBrokenFenceStopsTheOriginalAttempt(t *testing.T) {
	origin := bareOrigin(t)
	a, b := client(t, origin, "a"), client(t, origin, "b")
	ctx := context.Background()

	claim, holder, err := a.Claim(ctx, scope(), "task-a")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := b.Claim(ctx, scope(), "task-b"); !errors.Is(err, ErrHeld) {
		t.Fatalf("B must not claim a held fence: %v", err)
	}

	first := commitIn(t, a, "work-1")
	claim, err = a.Advance(ctx, claim, holder, "pushed", first+":refs/heads/pr-one")
	if err != nil {
		t.Fatalf("A holds the fence and must be able to push: %v", err)
	}
	if !strings.Contains(remoteRefs(t, origin), "refs/heads/pr-one") {
		t.Fatal("A's fenced push did not land")
	}

	if err := b.Break(ctx, scope().Ref(), claim.OID); err != nil {
		t.Fatalf("breaking the fence: %v", err)
	}

	second := commitIn(t, a, "work-2")
	if _, err := a.Advance(ctx, claim, holder, "pushed", second+":refs/heads/pr-two"); !errors.Is(err, ErrBroken) {
		t.Fatalf("A pushed after its fence was broken: %v", err)
	}
	if strings.Contains(remoteRefs(t, origin), "refs/heads/pr-two") {
		t.Fatal("the branch landed even though the fence was rejected; --atomic is not holding")
	}

	if _, _, err := b.Claim(ctx, scope(), "task-b"); err != nil {
		t.Fatalf("B could not claim the broken fence: %v", err)
	}
}

func TestAdvanceIsAnExactCompareAndSwap(t *testing.T) {
	origin := bareOrigin(t)
	a, b := client(t, origin, "a"), client(t, origin, "b")
	ctx := context.Background()

	claim, holder, err := a.Claim(ctx, scope(), "task-a")
	if err != nil {
		t.Fatal(err)
	}
	// B takes the fence over without deleting it, which a fast-forward-only
	// check would happily accept.
	if err := b.Break(ctx, claim.Ref, claim.OID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := b.Claim(ctx, scope(), "task-b"); err != nil {
		t.Fatal(err)
	}

	c := commitIn(t, a, "work")
	if _, err := a.Advance(ctx, claim, holder, "pushed", c+":refs/heads/leaked"); !errors.Is(err, ErrBroken) {
		t.Fatalf("A advanced onto a fence it no longer holds: %v", err)
	}
	if strings.Contains(remoteRefs(t, origin), "refs/heads/leaked") {
		t.Fatal("the branch leaked past a taken-over fence")
	}
}

func TestReleaseIsLeased(t *testing.T) {
	origin := bareOrigin(t)
	a, b := client(t, origin, "a"), client(t, origin, "b")
	ctx := context.Background()

	claim, _, err := a.Claim(ctx, scope(), "task-a")
	if err != nil {
		t.Fatal(err)
	}
	if err := b.Break(ctx, claim.Ref, claim.OID); err != nil {
		t.Fatal(err)
	}
	bClaim, _, err := b.Claim(ctx, scope(), "task-b")
	if err != nil {
		t.Fatal(err)
	}
	if err := a.Release(ctx, claim); !errors.Is(err, ErrBroken) {
		t.Fatalf("A released a fence B now holds: %v", err)
	}
	if err := b.Release(ctx, bClaim); err != nil {
		t.Fatalf("B could not release its own fence: %v", err)
	}
	if strings.Contains(remoteRefs(t, origin), Namespace) {
		t.Fatal("the fence survived its own release")
	}
}

func TestShowAndListReportTheHolder(t *testing.T) {
	origin := bareOrigin(t)
	a := client(t, origin, "mini-1")
	ctx := context.Background()

	if _, _, err := a.Claim(ctx, scope(), "task-a"); err != nil {
		t.Fatal(err)
	}
	entry, err := a.Show(ctx, scope().Ref())
	if err != nil {
		t.Fatal(err)
	}
	if entry.Holder.Task != "task-a" || entry.Holder.Node != "mini-1" {
		t.Fatalf("holder not readable back: %+v", entry.Holder)
	}
	if entry.Holder.ClaimedAt.IsZero() {
		t.Fatal("the claim time was not recorded")
	}

	entries, err := a.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Holder.Kind != "maintenance" {
		t.Fatalf("list: %+v", entries)
	}
}

func TestShowReportsAnAbsentFence(t *testing.T) {
	a := client(t, bareOrigin(t), "a")
	if _, err := a.Show(context.Background(), scope().Ref()); !errors.Is(err, ErrNoFence) {
		t.Fatalf("expected ErrNoFence, got %v", err)
	}
}

// A network failure must never be read as a rejection: that would push unfenced.
func TestTransportFailureIsNotARejection(t *testing.T) {
	a := client(t, filepath.Join(t.TempDir(), "does-not-exist.git"), "a")
	_, _, err := a.Claim(context.Background(), scope(), "task-a")
	if err == nil {
		t.Fatal("expected an error")
	}
	if errors.Is(err, ErrHeld) {
		t.Fatalf("an unreachable remote was reported as a held fence: %v", err)
	}
}

func TestRefusesToPushUnfencedWithoutAtomicSupport(t *testing.T) {
	a := client(t, bareOrigin(t), "a")
	ctx := context.Background()
	claim, holder, err := a.Claim(ctx, scope(), "task-a")
	if err != nil {
		t.Fatal(err)
	}
	a.Run = func(ctx context.Context, dir string, env, args []string, stdin string) (string, string, error) {
		for _, arg := range args {
			if arg == "--atomic" {
				return "", "fatal: the receiving end does not support --atomic push\n", errors.New("exit status 128")
			}
		}
		return execGit(ctx, dir, env, args, stdin)
	}
	c := commitIn(t, a, "work")
	if _, err := a.Advance(ctx, claim, holder, "pushed", c+":refs/heads/y"); !errors.Is(err, ErrNoAtomic) {
		t.Fatalf("expected ErrNoAtomic, got %v", err)
	}
}

func TestNamespaceIsDeepEnoughForGit(t *testing.T) {
	// git rejects a single-segment ref as a "funny refname"; the fence ref must
	// never degrade to one.
	if strings.Count(scope().Ref(), "/") < 2 {
		t.Fatalf("fence ref is too shallow for git to accept: %s", scope().Ref())
	}
}

func TestMain(m *testing.M) {
	if _, err := exec.LookPath("git"); err != nil {
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func TestAdoptFindsTheFenceByHolderNotByObject(t *testing.T) {
	origin := bareOrigin(t)
	a := client(t, origin, "mini-1")
	ctx := context.Background()

	claim, holder, err := a.Claim(ctx, scope(), "task-a")
	if err != nil {
		t.Fatal(err)
	}
	// Stand in for the job advancing the fence from inside the VM: the host's
	// claim object is now stale, but the fence is still ours.
	advanced, err := a.Advance(ctx, claim, holder, "pushed")
	if err != nil {
		t.Fatal(err)
	}
	if advanced.OID == claim.OID {
		t.Fatal("advance did not move the fence")
	}

	adopted, _, err := a.Adopt(ctx, scope().Ref(), "task-a")
	if err != nil {
		t.Fatalf("the host could not adopt its own advanced fence: %v", err)
	}
	if adopted.OID != advanced.OID {
		t.Fatalf("adopted %s, fence is at %s", adopted.OID, advanced.OID)
	}
	if err := a.Release(ctx, adopted); err != nil {
		t.Fatalf("releasing an adopted fence: %v", err)
	}
}

func TestAdoptRefusesSomebodyElsesFence(t *testing.T) {
	origin := bareOrigin(t)
	a, b := client(t, origin, "a"), client(t, origin, "b")
	ctx := context.Background()
	if _, _, err := b.Claim(ctx, scope(), "task-b"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := a.Adopt(ctx, scope().Ref(), "task-a"); !errors.Is(err, ErrBroken) {
		t.Fatalf("expected ErrBroken, got %v", err)
	}
}
