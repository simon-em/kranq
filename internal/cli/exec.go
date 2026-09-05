package cli

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/effetmonstre/forge/assets"
	"github.com/effetmonstre/forge/internal/daemon"
	"github.com/effetmonstre/forge/internal/exitcode"
	"github.com/effetmonstre/forge/internal/image"
	"github.com/effetmonstre/forge/internal/jobproc"
	"github.com/effetmonstre/forge/internal/run"
	"github.com/effetmonstre/forge/internal/state"
	"github.com/effetmonstre/forge/internal/vm"
)

// forge exec runs one job to completion and records what happened in the task
// directory. The daemon starts it and reads that record, which is what lets a
// job survive the daemon and a restarted daemon pick the outcome back up.
//
// It is not a command anyone should need to type. It is listed so that a job
// running under it is identifiable in ps, which is how re-adoption tells this
// task's process from a stranger that inherited its pid.
func runExec(env Env, args []string) int {
	if len(args) != 1 {
		fmt.Fprintln(env.Stderr, "usage: forge exec <task-id>")
		return exitcode.Usage
	}
	id := args[0]
	cfg := daemonConfig()
	if cfg.GitRemote == "" {
		cfg.GitRemote = defaultRemote
	}

	store, err := state.Open(cfg.TasksDir())
	if err != nil {
		fmt.Fprintf(env.Stderr, "forge: %v\n", err)
		return exitcode.InternalError
	}
	t, err := store.Get(id)
	if err != nil {
		fmt.Fprintf(env.Stderr, "forge: %v\n", err)
		return exitcode.InternalError
	}
	dir := store.Dir(id)
	script, err := os.ReadFile(jobproc.ScriptPath(dir))
	if err != nil {
		fmt.Fprintf(env.Stderr, "forge: %v\n", err)
		return exitcode.InternalError
	}

	// SIGTERM to the group is how a cancel and a timeout both arrive, and the
	// result still has to be written or the daemon cannot tell what happened.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	driver := vm.Lima{Bin: limactlPath(cfg.Home), Home: cfg.LimaHome}
	runner := &daemon.Runner{
		Engine: &run.Engine{
			Driver: driver,
			Images: &image.Manager{Driver: driver, Template: assets.LimaTemplate},
			Assets: assets.MCP(),
		},
		RemoteBase:   cfg.GitRemote,
		AgentRoot:    cfg.Home,
		FenceDir:     cfg.Home + "/fence",
		Node:         nodeName(),
		ArtifactsDir: func(string) string { return store.ArtifactsDir(id) },
		SourceRepos:  filepath.Join(cfg.Home, "repos"),
	}

	res := runner.Run(ctx, t, string(script), os.Stdout)
	if err := jobproc.WriteResult(dir, res); err != nil {
		fmt.Fprintf(env.Stderr, "forge: could not record the result: %v\n", err)
		return exitcode.InternalError
	}
	return exitcode.FromTask(res.ExitCode)
}
