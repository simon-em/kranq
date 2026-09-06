package daemon

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/simon-em/kranq/internal/ipc"
	"github.com/simon-em/kranq/internal/state"
)

type stubExec struct {
	code        int
	err         error
	hold        chan struct{}
	started     chan string
	writeLog    string
	artifact    string
	dir         func(id string) string
	releaseOnce sync.Once
}

func (s *stubExec) Adoptable(state.Task) bool { return false }

func (s *stubExec) Adopt(ctx context.Context, t state.Task) (int, error) {
	return -1, context.Canceled
}

func (s *stubExec) Execute(ctx context.Context, t state.Task, script string, out *os.File) (int, error) {
	if s.writeLog != "" {
		_, _ = out.WriteString(s.writeLog)
	}
	if s.started != nil {
		s.started <- t.ID
	}
	if s.hold != nil {
		select {
		case <-s.hold:
		case <-ctx.Done():
			return -1, ctx.Err()
		}
	}
	if s.artifact != "" && s.dir != nil {
		dir := s.dir(t.ID)
		_ = os.MkdirAll(dir, 0o700)
		_ = os.WriteFile(filepath.Join(dir, "report.txt"), []byte(s.artifact), 0o600)
	}
	return s.code, s.err
}

func (s *stubExec) release() {
	s.releaseOnce.Do(func() {
		if s.hold != nil {
			close(s.hold)
		}
	})
}

func daemonWith(t *testing.T, exec *stubExec) (*ipc.Client, *Daemon, context.CancelFunc) {
	t.Helper()
	home := t.TempDir()
	cfg := Config{Home: home, Version: "test", MaxVMs: 2, PollInterval: 20 * time.Millisecond, ClaudeToken: "tok"}
	d, err := New(cfg, exec)
	if err != nil {
		t.Fatal(err)
	}
	exec.dir = d.ArtifactsDir

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- d.Run(ctx) }()
	t.Cleanup(func() {
		// Order matters. Cancelling first stops the scheduler launching
		// anything else, so the count below cannot dip to zero between one job
		// finishing and the next queued one starting. Only then are held jobs
		// released, because shutdown deliberately leaves them running and a
		// stub blocked forever would still be writing into the temp directory
		// while it is removed.
		cancel()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("the daemon did not shut down")
		}
		exec.release()
		deadline := time.Now().Add(5 * time.Second)
		for d.sched.RunningCount() > 0 && time.Now().Before(deadline) {
			time.Sleep(10 * time.Millisecond)
		}
	})

	c := ipc.NewClient(d.cfg.SocketPath())
	if err := c.WaitReady(context.Background(), 5*time.Second); err != nil {
		t.Fatal(err)
	}
	return c, d, cancel
}

const spec = "name: smoke\nsteps:\n  - run: true\n"

func TestSubmitRunsAndReportsSuccess(t *testing.T) {
	c, _, _ := daemonWith(t, &stubExec{writeLog: "hello from the job\n"})
	ctx := context.Background()

	task, err := c.Submit(ctx, ipc.SubmitRequest{Spec: spec, Repo: "dx", Branch: "main"})
	if err != nil {
		t.Fatal(err)
	}
	final := await(t, c, task.ID, state.StatusSucceeded)
	if final.Attempts != 1 {
		t.Errorf("attempts = %d", final.Attempts)
	}

	var log bytes.Buffer
	if err := c.Logs(ctx, task.ID, false, &log); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(log.String(), "hello from the job") {
		t.Errorf("log did not come back: %q", log.String())
	}
}

func TestSubmitRejectsABadSpecWithItsOwnCode(t *testing.T) {
	c, _, _ := daemonWith(t, &stubExec{})
	_, err := c.Submit(context.Background(), ipc.SubmitRequest{Spec: "steps: []\n", Repo: "dx", Branch: "main"})
	var re *ipc.RemoteError
	if !as(err, &re) {
		t.Fatalf("err = %v, want a RemoteError", err)
	}
	if re.Code != 65 {
		t.Errorf("code = %d, want 65 (invalid spec) so a caller can tell it from an outage", re.Code)
	}
}

func TestSubmitRejectsAMissingRepo(t *testing.T) {
	c, _, _ := daemonWith(t, &stubExec{})
	_, err := c.Submit(context.Background(), ipc.SubmitRequest{Spec: spec, Branch: "main"})
	if err == nil || !strings.Contains(err.Error(), "no repo") {
		t.Errorf("err = %v, want it to name the missing repo", err)
	}
}

func TestFollowingLogsEndsWhenTheTaskDoes(t *testing.T) {
	exec := &stubExec{hold: make(chan struct{}), started: make(chan string, 1), writeLog: "working\n"}
	c, _, _ := daemonWith(t, exec)
	ctx := context.Background()

	task, err := c.Submit(ctx, ipc.SubmitRequest{Spec: spec, Repo: "dx", Branch: "main"})
	if err != nil {
		t.Fatal(err)
	}
	<-exec.started

	done := make(chan string, 1)
	go func() {
		var log bytes.Buffer
		_ = c.Logs(ctx, task.ID, true, &log)
		done <- log.String()
	}()

	time.Sleep(100 * time.Millisecond)
	exec.release()

	select {
	case log := <-done:
		if !strings.Contains(log, "working") {
			t.Errorf("followed log missing content: %q", log)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("following did not stop when the task reached a terminal state")
	}
}

func TestArtifactsComeBackAsATarball(t *testing.T) {
	c, _, _ := daemonWith(t, &stubExec{artifact: "the report"})
	ctx := context.Background()
	task, err := c.Submit(ctx, ipc.SubmitRequest{Spec: spec, Repo: "dx", Branch: "main"})
	if err != nil {
		t.Fatal(err)
	}
	await(t, c, task.ID, state.StatusSucceeded)

	var buf bytes.Buffer
	ok, err := c.Artifacts(ctx, task.ID, &buf)
	if err != nil || !ok {
		t.Fatalf("Artifacts ok=%v err=%v", ok, err)
	}
	names := untar(t, buf.Bytes())
	if names["report.txt"] != "the report" {
		t.Errorf("tarball contents = %v", names)
	}
}

func TestNoArtifactsIsNotAnError(t *testing.T) {
	c, _, _ := daemonWith(t, &stubExec{})
	ctx := context.Background()
	task, _ := c.Submit(ctx, ipc.SubmitRequest{Spec: spec, Repo: "dx", Branch: "main"})
	await(t, c, task.ID, state.StatusSucceeded)

	ok, err := c.Artifacts(ctx, task.ID, io.Discard)
	if ok || err != nil {
		t.Errorf("ok=%v err=%v, want a clean 'there were none'", ok, err)
	}
}

func TestCancelStopsAJob(t *testing.T) {
	exec := &stubExec{hold: make(chan struct{}), started: make(chan string, 1)}
	c, _, _ := daemonWith(t, exec)
	ctx := context.Background()
	task, _ := c.Submit(ctx, ipc.SubmitRequest{Spec: spec, Repo: "dx", Branch: "main"})
	<-exec.started

	if _, err := c.Cancel(ctx, task.ID); err != nil {
		t.Fatal(err)
	}
	await(t, c, task.ID, state.StatusCancelled)
	exec.release()
}

func TestStatusExplainsTheQueue(t *testing.T) {
	exec := &stubExec{hold: make(chan struct{}), started: make(chan string, 4)}
	c, _, _ := daemonWith(t, exec)
	ctx := context.Background()
	for range 3 {
		if _, err := c.Submit(ctx, ipc.SubmitRequest{Spec: spec, Repo: "dx", Branch: "main"}); err != nil {
			t.Fatal(err)
		}
	}
	<-exec.started
	<-exec.started

	s := awaitStatus(t, c, func(s ipc.Status) bool { return strings.Contains(s.StopReason, "vm slots") })
	if s.Running != 2 || s.MaxVMs != 2 {
		t.Errorf("status = %+v, want 2 running of 2 slots", s)
	}
	if s.Queued != 1 {
		t.Errorf("queued = %d, want the third task waiting", s.Queued)
	}
	if !s.Claude.Present {
		t.Error("the token was configured but Present is false")
	}
	exec.release()
}

func TestUnknownTaskIs404(t *testing.T) {
	c, _, _ := daemonWith(t, &stubExec{})
	_, err := c.Get(context.Background(), "nope")
	if err == nil || !strings.Contains(err.Error(), "no such task") {
		t.Errorf("err = %v", err)
	}
}

func TestASecondDaemonRefusesToStart(t *testing.T) {
	_, d, _ := daemonWith(t, &stubExec{})
	second, err := New(d.cfg, &stubExec{})
	if err != nil {
		t.Fatal(err)
	}
	err = second.Run(context.Background())
	if err == nil || !strings.Contains(err.Error(), "already running") {
		t.Errorf("err = %v, want a refusal naming the running daemon rather than corrupting its state", err)
	}
}

func TestTheSocketIsNotWorldReadable(t *testing.T) {
	_, d, _ := daemonWith(t, &stubExec{})
	info, err := os.Stat(d.cfg.SocketPath())
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("socket is %v, want 0600: file permissions are the whole authorization model here", info.Mode().Perm())
	}
}

func awaitStatus(t *testing.T, c *ipc.Client, ok func(ipc.Status) bool) ipc.Status {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	var last ipc.Status
	for time.Now().Before(deadline) {
		s, err := c.Status(context.Background())
		if err == nil {
			last = s
			if ok(s) {
				return s
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("status never satisfied the condition; last was %+v", last)
	return last
}

func await(t *testing.T, c *ipc.Client, id string, want state.Status) state.Task {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	var last state.Task
	for time.Now().Before(deadline) {
		got, err := c.Get(context.Background(), id)
		if err == nil {
			last = got
			if got.Status == want {
				return got
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("task %s stayed %q, want %q", id, last.Status, want)
	return last
}

func as(err error, target **ipc.RemoteError) bool {
	e, ok := err.(*ipc.RemoteError)
	if ok {
		*target = e
	}
	return ok
}

func untar(t *testing.T, data []byte) map[string]string {
	t.Helper()
	gz, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]string{}
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return out
		}
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(tr)
		out[hdr.Name] = string(body)
	}
}

// Shutting down used to wait for running jobs to finish, from when a restart
// lost them. Now they are deliberately left alive to be re-adopted, and a job's
// context is no longer tied to the daemon's, so that wait could only ever time
// out while the old process sat on the lock and the new one failed to start.
func TestShutdownDoesNotWaitForJobsItIsLeavingAlive(t *testing.T) {
	exec := &stubExec{hold: make(chan struct{}), started: make(chan string, 1)}
	_, d, _ := daemonWith(t, exec)

	if _, err := d.Submit(ipc.SubmitRequest{Spec: spec, Repo: "dx", Branch: "main"}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-exec.started:
	case <-time.After(5 * time.Second):
		t.Fatal("the job never started")
	}

	done := make(chan struct{})
	go func() { d.drain(); close(done) }()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("drain waited for a job that is meant to keep running")
	}
	exec.release()
}
