package sshagent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEnsureReusesAWorkingForwardedAgent(t *testing.T) {
	root := t.TempDir()
	sock, err := Ensure(root)
	if err != nil {
		t.Skipf("no ssh identity on this machine: %v", err)
	}
	again, err := Ensure(root)
	if err != nil {
		t.Fatalf("second Ensure failed: %v", err)
	}
	if again != sock {
		t.Errorf("Ensure returned %q then %q; it must not start a new agent each time", sock, again)
	}
}

func TestEnsureExplainsItselfWhenThereIsNothingToUse(t *testing.T) {
	t.Setenv("SSH_AUTH_SOCK", "")
	t.Setenv("HOME", t.TempDir())
	t.Setenv("FORGE_SSH_KEY_FILE", filepath.Join(t.TempDir(), "absent"))
	_, err := Ensure(t.TempDir())
	if err == nil {
		t.Skip("this machine has an agent with identities in the ambient environment")
	}
	for _, want := range []string{"forward an agent", "token"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q should mention %q, since forwarding a token is the other way out", err, want)
		}
	}
}

func TestDefaultKeysPrefersAnExplicitOne(t *testing.T) {
	t.Setenv("FORGE_SSH_KEY_FILE", "/custom/key")
	keys := DefaultKeys()
	if len(keys) == 0 || keys[0] != "/custom/key" {
		t.Errorf("DefaultKeys = %v, want the explicit key first", keys)
	}
	if _, err := os.Stat("/custom/key"); err == nil {
		t.Skip("unexpected")
	}
}
