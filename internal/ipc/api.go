package ipc

import (
	"time"

	"github.com/effetmonstre/forge/internal/gate"
	"github.com/effetmonstre/forge/internal/hostres"
	"github.com/effetmonstre/forge/internal/state"
)

type SubmitRequest struct {
	Spec   string            `json:"spec"`
	Repo   string            `json:"repo,omitempty"`
	Branch string            `json:"branch,omitempty"`
	Label  string            `json:"label,omitempty"`
	Env    map[string]string `json:"env,omitempty"`
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
