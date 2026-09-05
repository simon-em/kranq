package cli

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/effetmonstre/forge/assets"
	"github.com/effetmonstre/forge/internal/exitcode"
	"github.com/effetmonstre/forge/internal/fence"
	"github.com/effetmonstre/forge/internal/image"
	"github.com/effetmonstre/forge/internal/ipc"
	"github.com/effetmonstre/forge/internal/run"
	"github.com/effetmonstre/forge/internal/sshagent"
	"github.com/effetmonstre/forge/internal/state"
	"github.com/effetmonstre/forge/internal/task"
	"github.com/effetmonstre/forge/internal/vm"
)

const defaultRemote = "git@bitbucket.org:effetmonstre"

type envFlag map[string]string

func (e envFlag) String() string { return "" }

func (e envFlag) Set(v string) error {
	name, value, found := strings.Cut(v, "=")
	if name == "" {
		return fmt.Errorf("expected NAME=VALUE or NAME, got %q", v)
	}
	if !found {
		value = os.Getenv(name)
	}
	e[name] = value
	return nil
}

func runRun(env Env, args []string) int {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	fs.SetOutput(env.Stderr)
	repo := fs.String("repo", envOr("CI_REPO", "BITBUCKET_REPO_SLUG"), "repository slug")
	branch := fs.String("branch", envOr("CI_BRANCH", "BITBUCKET_BRANCH"), "branch to check out")
	label := fs.String("label", "", "label for the VM and artifacts (default: the task name)")
	artifacts := fs.String("artifacts", "", "directory to copy ci-artifacts/ into")
	remote := fs.String("remote", envOr("FORGE_GIT_REMOTE"), "git remote base")
	local := fs.Bool("local", false, "run in this process instead of submitting to the daemon")
	detach := fs.Bool("detach", false, "print the task id and exit without following")
	keep := fs.String("keep-vm", "never", "keep the job VM: never, on-failure, always")
	timeout := fs.Duration("timeout", 4*time.Hour, "ceiling on the run")
	forward := envFlag{}
	fs.Var(forward, "env", "NAME=VALUE, or bare NAME to forward it from this environment")
	positional, err := parsePermuted(fs, args)
	if err != nil {
		return exitcode.Usage
	}
	if len(positional) != 1 {
		fmt.Fprintln(env.Stderr, "usage: forge run <task.yaml> [flags]")
		fs.PrintDefaults()
		return exitcode.Usage
	}
	if _, ok := run.ParseKeep(*keep); !ok {
		fmt.Fprintf(env.Stderr, "forge: --keep-vm %q: want never, on-failure or always\n", *keep)
		return exitcode.Usage
	}
	if *repo == "" || *branch == "" {
		fmt.Fprintln(env.Stderr, "forge: set --repo and --branch (or CI_REPO/CI_BRANCH)")
		return exitcode.Misconfigured
	}
	if *remote == "" {
		*remote = defaultRemote
	}

	specRaw, code, err := readSpecFile(positional[0])
	if err != nil {
		fmt.Fprintf(env.Stderr, "%s: %v\n", positional[0], err)
		return code
	}
	spec, err := task.Parse(specRaw)
	if err != nil {
		fmt.Fprintf(env.Stderr, "%s: %v\n", positional[0], err)
		return exitcode.InvalidSpec
	}
	if *label == "" {
		*label = spec.Label
	}
	for name, value := range spec.Env {
		if _, set := forward[name]; !set {
			forward[name] = value
		}
	}

	if !*local {
		return submitAndFollow(env, submission{
			spec: string(specRaw), repo: *repo, branch: *branch, label: *label,
			env: forward, artifacts: *artifacts, detach: *detach, keep: *keep,
		})
	}

	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()
	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()

	taskID := time.Now().UTC().Format("20060102T150405")
	work, mkErr := os.MkdirTemp("", "forge-run-*")
	if mkErr != nil {
		fmt.Fprintf(env.Stderr, "forge: %v\n", mkErr)
		return exitcode.InternalError
	}
	defer os.RemoveAll(work)

	keepPolicy, _ := run.ParseKeep(*keep)
	remoteSpec := run.Remote{Base: *remote, Repo: *repo, Token: run.ResolveToken(forward)}
	if remoteSpec.Token == "" {
		sock, agentErr := sshagent.Ensure(forgeHome())
		if agentErr != nil {
			fmt.Fprintf(env.Stderr, "forge: %v\n", agentErr)
			return exitcode.Misconfigured
		}
		os.Setenv("SSH_AUTH_SOCK", sock)
	}
	checkout := work + "/repo"
	fmt.Fprintf(env.Stderr, "checking out %s of %s\n", *branch, *repo)
	if err := run.HostCheckout(ctx, remoteSpec, *branch, checkout); err != nil {
		fmt.Fprintf(env.Stderr, "forge: %v\n", err)
		return exitcode.CouldNotStart
	}

	driver := vm.Lima{Home: os.Getenv("FORGE_LIMA_HOME")}
	engine := &run.Engine{
		Driver: driver,
		Images: &image.Manager{Driver: driver, Template: assets.LimaTemplate},
		Assets: assets.MCP(),
	}
	res, err := engine.Execute(ctx, run.Request{
		TaskID:      taskID,
		Repo:        *repo,
		Ref:         *branch,
		Label:       *label,
		Script:      task.BuildScript(spec, forward),
		Env:         forward,
		Checkout:    checkout,
		ArtifactDir: *artifacts,
		RemoteBase:  *remote,
		Keep:        keepPolicy,
		Fence:       localFence(spec, *branch, *repo),
	}, env.Stderr)
	if err != nil {
		fmt.Fprintf(env.Stderr, "forge: %v\n", err)
		if ctx.Err() != nil {
			return exitcode.Cancelled
		}
		return exitcode.CouldNotStart
	}
	fmt.Fprintf(env.Stderr, "task exited %d\n", res.ExitCode)
	return exitcode.FromTask(res.ExitCode)
}

func localFence(spec task.Spec, branch, repo string) *run.FencePlan {
	if !spec.Fenced() {
		return nil
	}
	return &run.FencePlan{
		Dir:   filepath.Join(forgeHome(), "fence"),
		Node:  nodeName(),
		Scope: fence.Scope{Kind: spec.FenceKind(), Repo: repo, Branch: spec.FenceBranch(branch)},
	}
}

func envOr(names ...string) string {
	for _, name := range names {
		if v := os.Getenv(name); v != "" {
			return v
		}
	}
	return ""
}

func parsePermuted(fs *flag.FlagSet, args []string) ([]string, error) {
	var positional []string
	for {
		if err := fs.Parse(args); err != nil {
			return nil, err
		}
		if fs.NArg() == 0 {
			return positional, nil
		}
		positional = append(positional, fs.Arg(0))
		args = fs.Args()[1:]
	}
}

func forgeHome() string {
	if v := os.Getenv("FORGE_HOME"); v != "" {
		return v
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ".forge"
	}
	return home + "/.forge"
}

type submission struct {
	spec      string
	repo      string
	branch    string
	label     string
	env       map[string]string
	artifacts string
	detach    bool
	keep      string
}

func submitAndFollow(env Env, s submission) int {
	client, code := connect(env, true)
	if client == nil {
		return code
	}
	ctx := context.Background()
	t, err := client.Submit(ctx, ipc.SubmitRequest{
		Spec: s.spec, Repo: s.repo, Branch: s.branch, Label: s.label, Env: s.env, Keep: s.keep,
	})
	if err != nil {
		fmt.Fprintf(env.Stderr, "forge: %v\n", err)
		return codeOf(err, exitcode.InternalError)
	}
	if s.detach {
		fmt.Fprintln(env.Stdout, t.ID)
		return exitcode.OK
	}
	fmt.Fprintf(env.Stderr, "forge: task %s queued\n", t.ID)
	waitForStart(ctx, client, t.ID, env)

	if err := client.Logs(ctx, t.ID, true, env.Stdout); err != nil {
		fmt.Fprintf(env.Stderr, "forge: log stream ended: %v\n", err)
	}
	final, err := client.Get(ctx, t.ID)
	if err != nil {
		fmt.Fprintf(env.Stderr, "forge: %v\n", err)
		return exitcode.Unreachable
	}
	fetchArtifacts(client, t.ID, s.artifacts, env)
	return report(env, final)
}

func report(env Env, t state.Task) int {
	switch t.Status {
	case state.StatusSucceeded:
		fmt.Fprintf(env.Stderr, "forge: %s succeeded\n", t.ID)
		return exitcode.OK
	case state.StatusCancelled:
		fmt.Fprintf(env.Stderr, "forge: %s was cancelled\n", t.ID)
		return exitcode.Cancelled
	case state.StatusLost:
		fmt.Fprintf(env.Stderr, "forge: %s is LOST: %s\n", t.ID, t.LostReason)
		fmt.Fprintln(env.Stderr, "forge: it may still be running. Check before re-running it.")
		return exitcode.InternalError
	case state.StatusFailed:
		if t.Error != "" {
			fmt.Fprintf(env.Stderr, "forge: %s failed: %s\n", t.ID, t.Error)
			return exitcode.CouldNotStart
		}
		fmt.Fprintf(env.Stderr, "forge: %s exited %d\n", t.ID, t.ExitCode)
		return exitcode.FromTask(t.ExitCode)
	}
	fmt.Fprintf(env.Stderr, "forge: %s is %s\n", t.ID, t.Status)
	return exitcode.InternalError
}

func waitForStart(ctx context.Context, client *ipc.Client, id string, env Env) {
	reported := ""
	for {
		t, err := client.Get(ctx, id)
		if err != nil || t.Status == state.StatusRunning || t.Terminal() {
			return
		}
		if note := blockedNote(ctx, client, t); note != reported {
			fmt.Fprintf(env.Stderr, "forge: %s\n", note)
			reported = note
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(2 * time.Second):
		}
	}
}

func blockedNote(ctx context.Context, client *ipc.Client, t state.Task) string {
	if t.Status != state.StatusBlocked {
		return "queued, waiting for a free slot"
	}
	switch t.BlockedOn {
	case state.BlockedOnClaude:
		if s, err := client.Status(ctx); err == nil && !s.Claude.Available {
			return fmt.Sprintf("waiting for claude usage, next check in %s",
				s.Claude.Remaining.Truncate(time.Second))
		}
		return "waiting for claude usage"
	case state.BlockedOnMemory, state.BlockedOnSlots:
		if s, err := client.Status(ctx); err == nil && s.StopReason != "" {
			return "waiting: " + s.StopReason
		}
	}
	return "waiting: blocked on " + string(t.BlockedOn)
}
