package image

import (
	"context"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/effetmonstre/forge/internal/project"
	"github.com/effetmonstre/forge/internal/vm"
)

func manager(f *vm.Fake) *Manager {
	return &Manager{
		Driver:   f,
		Template: []byte("vmType: vz\n"),
		TTL:      14 * 24 * time.Hour,
		Now:      func() time.Time { return time.Unix(1_780_000_000, 0) },
	}
}

func dxProject() project.Project {
	return project.Project{
		SetupRaw: []byte("memory: 3GiB\nsetup: |\n  echo provisioning\n"),
		Setup:    project.Setup{Memory: "3GiB", Script: "echo provisioning\n"},
	}
}

func TestEnsureBuildsBothLayersOnAColdMachine(t *testing.T) {
	f := vm.NewFake()
	m := manager(f)
	plan, err := m.Ensure(context.Background(), "dx", "main", dxProject(), io.Discard)
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	if !f.Exists(plan.Base) || !f.Exists(plan.Project) {
		t.Fatalf("layers missing after Ensure: %v", f.Names())
	}
	calls := strings.Join(f.Calls, "\n")
	for _, want := range []string{"create " + plan.Base, "clone " + plan.Base + " " + plan.Project, "shell " + plan.Project} {
		if !strings.Contains(calls, want) {
			t.Errorf("missing %q in:\n%s", want, calls)
		}
	}
}

func TestEnsureLeavesBothLayersStoppedSoTheyCanBeCloned(t *testing.T) {
	f := vm.NewFake()
	m := manager(f)
	plan, err := m.Ensure(context.Background(), "dx", "main", dxProject(), io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{plan.Base, plan.Project} {
		list, _ := f.List(context.Background())
		for _, inst := range list {
			if inst.Name == name && inst.Running() {
				t.Errorf("%s is Running; an image must be stopped to be cloned", name)
			}
		}
	}
}

func TestEnsureReusesAWarmProjectImageWithoutTouchingTheDriver(t *testing.T) {
	f := vm.NewFake()
	m := manager(f)
	plan := m.PlanFor("dx", dxProject())
	f.Seed(plan.Project, "Stopped")
	f.Calls = nil

	got, err := m.Ensure(context.Background(), "dx", "main", dxProject(), io.Discard)
	if err != nil || got.Project != plan.Project {
		t.Fatalf("Ensure = %v, %v", got, err)
	}
	if len(f.Calls) != 0 {
		t.Errorf("a warm image must cost nothing, got calls: %v", f.Calls)
	}
}

func TestAFailedSetupDestroysThePartialImage(t *testing.T) {
	f := vm.NewFake()
	f.ShellFunc = func(name, script string, out io.Writer) (int, error) { return 3, nil }
	m := manager(f)

	plan, err := m.Ensure(context.Background(), "dx", "main", dxProject(), io.Discard)
	if err == nil {
		t.Fatal("expected the setup failure to surface")
	}
	if !strings.Contains(err.Error(), "exited 3") {
		t.Errorf("error = %v, want the exit code", err)
	}
	if f.Exists(plan.Project) {
		t.Error("a half-built project image must not be left behind to be reused")
	}
}

func TestSetupReceivesTheContractEnv(t *testing.T) {
	f := vm.NewFake()
	var got string
	f.ShellFunc = func(name, script string, out io.Writer) (int, error) {
		got = script
		return 0, nil
	}
	if _, err := manager(f).Ensure(context.Background(), "dx", "ci/lima", dxProject(), io.Discard); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"CI_REPO='dx'", "CI_REF='ci/lima'", "CI_GIT_REMOTE="} {
		if !strings.Contains(got, want) {
			t.Errorf("setup command %q is missing %q; ci/setup.yaml depends on it", got, want)
		}
	}
}

func TestConcurrentEnsureBuildsOnce(t *testing.T) {
	f := vm.NewFake()
	m := manager(f)
	var wg sync.WaitGroup
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = m.Ensure(context.Background(), "dx", "main", dxProject(), io.Discard)
		}()
	}
	wg.Wait()
	builds := 0
	for _, call := range f.Calls {
		if strings.HasPrefix(call, "create ") {
			builds++
		}
	}
	if builds != 1 {
		t.Errorf("built the base %d times, want 1: concurrent runs must share the build", builds)
	}
}

func TestPruneKeepsTheNewestAndNeverTouchesRunningOrForeign(t *testing.T) {
	f := vm.NewFake()
	for _, n := range []string{"forge-proj-dx-aa", "forge-proj-dx-bb", "forge-proj-dx-cc", "forge-proj-dx-dd"} {
		f.Seed(n, "Stopped")
	}
	f.Seed("forge-proj-dx-ee", "Running")
	f.Seed("ci-proj-dx-36ec09c9f7", "Stopped")
	m := manager(f)
	m.Keep = 2

	pruned, err := m.Prune(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(pruned) != 2 {
		t.Errorf("pruned %v, want 2 of the 4 stopped forge images", pruned)
	}
	if !f.Exists("ci-proj-dx-36ec09c9f7") {
		t.Error("prune destroyed an instance belonging to the system forge replaces")
	}
	if !f.Exists("forge-proj-dx-ee") {
		t.Error("prune destroyed a Running image")
	}
}

func TestDestroyRefusesForeignInstances(t *testing.T) {
	f := vm.NewFake()
	f.Seed("default", "Stopped")
	if err := manager(f).Destroy(context.Background(), "default"); err == nil {
		t.Error("Destroy must refuse an instance forge does not own")
	}
	if !f.Exists("default") {
		t.Error("it destroyed it anyway")
	}
}
