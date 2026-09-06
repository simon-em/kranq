package run

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/simon-em/kranq/internal/image"
	"github.com/simon-em/kranq/internal/vm"
)

func engine(t *testing.T, f *vm.Fake) *Engine {
	t.Helper()
	return &Engine{
		Driver: f,
		Images: &image.Manager{
			Driver:   f,
			Template: []byte("vmType: vz\n"),
			MetaDir:  filepath.Join(t.TempDir(), "layers"),
			Now:      func() time.Time { return time.Unix(1_780_000_000, 0) },
		},
	}
}

func checkout(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	body := "MEMORY 3GiB\nCPUS 4\nRUN echo provisioning\n"
	if err := os.WriteFile(filepath.Join(dir, "Kranqfile"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

func request(t *testing.T) Request {
	return Request{
		TaskID:     "20260904T150405-abc",
		Repo:       "dx",
		Ref:        "ci/lima",
		Label:      "spec",
		Script:     "#!/usr/bin/env bash\necho running\n",
		Checkout:   checkout(t),
		RemoteBase: base,
	}
}

func TestExecuteRunsAJobAndAlwaysDestroysItsVM(t *testing.T) {
	f := vm.NewFake()
	res, err := engine(t, f).Execute(context.Background(), request(t), io.Discard)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.ExitCode != 0 {
		t.Errorf("exit = %d", res.ExitCode)
	}
	if f.Exists(res.VMName) {
		t.Error("the run VM outlived the job")
	}
	if !f.Exists(res.Image) {
		t.Error("the project image must survive; it is the cache")
	}
}

func TestExecuteDestroysTheVMWhenTheJobFails(t *testing.T) {
	f := vm.NewFake()
	f.ShellFunc = func(name, script string, out io.Writer) (int, error) {
		if strings.Contains(script, "kranq-task.sh") {
			return 5, nil
		}
		return 0, nil
	}
	res, err := engine(t, f).Execute(context.Background(), request(t), io.Discard)
	if err != nil {
		t.Fatalf("a failing job is a result, not an error: %v", err)
	}
	if res.ExitCode != 5 {
		t.Errorf("exit = %d, want the job's own 5", res.ExitCode)
	}
	if f.Exists(res.VMName) {
		t.Error("a failing job must still have its VM destroyed")
	}
}

func TestExecuteDestroysTheVMWhenCancelled(t *testing.T) {
	f := vm.NewFake()
	ctx, cancel := context.WithCancel(context.Background())
	f.ShellFunc = func(name, script string, out io.Writer) (int, error) {
		if strings.Contains(script, "kranq-task.sh") {
			cancel()
			return -1, context.Canceled
		}
		return 0, nil
	}
	res, err := engine(t, f).Execute(ctx, request(t), io.Discard)
	if err == nil {
		t.Fatal("expected the cancellation to surface")
	}
	if res.VMName != "" && f.Exists(res.VMName) {
		t.Error("cancellation must still tear the VM down, using a context that is not itself cancelled")
	}
}

func TestTheJobRunsInAFreshCheckoutOfTheRequestedRef(t *testing.T) {
	f := vm.NewFake()
	var job string
	f.ShellFunc = func(name, script string, out io.Writer) (int, error) {
		if strings.Contains(script, "kranq-task.sh") {
			job = script
		}
		return 0, nil
	}
	req := request(t)
	req.Env = map[string]string{"BITBUCKET_TOKEN": "s3cret", "MAINTENANCE_SCAN_URL": "dx.ca"}
	if _, err := engine(t, f).Execute(context.Background(), req, io.Discard); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`rm -rf "$HOME/work"`,
		"--branch 'ci/lima'",
		`cd "$HOME/work"`,
		"exec bash /tmp/kranq-task.sh",
		"export MAINTENANCE_SCAN_URL='dx.ca'",
		"export KRANQ_GIT_TOKEN='s3cret'",
	} {
		if !strings.Contains(job, want) {
			t.Errorf("the job script is missing %q:\n%s", want, job)
		}
	}
}

func TestArtifactsAreCollectedEvenWhenTheJobFails(t *testing.T) {
	f := vm.NewFake()
	f.ShellFunc = func(name, script string, out io.Writer) (int, error) {
		switch {
		case strings.Contains(script, "kranq-task.sh"):
			return 1, nil
		case strings.HasPrefix(script, "test -d"):
			return 0, nil
		}
		return 0, nil
	}
	req := request(t)
	req.ArtifactDir = filepath.Join(t.TempDir(), "out")
	res, err := engine(t, f).Execute(context.Background(), req, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Artifacts {
		t.Error("a failing job is exactly when its report is worth having")
	}
	if !strings.Contains(strings.Join(f.Calls, "\n"), "copyout") {
		t.Errorf("no copyout was issued: %v", f.Calls)
	}
}

func TestNoArtifactsWhenTheJobLeftNone(t *testing.T) {
	f := vm.NewFake()
	f.ShellFunc = func(name, script string, out io.Writer) (int, error) {
		if strings.HasPrefix(script, "test -d") {
			return 1, nil
		}
		return 0, nil
	}
	req := request(t)
	req.ArtifactDir = filepath.Join(t.TempDir(), "out")
	res, err := engine(t, f).Execute(context.Background(), req, io.Discard)
	if err != nil || res.Artifacts {
		t.Errorf("artifacts=%v err=%v, want none reported", res.Artifacts, err)
	}
}

func TestAMissingKranqfileFailsBeforeAnyVMIsCreated(t *testing.T) {
	f := vm.NewFake()
	req := request(t)
	req.Checkout = t.TempDir()
	if _, err := engine(t, f).Execute(context.Background(), req, io.Discard); err == nil {
		t.Fatal("expected an error")
	}
	if len(f.Names()) != 0 {
		t.Errorf("instances were created before the spec was validated: %v", f.Names())
	}
}

func TestKeepNeverDestroysEvenOnFailure(t *testing.T) {
	f := vm.NewFake()
	f.ShellFunc = func(name, script string, out io.Writer) (int, error) {
		if strings.Contains(script, "kranq-task.sh") {
			return 1, nil
		}
		return 0, nil
	}
	req := request(t)
	req.Keep = KeepNever
	res, err := engine(t, f).Execute(context.Background(), req, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if res.Kept || f.Exists(res.VMName) {
		t.Error("the default must destroy the VM, or a busy machine fills with corpses")
	}
}

func TestKeepOnFailureKeepsAFailedRunForInspection(t *testing.T) {
	f := vm.NewFake()
	f.ShellFunc = func(name, script string, out io.Writer) (int, error) {
		if strings.Contains(script, "kranq-task.sh") {
			return 1, nil
		}
		return 0, nil
	}
	req := request(t)
	req.Keep = KeepOnFailure
	res, err := engine(t, f).Execute(context.Background(), req, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Kept || !f.Exists(res.VMName) {
		t.Error("a failure that only reproduces on the build machine is exactly what needs keeping")
	}
}

func TestKeepOnFailureStillDestroysASuccess(t *testing.T) {
	f := vm.NewFake()
	req := request(t)
	req.Keep = KeepOnFailure
	res, err := engine(t, f).Execute(context.Background(), req, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if res.Kept || f.Exists(res.VMName) {
		t.Error("a passing run has nothing to inspect")
	}
}

func TestKeepOnFailureKeepsAVMWhenTheRunErroredRatherThanExited(t *testing.T) {
	f := vm.NewFake()
	f.ShellFunc = func(name, script string, out io.Writer) (int, error) {
		if strings.Contains(script, "kranq-task.sh") {
			return -1, errors.New("the guest went away")
		}
		return 0, nil
	}
	req := request(t)
	req.Keep = KeepOnFailure
	res, _ := engine(t, f).Execute(context.Background(), req, io.Discard)
	if res.VMName == "" || !f.Exists(res.VMName) {
		t.Error("an infrastructure failure is the case most worth inspecting, and has no exit code")
	}
}

func TestKeepAlwaysKeepsASuccess(t *testing.T) {
	f := vm.NewFake()
	req := request(t)
	req.Keep = KeepAlways
	res, err := engine(t, f).Execute(context.Background(), req, io.Discard)
	if err != nil || !res.Kept || !f.Exists(res.VMName) {
		t.Errorf("kept=%v err=%v", res.Kept, err)
	}
}

func TestParseKeepRejectsNonsense(t *testing.T) {
	for _, good := range []string{"", "never", "on-failure", "always"} {
		if _, ok := ParseKeep(good); !ok {
			t.Errorf("ParseKeep(%q) was rejected", good)
		}
	}
	if _, ok := ParseKeep("sometimes"); ok {
		t.Error("a typo must be rejected at the flag, not silently mean never")
	}
}

// The Kranqfile sizes the run VM, not the layers, so a layer shared with a project
// that wants less memory is not the thing that decides how big this job gets.
func TestTheRunVMIsSizedByTheBuildFile(t *testing.T) {
	f := vm.NewFake()
	res, err := engine(t, f).Execute(context.Background(), request(t), io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	list, _ := f.List(context.Background())
	for _, inst := range list {
		if inst.Name == res.VMName && inst.CPUs != 4 {
			t.Errorf("the job VM got %d cpus, want the 4 the Kranqfile asked for", inst.CPUs)
		}
	}
}

func TestACustomBuildFileIsHonoured(t *testing.T) {
	f := vm.NewFake()
	req := request(t)
	body := "RUN echo staging\n"
	if err := os.WriteFile(filepath.Join(req.Checkout, "Kranqfile.staging"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	req.Kranqfile = "Kranqfile.staging"

	var scripts []string
	f.ShellFunc = func(name, script string, out io.Writer) (int, error) {
		scripts = append(scripts, script)
		return 0, nil
	}
	if _, err := engine(t, f).Execute(context.Background(), req, io.Discard); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(scripts, "\n"), "echo staging") {
		t.Error("the named build file was not the one that ran")
	}
}

func TestAMissingBuildFileNamesIt(t *testing.T) {
	req := request(t)
	if err := os.Remove(filepath.Join(req.Checkout, "Kranqfile")); err != nil {
		t.Fatal(err)
	}
	_, err := engine(t, vm.NewFake()).Execute(context.Background(), req, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "Kranqfile") {
		t.Errorf("error = %v, want it to name the file the repository is missing", err)
	}
}
