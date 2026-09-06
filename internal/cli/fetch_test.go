package cli

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/simon-em/kranq/internal/daemon"
	"github.com/simon-em/kranq/internal/exitcode"
	"github.com/simon-em/kranq/internal/ipc"
	"github.com/simon-em/kranq/internal/state"
)

// An executor that runs nothing and leaves one artifact behind, which is all
// fetch needs to have something to bring back.
type artifactExec struct{ dir func(string) string }

func (a artifactExec) Adoptable(state.Task) bool { return false }

func (a artifactExec) Adopt(context.Context, state.Task) (int, error) { return -1, context.Canceled }

func (a artifactExec) Execute(_ context.Context, t state.Task, _ string, _ *os.File) (int, error) {
	dir := a.dir(t.ID)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return 1, err
	}
	return 0, os.WriteFile(filepath.Join(dir, "report.txt"), []byte("the report\n"), 0o600)
}

func daemonWithAnArtifact(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("KRANQ_HOME", home)
	t.Setenv("KRANQ_AUTOSTART", "0")

	exec := &artifactExec{}
	d, err := daemon.New(daemon.Config{
		Home: home, Version: "test", MaxVMs: 1, PollInterval: 10 * time.Millisecond,
	}, exec)
	if err != nil {
		t.Fatal(err)
	}
	exec.dir = d.ArtifactsDir

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- d.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("the daemon did not stop")
		}
	})

	client := ipc.NewClient(daemon.Config{Home: home}.SocketPath())
	if err := client.WaitReady(ctx, 5*time.Second); err != nil {
		t.Fatal(err)
	}
	task, err := client.Submit(ctx, ipc.SubmitRequest{Spec: goodSpec, Repo: "dx", Branch: "main"})
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		got, err := client.Get(ctx, task.ID)
		if err == nil && got.Terminal() {
			return task.ID
		}
		if time.Now().After(deadline) {
			t.Fatal("the task never finished")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// The pipeline runs `ssh machine kranq fetch <id> | tar xzf -`, so stdout has
// to be the archive itself and nothing else.
func TestFetchWritesATarballToStdout(t *testing.T) {
	id := daemonWithAnArtifact(t)
	code, out, _ := invoke(t, "fetch", id)
	if code != exitcode.OK {
		t.Fatalf("exit = %d", code)
	}
	gz, err := gzip.NewReader(strings.NewReader(out))
	if err != nil {
		t.Fatalf("stdout is not a gzip stream: %v", err)
	}
	names := map[string]string{}
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(tr)
		names[hdr.Name] = string(body)
	}
	if names["report.txt"] != "the report\n" {
		t.Errorf("archive holds %v, want report.txt", names)
	}
}

func TestFetchUnpacksWithOut(t *testing.T) {
	id := daemonWithAnArtifact(t)
	dest := filepath.Join(t.TempDir(), "out")
	code, out, errb := invoke(t, "fetch", id, "--out", dest)
	if code != exitcode.OK {
		t.Fatalf("exit = %d: %s", code, errb)
	}
	if out != "" {
		t.Errorf("stdout should be empty when unpacking, got %q", out)
	}
	body, err := os.ReadFile(filepath.Join(dest, "report.txt"))
	if err != nil || string(body) != "the report\n" {
		t.Errorf("artifact not unpacked: %q %v", body, err)
	}
}

// dx pulls a test report from a run that failed, so nothing to fetch must not
// look like a failure to fetch.
func TestFetchingNothingIsNotAnError(t *testing.T) {
	daemonWithAnArtifact(t)
	code, out, errb := invoke(t, "fetch", "20260101T000000-nosuchtask")
	if code != exitcode.OK {
		t.Errorf("exit = %d, want 0 for a task with no artifacts", code)
	}
	if out != "" {
		t.Errorf("stdout = %q, want nothing", out)
	}
	if !strings.Contains(errb, "no artifacts") {
		t.Errorf("stderr = %q, want it to say so", errb)
	}
}

func TestFetchNeedsExactlyOneID(t *testing.T) {
	isolate(t)
	for _, args := range [][]string{{"fetch"}, {"fetch", "a", "b"}} {
		if code, _, _ := invoke(t, args...); code != exitcode.Usage {
			t.Errorf("%v: exit = %d, want %d", args, code, exitcode.Usage)
		}
	}
}
