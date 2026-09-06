package run

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

func HostCheckout(ctx context.Context, remote Remote, ref, dest string) error {
	url := remote.sshURL()
	args := []string{"clone", "--quiet", "--depth", "1", "--branch", ref}
	if remote.Token != "" {
		url = remote.httpsURL()
		args = append([]string{"-c", "credential.helper=",
			"-c", `credential.helper=!f() { echo username=x-token-auth; echo "password=$KRANQ_GIT_TOKEN"; }; f`}, args...)
	}
	cmd := exec.CommandContext(ctx, "git", append(args, url, dest)...)
	cmd.Env = append(os.Environ(), "KRANQ_GIT_TOKEN="+remote.Token, "GIT_TERMINAL_PROMPT=0")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("cloning %s of %s: %w: %s", ref, remote.Repo, err, strings.TrimSpace(string(out)))
	}
	return nil
}
