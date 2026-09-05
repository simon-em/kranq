package cli

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"

	"github.com/effetmonstre/forge/internal/exitcode"
	"github.com/effetmonstre/forge/internal/gitsrv"
)

// forge git-receive is the forced command an authorized_keys entry runs. ssh
// puts what git asked for in SSH_ORIGINAL_COMMAND and runs this instead, so
// this is the whole boundary between a key and a shell on the build machine.
//
// It exists so a push can bypass an http tunnel entirely: no body limit, no
// proxy, and the key is the credential rather than a token.
func runGitReceive(env Env, args []string) int {
	fs := flag.NewFlagSet("git-receive", flag.ContinueOnError)
	fs.SetOutput(env.Stderr)
	name := fs.String("name", "", "which key this is, for the log")
	if _, err := parsePermuted(fs, args); err != nil {
		return exitcode.Usage
	}

	cmd, err := gitsrv.ParseSSHCommand(os.Getenv("SSH_ORIGINAL_COMMAND"))
	if err != nil {
		fmt.Fprintf(env.Stderr, "forge: %v\n", err)
		return exitcode.Unauthorized
	}

	home := forgeHome()
	self, err := os.Executable()
	if err != nil {
		fmt.Fprintf(env.Stderr, "forge: %v\n", err)
		return exitcode.InternalError
	}
	store := &gitsrv.Store{
		Root:       filepath.Join(home, "repos"),
		ForgeBin:   self,
		SocketPath: daemonConfig().SocketPath(),
	}
	dir, err := store.Ensure(context.Background(), cmd.Repo)
	if err != nil {
		fmt.Fprintf(env.Stderr, "forge: %v\n", err)
		return exitcode.InternalError
	}

	binary, err := gitCorePath(cmd.Verb)
	if err != nil {
		fmt.Fprintf(env.Stderr, "forge: %v\n", err)
		return exitcode.MissingDep
	}
	// Replacing this process rather than wrapping it keeps git's own protocol on
	// the original stdin and stdout, which is the whole conversation.
	envv := append(os.Environ(), "FORGE_KEY_NAME="+*name, "FORGE_HOME="+home)
	if err := syscall.Exec(binary, []string{cmd.Verb, dir}, envv); err != nil {
		fmt.Fprintf(env.Stderr, "forge: %v\n", err)
		return exitcode.InternalError
	}
	return exitcode.InternalError
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
