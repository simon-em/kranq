package cli

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"

	"github.com/simon-em/kranq/internal/exitcode"
	"github.com/simon-em/kranq/internal/gitsrv"
)

// kranq git-receive is the forced command an authorized_keys entry runs. ssh
// puts what git asked for in SSH_ORIGINAL_COMMAND and runs this instead, so
// this is the whole boundary between a key and a shell on the build machine.
//
// It exists so a push can bypass an http tunnel entirely: no body limit, no
// proxy, and the key is the credential rather than a token.
func runGitReceive(env Env, args []string) int {
	fs := flag.NewFlagSet("git-receive", flag.ContinueOnError)
	fs.SetOutput(env.Stderr)
	name := fs.String("name", "", "which key this is, for the log")
	upload := fs.Bool("upload", false, "serve a fetch rather than a push")
	rest, err := parsePermuted(fs, args)
	if err != nil {
		return exitcode.Usage
	}

	cmd, err := receiveTarget(rest, *upload)
	if err != nil {
		fmt.Fprintf(env.Stderr, "kranq: %v\n", err)
		return exitcode.Unauthorized
	}

	home := kranqHome()
	cfg := daemonConfig()
	if !cfg.AutoCreateRepos {
		if _, statErr := os.Stat(filepath.Join(home, "repos", cmd.Repo+".git")); statErr != nil {
			fmt.Fprintf(env.Stderr, "kranq: no repository called %q here, and this machine "+
				"does not create them on demand.\nkranq repo create %s\n", cmd.Repo, cmd.Repo)
			return exitcode.Misconfigured
		}
	}
	self, err := os.Executable()
	if err != nil {
		fmt.Fprintf(env.Stderr, "kranq: %v\n", err)
		return exitcode.InternalError
	}
	store := &gitsrv.Store{
		Root:       filepath.Join(home, "repos"),
		KranqBin:   self,
		SocketPath: cfg.SocketPath(),
	}
	dir, err := store.Ensure(context.Background(), cmd.Repo)
	if err != nil {
		fmt.Fprintf(env.Stderr, "kranq: %v\n", err)
		return exitcode.InternalError
	}

	binary, err := gitCorePath(cmd.Verb)
	if err != nil {
		fmt.Fprintf(env.Stderr, "kranq: %v\n", err)
		return exitcode.MissingDep
	}
	// Replacing this process rather than wrapping it keeps git's own protocol on
	// the original stdin and stdout, which is the whole conversation.
	envv := append(os.Environ(), "KRANQ_KEY_NAME="+*name, "KRANQ_HOME="+home)
	if err := syscall.Exec(binary, []string{cmd.Verb, dir}, envv); err != nil {
		fmt.Fprintf(env.Stderr, "kranq: %v\n", err)
		return exitcode.InternalError
	}
	return exitcode.InternalError
}

// Two ways in. Under a forced command, ssh puts what git asked for in
// SSH_ORIGINAL_COMMAND. With `git push --receive-pack="kranq git-receive"`,
// git runs kranq directly and the repository arrives as an argument, which is
// what lets anyone who can already ssh here push without a kranq-specific key.
func receiveTarget(args []string, upload bool) (gitsrv.SSHCommand, error) {
	if len(args) > 0 {
		repo, err := gitsrv.RepoFromPath(args[0])
		if err != nil {
			return gitsrv.SSHCommand{}, err
		}
		verb := gitsrv.VerbReceive
		if upload {
			verb = gitsrv.VerbUpload
		}
		return gitsrv.SSHCommand{Verb: verb, Repo: repo}, nil
	}
	return gitsrv.ParseSSHCommand(os.Getenv("SSH_ORIGINAL_COMMAND"))
}

// git's helpers are not on PATH on macOS, and a non-login ssh session has a
// minimal PATH regardless, so neither is trusted here.
func gitCorePath(verb string) (string, error) {
	out, err := exec.Command("git", "--exec-path").Output()
	if err == nil {
		candidate := filepath.Join(trimLine(string(out)), verb)
		if _, statErr := os.Stat(candidate); statErr == nil {
			return candidate, nil
		}
	}
	if path, lookErr := exec.LookPath(verb); lookErr == nil {
		return path, nil
	}
	return "", fmt.Errorf("%s is not installed on this machine", verb)
}

func trimLine(v string) string {
	for len(v) > 0 && (v[len(v)-1] == '\n' || v[len(v)-1] == '\r') {
		v = v[:len(v)-1]
	}
	return v
}
