package daemon

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"time"

	"github.com/effetmonstre/forge/internal/gate"
	"github.com/effetmonstre/forge/internal/hostres"
	"github.com/effetmonstre/forge/internal/ipc"
	"github.com/effetmonstre/forge/internal/jobproc"
	"github.com/effetmonstre/forge/internal/sched"
	"github.com/effetmonstre/forge/internal/sockpath"
	"github.com/effetmonstre/forge/internal/state"
	"github.com/effetmonstre/forge/internal/svc"
)

type Config struct {
	Home           string
	Version        string
	MaxVMs         int
	MemoryHeadroom int64
	PollInterval   time.Duration
	TaskTimeout    time.Duration
	Retention      time.Duration
	ClaudeRetry    time.Duration
	ClaudeToken    string
	GitRemote      string
	LimaHome       string
	HTTPAddr       string
	// A push to a name nothing has used yet makes the repository, which is what
	// keeps the simple case to one command. Turn it off on a machine that should
	// only ever accept repositories somebody set up deliberately.
	AutoCreateRepos bool
	// Lima is fetched on first use rather than by a separate install step, so
	// the command somebody ran is the command that happens. Turn it off on a
	// machine that should never reach the network on its own.
	AutoInstallDeps bool
}

func (c Config) SocketPath() string { return sockpath.For(filepath.Join(c.Home, "forge.sock")) }
func (c Config) LockPath() string   { return filepath.Join(c.Home, "forge.pid") }
func (c Config) TasksDir() string   { return filepath.Join(c.Home, "tasks") }
func (c Config) GatePath() string   { return filepath.Join(c.Home, "gate.json") }

func (c Config) withDefaults() Config {
	if c.MaxVMs == 0 {
		c.MaxVMs = 2
	}
	if c.MemoryHeadroom == 0 {
		c.MemoryHeadroom = 2 << 30
	}
	if c.PollInterval == 0 {
		c.PollInterval = 5 * time.Second
	}
	if c.TaskTimeout == 0 {
		c.TaskTimeout = 4 * time.Hour
	}
	if c.Retention == 0 {
		c.Retention = 7 * 24 * time.Hour
	}
	if c.ClaudeRetry == 0 {
		c.ClaudeRetry = gate.DefaultRetry
	}
	return c
}

type Daemon struct {
	cfg     Config
	store   *state.Store
	gate    *gate.Gate
	sched   *sched.Scheduler
	started time.Time
}

func New(cfg Config, exec sched.Executor) (*Daemon, error) {
	cfg = cfg.withDefaults()
	store, err := state.Open(cfg.TasksDir())
	if err != nil {
		return nil, err
	}
	g := gate.New(cfg.GatePath(), cfg.ClaudeToken, cfg.ClaudeRetry)
	s := sched.New(sched.Config{
		MaxVMs:         cfg.MaxVMs,
		MemoryHeadroom: cfg.MemoryHeadroom,
		PollInterval:   cfg.PollInterval,
		TaskTimeout:    cfg.TaskTimeout,
		ClaudeToken:    cfg.ClaudeToken,
	}, store, g, exec)
	return &Daemon{cfg: cfg, store: store, gate: g, sched: s, started: time.Now()}, nil
}

func (d *Daemon) Run(ctx context.Context) error {
	release, err := Lock(d.cfg.LockPath())
	if err != nil {
		return err
	}
	defer release()

	listener, err := ipc.Listen(d.cfg.SocketPath())
	if err != nil {
		return err
	}
	defer os.Remove(d.cfg.SocketPath())

	d.sched.Start(ctx)
	d.serveGit(ctx)
	go d.prune(ctx)
	err = ipc.Serve(ctx, listener, ipc.NewServer(d).Handler())
	d.drain()
	return err
}

// Shutdown does not wait for running jobs. It used to, back when a restart lost
// them; now each job is its own process and the daemon that comes back
// re-adopts it, so waiting would be waiting for something that is deliberately
// being left alive. Worse, a job's context is no longer tied to the daemon's,
// so the wait could only ever time out, and the old process would sit on the
// lock for the whole grace period while the new one failed to start.
func (d *Daemon) drain() {
	if n := d.sched.RunningCount(); n > 0 {
		log.Printf("leaving %d job(s) running; they are re-adopted on the next start", n)
	}
}

func (d *Daemon) prune(ctx context.Context) {
	ticker := time.NewTicker(time.Hour)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			d.store.Prune(d.cfg.Retention, time.Now())
		}
	}
}

func (d *Daemon) Submit(req ipc.SubmitRequest) (state.Task, error) {
	snapshot := hostres.Probe()
	t, err := svc.Prepare(svc.SubmitRequest{
		SpecYAML:     []byte(req.Spec),
		Repo:         req.Repo,
		Branch:       req.Branch,
		Label:        req.Label,
		Env:          req.Env,
		Keep:         req.Keep,
		SourceCommit: req.SourceCommit,
	}, svc.Capabilities{
		HasClaudeToken: d.gate.Present(),
		TotalMemory:    snapshot.TotalBytes,
		TotalCPUs:      snapshot.CPUs,
	}, time.Now(), state.NewID(time.Now()))
	if err != nil {
		return t, err
	}
	if err := d.store.Create(t); err != nil {
		return t, err
	}
	d.sched.Wake()
	return t, nil
}

func (d *Daemon) RecordPGID(taskID string, pgid int) {
	_, _ = d.store.Update(taskID, func(u *state.Task) {
		u.ExecPGID = pgid
	})
}

// Everything the daemon learns about the run itself comes from here, because
// the run happened in another process.
func (d *Daemon) RecordResult(taskID string, res jobproc.Result) {
	_, _ = d.store.Update(taskID, func(u *state.Task) {
		u.VMName = res.VMName
		u.VMKept = res.Kept
		u.FenceRef = res.FenceRef
		u.FenceHeld = res.FenceHeld
	})
}

func (d *Daemon) List() []state.Task                { return d.store.List() }
func (d *Daemon) Get(id string) (state.Task, error) { return d.store.Get(id) }
func (d *Daemon) Cancel(id string) error            { return d.sched.Cancel(id) }
func (d *Daemon) LogPath(id string) string          { return d.store.LogPath(id) }
func (d *Daemon) TaskDir(id string) string          { return d.store.Dir(id) }
func (d *Daemon) ArtifactsDir(id string) string     { return d.store.ArtifactsDir(id) }

func (d *Daemon) Status() ipc.Status {
	s := ipc.Status{
		Version:    d.cfg.Version,
		PID:        os.Getpid(),
		Uptime:     time.Since(d.started).Truncate(time.Second),
		MaxVMs:     d.cfg.MaxVMs,
		StopReason: d.sched.StopReason(),
		Resources:  hostres.Probe(),
		Claude:     d.gate.State(),
	}
	for _, t := range d.store.List() {
		switch t.Status {
		case state.StatusQueued:
			s.Queued++
		case state.StatusBlocked:
			s.Blocked++
		case state.StatusRunning:
			s.Running++
		case state.StatusLost:
			s.Lost++
		}
	}
	return s
}

func Home() string {
	if v := os.Getenv("FORGE_HOME"); v != "" {
		return v
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ".forge"
	}
	return filepath.Join(home, ".forge")
}

func InFlight(s ipc.Status) int { return s.Queued + s.Blocked + s.Running }

func Describe(s ipc.Status) string {
	return fmt.Sprintf("queued=%d blocked=%d running=%d lost=%d", s.Queued, s.Blocked, s.Running, s.Lost)
}
