package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/simon-em/kranq/internal/exitcode"
)

// A fake ssh and scp on PATH, so the whole deploy path is exercised without a
// second machine. Each invocation appends its arguments to a log the test reads.
type fakeSSH struct {
	dir         string
	log         string
	kranqExists bool
	arch        string
}

func withFakeSSH(t *testing.T, arch string, kranqExists bool) *fakeSSH {
	t.Helper()
	dir := t.TempDir()
	f := &fakeSSH{dir: dir, log: filepath.Join(dir, "log"), kranqExists: kranqExists, arch: arch}

	kranqReply := "exit 127"
	if kranqExists {
		kranqReply = `echo '{"version":"0.1.0"}'; exit 0`
	}
	ssh := "#!/bin/sh\n" +
		"printf '%s\\n' \"$*\" >> " + f.log + "\n" +
		"last=\"${@: -1}\"\n" +
		"case \"$last\" in\n" +
		"  *'uname -m'*) echo " + arch + "; exit 0 ;;\n" +
		"  *version*) " + kranqReply + " ;;\n" +
		"  *doctor*) echo 'ok everything fine'; exit 0 ;;\n" +
		"esac\n" +
		"exit 0\n"
	if err := os.WriteFile(filepath.Join(dir, "ssh"), []byte(ssh), 0o755); err != nil {
		t.Fatal(err)
	}
	scp := "#!/bin/sh\nprintf 'scp %s\\n' \"$*\" >> " + f.log + "\nexit 0\n"
	if err := os.WriteFile(filepath.Join(dir, "scp"), []byte(scp), 0o755); err != nil {
		t.Fatal(err)
	}

	t.Setenv("PATH", dir+":"+os.Getenv("PATH"))
	t.Setenv("KRANQ_HOME", filepath.Join(dir, "home"))
	return f
}

func (f *fakeSSH) calls(t *testing.T) string {
	t.Helper()
	body, err := os.ReadFile(f.log)
	if err != nil {
		return ""
	}
	return string(body)
}

func addPeer(t *testing.T, args ...string) {
	t.Helper()
	if code, _, errb := invoke(t, append([]string{"peer", "add"}, args...)...); code != exitcode.OK {
		t.Fatalf("peer add: %d %s", code, errb)
	}
}

func TestPeerUpgradeInstallsWhenNoKranqIsThere(t *testing.T) {
	f := withFakeSSH(t, "arm64", false)
	addPeer(t, "mini-1", "--ssh", "macmini@host:333")

	code, _, errb := invoke(t, "peer", "upgrade", "mini-1")
	if code != exitcode.OK {
		t.Fatalf("code %d: %s", code, errb)
	}
	calls := f.calls(t)
	if !strings.Contains(calls, "scp ") {
		t.Fatalf("nothing was copied:\n%s", calls)
	}
	if !strings.Contains(calls, "install") {
		t.Fatalf("a machine with no kranq did not get an install:\n%s", calls)
	}
	if strings.Contains(calls, " upgrade ") {
		t.Fatalf("a machine with no kranq was told to upgrade:\n%s", calls)
	}
}

func TestPeerUpgradeUpgradesAnExistingKranq(t *testing.T) {
	f := withFakeSSH(t, "arm64", true)
	addPeer(t, "mini-1", "--ssh", "macmini@host:333")

	if code, _, errb := invoke(t, "peer", "upgrade", "mini-1"); code != exitcode.OK {
		t.Fatalf("code %d: %s", code, errb)
	}
	calls := f.calls(t)
	if !strings.Contains(calls, "upgrade") || !strings.Contains(calls, "--target") {
		t.Fatalf("no targeted upgrade was issued:\n%s", calls)
	}
	if strings.Contains(calls, "--force") {
		t.Fatalf("--force was passed without being asked for:\n%s", calls)
	}
}

func TestPeerUpgradePassesForceThrough(t *testing.T) {
	f := withFakeSSH(t, "arm64", true)
	addPeer(t, "mini-1", "--ssh", "macmini@host:333")
	if code, _, errb := invoke(t, "peer", "upgrade", "mini-1", "--force"); code != exitcode.OK {
		t.Fatalf("code %d: %s", code, errb)
	}
	if !strings.Contains(f.calls(t), "--force") {
		t.Fatalf("--force did not reach the peer:\n%s", f.calls(t))
	}
}

// The default kranq path starts with ~, and a single-quoted tilde is a literal
// directory named "~" rather than the home directory.
func TestPeerUpgradeLetsTheRemoteTildeExpand(t *testing.T) {
	f := withFakeSSH(t, "arm64", true)
	addPeer(t, "mini-1", "--ssh", "macmini@host:333")
	invoke(t, "peer", "upgrade", "mini-1")

	calls := f.calls(t)
	if strings.Contains(calls, "'~/") {
		t.Fatalf("the remote path was single-quoted, so ~ will not expand:\n%s", calls)
	}
	if !strings.Contains(calls, `"$HOME/.local/bin/kranq"`) {
		t.Fatalf("the remote path did not go through $HOME:\n%s", calls)
	}
}

func TestPeerUpgradeRefusesAnArchitectureMismatch(t *testing.T) {
	f := withFakeSSH(t, "s390x", true)
	addPeer(t, "mini-1", "--ssh", "macmini@host:333")

	code, _, errb := invoke(t, "peer", "upgrade", "mini-1")
	if code == exitcode.OK {
		t.Fatal("a binary was sent to a machine of another architecture")
	}
	if !strings.Contains(errb, "--binary") {
		t.Fatalf("the refusal does not say how to fix it: %s", errb)
	}
	if strings.Contains(f.calls(t), "scp ") {
		t.Fatalf("the binary was copied anyway:\n%s", f.calls(t))
	}
}

func TestPeerCommandsNeedAPeer(t *testing.T) {
	withFakeSSH(t, "arm64", true)
	code, _, errb := invoke(t, "peer", "upgrade")
	if code == exitcode.OK {
		t.Fatal("upgrading with no peers registered succeeded")
	}
	if !strings.Contains(errb, "peer add") {
		t.Fatalf("the error does not say what to do: %s", errb)
	}
}

func TestPeerTestReportsAMachineWithNoKranq(t *testing.T) {
	withFakeSSH(t, "arm64", false)
	addPeer(t, "mini-1", "--ssh", "macmini@host:333")
	code, _, errb := invoke(t, "peer", "test", "mini-1")
	if code != exitcode.MissingDep {
		t.Fatalf("code %d: %s", code, errb)
	}
	if !strings.Contains(errb, "peer upgrade") {
		t.Fatalf("the error does not say how to fix it: %s", errb)
	}
}
