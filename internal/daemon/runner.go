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
	shared := filepath.Join(r.SourceRepos, t.Repo+".git")
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
	if remote.Token == "" {
		sock, err := sshagent.Ensure(r.AgentRoot)
		if err != nil {
			return empty, err
		}
		os.Setenv("SSH_AUTH_SOCK", sock)
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
	return r.Engine.Execute(ctx, run.Request{
		TaskID:      t.ID,
		Repo:        t.Repo,
		Ref:         t.Branch,
		Label:       t.Label,
		Script:      script,
		Env:         t.Env,
		Checkout:    checkout,
		ArtifactDir: r.ArtifactsDir(t.ID),
		RemoteBase:  r.RemoteBase,
		Keep:        keep,
		Fence:       r.fence(t),
		Source:      source,
		Kranqfile:   t.Kranqfile,
	}, out)
}
