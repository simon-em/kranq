package jobproc

import (
	"os"
	"os/exec"
	"syscall"
	"testing"
	"time"
)

func TestResultRoundTrip(t *testing.T) {
	dir := t.TempDir()
	if _, ok := ReadResult(dir); ok {
		t.Fatal("a result appeared before anything wrote one")
	}
	want := Result{ExitCode: 42, VMName: "forge-run-dx-spec-aa", FenceRef: "refs/forge/fence/x", FenceHeld: true}
	if err := WriteResult(dir, want); err != nil {
		t.Fatal(err)
	}
	got, ok := ReadResult(dir)
	if !ok {
		t.Fatal("the result did not read back")
	}
	if got.ExitCode != 42 || got.VMName != want.VMName || !got.FenceHeld {
		t.Fatalf("got %+v", got)
	}
	if got.FinishedAt.IsZero() {
		t.Fatal("a result with no finish time cannot be ordered against anything")
	}
}

func TestClearingAResultIsIdempotent(t *testing.T) {
	dir := t.TempDir()
	if err := ClearResult(dir); err != nil {
		t.Fatalf("clearing nothing: %v", err)
	}
	if err := WriteResult(dir, Result{}); err != nil {
		t.Fatal(err)
	}
	if err := ClearResult(dir); err != nil {
		t.Fatal(err)
	}
	if _, ok := ReadResult(dir); ok {
		t.Fatal("the result survived being cleared; a stale one would be read as this attempt's")
	}
}

func TestAHalfWrittenResultIsNotRead(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(ResultPath(dir), []byte(`{"exit_code": 4`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, ok := ReadResult(dir); ok {
		t.Fatal("truncated json was accepted as a result")
	}
}

func sleeper(t *testing.T, args ...string) *exec.Cmd {
	t.Helper()
	cmd := exec.Command("sleep", append([]string{"30"}, args...)...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = Kill(cmd.Process.Pid)
		_ = cmd.Wait()
	})
	return cmd
}

// A pid alone is not an identity: the number is reused, and adopting a stranger
// that inherited it would be worse than losing the task.
func TestAliveChecksWhatTheProcessActuallyIs(t *testing.T) {
	cmd := sleeper(t)
	pid := cmd.Process.Pid

	if Alive(pid, "20260904T150405-abc") {
		t.Fatal("an unrelated process was accepted as this task's job")
	}
	if Alive(0, "anything") || Alive(-1, "anything") {
		t.Fatal("a nonsense pid was reported alive")
	}
}

func TestAliveRecognisesTheRealJobProcess(t *testing.T) {
	const id = "20260904T150405-abc"
	// Shaped like what the supervisor starts: a process whose command line
	// carries the exec verb and the task id.
	cmd := exec.Command("sh", "-c", "exec sleep 30 "+ExecVerb+" "+id)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = Kill(cmd.Process.Pid)
		_ = cmd.Wait()
	}()
	deadline := time.Now().Add(3 * time.Second)
	for !Alive(cmd.Process.Pid, id) {
		if time.Now().After(deadline) {
			t.Fatal("the job's own process was not recognised")
		}
		time.Sleep(20 * time.Millisecond)
	}
	if Alive(cmd.Process.Pid, "some-other-task") {
		t.Fatal("a live job was claimed by the wrong task id")
	}
}

func TestAliveIsFalseOnceTheProcessIsGone(t *testing.T) {
	cmd := exec.Command("true")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	pid := cmd.Process.Pid
	_ = cmd.Wait()
	if Alive(pid, "anything") {
		t.Fatal("a reaped process was reported alive")
	}
}

// Signalling only the leader leaves the VM up until the job ends on its own,
// which permanently costs a slot.
func TestTerminateSignalsTheWholeGroup(t *testing.T) {
	cmd := exec.Command("sh", "-c", "sleep 30 & sleep 30")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	pgid := cmd.Process.Pid
	if err := Terminate(pgid); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() { _ = cmd.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		_ = Kill(pgid)
		t.Fatal("the group did not die on SIGTERM")
	}
	if err := syscall.Kill(-pgid, 0); err == nil {
		t.Fatal("something in the group is still running")
	}
}

func TestSignallingNothingIsAnError(t *testing.T) {
	if Terminate(0) == nil || Kill(0) == nil {
		t.Fatal("signalling process group 0 would hit every process we can reach")
	}
}
