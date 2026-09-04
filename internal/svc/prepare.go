package svc

import (
	"fmt"
	"time"

	"github.com/effetmonstre/forge/internal/exitcode"
	"github.com/effetmonstre/forge/internal/state"
	"github.com/effetmonstre/forge/internal/task"
)

type Error struct {
	Code int
	Msg  string
}

func (e *Error) Error() string { return e.Msg }

func errf(code int, format string, args ...any) *Error {
	return &Error{Code: code, Msg: fmt.Sprintf(format, args...)}
}

type Capabilities struct {
	HasClaudeToken bool
	TotalMemory    int64
	TotalCPUs      int
}

type SubmitRequest struct {
	SpecYAML []byte
	Repo     string
	Branch   string
	Label    string
	Env      map[string]string
}

func Prepare(req SubmitRequest, caps Capabilities, now time.Time, id string) (state.Task, error) {
	spec, err := task.Parse(req.SpecYAML)
	if err != nil {
		return state.Task{}, errf(exitcode.InvalidSpec, "%v", err)
	}

	repo := firstNonEmpty(req.Repo, spec.Repo)
	if repo == "" {
		return state.Task{}, errf(exitcode.Misconfigured, "no repo: the spec names none and the request supplied none")
	}
	branch := firstNonEmpty(req.Branch, spec.Branch)
	if branch == "" {
		return state.Task{}, errf(exitcode.Misconfigured, "no branch: the spec names none and the request supplied none")
	}
	if spec.NeedsClaude() && !caps.HasClaudeToken {
		return state.Task{}, errf(exitcode.Misconfigured,
			"this task has a claude step but the runner has no CLAUDE_CODE_OAUTH_TOKEN")
	}

	memory := spec.MemoryBytes()
	if caps.TotalMemory > 0 && memory > caps.TotalMemory {
		return state.Task{}, errf(exitcode.Misconfigured,
			"this task asks for %s but the machine has %s in total, so it would never be admitted",
			humanBytes(memory), humanBytes(caps.TotalMemory))
	}
	if caps.TotalCPUs > 0 && spec.Resources.CPUs > caps.TotalCPUs {
		return state.Task{}, errf(exitcode.Misconfigured,
			"this task asks for %d cpus but the machine has %d", spec.Resources.CPUs, caps.TotalCPUs)
	}

	return state.Task{
		ID:          id,
		Name:        spec.Name,
		Repo:        repo,
		Branch:      branch,
		Label:       firstNonEmpty(req.Label, spec.Label, spec.Name),
		Status:      state.StatusQueued,
		NeedsClaude: spec.NeedsClaude(),
		MemoryBytes: memory,
		CPUs:        spec.Resources.CPUs,
		SpecYAML:    string(req.SpecYAML),
		Env:         req.Env,
		CreatedAt:   now,
	}, nil
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

func humanBytes(n int64) string {
	const gib = 1 << 30
	if n >= gib {
		return fmt.Sprintf("%.1fGiB", float64(n)/gib)
	}
	return fmt.Sprintf("%dMiB", n/(1<<20))
}
