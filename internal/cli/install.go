package cli

import (
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/effetmonstre/forge/internal/daemon"
	"github.com/effetmonstre/forge/internal/deps"
	"github.com/effetmonstre/forge/internal/exitcode"
	"github.com/effetmonstre/forge/internal/selfinstall"
)

func runInstall(env Env, args []string) int {
	fs := flag.NewFlagSet("install", flag.ContinueOnError)
	fs.SetOutput(env.Stderr)
	prefix := fs.String("prefix", selfinstall.DefaultPrefix(), "where the binary goes")
	noPath := fs.Bool("no-path", false, "do not touch shell startup files")
	skipDeps := fs.Bool("skip-deps", false, "do not install lima")
	clientOnly := fs.Bool("client-only", false, "binary and PATH only: no lima, no launchd")
	withDaemon := fs.Bool("with-daemon", false, "also install and start the launchd job")
	depsOnly := fs.Bool("deps-only", false, "lima and launchd only: leave the binary where it is")
	if _, err := parsePermuted(fs, args); err != nil {
		return exitcode.Usage
	}

	home := forgeHome()
	if err := selfinstall.EnsureHome(home); err != nil {
		fmt.Fprintf(env.Stderr, "forge: %v\n", err)
		return exitcode.InternalError
	}
	// A package manager owns the binary and PATH when it installed forge, so
	// copying it somewhere else would leave two copies that upgrade separately.
	binary, err := os.Executable()
	if err != nil {
		fmt.Fprintf(env.Stderr, "forge: %v\n", err)
		return exitcode.InternalError
	}
	if !*depsOnly {
		binary, err = selfinstall.InstallBinary(*prefix, env.Stderr)
		if err != nil {
			fmt.Fprintf(env.Stderr, "forge: %v\n", err)
			return exitcode.InternalError
		}
	}

	if !*noPath && !*depsOnly {
		shell := os.Getenv("SHELL")
		profile, err := selfinstall.ProfileFor(shell)
		if err != nil {
			fmt.Fprintf(env.Stderr, "forge: %v\n", err)
			return exitcode.InternalError
		}
		if selfinstall.OnPath(*prefix) {
			fmt.Fprintf(env.Stderr, "%s is already on PATH\n", *prefix)
		} else if err := selfinstall.AddToProfile(profile, *prefix, shell, env.Stderr); err != nil {
			fmt.Fprintf(env.Stderr, "forge: %v\n", err)
			return exitcode.InternalError
		} else {
			fmt.Fprintf(env.Stderr, "open a new shell, or run: %s\n",
				selfinstall.ExportLine(*prefix, shell))
		}
	}

	if *clientOnly {
		fmt.Fprintln(env.Stderr, "client-only install: no lima, no daemon")
		return exitcode.OK
	}

	if !*skipDeps {
		lima := deps.Lima{Root: home}
		if lima.Installed() {
			fmt.Fprintf(env.Stderr, "lima %s already installed at %s\n", deps.LimaVersion, lima.Binary())
		} else if err := lima.Install(env.Stderr); err != nil {
			fmt.Fprintf(env.Stderr, "forge: %v\n", err)
			return exitcode.MissingDep
		}
	}

	if *withDaemon {
		svc := selfinstall.Service{Binary: binary, Home: home, Path: os.Getenv("PATH")}
		if err := svc.Install(env.Stderr); err != nil {
			fmt.Fprintf(env.Stderr, "forge: %v\n", err)
			return exitcode.InternalError
		}
	}

	fmt.Fprintf(env.Stderr, "\nforge is installed. State lives in %s\n", home)
	if !*withDaemon {
		fmt.Fprintln(env.Stderr, "the daemon starts on demand; `forge install --with-daemon` makes it permanent")
	}
	if _, err := daemon.LoadEnv(home); err == nil {
		if e, _ := daemon.LoadEnv(home); e["CLAUDE_CODE_OAUTH_TOKEN"] == "" {
			fmt.Fprintln(env.Stderr, "claude tasks need a token: `forge auth claude --stdin < token.txt`")
		}
	}
	return exitcode.OK
}

func runUninstall(env Env, args []string) int {
	fs := flag.NewFlagSet("uninstall", flag.ContinueOnError)
	fs.SetOutput(env.Stderr)
	purge := fs.Bool("purge", false, "also delete tasks, artifacts and images metadata")
	if _, err := parsePermuted(fs, args); err != nil {
		return exitcode.Usage
	}
	home := forgeHome()
	if client, _ := connect(env, false); client != nil {
		fmt.Fprintln(env.Stderr, "forge: the daemon is running; stop it first with `forge daemon stop`")
		return exitcode.Misconfigured
	}
	if err := selfinstall.Uninstall(env.Stderr); err != nil {
		fmt.Fprintf(env.Stderr, "forge: %v\n", err)
	}
	shell := os.Getenv("SHELL")
	if profile, err := selfinstall.ProfileFor(shell); err == nil {
		_ = selfinstall.RemoveFromProfile(profile, env.Stderr)
	}
	if *purge {
		if err := os.RemoveAll(home); err != nil {
			fmt.Fprintf(env.Stderr, "forge: %v\n", err)
			return exitcode.InternalError
		}
		fmt.Fprintf(env.Stderr, "removed %s\n", home)
	} else {
		fmt.Fprintf(env.Stderr, "left %s in place; pass --purge to delete it\n", home)
	}
	return exitcode.OK
}

func runAuth(env Env, args []string) int {
	if len(args) == 0 || args[0] != "claude" {
		fmt.Fprintln(env.Stderr, "usage: forge auth claude [--stdin | --show]")
		return exitcode.Usage
	}
	fs := flag.NewFlagSet("auth claude", flag.ContinueOnError)
	fs.SetOutput(env.Stderr)
	stdin := fs.Bool("stdin", false, "read the token from stdin")
	show := fs.Bool("show", false, "report whether a token is set, without printing it")
	clear := fs.Bool("clear", false, "remove the stored token")
	if _, err := parsePermuted(fs, args[1:]); err != nil {
		return exitcode.Usage
	}
	home := forgeHome()

	switch {
	case *show:
		stored, err := daemon.LoadEnv(home)
		if err != nil {
			fmt.Fprintf(env.Stderr, "forge: %v\n", err)
			return exitcode.InternalError
		}
		token := stored["CLAUDE_CODE_OAUTH_TOKEN"]
		if token == "" {
			fmt.Fprintln(env.Stdout, "no claude token is stored")
			return exitcode.OK
		}
		fmt.Fprintf(env.Stdout, "a claude token is stored (%d chars, fingerprint %s)\n",
			len(token), fingerprint(token))
		return exitcode.OK
	case *clear:
		if err := daemon.SetEnv(home, "CLAUDE_CODE_OAUTH_TOKEN", ""); err != nil {
			fmt.Fprintf(env.Stderr, "forge: %v\n", err)
			return exitcode.InternalError
		}
		fmt.Fprintln(env.Stderr, "token removed; restart the daemon for it to take effect")
		return exitcode.OK
	case *stdin:
		raw, err := io.ReadAll(os.Stdin)
		if err != nil {
			fmt.Fprintf(env.Stderr, "forge: %v\n", err)
			return exitcode.InternalError
		}
		token := strings.TrimSpace(string(raw))
		if token == "" {
			fmt.Fprintln(env.Stderr, "forge: nothing on stdin")
			return exitcode.Usage
		}
		if err := daemon.SetEnv(home, "CLAUDE_CODE_OAUTH_TOKEN", token); err != nil {
			fmt.Fprintf(env.Stderr, "forge: %v\n", err)
			return exitcode.InternalError
		}
		fmt.Fprintf(env.Stderr, "stored a %d character token (fingerprint %s) in %s\n",
			len(token), fingerprint(token), daemon.EnvPath(home))
		fmt.Fprintln(env.Stderr, "restart the daemon for it to take effect: `forge daemon stop`")
		return exitcode.OK
	}
	fmt.Fprintln(env.Stderr, "usage: forge auth claude [--stdin | --show | --clear]")
	return exitcode.Usage
}
