package daemon

import (
	"context"
	"fmt"
	"os"

	"github.com/effetmonstre/forge/internal/fence"
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
	Record       func(taskID, vmName string, kept bool)
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

func (r *Runner) Execute(ctx context.Context, t state.Task, script string, out *os.File) (int, error) {
	remote := run.Remote{Base: r.RemoteBase, Repo: t.Repo, Token: run.ResolveToken(t.Env)}
	if remote.Token == "" {
		sock, err := sshagent.Ensure(r.AgentRoot)
		if err != nil {
			return -1, err
		}
		os.Setenv("SSH_AUTH_SOCK", sock)
	}

	work, err := os.MkdirTemp("", "forge-task-*")
	if err != nil {
		return -1, err
	}
	defer os.RemoveAll(work)

	checkout := work + "/repo"
	fmt.Fprintf(out, "checking out %s of %s\n", t.Branch, t.Repo)
	if err := run.HostCheckout(ctx, remote, t.Branch, checkout); err != nil {
		return -1, err
	}

	keep, _ := run.ParseKeep(t.Keep)
	res, err := r.Engine.Execute(ctx, run.Request{
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
	if r.Record != nil {
		r.Record(t.ID, res.VMName, res.Kept)
	}
	if err != nil {
		return -1, err
	}
	return res.ExitCode, nil
}
