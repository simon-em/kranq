package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/simon-em/kranq/internal/daemon"
	"github.com/simon-em/kranq/internal/exitcode"
	"github.com/simon-em/kranq/internal/ipc"
	"github.com/simon-em/kranq/internal/selfinstall"
	"github.com/simon-em/kranq/internal/upgrade"
)

// The installed copy, not the running one: `./kranq upgrade` from a build
// directory means "replace what is installed", never "replace this file".
func upgradeTarget(explicit string) string {
	if explicit != "" {
		return explicit
	}
	if path, err := exec.LookPath("kranq"); err == nil {
		if resolved, err := filepath.EvalSymlinks(path); err == nil {
			return resolved
		}
		return path
	}
	return filepath.Join(selfinstall.DefaultPrefix(), "kranq")
}

func runUpgrade(env Env, args []string) int {
	fs := flag.NewFlagSet("upgrade", flag.ContinueOnError)
	fs.SetOutput(env.Stderr)
	target := fs.String("target", "", "binary to replace (default: the kranq on PATH)")
	force := fs.Bool("force", false, "upgrade even while work is in flight")
	rest, err := parsePermuted(fs, args)
	if err != nil {
		return exitcode.Usage
	}
	if len(rest) != 1 {
		fmt.Fprintln(env.Stderr, "usage: kranq upgrade <path-to-new-kranq> [--target PATH] [--force]")
		return exitcode.Usage
	}
	return swap(env, upgradeTarget(*target), *force, "use `brew upgrade simon-em/kranq/kranq`",
		func(ctx context.Context, to string) (upgrade.Report, error) {
			return upgrade.Install(ctx, rest[0], to)
		})
}

func runRollback(env Env, args []string) int {
	fs := flag.NewFlagSet("rollback", flag.ContinueOnError)
	fs.SetOutput(env.Stderr)
	target := fs.String("target", "", "binary to roll back (default: the kranq on PATH)")
	force := fs.Bool("force", false, "roll back even while work is in flight")
	if _, err := parsePermuted(fs, args); err != nil {
		return exitcode.Usage
	}
	return swap(env, upgradeTarget(*target), *force,
		"install the version you want with your package manager", upgrade.Rollback)
}

func swap(env Env, target string, force bool, advice string, apply func(context.Context, string) (upgrade.Report, error)) int {
	ctx := context.Background()
	if manager, managed := upgrade.Managed(target); managed {
		fmt.Fprintf(env.Stderr, "kranq: %s is managed by %s\n", target, manager.Name)
		fmt.Fprintf(env.Stderr, "%s\nreplacing it here would be undone by the next %s upgrade\n",
			advice, manager.Command)
		return exitcode.Misconfigured
	}
	client := ipc.NewClient(daemonConfig().SocketPath())

	wasRunning := false
	if status, err := client.Status(ctx); err == nil {
		wasRunning = true
		if n := daemon.InFlight(status); n > 0 && !force {
			fmt.Fprintf(env.Stderr, "kranq: %d task(s) in flight (%s)\n", n, daemon.Describe(status))
			// They are not lost by a restart any more: a job runs in its own
			// process and the daemon that comes back re-adopts it. --force is
			// still asked for, because a build machine is not a place to
			// discover that for the first time.
			fmt.Fprintln(env.Stderr, "they keep running and the new daemon re-adopts them, "+
				"but the daemon waits for them to finish first. Wait, cancel them, or pass --force")
			return exitcode.Misconfigured
		}
		if code := daemonStop(env, forceArgs(force)); code != exitcode.OK {
			return code
		}
	}

	report, err := apply(ctx, target)
	if err != nil {
		if errors.Is(err, upgrade.ErrNoPrevious) {
			fmt.Fprintf(env.Stderr, "kranq: nothing to roll back to; %s has never been upgraded\n", target)
		} else {
			fmt.Fprintf(env.Stderr, "kranq: %v\n", err)
		}
		restart(env, wasRunning, client, target)
		return exitcode.InternalError
	}
	fmt.Fprintf(env.Stderr, "%s: %s -> %s\n", report.Target, orNone(report.From), report.To)

	if !wasRunning {
		return exitcode.OK
	}
	if restart(env, true, client, target) {
		return exitcode.OK
	}
	// A daemon that will not come back is the failure this exists to survive,
	// so put the old binary back rather than leaving the machine without one.
	fmt.Fprintln(env.Stderr, "kranq: the daemon did not come back; rolling back")
	if _, err := upgrade.Rollback(ctx, target); err != nil {
		fmt.Fprintf(env.Stderr, "kranq: could not roll back: %v\n", err)
		return exitcode.InternalError
	}
	if !restart(env, true, client, target) {
		fmt.Fprintf(env.Stderr, "kranq: rolled back to %s but the daemon still will not start; see %s/daemon.log\n",
			orNone(report.From), kranqHome())
		return exitcode.InternalError
	}
	fmt.Fprintf(env.Stderr, "rolled back to %s\n", orNone(report.From))
	return exitcode.InternalError
}

func restart(env Env, wanted bool, client *ipc.Client, bin string) bool {
	if !wanted {
		return true
	}
	if err := spawnDaemonFrom(bin); err != nil {
		fmt.Fprintf(env.Stderr, "kranq: %v\n", err)
		return false
	}
	if err := client.WaitReady(context.Background(), 15*time.Second); err != nil {
		return false
	}
	fmt.Fprintln(env.Stderr, "daemon restarted")
	return true
}

func forceArgs(force bool) []string {
	if force {
		return []string{"--force"}
	}
	return nil
}

func orNone(v string) string {
	if v == "" {
		return "nothing"
	}
	return v
}
