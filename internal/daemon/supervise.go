package daemon

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"syscall"
	"time"

	"github.com/simon-em/kranq/internal/jobproc"
	"github.com/simon-em/kranq/internal/state"
)

// Supervisor runs each job as `kranq exec <id>` in its own process group, so a
// daemon restart neither kills the job nor loses its result. It never does the
// work itself; everything it knows about a job comes from the store and the
// result file the child writes.
type Supervisor struct {
	Binary  string
	Home    string
	TaskDir func(id string) string
	Poll    time.Duration
	Grace   time.Duration

	RecordPGID   func(taskID string, pgid int)
	RecordResult func(taskID string, res jobproc.Result)
}

func (s *Supervisor) poll() time.Duration {
	if s.Poll > 0 {
		return s.Poll
	}
	return time.Second
}

func (s *Supervisor) Execute(ctx context.Context, t state.Task, script string, out *os.File) (int, error) {
	dir := s.TaskDir(t.ID)
	// A result left by an earlier attempt would be read as this one's.
	if err := jobproc.ClearResult(dir); err != nil {
		return -1, err
	}
	if err := os.WriteFile(jobproc.ScriptPath(dir), []byte(script), 0o600); err != nil {
		return -1, err
	}

	cmd := exec.Command(s.Binary, "exec", t.ID)
	cmd.Dir = dir
	cmd.Stdout = out
	cmd.Stderr = out
	cmd.Env = append(os.Environ(), "KRANQ_HOME="+s.Home)
	// Its own group, so a SIGKILL of the daemon orphans the job rather than
	// killing it, and so cancelling can signal the group rather than the leader.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		return -1, fmt.Errorf("starting the job process: %w", err)
	}
	pgid := cmd.Process.Pid
	if s.RecordPGID != nil {
		s.RecordPGID(t.ID, pgid)
	}
	// Reaped in the background so the child never becomes a zombie; the result
	// itself comes from the file, which works whether or not we are its parent.
	go cmd.Wait()

	return s.await(ctx, t.ID, pgid, dir)
}

func (s *Supervisor) Adoptable(t state.Task) bool {
	if _, ok := jobproc.ReadResult(s.TaskDir(t.ID)); ok {
		return true
	}
	return jobproc.Alive(t.ExecPGID, t.ID)
}

func (s *Supervisor) Adopt(ctx context.Context, t state.Task) (int, error) {
	return s.await(ctx, t.ID, t.ExecPGID, s.TaskDir(t.ID))
}

// await polls rather than waiting on the process, because an adopted job is not
// this process's child and cannot be waited on at all. One code path serves
// both cases.
func (s *Supervisor) await(ctx context.Context, id string, pgid int, dir string) (int, error) {
	ticker := time.NewTicker(s.poll())
	defer ticker.Stop()
	for {
		if res, ok := jobproc.ReadResult(dir); ok {
			return s.report(id, res)
		}
		if !jobproc.Alive(pgid, id) {
			// One last look: the job may have written its result in the window
			// between the read above and the process exiting.
			if res, ok := jobproc.ReadResult(dir); ok {
				return s.report(id, res)
			}
			return -1, fmt.Errorf("the job process for %s is gone and left no result", id)
		}
		select {
		case <-ctx.Done():
			s.teardown(pgid, id)
			return -1, ctx.Err()
		case <-ticker.C:
		}
	}
}

// SIGTERM gives the job a chance to destroy its VM, which is the whole reason
// not to lead with SIGKILL. But a job that ignores it would keep the VM forever,
// and a leaked VM permanently costs a slot, so the signal escalates.
func (s *Supervisor) teardown(pgid int, id string) {
	if err := jobproc.Terminate(pgid); err != nil {
		return
	}
	deadline := time.Now().Add(s.grace())
	for time.Now().Before(deadline) {
		if !jobproc.Alive(pgid, id) {
			return
		}
		time.Sleep(s.poll())
	}
	_ = jobproc.Kill(pgid)
}

func (s *Supervisor) grace() time.Duration {
	if s.Grace > 0 {
		return s.Grace
	}
	return 30 * time.Second
}

func (s *Supervisor) report(id string, res jobproc.Result) (int, error) {
	if s.RecordResult != nil {
		s.RecordResult(id, res)
	}
	if res.Error != "" {
		return res.ExitCode, fmt.Errorf("%s", res.Error)
	}
	return res.ExitCode, nil
}

func (s *Supervisor) Stop(t state.Task) error {
	if !jobproc.Alive(t.ExecPGID, t.ID) {
		return nil
	}
	return jobproc.Terminate(t.ExecPGID)
}
