package cli

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/effetmonstre/forge/assets"
	"github.com/effetmonstre/forge/internal/exitcode"
	"github.com/effetmonstre/forge/internal/image"
	"github.com/effetmonstre/forge/internal/run"
	"github.com/effetmonstre/forge/internal/task"
	"github.com/effetmonstre/forge/internal/vm"
)

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
	local := fs.Bool("local", true, "run in this process rather than submitting to a daemon")
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
	if !*local {
		fmt.Fprintln(env.Stderr, "forge: only --local is implemented so far")
		return exitcode.Misconfigured
	}
	if *repo == "" || *branch == "" {
		fmt.Fprintln(env.Stderr, "forge: set --repo and --branch (or CI_REPO/CI_BRANCH)")
		return exitcode.Misconfigured
	}
	if *remote == "" {
		*remote = "git@bitbucket.org:effetmonstre"
	}

	spec, code, err := loadSpec(positional[0])
	if err != nil {
		fmt.Fprintf(env.Stderr, "%s: %v\n", positional[0], err)
		return code
	}
	if *label == "" {
		*label = spec.Label
	}
	for name, value := range spec.Env {
		if _, set := forward[name]; !set {
			forward[name] = value
		}
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

	remoteSpec := run.Remote{Base: *remote, Repo: *repo, Token: run.ResolveToken(forward)}
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
