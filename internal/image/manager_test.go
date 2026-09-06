package image

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/effetmonstre/forge/internal/project"
	"github.com/effetmonstre/forge/internal/vm"
)

func manager(t *testing.T, f *vm.Fake) *Manager {
	t.Helper()
	return &Manager{
		Driver:   f,
		Template: []byte("vmType: vz\n"),
		TTL:      14 * 24 * time.Hour,
		MetaDir:  filepath.Join(t.TempDir(), "layers"),
		Now:      func() time.Time { return time.Unix(1_780_000_000, 0) },
	}
}

func load(t *testing.T, files map[string]string) project.Project {
	t.Helper()
	dir := t.TempDir()
	for name, body := range files {
		path := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	p, err := project.Load(dir, "")
	if err != nil {
		t.Fatal(err)
	}
	return p
}

const dxBuild = "MEMORY 3GiB\n" +
	"CPUS 4\n" +
	"RUN sudo apt-get install -y default-jdk\n" +
	"COPY .ruby-version .\n" +
	"RUN ruby-build \"$(cat .ruby-version)\" /opt/ci/ruby\n" +
	"ENV BUNDLE_JOBS=4\n" +
	"COPY Gemfile.lock .\n" +
	"RUN bundle install\n"

func dxProject(t *testing.T) project.Project {
	return load(t, map[string]string{
		project.DefaultFile: dxBuild,
		".ruby-version":     "3.4.1\n",
		"Gemfile.lock":      "GEM\n",
	})
}

func TestEnsureBuildsTheWholeChainOnAColdMachine(t *testing.T) {
	f := vm.NewFake()
	m := manager(t, f)
	plan, err := m.Ensure(context.Background(), "dx", dxProject(t), io.Discard)
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	if len(plan.Chain) != 4 {
		t.Fatalf("chain = %v, want the base plus three layers", plan.Chain)
	}
	for _, name := range plan.Chain {
		if !f.Exists(name) {
			t.Errorf("%s missing after Ensure: %v", name, f.Names())
		}
	}
	calls := strings.Join(f.Calls, "\n")
	if !strings.Contains(calls, "create "+plan.Base) {
		t.Errorf("the base was not built:\n%s", calls)
	}
	for i := range plan.Chain[1:] {
		want := "clone " + plan.Chain[i] + " " + plan.Chain[i+1]
		if !strings.Contains(calls, want) {
			t.Errorf("missing %q; each layer must be built on the one before it:\n%s", want, calls)
		}
	}
}

func TestEnsureLeavesEveryLayerStoppedSoItCanBeCloned(t *testing.T) {
	f := vm.NewFake()
	plan, err := manager(t, f).Ensure(context.Background(), "dx", dxProject(t), io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	list, _ := f.List(context.Background())
	for _, inst := range list {
		for _, name := range plan.Chain {
			if inst.Name == name && inst.Running() {
				t.Errorf("%s is Running; an image must be stopped to be cloned", name)
			}
		}
	}
}

func TestAWarmChainCostsNothing(t *testing.T) {
	f := vm.NewFake()
	m := manager(t, f)
	if _, err := m.Ensure(context.Background(), "dx", dxProject(t), io.Discard); err != nil {
		t.Fatal(err)
	}
	f.Calls = nil
	if _, err := m.Ensure(context.Background(), "dx", dxProject(t), io.Discard); err != nil {
		t.Fatal(err)
	}
	for _, call := range f.Calls {
		if !strings.HasPrefix(call, "list") {
			t.Errorf("a warm chain must cost nothing, got: %v", f.Calls)
			break
		}
	}
}

// The reason the layers exist. A lockfile edit must not repeat the apt-get and
// the ruby build that preceded it.
func TestAnEditRebuildsOnlyTheTail(t *testing.T) {
	f := vm.NewFake()
	m := manager(t, f)
	before, err := m.Ensure(context.Background(), "dx", dxProject(t), io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	f.Calls = nil

	edited := load(t, map[string]string{
		project.DefaultFile: dxBuild,
		".ruby-version":     "3.4.1\n",
		"Gemfile.lock":      "GEM\n  rails\n",
	})
	after, err := m.Ensure(context.Background(), "dx", edited, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	for i := range 3 {
		if before.Chain[i] != after.Chain[i] {
			t.Errorf("layer %d changed, but nothing it depends on did", i)
		}
	}
	if before.Image() == after.Image() {
		t.Fatal("the edited layer was reused")
	}
	if got := strings.Count(strings.Join(f.Calls, "\n"), "clone "); got != 1 {
		t.Errorf("built %d layers, want only the one the edit invalidated: %v", got, f.Calls)
	}
}

// Content addressing, stated as a test: a second project that happens to
// install the same packages inherits the first project's work.
func TestTwoProjectsShareTheLayersTheyHaveInCommon(t *testing.T) {
	f := vm.NewFake()
	m := manager(t, f)
	dx, err := m.Ensure(context.Background(), "dx", dxProject(t), io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	f.Calls = nil

	other := load(t, map[string]string{
		project.DefaultFile: strings.Replace(dxBuild, "RUN bundle install", "RUN mix deps.get", 1),
		".ruby-version":     "3.4.1\n",
		"Gemfile.lock":      "GEM\n",
	})
	got, err := m.Ensure(context.Background(), "refrabec", other, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	for i := range 3 {
		if dx.Chain[i] != got.Chain[i] {
			t.Errorf("layer %d was rebuilt for a second repository doing identical work", i)
		}
	}
	if got := strings.Count(strings.Join(f.Calls, "\n"), "clone "); got != 1 {
		t.Errorf("built %d layers for the second project, want 1: %v", got, f.Calls)
	}
	meta, ok := m.Describe(dx.Chain[1])
	if !ok || len(meta.Repos) != 2 {
		t.Errorf("shared layer records %v, want both repositories", meta.Repos)
	}
}

func TestALayerRunsInItsWorkdirWithItsEnv(t *testing.T) {
	f := vm.NewFake()
	var scripts []string
	f.ShellFunc = func(name, script string, out io.Writer) (int, error) {
		scripts = append(scripts, script)
		return 0, nil
	}
	if _, err := manager(t, f).Ensure(context.Background(), "dx", dxProject(t), io.Discard); err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(scripts, "\n---\n")
	for _, want := range []string{
		"mkdir -p '" + project.DefaultWorkdir + "'",
		"cd '" + project.DefaultWorkdir + "'",
		`export BUNDLE_JOBS="4"`,
		"bundle install",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("missing %q in:\n%s", want, joined)
		}
	}
	copies := 0
	for _, call := range f.Calls {
		if strings.HasPrefix(call, "copyin ") {
			copies++
		}
	}
	if copies != 2 {
		t.Errorf("copied into %d layers, want the two that declare copy:", copies)
	}
}

func TestAFailedLayerIsDestroyedAndItsAncestorsSurvive(t *testing.T) {
	f := vm.NewFake()
	f.ShellFunc = func(name, script string, out io.Writer) (int, error) {
		if strings.Contains(script, "bundle install") {
			return 3, nil
		}
		return 0, nil
	}
	m := manager(t, f)

	plan, err := m.Ensure(context.Background(), "dx", dxProject(t), io.Discard)
	if err == nil {
		t.Fatal("expected the layer failure to surface")
	}
	if !strings.Contains(err.Error(), "exited 3") {
		t.Errorf("error = %v, want the exit code", err)
	}
	if f.Exists(plan.Image()) {
		t.Error("a half-built layer must not be left behind to be reused")
	}
	for _, name := range plan.Chain[:3] {
		if !f.Exists(name) {
			t.Errorf("%s was destroyed; the layers that succeeded are still valid", name)
		}
	}
}

func TestConcurrentEnsureBuildsOnce(t *testing.T) {
	f := vm.NewFake()
	m := manager(t, f)
	p := dxProject(t)
	var wg sync.WaitGroup
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = m.Ensure(context.Background(), "dx", p, io.Discard)
		}()
	}
	wg.Wait()
	for _, prefix := range []string{"create ", "clone "} {
		got := strings.Count(strings.Join(f.Calls, "\n"), prefix)
		want := 1
		if prefix == "clone " {
			want = 3
		}
		if got != want {
			t.Errorf("%q happened %d times, want %d: concurrent runs must share the build", prefix, got, want)
		}
	}
}

func TestPruneKeepsRecentHeadsWholeAndNeverTouchesRunningOrForeign(t *testing.T) {
	f := vm.NewFake()
	m := manager(t, f)
	m.Keep = 1

	base := "forge-base-abc-100"
	f.Seed(base, "Stopped")
	chains := map[string][]string{
		"fresh": {"forge-layer-01-aaaa", "forge-layer-02-aaab"},
		"stale": {"forge-layer-01-aaaa", "forge-layer-02-bbbb"},
	}
	for label, chain := range chains {
		used := time.Unix(1_780_000_000, 0)
		if label == "stale" {
			used = used.Add(-72 * time.Hour)
		}
		parent := base
		for i, name := range chain {
			f.Seed(name, "Stopped")
			if err := m.meta().save(Meta{Name: name, Parent: parent, Depth: i + 1, LastUsedAt: used}); err != nil {
				t.Fatal(err)
			}
			parent = name
		}
	}
	f.Seed("forge-proj-dx-old", "Stopped")
	f.Seed("ci-proj-dx-36ec09c9f7", "Stopped")
	f.Seed("forge-layer-09-busy", "Running")

	pruned, err := m.Prune(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"forge-layer-01-aaaa", "forge-layer-02-aaab"} {
		if !f.Exists(name) {
			t.Errorf("%s was pruned; the most recent head must keep its whole ancestry", name)
		}
	}
	if f.Exists("forge-layer-02-bbbb") {
		t.Error("the least recently used head survived")
	}
	if f.Exists("forge-proj-dx-old") {
		t.Error("an image from the scheme the Forgefile replaced was kept; nothing can use it")
	}
	if !f.Exists("ci-proj-dx-36ec09c9f7") {
		t.Error("prune destroyed an instance belonging to the system forge replaces")
	}
	if !f.Exists("forge-layer-09-busy") {
		t.Error("prune destroyed a Running layer")
	}
	if !f.Exists(base) {
		t.Error("prune destroyed the base every kept layer descends from")
	}
	if len(pruned) != 2 {
		t.Errorf("pruned %v, want the stale head and the legacy image", pruned)
	}
}

func TestPruneForgetsMetadataForImagesThatAreGone(t *testing.T) {
	f := vm.NewFake()
	m := manager(t, f)
	if err := m.meta().save(Meta{Name: "forge-layer-01-ghost"}); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Prune(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, ok := m.Describe("forge-layer-01-ghost"); ok {
		t.Error("metadata outlived the image it describes")
	}
}

func TestDestroyRefusesForeignInstances(t *testing.T) {
	f := vm.NewFake()
	f.Seed("default", "Stopped")
	if err := manager(t, f).Destroy(context.Background(), "default"); err == nil {
		t.Error("Destroy must refuse an instance forge does not own")
	}
	if !f.Exists("default") {
		t.Error("it destroyed it anyway")
	}
}

// limactl creates the instance directory the moment a clone starts, so
// existence alone would hand a job a layer whose build is still running, or one
// left behind by a machine that died mid-build.
func TestAHalfBuiltLayerIsNotTreatedAsCached(t *testing.T) {
	f := vm.NewFake()
	m := manager(t, f)
	plan := m.PlanFor(dxProject(t))
	f.Seed(plan.Base, "Stopped")
	f.Seed(plan.Chain[1], "Stopped")

	if _, err := m.Ensure(context.Background(), "dx", dxProject(t), io.Discard); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(f.Calls, "\n"), "delete "+plan.Chain[1]) {
		t.Errorf("the leftover layer was reused rather than rebuilt: %v", f.Calls)
	}
	if _, ok := m.Describe(plan.Chain[1]); !ok {
		t.Error("a finished layer must leave a record saying so")
	}
}

func TestEnsureRefusesToRunWithoutSomewhereToRecordCompletion(t *testing.T) {
	m := manager(t, vm.NewFake())
	m.MetaDir = ""
	if _, err := m.Ensure(context.Background(), "dx", dxProject(t), io.Discard); err == nil {
		t.Error("expected Ensure to refuse rather than silently lose the completion marker")
	}
}

// The base template is 1GiB, which is not enough to run a bundle install. A
// layer builds at the size the Forgefile asked for.
func TestLayersAreBuiltAtTheSizeTheBuildFileAsksFor(t *testing.T) {
	f := vm.NewFake()
	plan, err := manager(t, f).Ensure(context.Background(), "dx", dxProject(t), io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	list, _ := f.List(context.Background())
	seen := 0
	for _, inst := range list {
		if !IsLayer(inst.Name) {
			continue
		}
		seen++
		if inst.Memory != 3<<30 || inst.CPUs != 4 {
			t.Errorf("%s built with %dGiB/%d cpus, want 3GiB/4", inst.Name, inst.Memory>>30, inst.CPUs)
		}
	}
	if seen != len(plan.Chain)-1 {
		t.Errorf("checked %d layers, want %d", seen, len(plan.Chain)-1)
	}
}

// Measured on a real VM: `sudo cp -a stage/. /` applies the stage directory's
// ownership to /, turning root:root 755 into the build user's. The image still
// boots; its sshd never answers again. Nothing in a unit test can see that, so
// the shape of the command is pinned here instead.
func TestTheCopyIsInstalledWithoutTouchingExistingDirectories(t *testing.T) {
	script := installScript([]string{"/forge/build", "/opt/ci/seed.sql"})
	if !strings.Contains(script, "--no-overwrite-dir") {
		t.Errorf("install script does not preserve existing directory metadata:\n%s", script)
	}
	if strings.Contains(script, "cp -a") {
		t.Errorf("install script is back to cp -a, which chowns /:\n%s", script)
	}
	if !strings.Contains(script, "set -euo pipefail") {
		t.Errorf("the tar pipeline needs pipefail, or a failed pack reads as success:\n%s", script)
	}
	for _, root := range []string{"'/forge/build'", "'/opt/ci/seed.sql'"} {
		if !strings.Contains(script, root) {
			t.Errorf("copied paths are not handed to the build user: %s missing from\n%s", root, script)
		}
	}
}
