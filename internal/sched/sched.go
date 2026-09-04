package sched

import (
	"context"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/effetmonstre/forge/internal/gate"
	"github.com/effetmonstre/forge/internal/hostres"
	"github.com/effetmonstre/forge/internal/state"
	"github.com/effetmonstre/forge/internal/task"
)

type Executor interface {
	Execute(ctx context.Context, t state.Task, script string, out *os.File) (exitCode int, err error)
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
	s.Recover()
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

func (s *Scheduler) Recover() {
	for _, t := range s.store.List() {
		if t.Status != state.StatusRunning {
			continue
		}
		s.markLost(t.ID, "the daemon restarted while this task was running, and its executor is gone")
	}
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
		if t.NeedsClaude && !s.gate.Available() {
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

func (s *Scheduler) launch(ctx context.Context, t state.Task) {
	runCtx, cancel := context.WithTimeout(ctx, s.cfg.TaskTimeout)
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
	if spec.NeedsClaude() && s.cfg.ClaudeToken != "" {
		env["CLAUDE_CODE_OAUTH_TOKEN"] = s.cfg.ClaudeToken
	}

	logFile, err := os.OpenFile(s.store.LogPath(t.ID), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		s.fail(t.ID, err.Error())
		return
	}
	defer logFile.Close()
	fmt.Fprintf(logFile, "===== attempt %d at %s =====\n", t.Attempts, s.now().Format(time.RFC3339))

	code, runErr := s.exec.Execute(ctx, t, task.BuildScript(spec, env), logFile)
	if runErr != nil {
		fmt.Fprintf(logFile, "runner error: %v\n", runErr)
		if ctx.Err() != nil {
			s.markLost(t.ID, fmt.Sprintf("the run was interrupted: %v", ctx.Err()))
			return
		}
		s.fail(t.ID, runErr.Error())
		return
	}

	if code == task.RateLimitExitCode && t.NeedsClaude {
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
			u.Error = "waiting for claude usage"
		})
		return
	}
	if t.NeedsClaude && code == 0 {
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
