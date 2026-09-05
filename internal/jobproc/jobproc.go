package jobproc

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

const (
	ResultFile = "result.json"
	ScriptFile = "script.sh"
)

// What the child records for the daemon to read. It is the whole handover, so a
// daemon that was not running when the job finished still learns what happened.
type Result struct {
	ExitCode   int       `json:"exit_code"`
	Error      string    `json:"error,omitempty"`
	VMName     string    `json:"vm_name,omitempty"`
	Kept       bool      `json:"kept,omitempty"`
	Artifacts  bool      `json:"artifacts,omitempty"`
	FenceRef   string    `json:"fence_ref,omitempty"`
	FenceHeld  bool      `json:"fence_held,omitempty"`
	FinishedAt time.Time `json:"finished_at"`
}

func ResultPath(dir string) string { return filepath.Join(dir, ResultFile) }
func ScriptPath(dir string) string { return filepath.Join(dir, ScriptFile) }

func WriteResult(dir string, r Result) error {
	if r.FinishedAt.IsZero() {
		r.FinishedAt = time.Now()
	}
	body, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	tmp := ResultPath(dir) + ".tmp"
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.Write(append(body, '\n')); err != nil {
		f.Close()
		return err
	}
	// Synced before the rename, because the reader of this file is a process
	// that may only start after the writer's machine has crashed.
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, ResultPath(dir))
}

func ReadResult(dir string) (Result, bool) {
	var r Result
	body, err := os.ReadFile(ResultPath(dir))
	if err != nil {
		return r, false
	}
	if err := json.Unmarshal(body, &r); err != nil {
		return r, false
	}
	return r, true
}

func ClearResult(dir string) error {
	err := os.Remove(ResultPath(dir))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

// Alive answers whether pid is still this task's job, not merely whether some
// process holds that pid. A pid alone is not an identity: the number is reused,
// and adopting a stranger would be worse than losing the task.
func Alive(pid int, taskID string) bool {
	if pid <= 0 {
		return false
	}
	if err := syscall.Kill(pid, 0); err != nil {
		return false
	}
	out, err := exec.Command("ps", "-p", strconv.Itoa(pid), "-o", "command=").Output()
	if err != nil {
		return false
	}
	command := string(out)
	return strings.Contains(command, ExecVerb) && strings.Contains(command, taskID)
}

const ExecVerb = "exec"

// Signals the whole group: the job sits in a foreground limactl shell, and
// signalling only the leader leaves the VM running until the job ends on its
// own, which permanently costs a slot.
func Terminate(pgid int) error {
	if pgid <= 0 {
		return fmt.Errorf("no process group to signal")
	}
	return syscall.Kill(-pgid, syscall.SIGTERM)
}

func Kill(pgid int) error {
	if pgid <= 0 {
		return fmt.Errorf("no process group to signal")
	}
	return syscall.Kill(-pgid, syscall.SIGKILL)
}
