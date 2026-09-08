package sched

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/simon-em/kranq/internal/gate"
	"github.com/simon-em/kranq/internal/hostres"
	"github.com/simon-em/kranq/internal/state"
	"github.com/simon-em/kranq/internal/svc"
	"github.com/simon-em/kranq/internal/task"
)

type fakeExec struct {
	mu       sync.Mutex
	ran      []string
	adopted  []string
	code     int
	err      error
	hold     chan struct{}
	started  chan string
	adoptIDs map[string]bool
}

func (f *fakeExec) Adoptable(t state.Task) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.adoptIDs[t.ID]
}

func (f *fakeExec) Adopt(ctx context.Context, t state.Task) (int, error) {
	f.mu.Lock()
	f.adopted = append(f.adopted, t.ID)
	hold, code, err := f.hold, f.code, f.err
	f.mu.Unlock()
	if hold != nil {
		select {
		case <-hold:
		case <-ctx.Done():
			return -1, ctx.Err()
		}
	}
	return code, err
}

func (f *fakeExec) adoptedIDs() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.adopted...)
}

func (f *fakeExec) Execute(ctx context.Context, t state.Task, script string, out *os.File) (int, error) {
	f.mu.Lock()
	f.ran = append(f.ran, t.ID)
	hold, started := f.hold, f.started
	code, err := f.code, f.err
	f.mu.Unlock()
	if started != nil {
		started <- t.ID
	}
	if hold != nil {
		select {
		case <-hold:
		case <-ctx.Done():
			return -1, ctx.Err()
		}
	}
	return code, err
}

func (f *fakeExec) idsRun() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.ran...)
}

func harness(t *testing.T, exec Executor) (*Scheduler, *state.Store, *time.Time) {
	t.Helper()
	dir := t.TempDir()
	store, err := state.Open(filepath.Join(dir, "tasks"))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1_780_000_000, 0)
	g := gate.New(filepath.Join(dir, "gate.json"), "tok", time.Minute)
	s := New(Config{
		MaxVMs: 2, MemoryHeadroom: 2 << 30, PollInterval: time.Hour,
		TaskTimeout: time.Minute, ClaudeToken: "tok",
	}, store, g, exec)
	s.now = func() time.Time { return now }
	s.probe = func() hostres.Snapshot {
		return hostres.Snapshot{TotalBytes: 16 << 30, AvailableBytes: 16 << 30, CPUs: 8, OK: true}
	}
	t.Cleanup(func() {
		for _, task := range store.List() {
			_ = s.Cancel(task.ID)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		s.Wait(ctx)
	})
	return s, store, &now
}

func queue(t *testing.T, store *state.Store, id string, at time.Time, spec string) state.Task {
	t.Helper()
	parsed, err := task.Parse([]byte(spec))
	if err != nil {
		t.Fatal(err)
	}
	tk := state.Task{
		ID: id, Name: parsed.Name, Repo: "dx", Branch: "main", Label: parsed.Label,
		Status: state.StatusQueued, NeedsClaude: parsed.NeedsClaude(),
		MemoryBytes: parsed.MemoryBytes(), CPUs: parsed.Resources.CPUs,
		SpecYAML: spec, CreatedAt: at,
	}
	if err := store.Create(tk); err != nil {
		t.Fatal(err)
	}
	return tk
}

const shellSpec = "name: spec\nsteps:\n  - run: true\n"
const claudeSpec = "name: review\nsteps:\n  - claude: look\n"

func bigSpec(gib int) string {
	return "name: big\nresources:\n  memory: " + itoa(gib) + "GiB\nsteps:\n  - run: true\n"
}

func itoa(n int) string { return string(rune('0' + n)) }

func TestATaskRunsAndSucceeds(t *testing.T) {
	exec := &fakeExec{}
	s, store, now := harness(t, exec)
	queue(t, store, "a", *now, shellSpec)

	s.Tick(context.Background())
	waitFor(t, store, "a", state.StatusSucceeded)

	got, _ := store.Get("a")
	if got.Attempts != 1 || got.ExitCode != 0 {
		t.Errorf("task = %+v", got)
	}
}

func TestANonZeroExitIsAFailureNotAnError(t *testing.T) {
	exec := &fakeExec{code: 3}
	s, store, now := harness(t, exec)
	queue(t, store, "a", *now, shellSpec)
	s.Tick(context.Background())
	got := waitFor(t, store, "a", state.StatusFailed)
	if got.ExitCode != 3 {
		t.Errorf("exit = %d, want the task's own 3", got.ExitCode)
	}
}

func TestTasksAreAdmittedInCreationOrder(t *testing.T) {
	exec := &fakeExec{}
	s, store, now := harness(t, exec)
	queue(t, store, "second", now.Add(time.Second), shellSpec)
	queue(t, store, "first", *now, shellSpec)

	s.Tick(context.Background())
	waitFor(t, store, "first", state.StatusSucceeded)
	waitFor(t, store, "second", state.StatusSucceeded)

	if ran := exec.idsRun(); ran[0] != "first" {
		t.Errorf("ran %v, want the older task first", ran)
	}
}

func TestMaxVMsCapsConcurrency(t *testing.T) {
	exec := &fakeExec{hold: make(chan struct{}), started: make(chan string, 8)}
	s, store, now := harness(t, exec)
	for i, id := range []string{"a", "b", "c"} {
		queue(t, store, id, now.Add(time.Duration(i)*time.Second), shellSpec)
	}
	s.Tick(context.Background())
	<-exec.started
	<-exec.started

	if got := s.RunningCount(); got != 2 {
		t.Fatalf("running = %d, want 2", got)
	}
	if !strings.Contains(s.StopReason(), "vm slots") {
		t.Errorf("stop reason = %q, want it to explain the queue is not stuck", s.StopReason())
	}
	third, _ := store.Get("c")
	if third.Status == state.StatusRunning {
		t.Error("a third task started despite MaxVMs=2")
	}
	close(exec.hold)
}

func TestATaskThatDoesNotFitBlocksTheQueueRatherThanBeingSkipped(t *testing.T) {
	exec := &fakeExec{}
	s, store, now := harness(t, exec)
	s.probe = func() hostres.Snapshot {
		return hostres.Snapshot{TotalBytes: 16 << 30, AvailableBytes: 4 << 30, CPUs: 8, OK: true}
	}
	queue(t, store, "big", *now, bigSpec(8))
	queue(t, store, "small", now.Add(time.Second), shellSpec)

	s.Tick(context.Background())

	big, _ := store.Get("big")
	if big.Status != state.StatusBlocked || big.BlockedOn != state.BlockedOnMemory {
		t.Errorf("big = %+v, want blocked on memory", big)
	}
	small, _ := store.Get("small")
	if small.Status == state.StatusRunning || small.Status == state.StatusSucceeded {
		t.Error("a small task jumped ahead of a big one, which starves the big one indefinitely")
	}
	if !strings.Contains(s.StopReason(), "GiB free") {
		t.Errorf("stop reason = %q, want it to say why the machine looks idle", s.StopReason())
	}
}

func TestAClaudeTaskIsSkippedForwardRatherThanBlockingTheQueue(t *testing.T) {
	exec := &fakeExec{}
	s, store, now := harness(t, exec)
	s.gate.MarkExhausted()
	queue(t, store, "claude", *now, claudeSpec)
	queue(t, store, "shell", now.Add(time.Second), shellSpec)

	s.Tick(context.Background())

	blocked, _ := store.Get("claude")
	if blocked.Status != state.StatusBlocked || blocked.BlockedOn != state.BlockedOnClaude {
		t.Errorf("claude task = %+v, want blocked on claude", blocked)
	}
	waitFor(t, store, "shell", state.StatusSucceeded)
}

func TestAGateReopeningMakesTheTaskEligibleImmediately(t *testing.T) {
	exec := &fakeExec{}
	s, store, now := harness(t, exec)
	s.gate.MarkExhausted()
	queue(t, store, "claude", *now, claudeSpec)
	s.Tick(context.Background())
	if got, _ := store.Get("claude"); got.Status != state.StatusBlocked {
		t.Fatalf("want blocked, got %v", got.Status)
	}

	s.gate.Reset()
	s.Tick(context.Background())
	waitFor(t, store, "claude", state.StatusSucceeded)
}

func TestUsageExhaustionHoldsTheTaskWithoutFailingIt(t *testing.T) {
	exec := &fakeExec{code: task.RateLimitExitCode}
	s, store, now := harness(t, exec)
	queue(t, store, "claude", *now, claudeSpec)

	s.Tick(context.Background())
	got := waitFor(t, store, "claude", state.StatusBlocked)
	if got.BlockedOn != state.BlockedOnClaude {
		t.Errorf("blocked on %q, want claude", got.BlockedOn)
	}
	if got.FinishedAt != nil {
		t.Error("an exhausted task must not be terminal; it keeps its place in the queue")
	}
	if s.gate.Available() {
		t.Error("the gate should have closed")
	}
}

func TestARestartMarksARunningTaskLostAndNeverRequeuesIt(t *testing.T) {
	exec := &fakeExec{}
	s, store, now := harness(t, exec)
	queue(t, store, "a", *now, shellSpec)
	if _, err := store.Update("a", func(u *state.Task) { u.Status = state.StatusRunning }); err != nil {
		t.Fatal(err)
	}

	s.Recover(context.Background())

	got, _ := store.Get("a")
	if got.Status != state.StatusLost {
		t.Fatalf("status = %q, want lost. Requeueing is the duplicate-pull-request bug: the "+
			"orphaned job keeps running and pushes, and the restarted daemon runs it again", got.Status)
	}
	if got.LostAt == nil || got.LostReason == "" {
		t.Error("a lost task must record when and why, since a human has to decide what to do")
	}

	s.Tick(context.Background())
	if len(exec.idsRun()) != 0 {
		t.Error("a lost task was executed; there must be no path from lost back to queued")
	}
}

func TestRecoverLeavesQueuedWorkAlone(t *testing.T) {
	exec := &fakeExec{}
	s, store, now := harness(t, exec)
	queue(t, store, "a", *now, shellSpec)
	s.Recover(context.Background())
	if got, _ := store.Get("a"); got.Status != state.StatusQueued {
		t.Errorf("status = %q; a task that never started is safe to keep", got.Status)
	}
}

func TestCancelStopsARunningTask(t *testing.T) {
	exec := &fakeExec{hold: make(chan struct{}), started: make(chan string, 4)}
	s, store, now := harness(t, exec)
	queue(t, store, "a", *now, shellSpec)
	s.Tick(context.Background())
	<-exec.started

	if err := s.Cancel("a"); err != nil {
		t.Fatal(err)
	}
	got := waitFor(t, store, "a", state.StatusCancelled)
	if got.FinishedAt == nil {
		t.Error("a cancelled task must be terminal")
	}
	close(exec.hold)
}

func TestCancelWorksOnAQueuedTaskToo(t *testing.T) {
	exec := &fakeExec{}
	s, store, now := harness(t, exec)
	queue(t, store, "a", *now, shellSpec)
	if err := s.Cancel("a"); err != nil {
		t.Fatal(err)
	}
	s.Tick(context.Background())
	if len(exec.idsRun()) != 0 {
		t.Error("a cancelled task must not then be admitted")
	}
}

func TestCancelDoesNotRewriteATerminalTask(t *testing.T) {
	exec := &fakeExec{}
	s, store, now := harness(t, exec)
	queue(t, store, "a", *now, shellSpec)
	s.Tick(context.Background())
	waitFor(t, store, "a", state.StatusSucceeded)

	_ = s.Cancel("a")
	if got, _ := store.Get("a"); got.Status != state.StatusSucceeded {
		t.Errorf("status = %q, want the result preserved", got.Status)
	}
}

func TestWakeIsNonBlockingAndCoalesces(t *testing.T) {
	s, _, _ := harness(t, &fakeExec{})
	for range 100 {
		s.Wake()
	}
}

func waitFor(t *testing.T, store *state.Store, id string, want state.Status) state.Task {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	var last state.Task
	for time.Now().Before(deadline) {
		got, err := store.Get(id)
		if err == nil {
			last = got
			if got.Status == want {
				return got
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("task %s stayed %q, want %q", id, last.Status, want)
	return last
}

func TestExhaustionWaitsForAKnownResetRatherThanPollingBlindly(t *testing.T) {
	exec := &fakeExec{code: task.RateLimitExitCode}
	s, store, now := harness(t, exec)
	queue(t, store, "claude", *now, claudeSpec)

	resets := time.Now().Add(3 * time.Hour)
	logLine := "KRANQ-GATE exhausted resets_at=" + strconv.FormatInt(resets.Unix(), 10) + " window=five_hour\n"
	if err := os.MkdirAll(store.Dir("claude"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(store.LogPath("claude"), []byte(logLine), 0o600); err != nil {
		t.Fatal(err)
	}

	s.Tick(context.Background())
	waitFor(t, store, "claude", state.StatusBlocked)

	remaining := s.gate.State().Remaining
	if remaining < 2*time.Hour {
		t.Errorf("next check in %v, want it to wait for the known reset instead of retrying every "+
			"minute for three hours and burning a VM boot each time", remaining)
	}
	if got := s.gate.State().LastReason; got != "five_hour" {
		t.Errorf("reason = %q, want the window name surfaced", got)
	}
}

func TestCancellationIsNotOverwrittenByTheInterruptedRun(t *testing.T) {
	exec := &fakeExec{hold: make(chan struct{}), started: make(chan string, 1)}
	s, store, now := harness(t, exec)
	queue(t, store, "a", *now, shellSpec)
	s.Tick(context.Background())
	<-exec.started

	if err := s.Cancel("a"); err != nil {
		t.Fatal(err)
	}
	got := waitFor(t, store, "a", state.StatusCancelled)
	time.Sleep(50 * time.Millisecond)

	got, _ = store.Get("a")
	if got.Status != state.StatusCancelled {
		t.Errorf("status = %q, want cancelled to survive; the executor returning ctx.Err() must not "+
			"rewrite a deliberate cancellation as lost", got.Status)
	}
	close(exec.hold)
}

func TestARestartReadoptsAJobThatSurvivedIt(t *testing.T) {
	exec := &fakeExec{adoptIDs: map[string]bool{"a": true}, code: 0}
	s, store, now := harness(t, exec)
	queue(t, store, "a", *now, shellSpec)
	started := now.Add(-time.Minute)
	if _, err := store.Update("a", func(u *state.Task) {
		u.Status = state.StatusRunning
		u.StartedAt = &started
		u.ExecPGID = 4242
		u.Attempts = 1
	}); err != nil {
		t.Fatal(err)
	}

	s.Recover(context.Background())

	got := waitFor(t, store, "a", state.StatusSucceeded)
	if got.ExitCode != 0 {
		t.Fatalf("exit code %d", got.ExitCode)
	}
	if ids := exec.adoptedIDs(); len(ids) != 1 || ids[0] != "a" {
		t.Fatalf("adopted %v; a surviving job must be waited on, not restarted", ids)
	}
	if len(exec.idsRun()) != 0 {
		t.Fatal("a surviving job was started a second time, which is the duplicate-run bug")
	}
	if got.Attempts != 1 {
		t.Fatalf("attempts = %d; re-adopting is not a new attempt", got.Attempts)
	}
}

func TestARestartStillLosesAJobThatDidNotSurvive(t *testing.T) {
	exec := &fakeExec{adoptIDs: map[string]bool{}}
	s, store, now := harness(t, exec)
	queue(t, store, "a", *now, shellSpec)
	if _, err := store.Update("a", func(u *state.Task) {
		u.Status = state.StatusRunning
		u.ExecPGID = 4242
	}); err != nil {
		t.Fatal(err)
	}

	s.Recover(context.Background())

	got, _ := store.Get("a")
	if got.Status != state.StatusLost {
		t.Fatalf("status = %q, want lost", got.Status)
	}
	if len(exec.adoptedIDs()) != 0 {
		t.Fatal("a job that is gone was adopted anyway")
	}
}

// A re-adopted job keeps the deadline it already started against, so a restart
// cannot be used to extend a run indefinitely.
func TestReadoptionDoesNotHandTheJobAFreshTimeout(t *testing.T) {
	exec := &fakeExec{}
	s, _, now := harness(t, exec)
	s.cfg.TaskTimeout = time.Hour

	long := now.Add(-50 * time.Minute)
	if got := s.remaining(state.Task{StartedAt: &long}); got > 11*time.Minute {
		t.Fatalf("remaining = %s; the elapsed time was not deducted", got)
	}
	overdue := now.Add(-90 * time.Minute)
	if got := s.remaining(state.Task{StartedAt: &overdue}); got != time.Minute {
		t.Fatalf("remaining = %s; an overdue job needs a floor, not a negative deadline", got)
	}
	if got := s.remaining(state.Task{}); got != time.Hour {
		t.Fatalf("remaining = %s; a job with no start time gets the full budget", got)
	}
}

// The machine's token must not overwrite one the caller forwarded, or the task
// silently runs as somebody else and spends the wrong usage.
func TestAForwardedClaudeTokenIsNotOverwritten(t *testing.T) {
	env := map[string]string{svc.ClaudeTokenVar: "callers-token"}
	if !ownToken(state.Task{}) {
		t.Error("a task with no token of its own was not treated as using the machine's")
	}
	if ownToken(state.Task{Env: env}) {
		t.Error("a task carrying its own token was treated as spending the machine's")
	}
}
