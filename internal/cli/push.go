package cli

import (
	"flag"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"strings"

	"github.com/effetmonstre/forge/internal/exitcode"
	"github.com/effetmonstre/forge/internal/gitsrv"
)

// forge push sends the working repository to forge and runs a task against
// exactly what was sent. Nothing is cloned from the git host, so no credential
// is needed to read the code, and uncommitted history that exists only here
// still runs.
func runPush(env Env, args []string) int {
	fs := flag.NewFlagSet("push", flag.ContinueOnError)
	fs.SetOutput(env.Stderr)
	endpoint := fs.String("endpoint", envOr("FORGE_ENDPOINT"), "https://... where forge receives pushes")
	// Deliberately not defaulted from the environment here: flag.PrintDefaults
	// prints a flag's default value, and usage is printed on every misuse, so
	// the token would end up in the terminal and in CI logs.
	tok := fs.String("token", "", "a forge token (default: $FORGE_TOKEN)")
	repo := fs.String("repo", envOr("FORGE_REPO", "CI_REPO", "BITBUCKET_REPO_SLUG"), "repository name on the runner")
	branch := fs.String("branch", envOr("CI_BRANCH", "BITBUCKET_BRANCH"), "branch name to report for this run")
	label := fs.String("label", "", "label for the VM and artifacts")
	keep := fs.String("keep-vm", "", "keep the job VM: never, on-failure, always")
	rev := fs.String("rev", "HEAD", "what to send")
	detach := fs.Bool("detach", false, "queue it and return without following")
	forward := envFlag{}
	fs.Var(forward, "env", "NAME=VALUE, or bare NAME to forward it from this environment")
	fs.Var(forward, "e", "shorthand for -env")
	positional, err := parsePermuted(fs, args)
	if err != nil {
		return exitcode.Usage
	}
	if len(positional) != 1 {
		fmt.Fprintln(env.Stderr, "usage: forge push <task.yaml> [flags]")
		fs.PrintDefaults()
		return exitcode.Usage
	}
	if *tok == "" {
		*tok = envOr("FORGE_TOKEN")
	}
	if *endpoint == "" || *tok == "" {
		fmt.Fprintln(env.Stderr, "forge: set --endpoint and --token (or FORGE_ENDPOINT and FORGE_TOKEN)")
		return exitcode.Misconfigured
	}
	if *repo == "" {
		fmt.Fprintln(env.Stderr, "forge: set --repo (or CI_REPO)")
		return exitcode.Misconfigured
	}

	target, err := pushURL(*endpoint, *repo, *tok)
	if err != nil {
		fmt.Fprintf(env.Stderr, "forge: %v\n", err)
		return exitcode.Misconfigured
	}

	options := []string{"-o", "task=" + positional[0]}
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

	dest := *branch
	if dest == "" {
		dest = "forge-push"
	}
	args = append([]string{"push", "--force", target}, options...)
	args = append(args, *rev+":refs/heads/"+dest)

	cmd := exec.Command("git", args...)
	cmd.Stdin = os.Stdin
	// Tee, because the run's outcome only reaches us in git's own output.
	var captured strings.Builder
	cmd.Stdout = io.MultiWriter(env.Stderr, &captured)
	cmd.Stderr = cmd.Stdout
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	runErr := cmd.Run()

	result := gitsrv.ParseResult(captured.String())
	switch {
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
	fmt.Fprintln(env.Stderr, "forge: the push was accepted but no result came back; the run may still be going")
	return exitcode.Unreachable
}

func refusalCode(output string) int {
	lower := strings.ToLower(output)
	for _, marker := range []string{"authentication failed", "a forge token is required", "401"} {
		if strings.Contains(lower, marker) {
			return exitcode.Unauthorized
		}
	}
	if strings.Contains(lower, "could not resolve host") || strings.Contains(lower, "failed to connect") {
		return exitcode.Unreachable
	}
	return exitcode.InvalidSpec
}

// The token is the password, so it goes in the URL rather than an argument that
// would sit in the process list of whatever else is on this machine.
func pushURL(endpoint, repo, secret string) (string, error) {
	u, err := url.Parse(strings.TrimSuffix(endpoint, "/"))
	if err != nil {
		return "", fmt.Errorf("%q is not a usable endpoint: %w", endpoint, err)
	}
	if u.Scheme == "" || u.Host == "" {
		return "", fmt.Errorf("%q needs a scheme and a host, like https://ci.example.com", endpoint)
	}
	u.User = url.UserPassword("forge", secret)
	if !strings.HasSuffix(u.Path, "/git") {
		u.Path = strings.TrimSuffix(u.Path, "/") + "/git"
	}
	u.Path += "/" + repo + ".git"
	return u.String(), nil
}
