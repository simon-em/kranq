package run

import (
	"context"
	"errors"
	"io"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/effetmonstre/forge/internal/fence"
	"github.com/effetmonstre/forge/internal/vm"
)

// A local bare repo stands in for the git host: Remote.URL() with no token is
// just <base>/<repo>.git, which a path satisfies.
func fencedRequest(t *testing.T) (Request, *fence.Client) {
	t.Helper()
	root := t.TempDir()
	origin := filepath.Join(root, "dx.git")
	if out, err := exec.Command("git", "init", "--bare", "--quiet", origin).CombinedOutput(); err != nil {
		t.Fatalf("git init --bare: %v: %s", err, out)
	}
	req := request(t)
	req.RemoteBase = root
	req.Fence = &FencePlan{
		Dir:   filepath.Join(root, "scratch"),
		Node:  "test-node",
		Scope: fence.Scope{Kind: "maintenance", Repo: "dx", Branch: "ci/lima"},
	}
	client := &fence.Client{Dir: filepath.Join(root, "reader"), URL: origin, Node: "reader"}
	return req, client
}

func TestFencedRunClaimsAndReleases(t *testing.T) {
	req, reader := fencedRequest(t)
	res, err := engine(t, vm.NewFake()).Execute(context.Background(), req, io.Discard)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.FenceRef == "" {
		t.Fatal("the run did not report which fence it took")
	}
	if res.FenceHeld {
		t.Fatal("a clean run left its fence held")
	}
	if _, err := reader.Show(context.Background(), res.FenceRef); !errors.Is(err, fence.ErrNoFence) {
		t.Fatalf("the fence outlived a clean run: %v", err)
	}
}

func TestASecondRunIsRefusedWhileTheFirstHoldsTheFence(t *testing.T) {
	req, _ := fencedRequest(t)
	f := vm.NewFake()
	blocked := make(chan struct{})
	release := make(chan struct{})
	f.ShellFunc = func(name, script string, out io.Writer) (int, error) {
		if strings.Contains(script, "forge-task.sh") {
			close(blocked)
			<-release
		}
		return 0, nil
	}
	go func() {
		engine(t, f).Execute(context.Background(), req, io.Discard)
	}()
	<-blocked

	second := req
	second.TaskID = "20260904T160000-def"
	_, err := engine(t, vm.NewFake()).Execute(context.Background(), second, io.Discard)
	close(release)
	if !errors.Is(err, fence.ErrHeld) {
		t.Fatalf("a second run started while the first held the fence: %v", err)
	}
	if !strings.Contains(err.Error(), "forge fence break") {
		t.Fatalf("the refusal does not say how to resolve it: %v", err)
	}
}

// A run that failed before touching the remote must not leave a fence behind:
// that would need a human before every retry of a merely-failing task.
func TestAFailedRunThatPushedNothingReleasesItsFence(t *testing.T) {
	req, reader := fencedRequest(t)
	f := vm.NewFake()
	f.ShellFunc = func(name, script string, out io.Writer) (int, error) {
		if strings.Contains(script, "forge-task.sh") {
			return 1, nil
		}
		return 0, nil
	}
	res, err := engine(t, f).Execute(context.Background(), req, io.Discard)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.ExitCode != 1 {
		t.Fatalf("exit code %d", res.ExitCode)
	}
	if res.FenceHeld {
		t.Fatal("a run that pushed nothing held its fence")
	}
	if _, err := reader.Show(context.Background(), res.FenceRef); !errors.Is(err, fence.ErrNoFence) {
		t.Fatalf("the fence survived a run that pushed nothing: %v", err)
	}
}

// The other half of that rule: once something landed, a failure holds the fence
// so the next attempt has to be a deliberate human decision.
func TestAFailedRunThatPushedHoldsItsFence(t *testing.T) {
	req, reader := fencedRequest(t)
	f := vm.NewFake()
	f.ShellFunc = func(name, script string, out io.Writer) (int, error) {
		if !strings.Contains(script, "forge-task.sh") {
			return 0, nil
		}
		advanceFence(t, req)
		return 1, nil
	}
	res, err := engine(t, f).Execute(context.Background(), req, io.Discard)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !res.FenceHeld {
		t.Fatal("a run that pushed and then failed released its fence")
	}
	entry, err := reader.Show(context.Background(), res.FenceRef)
	if err != nil {
		t.Fatalf("the fence should still be standing: %v", err)
	}
	if entry.Holder.Task != req.TaskID {
		t.Fatalf("the held fence names %q, not the run that took it", entry.Holder.Task)
	}
}

// Stands in for forge_push inside the VM, which is what moves the fence off the
// object the host claimed.
func advanceFence(t *testing.T, req Request) {
	t.Helper()
	c := &fence.Client{
		Dir: filepath.Join(t.TempDir(), "vm"),
		URL: Remote{Base: req.RemoteBase, Repo: req.Repo}.URL(),
	}
	ref := req.Fence.Scope.Ref()
	claim, holder, err := c.Adopt(context.Background(), ref, req.TaskID)
	if err != nil {
		t.Fatalf("adopting from the VM side: %v", err)
	}
	if _, err := c.Advance(context.Background(), claim, holder, "pushed"); err != nil {
		t.Fatalf("advancing from the VM side: %v", err)
	}
}

func TestAnUnfencedRunTouchesNoFence(t *testing.T) {
	req, _ := fencedRequest(t)
	req.Fence = nil
	res, err := engine(t, vm.NewFake()).Execute(context.Background(), req, io.Discard)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.FenceRef != "" || res.FenceHeld {
		t.Fatalf("an unfenced run reported a fence: %+v", res)
	}
}
