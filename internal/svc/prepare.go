package svc

import (
	"fmt"
	"time"

	"github.com/simon-em/kranq/internal/exitcode"
	"github.com/simon-em/kranq/internal/state"
	"github.com/simon-em/kranq/internal/task"
)

type Error struct {
	Code int
	Msg  string
}

func (e *Error) Error() string { return e.Msg }

func errf(code int, format string, args ...any) *Error {
	return &Error{Code: code, Msg: fmt.Sprintf(format, args...)}
}

// ClaudeTokenVar is the one name a task may bring its own copy of, because it
// is the one credential kranq itself injects.
const ClaudeTokenVar = "CLAUDE_CODE_OAUTH_TOKEN"

type Capabilities struct {
	HasClaudeToken bool
	TotalMemory    int64
	TotalCPUs      int
}

type SubmitRequest struct {
	SpecYAML     []byte
	Repo         string
	SourceRepo   string
	Branch       string
	Label        string
	Env          map[string]string
	Keep         string
	SourceCommit string
	Kranqfile    string
}

func Prepare(req SubmitRequest, caps Capabilities, now time.Time, id string) (state.Task, error) {
	spec, err := task.Parse(req.SpecYAML)
	if err != nil {
		return state.Task{}, errf(exitcode.InvalidSpec, "%v", err)
	}

	// A repository name is what the git host is asked for, so it is needed only
	// by a task that goes there: one holding a fence, and one with no pushed
	// source, which has to clone. Everything else already has the code and is
	// identified by the commit it arrived as -- naming it would be a label
	// nobody chose, which is what "kranq" was when every codebase shared one
	// repository.
	repo := firstNonEmpty(req.Repo, spec.Repo)
	branch := firstNonEmpty(req.Branch, spec.Branch)
	if why := reachesGitHost(spec, req); why != "" {
		if repo == "" {
			return state.Task{}, errf(exitcode.Misconfigured,
				"no repo: this task %s, so it needs a repository name. "+
					"Push with -o repo=<name>, or name one in the spec", why)
		}
		if branch == "" {
			return state.Task{}, errf(exitcode.Misconfigured,
				"no branch: this task %s, so it needs a branch. "+
					"Push with -o branch=<name>, or name one in the spec", why)
		}
	}
	// A forwarded token counts. The caller may be the only one that has one --
	// a pipeline holds its own credentials and the build machine need hold
	// none -- and refusing it because the *machine* has nothing was checking
	// the wrong place.
	if spec.NeedsClaude() && !caps.HasClaudeToken && req.Env[ClaudeTokenVar] == "" {
		return state.Task{}, errf(exitcode.Misconfigured,
			"this task has a claude step and neither the runner nor this push "+
				"has a CLAUDE_CODE_OAUTH_TOKEN")
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
		ID:           id,
		Name:         spec.Name,
		Repo:         repo,
		SourceRepo:   req.SourceRepo,
		Branch:       branch,
		Label:        firstNonEmpty(req.Label, spec.Label, spec.Name),
		Status:       state.StatusQueued,
		NeedsClaude:  spec.NeedsClaude(),
		MemoryBytes:  memory,
		CPUs:         spec.Resources.CPUs,
		SpecYAML:     string(req.SpecYAML),
		Kranqfile:    firstNonEmpty(req.Kranqfile, spec.Kranqfile),
		Env:          req.Env,
		Keep:         req.Keep,
		SourceCommit: req.SourceCommit,
		FenceKind:    fenceKind(spec),
		FenceBranch:  spec.FenceBranch(branch),
		CreatedAt:    now,
	}, nil
}

// An empty kind means the task declared no effects and runs unfenced.
// The two things that turn a name into an address.
func reachesGitHost(spec task.Spec, req SubmitRequest) string {
	if spec.Fenced() {
		return "declares an effect, which is fenced at the git host"
	}
	if req.SourceCommit == "" {
		return "arrived with no commit, so it has to clone its source"
	}
	return ""
}

func fenceKind(spec task.Spec) string {
	if !spec.Fenced() {
		return ""
	}
	return spec.FenceKind()
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
