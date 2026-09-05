package daemon

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/effetmonstre/forge/internal/fence"
	"github.com/effetmonstre/forge/internal/jobproc"
	"github.com/effetmonstre/forge/internal/run"
	"github.com/effetmonstre/forge/internal/sshagent"
	"github.com/effetmonstre/forge/internal/state"
)

type Runner struct {
	Engine       *run.Engine
	RemoteBase   string
	ArtifactsDir func(id string) string
	AgentRoot    string
	FenceDir     string
	Node         string
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

// Run does the whole job in this process: checkout, VM, artifacts, fence. It is
// what `forge exec` calls, so the work outlives the daemon that asked for it and
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

	work, err := os.MkdirTemp("", "forge-task-*")
	if err != nil {
		return empty, err
	}
	defer os.RemoveAll(work)

	checkout := work + "/repo"
	fmt.Fprintf(out, "checking out %s of %s\n", t.Branch, t.Repo)
	if err := run.HostCheckout(ctx, remote, t.Branch, checkout); err != nil {
		return empty, err
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
	}, out)
}
