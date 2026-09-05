package daemon

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/effetmonstre/forge/internal/jobproc"
	"github.com/effetmonstre/forge/internal/state"
)

// A stand-in for the forge binary: invoked as `<script> exec <id>`, so its
// command line looks to ps exactly like the real job process does.
func fakeJob(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "forge")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func supervisor(t *testing.T, binary string) (*Supervisor, string) {
	t.Helper()
	dir := t.TempDir()
	return &Supervisor{
		Binary:  binary,
		Home:    dir,
		TaskDir: func(string) string { return dir },
		Poll:    10 * time.Millisecond,
	}, dir
}

func job(id string) state.Task { return state.Task{ID: id, Repo: "dx", Branch: "main"} }

func devnull(t *testing.T) *os.File {
	t.Helper()
	f, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.Close() })
	return f
}

func writeResultScript(dir string, code int, message string) string {
	return "printf '{\"exit_code\":" + itoa(code) + ",\"vm_name\":\"forge-run-x\"" + message +
		"}\\n' > " + dir + "/result.json\n"
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	if neg {
		return "-" + string(b)
	}
	return string(b)
}

func TestExecuteReportsWhatTheChildRecorded(t *testing.T) {
	s, dir := supervisor(t, "")
	s.Binary = fakeJob(t, writeResultScript(dir, 7, ""))

	code, err := s.Execute(context.Background(), job("t-1"), "echo hi", devnull(t))
	if err != nil {
		t.Fatal(err)
	}
	if code != 7 {
		t.Fatalf("exit code %d, want 7", code)
	}
	script, readErr := os.ReadFile(jobproc.ScriptPath(dir))
	if readErr != nil || string(script) != "echo hi" {
		t.Fatalf("the child was not handed its script: %q %v", script, readErr)
	}
}

func TestExecuteSurfacesTheChildsError(t *testing.T) {
	s, dir := supervisor(t, "")
	s.Binary = fakeJob(t, writeResultScript(dir, -1, `,"error":"the image build failed"`))

	_, err := s.Execute(context.Background(), job("t-1"), "", devnull(t))
	if err == nil || !strings.Contains(err.Error(), "image build failed") {
		t.Fatalf("got %v", err)
	}
}

func TestAChildThatDiesWithoutRecordingAnythingIsAnError(t *testing.T) {
	s, _ := supervisor(t, "")
	s.Binary = fakeJob(t, "exit 3\n")

	_, err := s.Execute(context.Background(), job("t-1"), "", devnull(t))
	if err == nil || !strings.Contains(err.Error(), "left no result") {
		t.Fatalf("got %v; a job that vanished must not be read as a clean exit", err)
	}
}

// The reason any of this exists: the daemon goes away mid-run, the job keeps
// going, and a new daemon picks the outcome back up.
func TestAJobSurvivesTheDaemonAndIsAdopted(t *testing.T) {
	s, dir := supervisor(t, "")
	s.Binary = fakeJob(t, "sleep 0.6\n"+writeResultScript(dir, 0, ""))

	var pgid int
	s.RecordPGID = func(_ string, p int) { pgid = p }

	// The daemon starts the job and then dies. A crash is not a cancellation:
	// it kills the daemon and leaves the child running, so the await is simply
	// abandoned rather than cancelled. Cancelling really would end the job,
	// which is what TestCancellingTheContextSignalsTheJob checks.
	go s.Execute(context.Background(), job("t-1"), "", devnull(t))
	waitFor(t, func() bool { return pgid != 0 })

	// A fresh daemon, with only what the store recorded.
	next, _ := supervisor(t, s.Binary)
	next.TaskDir = func(string) string { return dir }
	recovered := state.Task{ID: "t-1", ExecPGID: pgid}

	if !next.Adoptable(recovered) {
		t.Fatal("a job that is still running was not adoptable")
	}
	code, err := next.Adopt(context.Background(), recovered)
	if err != nil {
		t.Fatal(err)
	}
	if code != 0 {
		t.Fatalf("exit code %d", code)
	}
}

func TestAJobThatFinishedWhileTheDaemonWasDownIsStillAdoptable(t *testing.T) {
	s, dir := supervisor(t, "")
	if err := jobproc.WriteResult(dir, jobproc.Result{ExitCode: 5}); err != nil {
		t.Fatal(err)
	}
	// The process is long gone; only the record remains.
	recovered := state.Task{ID: "t-1", ExecPGID: 999999}
	if !s.Adoptable(recovered) {
		t.Fatal("a finished job's result was thrown away")
	}
	code, err := s.Adopt(context.Background(), recovered)
	if err != nil {
		t.Fatal(err)
	}
	if code != 5 {
		t.Fatalf("exit code %d, want 5", code)
	}
}

func TestAJobWithNeitherProcessNorResultIsNotAdoptable(t *testing.T) {
	s, _ := supervisor(t, "")
	if s.Adoptable(state.Task{ID: "t-1", ExecPGID: 999999}) {
		t.Fatal("a task with nothing behind it was reported adoptable")
	}
	if s.Adoptable(state.Task{ID: "t-1"}) {
		t.Fatal("a task that never recorded a pgid was reported adoptable")
	}
}

// A result left by an earlier attempt would otherwise be read as this one's, and
// the job would be reported finished before it had started.
func TestAStaleResultIsClearedBeforeStarting(t *testing.T) {
	s, dir := supervisor(t, "")
	if err := jobproc.WriteResult(dir, jobproc.Result{ExitCode: 99}); err != nil {
		t.Fatal(err)
	}
	s.Binary = fakeJob(t, "sleep 0.3\n"+writeResultScript(dir, 0, ""))

	code, err := s.Execute(context.Background(), job("t-1"), "", devnull(t))
	if err != nil {
		t.Fatal(err)
	}
	if code == 99 {
		t.Fatal("the previous attempt's result was reported as this one's")
	}
	if code != 0 {
		t.Fatalf("exit code %d", code)
	}
}

func TestCancellingTheContextSignalsTheJob(t *testing.T) {
	s, dir := supervisor(t, "")
	s.Binary = fakeJob(t, "sleep 30\n"+writeResultScript(dir, 0, ""))
	var pgid int
	s.RecordPGID = func(_ string, p int) { pgid = p }

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := s.Execute(ctx, job("t-1"), "", devnull(t))
		done <- err
	}()
	waitFor(t, func() bool { return pgid != 0 })
	cancel()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("a cancelled job reported success")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("cancelling did not return")
	}
	waitFor(t, func() bool { return !jobproc.Alive(pgid, "t-1") })
}

func TestTheResultIsHandedBackForRecording(t *testing.T) {
	s, dir := supervisor(t, "")
	s.Binary = fakeJob(t, writeResultScript(dir, 0, ""))
	var got jobproc.Result
	s.RecordResult = func(_ string, r jobproc.Result) { got = r }

	if _, err := s.Execute(context.Background(), job("t-1"), "", devnull(t)); err != nil {
		t.Fatal(err)
	}
	if got.VMName != "forge-run-x" {
		t.Fatalf("the daemon was not told which VM ran the job: %+v", got)
	}
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("condition never became true")
		}
		time.Sleep(10 * time.Millisecond)
	}
}
