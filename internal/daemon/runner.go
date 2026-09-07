package daemon

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/simon-em/kranq/internal/fence"
	"github.com/simon-em/kranq/internal/jobproc"
	"github.com/simon-em/kranq/internal/run"
	"github.com/simon-em/kranq/internal/sshagent"
	"github.com/simon-em/kranq/internal/state"
	"github.com/simon-em/kranq/internal/task"
)

type Runner struct {
	Engine       *run.Engine
	RemoteBase   string
	ArtifactsDir func(id string) string
	AgentRoot    string
	FenceDir     string
	Node         string
	SourceRepos  string
}

func (r *Runner) fence(t state.Task) *run.FencePlan {
	if t.FenceKind == "" || r.FenceDir == "" {
		return nil
	}
	return &run.FencePlan{
		Dir:  r.FenceDir,
		Node: r.Node,
		Scope: fence.Scope{
			Kind: t.FenceKind, Repo: t.Repo, Branch: t.FenceBranch,
		},
	}
}

// A pushed source needs no credential and no network: the code is already here,
// so the host reads the Kranqfile straight out of it and the VM gets a bare
// repository holding exactly the one commit.
func (r *Runner) source(ctx context.Context, t state.Task, work, checkout string, out *os.File) (run.Source, error) {
	if t.SourceCommit == "" || r.SourceRepos == "" {
		return run.Source{}, nil
	}
	shared := r.sharedRepo(sourceRepo(t))
	staged := run.StageDir(work)
	fmt.Fprintf(out, "using the pushed source at %s\n", short(t.SourceCommit))
	branch, err := run.Stage(ctx, run.Source{Bare: shared, Commit: t.SourceCommit}, t.ID, staged, nil)
	if err != nil {
		return run.Source{}, err
	}
	if err := run.CheckoutPushed(ctx, shared, branch, checkout, nil); err != nil {
		return run.Source{}, err
	}
	return run.Source{Bare: staged, Commit: t.SourceCommit, Branch: branch}, nil
}

func (r *Runner) sharedRepo(repo string) string {
	if r.SourceRepos == "" || repo == "" {
		return ""
	}
	return filepath.Join(r.SourceRepos, repo+".git")
}

// Where the objects are, which is not necessarily what the code is called.
// Older task records predate the distinction and carry only the one name.
func sourceRepo(t state.Task) string {
	if t.SourceRepo != "" {
		return t.SourceRepo
	}
	return t.Repo
}

func short(sha string) string {
	if len(sha) > 12 {
		return sha[:12]
	}
	return sha
}

// Run does the whole job in this process: checkout, VM, artifacts, fence. It is
// what `kranq exec` calls, so the work outlives the daemon that asked for it and
// a restart can pick the result back up instead of throwing the run away.
func (r *Runner) Run(ctx context.Context, t state.Task, script string, out *os.File) jobproc.Result {
	res, err := r.execute(ctx, t, script, out)
	report := jobproc.Result{
		ExitCode:   res.ExitCode,
		VMName:     res.VMName,
		Kept:       res.Kept,
		Artifacts:  res.Artifacts,
		FenceRef:   res.FenceRef,
		FenceHeld:  res.FenceHeld,
		ResultRef:  res.ResultRef,
		FinishedAt: time.Now(),
	}
	if err != nil {
		report.Error = err.Error()
		report.ExitCode = -1
	}
	return report
}

func (r *Runner) execute(ctx context.Context, t state.Task, script string, out *os.File) (run.Result, error) {
	var empty run.Result
	remote := run.Remote{Base: r.RemoteBase, Repo: t.Repo, Token: run.ResolveToken(t.Env)}
	// The token and the agent are not alternatives. The token is for kranq's
	// own clone of the project; the agent is for whatever the build itself
	// reaches for, and a Gemfile with `git_source(:x) { "git@bitbucket.org:..." }`
	// reaches for it during `bundle install`, inside an image layer, long
	// before any task step runs.
	//
	// Making the agent conditional on the token being absent meant a pipeline
	// that forwarded BITBUCKET_TOKEN got no agent at all. Measured: the layer
	// failed with "fatal: repository ... does not exist" cloning a git-sourced
	// gem, on a machine whose own key can read that repository. lima forwards
	// the agent into the VM, so there was simply nothing to forward.
	sock, agentErr := sshagent.Ensure(r.AgentRoot)
	switch {
	case agentErr == nil:
		os.Setenv("SSH_AUTH_SOCK", sock)
	case remote.Token == "":
		// Nothing can reach the git host at all, and kranq has to clone.
		return empty, agentErr
	default:
		fmt.Fprintf(out, "warning: no ssh agent (%v); anything the build fetches over ssh will fail\n", agentErr)
	}

	work, err := os.MkdirTemp("", "kranq-task-*")
	if err != nil {
		return empty, err
	}
	defer os.RemoveAll(work)

	checkout := work + "/repo"
	source, err := r.source(ctx, t, work, checkout, out)
	if err != nil {
		return empty, err
	}
	if !source.Pushed() {
		fmt.Fprintf(out, "checking out %s of %s\n", t.Branch, t.Repo)
		if err := run.HostCheckout(ctx, remote, t.Branch, checkout); err != nil {
			return empty, err
		}
	}

	keep, _ := run.ParseKeep(t.Keep)
	// Re-read rather than plumbed through: the spec is already on the record,
	// and one parse is cheaper than a field every caller has to remember.
	artifacts := ""
	if spec, err := task.Parse([]byte(t.SpecYAML)); err == nil {
		artifacts = spec.Artifacts
	}
	return r.Engine.Execute(ctx, run.Request{
		TaskID:      t.ID,
		Repo:        t.Repo,
		Ref:         t.Branch,
		Label:       t.Label,
		Script:      script,
		Env:         t.Env,
		Checkout:    checkout,
		ArtifactDir: r.ArtifactsDir(t.ID),
		Artifacts:   artifacts,
		RemoteBase:  r.RemoteBase,
		Keep:        keep,
		Fence:       r.fence(t),
		Source:      source,
		Kranqfile:   t.Kranqfile,
		ResultRepo:  r.sharedRepo(sourceRepo(t)),
	}, out)
}
