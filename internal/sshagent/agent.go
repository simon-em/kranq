package sshagent

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/simon-em/kranq/internal/sockpath"
)

var sockPattern = regexp.MustCompile(`SSH_AUTH_SOCK=([^;]+);`)

func DefaultKeys() []string {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}
	var keys []string
	if v := os.Getenv("KRANQ_SSH_KEY_FILE"); v != "" {
		keys = append(keys, v)
	}
	for _, name := range []string{"id_ed25519", "id_rsa", "id_ecdsa"} {
		keys = append(keys, filepath.Join(home, ".ssh", name))
	}
	return keys
}

func hasIdentities(sock string) bool {
	cmd := exec.Command("ssh-add", "-l")
	cmd.Env = append(os.Environ(), "SSH_AUTH_SOCK="+sock)
	return cmd.Run() == nil
}

func Ensure(root string) (string, error) {
	if sock := os.Getenv("SSH_AUTH_SOCK"); sock != "" && hasIdentities(sock) {
		return sock, nil
	}
	sock := sockpath.For(filepath.Join(root, "agent.sock"))
	if hasIdentities(sock) {
		return sock, nil
	}
	if err := os.MkdirAll(filepath.Dir(sock), 0o700); err != nil {
		return "", err
	}
	_ = os.Remove(sock)
	if out, err := exec.Command("ssh-agent", "-a", sock).CombinedOutput(); err != nil {
		return "", errors.New("could not start an ssh agent: " + strings.TrimSpace(string(out)))
	}
	for _, key := range DefaultKeys() {
		if _, err := os.Stat(key); err != nil {
			continue
		}
		cmd := exec.Command("ssh-add", key)
		cmd.Env = append(os.Environ(), "SSH_AUTH_SOCK="+sock)
		cmd.Stdin = strings.NewReader("")
		if cmd.Run() == nil && hasIdentities(sock) {
			return sock, nil
		}
	}
	if hasIdentities(sock) {
		return sock, nil
	}
	return "", errors.New("no ssh identity is available: forward an agent, set KRANQ_SSH_KEY_FILE, or forward a token in the task env")
}
