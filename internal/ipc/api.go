package ipc

import (
	"time"

	"github.com/simon-em/kranq/internal/gate"
	"github.com/simon-em/kranq/internal/hostres"
	"github.com/simon-em/kranq/internal/state"
)

type SubmitRequest struct {
	Spec       string            `json:"spec"`
	Repo       string            `json:"repo,omitempty"`
	SourceRepo string            `json:"source_repo,omitempty"`
	Branch     string            `json:"branch,omitempty"`
	Label      string            `json:"label,omitempty"`
	Env        map[string]string `json:"env,omitempty"`
	Keep       string            `json:"keep,omitempty"`
	Kranqfile  string            `json:"kranqfile,omitempty"`
	// Set when the source arrived by git push rather than being cloned from
	// the git host. The commit is the exact object to build, not a branch tip
	// that can move while the task waits in the queue.
	SourceCommit string `json:"source_commit,omitempty"`
}

type Status struct {
	Version    string           `json:"version"`
	PID        int              `json:"pid"`
	Uptime     time.Duration    `json:"uptime"`
	Queued     int              `json:"queued"`
	Blocked    int              `json:"blocked"`
	Running    int              `json:"running"`
	Lost       int              `json:"lost"`
	MaxVMs     int              `json:"max_vms"`
	StopReason string           `json:"stop_reason,omitempty"`
	Resources  hostres.Snapshot `json:"resources"`
	Claude     gate.State       `json:"claude"`
}

type ErrorBody struct {
	Error string `json:"error"`
	Code  int    `json:"code"`
}

type TaskList struct {
	Tasks []state.Task `json:"tasks"`
}
