package cli

import (
	"crypto/rand"
	"flag"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/simon-em/kranq/internal/exitcode"
	"github.com/simon-em/kranq/internal/gitsrv"
)

// kranq push sends the working repository to kranq and runs a task against
// exactly what was sent. Nothing is cloned from the git host, so no credential
// is needed to read the code, and uncommitted history that exists only here
// still runs.
func runPush(env Env, args []string) int {
	fs := flag.NewFlagSet("push", flag.ContinueOnError)
	fs.SetOutput(env.Stderr)
	endpoint := fs.String("endpoint", envOr("KRANQ_ENDPOINT"), "https://... where kranq receives pushes")
	// Deliberately not defaulted from the environment here: flag.PrintDefaults
	// prints a flag's default value, and usage is printed on every misuse, so
	// the token would end up in the terminal and in CI logs.
	tok := fs.String("token", "", "a kranq token (default: $KRANQ_TOKEN)")
	repo := fs.String("repo", envOr("KRANQ_REPO", "CI_REPO", "BITBUCKET_REPO_SLUG"), "repository name on the runner")
	branch := fs.String("branch", envOr("CI_BRANCH", "BITBUCKET_BRANCH"), "branch name to report for this run")
	label := fs.String("label", "", "label for the VM and artifacts")
	keep := fs.String("keep-vm", "", "keep the job VM: never, on-failure, always")
	rev := fs.String("rev", "HEAD", "what to send")
	sshKey := fs.String("ssh-key", envOr("KRANQ_SSH_KEY"), "identity to push with, for an ssh:// endpoint")
	receivePack := fs.String("receive-pack", orElse(envOr("KRANQ_RECEIVE_PACK"), defaultReceivePack),
		"kranq on the far side, so no kranq-specific ssh key is needed")
	detach := fs.Bool("detach", false, "queue it and return without following")
	forward := envFlag{}
	fs.Var(forward, "env", "NAME=VALUE, or bare NAME to forward it from this environment")
	fs.Var(forward, "e", "shorthand for -env")
	positional, err := parsePermuted(fs, args)
	if err != nil {
		return exitcode.Usage
	}
	if len(positional) != 1 {
		fmt.Fprintln(env.Stderr, "usage: kranq push <task.yaml> [flags]")
		fs.PrintDefaults()
		return exitcode.Usage
	}
	if *tok == "" {
		*tok = envOr("KRANQ_TOKEN")
	}
	if *endpoint == "" {
		fmt.Fprintln(env.Stderr, "kranq: set --endpoint (or KRANQ_ENDPOINT)")
		return exitcode.Misconfigured
	}
	// An ssh endpoint authenticates with a key, so it needs no token at all.
	if *tok == "" && !isSSH(*endpoint) {
		fmt.Fprintln(env.Stderr, "kranq: set --token (or KRANQ_TOKEN), or use an ssh:// endpoint")
		return exitcode.Misconfigured
	}
	if *repo == "" {
		fmt.Fprintln(env.Stderr, "kranq: set --repo (or CI_REPO)")
		return exitcode.Misconfigured
	}

	target, err := pushURL(*endpoint, *repo, *tok)
	if err != nil {
		fmt.Fprintf(env.Stderr, "kranq: %v\n", err)
		return exitcode.Misconfigured
	}

	options := []string{"-o", "task_file=" + positional[0]}
	// The spec travels with the push rather than being read out of the commit,
	// so a task that lives in a submodule, or one you have edited and not
	// committed, still runs. A submodule is a gitlink: its files are not in the
	// commit at all, and no path can reach them.
	if spec, err := os.ReadFile(positional[0]); err == nil {
		encoded, encErr := gitsrv.EncodeSpec(spec)
		if encErr != nil {
			fmt.Fprintf(env.Stderr, "kranq: %v\n", encErr)
			return exitcode.InternalError
		}
		options = append(options, "-o", "spec="+encoded)
	}
	for _, kv := range [][2]string{{"label", *label}, {"branch", *branch}, {"keep-vm", *keep}} {
		if kv[1] != "" {
			options = append(options, "-o", kv[0]+"="+kv[1])
		}
	}
	for name, value := range forward {
		options = append(options, "-o", gitsrv.EnvPrefix+name+"="+value)
	}
	if *detach {
		options = append(options, "-o", "detach")
	}

	args = []string{"push", "--force"}
	// Asking for kranq as the receive-pack is what lets an ordinary ssh key
	// push: git runs kranq on the far side itself, so nothing has to be set up
	// there first. A forced-command key ignores this and still works.
	if isSSH(*endpoint) && *receivePack != "" {
		args = append(args, "--receive-pack="+*receivePack+" git-receive")
	}
	args = append(args, target)
	args = append(args, options...)
	args = append(args, *rev+":"+pushRef())

	cmd := exec.Command("git", args...)
	cmd.Stdin = os.Stdin
	// Tee, because the run's outcome only reaches us in git's own output.
	var captured strings.Builder
	cmd.Stdout = io.MultiWriter(env.Stderr, &captured)
	cmd.Stderr = cmd.Stdout
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	if *sshKey != "" {
		cmd.Env = append(cmd.Env, "GIT_SSH_COMMAND="+sshCommand(*sshKey))
	}
	runErr := cmd.Run()

	result := gitsrv.ParseResult(captured.String())
	switch {
	case result.Found && result.Status == gitsrv.StatusRefused:
		return result.ExitCode
	case result.Found:
		return exitcode.FromTask(result.ExitCode)
	case runErr != nil:
		// The push itself was refused. Telling a rejected token from a rejected
		// spec matters: one is the pipeline's configuration and the other is
		// the code being pushed.
		return refusalCode(captured.String())
	case *detach:
		return exitcode.OK
	}
	fmt.Fprintln(env.Stderr, "kranq: the push was accepted but no result came back; the run may still be going")
	return exitcode.Unreachable
}

// A ref that has already been pushed makes git say "Everything up-to-date" and
// run no hook at all, so pushing the same commit twice would do nothing. That is
// not an edge case: retrying a failed pipeline step is the ordinary way to
// re-run a job. The destination ref is therefore unique per push, which costs
// nothing because the branch the run reports comes from -o branch, not from
// where the objects landed.
func pushRef() string {
	var nonce [8]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return fmt.Sprintf("refs/kranq/push/%d", time.Now().UnixNano())
	}
	return fmt.Sprintf("refs/kranq/push/%s-%x", time.Now().UTC().Format("20060102T150405"), nonce)
}

func refusalCode(output string) int {
	lower := strings.ToLower(output)
	for _, marker := range []string{
		// http
		"authentication failed", "a kranq token is required", "401",
		// ssh
		"permission denied (publickey)", "could not read from remote repository",
		"this key may only push and fetch", "it has no shell",
	} {
		if strings.Contains(lower, marker) {
			return exitcode.Unauthorized
		}
	}
	if strings.Contains(lower, "could not resolve host") || strings.Contains(lower, "failed to connect") {
		return exitcode.Unreachable
	}
	return exitcode.InvalidSpec
}

// Over http the token is the password, and it goes in the URL rather than an
// argument that would sit in the process list. Over ssh there is no token at
// all: the key is the credential, and the path is the repository, because a
// forced command on the far side is what decides where it lands.
func pushURL(endpoint, repo, secret string) (string, error) {
	u, err := url.Parse(strings.TrimSuffix(endpoint, "/"))
	if err != nil {
		return "", fmt.Errorf("%q is not a usable endpoint: %w", endpoint, err)
	}
	if u.Scheme == "" || u.Host == "" {
		return "", fmt.Errorf("%q needs a scheme and a host, like https://ci.example.com", endpoint)
	}
	if u.Scheme == "ssh" {
		u.Path = "/" + repo + ".git"
		return u.String(), nil
	}
	if secret == "" {
		return "", fmt.Errorf("an %s endpoint needs a token", u.Scheme)
	}
	u.User = url.UserPassword("kranq", secret)
	if !strings.HasSuffix(u.Path, "/git") {
		u.Path = strings.TrimSuffix(u.Path, "/") + "/git"
	}
	u.Path += "/" + repo + ".git"
	return u.String(), nil
}

func isSSH(endpoint string) bool { return strings.HasPrefix(endpoint, "ssh://") }

// Where kranq installs itself. Overridable for a machine that put it elsewhere,
// and clearable with --receive-pack="" for a key whose forced command already
// decides what runs.
const defaultReceivePack = "$HOME/.local/bin/kranq"

func orElse(value, fallback string) string {
	if value != "" {
		return value
	}
	return fallback
}

// IdentitiesOnly matters: the push key is a forced-command key that can do
// nothing but push, and it usually sits beside an ordinary key for the same
// host. Without this, ssh offers the ordinary one first and the forced command
// never runs.
func sshCommand(key string) string {
	return fmt.Sprintf("ssh -i %s -o IdentitiesOnly=yes -o IdentityAgent=none", shellQuote(key))
}
