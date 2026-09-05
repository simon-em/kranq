package state

import "time"

type Status string

const (
	StatusQueued    Status = "queued"
	StatusBlocked   Status = "blocked"
	StatusRunning   Status = "running"
	StatusSucceeded Status = "succeeded"
	StatusFailed    Status = "failed"
	StatusCancelled Status = "cancelled"
	StatusLost      Status = "lost"
)

type Blocker string

const (
	BlockedOnNothing Blocker = ""
	BlockedOnClaude  Blocker = "claude"
	BlockedOnMemory  Blocker = "memory"
	BlockedOnSlots   Blocker = "slots"
)

type Task struct {
	ID           string            `json:"id"`
	Name         string            `json:"name"`
	Repo         string            `json:"repo"`
	Branch       string            `json:"branch"`
	Label        string            `json:"label"`
	Status       Status            `json:"status"`
	BlockedOn    Blocker           `json:"blocked_on,omitempty"`
	ExitCode     int               `json:"exit_code"`
	Error        string            `json:"error,omitempty"`
	Attempts     int               `json:"attempts"`
	NeedsClaude  bool              `json:"needs_claude"`
	MemoryBytes  int64             `json:"memory_bytes"`
	CPUs         int               `json:"cpus"`
	SpecYAML     string            `json:"spec_yaml"`
	Env          map[string]string `json:"-"`
	VMName       string            `json:"vm_name,omitempty"`
	VMKept       bool              `json:"vm_kept,omitempty"`
	Keep         string            `json:"keep,omitempty"`
	SourceCommit string            `json:"source_commit,omitempty"`
	FenceRef     string            `json:"fence_ref,omitempty"`
	FenceHeld    bool              `json:"fence_held,omitempty"`
	FenceKind    string            `json:"fence_kind,omitempty"`
	FenceBranch  string            `json:"fence_branch,omitempty"`
	ExecPGID     int               `json:"exec_pgid,omitempty"`
	CreatedAt    time.Time         `json:"created_at"`
	StartedAt    *time.Time        `json:"started_at,omitempty"`
	FinishedAt   *time.Time        `json:"finished_at,omitempty"`
	LostAt       *time.Time        `json:"lost_at,omitempty"`
	LostReason   string            `json:"lost_reason,omitempty"`
}

func (t Task) Terminal() bool {
	switch t.Status {
	case StatusSucceeded, StatusFailed, StatusCancelled, StatusLost:
		return true
	}
	return false
}

func (t Task) Pending() bool {
	return t.Status == StatusQueued || t.Status == StatusBlocked
}
