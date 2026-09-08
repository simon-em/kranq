package sched

import (
	"context"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/simon-em/kranq/internal/gate"
	"github.com/simon-em/kranq/internal/hostres"
	"github.com/simon-em/kranq/internal/state"
	"github.com/simon-em/kranq/internal/svc"
	"github.com/simon-em/kranq/internal/task"
)

type Executor interface {
	Execute(ctx context.Context, t state.Task, script string, out *os.File) (exitCode int, err error)
	// Adoptable reports whether a task found running at startup still has a
	// job behind it, either still going or finished and waiting to be read.
	Adoptable(t state.Task) bool
	// Adopt waits on that job as Execute would, without starting a new one.
	Adopt(ctx context.Context, t state.Task) (exitCode int, err error)
}

type Config struct {
	MaxVMs         int
	MemoryHeadroom int64
	PollInterval   time.Duration
	TaskTimeout    time.Duration
	ClaudeToken    string
}

type Scheduler struct {
	cfg   Config
	store *state.Store
	gate  *gate.Gate
	exec  Executor
	probe func() hostres.Snapshot
	now   func() time.Time

	mu      sync.Mutex
	running map[string]context.CancelFunc
	wake    chan struct{}
	stopped string
}

func New(cfg Config, store *state.Store, g *gate.Gate, exec Executor) *Scheduler {
	return &Scheduler{
		cfg:     cfg,
		store:   store,
		gate:    g,
		exec:    exec,
		probe:   hostres.Probe,
		now:     time.Now,
		running: map[string]context.CancelFunc{},
		wake:    make(chan struct{}, 1),
	}
}

func (s *Scheduler) Wake() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

func (s *Scheduler) Start(ctx context.Context) {
	s.Recover(ctx)
	go func() {
		ticker := time.NewTicker(s.cfg.PollInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			case <-s.wake:
			}
			s.Tick(ctx)
		}
	}()
}

// A task found running at startup is re-adopted when its job survived, and lost
// otherwise. There is deliberately no path from running back to queued: the job
// may have pushed a branch and opened a pull request already, so re-running it
// is a human decision under a new task id, never something that happens on a
// restart.
func (s *Scheduler) Recover(ctx context.Context) {
	for _, t := range s.store.List() {
		if t.Status != state.StatusRunning {
			continue
		}
		if !s.exec.Adoptable(t) {
			s.markLost(t.ID, "the daemon restarted while this task was running, and its job is gone")
			continue
		}
		s.readopt(ctx, t)
	}
}

func (s *Scheduler) readopt(_ context.Context, t state.Task) {
	runCtx, cancel := context.WithTimeout(context.Background(), s.remaining(t))
	s.mu.Lock()
	s.running[t.ID] = cancel
	s.mu.Unlock()
	go func() {
		defer s.finish(t.ID, cancel)
		s.readoptRun(runCtx, t)
		s.Wake()
	}()
}

// The timeout is measured from when the job actually started, not from now, so
// a restart does not hand a long-running job a fresh full budget.
func (s *Scheduler) remaining(t state.Task) time.Duration {
	if t.StartedAt == nil {
		return s.cfg.TaskTimeout
	}
	left := s.cfg.TaskTimeout - s.now().Sub(*t.StartedAt)
	if left < time.Minute {
		return time.Minute
	}
	return left
}

func (s *Scheduler) markLost(id, reason string) {
	at := s.now()
	_, _ = s.store.Update(id, func(u *state.Task) {
		if u.Terminal() {
			return
		}
		u.Status = state.StatusLost
		u.LostAt = &at
		u.LostReason = reason
		u.FinishedAt = &at
	})
}

func (s *Scheduler) RunningCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.running)
}

func (s *Scheduler) cpusInUse() int {
	s.mu.Lock()
	ids := make([]string, 0, len(s.running))
	for id := range s.running {
		ids = append(ids, id)
	}
	s.mu.Unlock()
	total := 0
	for _, id := range ids {
		if t, err := s.store.Get(id); err == nil {
			total += t.CPUs
		}
	}
	return total
}

func (s *Scheduler) StopReason() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.stopped
}

func (s *Scheduler) setStopReason(reason string) {
	s.mu.Lock()
	s.stopped = reason
	s.mu.Unlock()
}

func (s *Scheduler) Tick(ctx context.Context) {
	snapshot := s.probe()
	cpus := s.cpusInUse()
	s.setStopReason("")

	for _, t := range s.store.List() {
		if !t.Pending() {
			continue
		}
		if s.RunningCount() >= s.cfg.MaxVMs {
			s.setStopReason(fmt.Sprintf("all %d vm slots are in use", s.cfg.MaxVMs))
			return
		}
		// The gate records that *this machine's* token is exhausted. A task
		// that brought its own is not spending that budget, so holding it back
		// would be waiting on something that does not apply to it.
		if t.NeedsClaude && t.Env[svc.ClaudeTokenVar] == "" && !s.gate.Available() {
			s.block(t, state.BlockedOnClaude)
			continue
		}
		if !snapshot.Fits(t.MemoryBytes, s.cfg.MemoryHeadroom) {
			s.block(t, state.BlockedOnMemory)
			s.setStopReason(fmt.Sprintf("%s needs %.1fGiB, %.1fGiB free",
				t.ID, gib(t.MemoryBytes), gib(snapshot.AvailableBytes)))
			return
		}
		if !snapshot.FitsCPU(t.CPUs, cpus) {
			s.block(t, state.BlockedOnSlots)
			s.setStopReason(fmt.Sprintf("%s needs %d cpus, %d of %d in use", t.ID, t.CPUs, cpus, snapshot.CPUs))
			return
		}
		s.launch(ctx, t)
		snapshot.AvailableBytes -= t.MemoryBytes
		cpus += t.CPUs
	}
}

func (s *Scheduler) block(t state.Task, on state.Blocker) {
	if t.Status == state.StatusBlocked && t.BlockedOn == on {
		return
	}
	_, _ = s.store.Update(t.ID, func(u *state.Task) {
		u.Status = state.StatusBlocked
		u.BlockedOn = on
	})
}

// A job's deadline is its own, not the daemon's. Tying it to the daemon context
// would mean every shutdown killed the work in flight, which is the opposite of
// what re-adoption is for: a restart should leave jobs running and pick them up
// again. Only a cancel or the task's own timeout ends a job early.
func (s *Scheduler) launch(_ context.Context, t state.Task) {
	runCtx, cancel := context.WithTimeout(context.Background(), s.cfg.TaskTimeout)
	s.mu.Lock()
	s.running[t.ID] = cancel
	s.mu.Unlock()

	started := s.now()
	updated, err := s.store.Update(t.ID, func(u *state.Task) {
		u.Status = state.StatusRunning
		u.BlockedOn = state.BlockedOnNothing
		u.StartedAt = &started
		u.Attempts++
	})
	if err != nil {
		s.finish(t.ID, cancel)
		return
	}

	go func() {
		defer s.finish(t.ID, cancel)
		s.execute(runCtx, updated)
		s.Wake()
	}()
}

func (s *Scheduler) finish(id string, cancel context.CancelFunc) {
	cancel()
	s.mu.Lock()
	delete(s.running, id)
	s.mu.Unlock()
}

func (s *Scheduler) execute(ctx context.Context, t state.Task) {
	spec, err := task.Parse([]byte(t.SpecYAML))
	if err != nil {
		s.fail(t.ID, fmt.Sprintf("invalid spec: %v", err))
		return
	}

	env := map[string]string{}
	for k, v := range t.Env {
		env[k] = v
	}
	// Only when the caller brought none. A pipeline that forwards its own token
	// is spending its own usage, and overwriting it with the machine's would
	// silently run the task as somebody else.
	if spec.NeedsClaude() && env[svc.ClaudeTokenVar] == "" && s.cfg.ClaudeToken != "" {
		env[svc.ClaudeTokenVar] = s.cfg.ClaudeToken
	}

	logFile, err := s.openLog(t.ID)
	if err != nil {
		s.fail(t.ID, err.Error())
		return
	}
	defer logFile.Close()
	fmt.Fprintf(logFile, "===== attempt %d at %s =====\n", t.Attempts, s.now().Format(time.RFC3339))

	script := task.BuildScript(spec, env)
	s.settle(ctx, t, logFile, func() (int, error) {
		return s.exec.Execute(ctx, t, script, logFile)
	})
}

func (s *Scheduler) readoptRun(ctx context.Context, t state.Task) {
	logFile, err := s.openLog(t.ID)
	if err != nil {
		s.markLost(t.ID, fmt.Sprintf("re-adopted but its log could not be opened: %v", err))
		return
	}
	defer logFile.Close()
	fmt.Fprintf(logFile, "===== re-adopted by the daemon at %s =====\n", s.now().Format(time.RFC3339))
	s.settle(ctx, t, logFile, func() (int, error) { return s.exec.Adopt(ctx, t) })
}

func (s *Scheduler) openLog(id string) (*os.File, error) {
	return os.OpenFile(s.store.LogPath(id), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
}

// Everything that happens to a task once its job has an outcome, shared by a
// fresh run and a re-adopted one so the two cannot drift apart.
func (s *Scheduler) settle(ctx context.Context, t state.Task, logFile *os.File, run func() (int, error)) {
	code, runErr := run()
	if runErr != nil {
		fmt.Fprintf(logFile, "runner error: %v\n", runErr)
		if ctx.Err() != nil {
			s.markLost(t.ID, fmt.Sprintf("the run was interrupted: %v", ctx.Err()))
			return
		}
		s.fail(t.ID, runErr.Error())
		return
	}

	// Only the machine's own token has a gate here. A run that brought its own
	// exhausted somebody else's budget, and closing this gate would hold back
	// every task that uses the machine's for a window it is not in.
	if code == task.RateLimitExitCode && t.NeedsClaude && ownToken(t) {
		resetsAt, window, _ := gate.ParseExhaustion(s.tailLog(t.ID))
		until := s.gate.MarkExhaustedUntil(resetsAt, window)
		if resetsAt.IsZero() {
			fmt.Fprintf(logFile, "claude usage exhausted; checking again at %s\n", until.Format(time.RFC3339))
		} else {
			fmt.Fprintf(logFile, "claude usage exhausted; the %s window resets at %s\n",
				window, until.Format(time.RFC3339))
		}
		_, _ = s.store.Update(t.ID, func(u *state.Task) {
			u.Status = state.StatusBlocked
			u.BlockedOn = state.BlockedOnClaude
			u.StartedAt = nil
			u.ExecPGID = 0
			u.Error = "waiting for claude usage"
		})
		return
	}
	if t.NeedsClaude && code == 0 && ownToken(t) {
		s.gate.MarkHealthy()
	}

	finished := s.now()
	status := state.StatusSucceeded
	if code != 0 {
		status = state.StatusFailed
	}
	_, _ = s.store.Update(t.ID, func(u *state.Task) {
		if u.Terminal() {
			return
		}
		u.Status = status
		u.ExitCode = code
		u.FinishedAt = &finished
		u.ExecPGID = 0
		u.Error = ""
	})
}

func (s *Scheduler) fail(id, msg string) {
	finished := s.now()
	_, _ = s.store.Update(id, func(u *state.Task) {
		if u.Terminal() {
			return
		}
		u.Status = state.StatusFailed
		u.Error = msg
		u.ExitCode = -1
		u.FinishedAt = &finished
	})
}

func (s *Scheduler) Wait(ctx context.Context) {
	for s.RunningCount() > 0 {
		select {
		case <-ctx.Done():
			return
		case <-time.After(20 * time.Millisecond):
		}
	}
}

func (s *Scheduler) Cancel(id string) error {
	s.mu.Lock()
	cancel, running := s.running[id]
	s.mu.Unlock()
	if running {
		cancel()
	}
	finished := s.now()
	_, err := s.store.Update(id, func(u *state.Task) {
		if u.Terminal() {
			return
		}
		u.Status = state.StatusCancelled
		u.FinishedAt = &finished
	})
	return err
}

// Whether the run spent the machine's claude usage rather than the caller's.
func ownToken(t state.Task) bool { return t.Env[svc.ClaudeTokenVar] == "" }

func (s *Scheduler) tailLog(id string) string {
	data, err := os.ReadFile(s.store.LogPath(id))
	if err != nil {
		return ""
	}
	const tail = 64 << 10
	if len(data) > tail {
		data = data[len(data)-tail:]
	}
	return string(data)
}

func gib(n int64) float64 { return float64(n) / (1 << 30) }
