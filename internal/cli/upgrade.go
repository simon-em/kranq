package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/effetmonstre/forge/internal/daemon"
	"github.com/effetmonstre/forge/internal/exitcode"
	"github.com/effetmonstre/forge/internal/ipc"
	"github.com/effetmonstre/forge/internal/selfinstall"
	"github.com/effetmonstre/forge/internal/upgrade"
)

// The installed copy, not the running one: `./forge upgrade` from a build
// directory means "replace what is installed", never "replace this file".
func upgradeTarget(explicit string) string {
	if explicit != "" {
		return explicit
	}
	if path, err := exec.LookPath("forge"); err == nil {
		if resolved, err := filepath.EvalSymlinks(path); err == nil {
			return resolved
		}
		return path
	}
	return filepath.Join(selfinstall.DefaultPrefix(), "forge")
}

func runUpgrade(env Env, args []string) int {
	fs := flag.NewFlagSet("upgrade", flag.ContinueOnError)
	fs.SetOutput(env.Stderr)
	target := fs.String("target", "", "binary to replace (default: the forge on PATH)")
	force := fs.Bool("force", false, "upgrade even while work is in flight")
	rest, err := parsePermuted(fs, args)
	if err != nil {
		return exitcode.Usage
	}
	if len(rest) != 1 {
		fmt.Fprintln(env.Stderr, "usage: forge upgrade <path-to-new-forge> [--target PATH] [--force]")
		return exitcode.Usage
	}
	return swap(env, upgradeTarget(*target), *force, func(ctx context.Context, to string) (upgrade.Report, error) {
		return upgrade.Install(ctx, rest[0], to)
	})
}

func runRollback(env Env, args []string) int {
	fs := flag.NewFlagSet("rollback", flag.ContinueOnError)
	fs.SetOutput(env.Stderr)
	target := fs.String("target", "", "binary to roll back (default: the forge on PATH)")
	force := fs.Bool("force", false, "roll back even while work is in flight")
	if _, err := parsePermuted(fs, args); err != nil {
		return exitcode.Usage
	}
	return swap(env, upgradeTarget(*target), *force, upgrade.Rollback)
}

func swap(env Env, target string, force bool, apply func(context.Context, string) (upgrade.Report, error)) int {
	ctx := context.Background()
	client := ipc.NewClient(daemonConfig().SocketPath())

	wasRunning := false
	if status, err := client.Status(ctx); err == nil {
		wasRunning = true
		if n := daemon.InFlight(status); n > 0 && !force {
			fmt.Fprintf(env.Stderr, "forge: %d task(s) in flight (%s)\n", n, daemon.Describe(status))
			fmt.Fprintln(env.Stderr, "restarting would lose them. Wait, cancel them, or pass --force")
			return exitcode.Misconfigured
		}
		if code := daemonStop(env, forceArgs(force)); code != exitcode.OK {
			return code
		}
	}

	report, err := apply(ctx, target)
	if err != nil {
		if errors.Is(err, upgrade.ErrNoPrevious) {
			fmt.Fprintf(env.Stderr, "forge: nothing to roll back to; %s has never been upgraded\n", target)
		} else {
			fmt.Fprintf(env.Stderr, "forge: %v\n", err)
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
	fmt.Fprintln(env.Stderr, "forge: the daemon did not come back; rolling back")
	if _, err := upgrade.Rollback(ctx, target); err != nil {
		fmt.Fprintf(env.Stderr, "forge: could not roll back: %v\n", err)
		return exitcode.InternalError
	}
	if !restart(env, true, client, target) {
		fmt.Fprintf(env.Stderr, "forge: rolled back to %s but the daemon still will not start; see %s/daemon.log\n",
			orNone(report.From), forgeHome())
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
		fmt.Fprintf(env.Stderr, "forge: %v\n", err)
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
