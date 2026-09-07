package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/simon-em/kranq/internal/exitcode"
)

// A known_hosts entry for a non-default port is written [host]:port. In the
// plain form ssh simply never finds it -- it looks up the port form -- and the
// failure reads as an unknown host rather than as a malformed file.
func TestAHostKeyForACustomPortIsBracketed(t *testing.T) {
	key := filepath.Join(t.TempDir(), "host.pub")
	const data = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIOrSkyMg root@buildhost\n"
	if err := os.WriteFile(key, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
	got := hostKeyFrom(key, "142.127.69.2", 333)
	want := "[142.127.69.2]:333 ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIOrSkyMg"
	if got != want {
		t.Errorf("host key line = %q, want %q", got, want)
	}
	// The comment must not survive: ssh reads the third field as the start of
	// the key options on some lines, and it names the wrong host anyway.
	if strings.Contains(got, "root@buildhost") {
		t.Error("the comment was carried into the known_hosts line")
	}
	if got := hostKeyFrom(key, "192.0.2.1", 22); !strings.HasPrefix(got, "192.0.2.1 ssh-ed25519 ") {
		t.Errorf("host key line = %q, want the plain form on 22", got)
	}
	if got := hostKeyFrom(filepath.Join(t.TempDir(), "absent"), "h", 22); got != "" {
		t.Errorf("a missing host key produced %q, want nothing rather than a broken line", got)
	}
}

// The address is given as host:port as often as it is given as two flags,
// because that is how it is written everywhere else -- KRANQ_PEER included.
func TestTheAddressCanCarryItsUserAndPort(t *testing.T) {
	cases := map[string]struct {
		login, host string
		port        int
	}{
		"macmini@142.127.69.2:333": {"macmini", "142.127.69.2", 333},
		"142.127.69.2:333":         {"", "142.127.69.2", 333},
		"macmini@buildhost":        {"macmini", "buildhost", 0},
		"buildhost":                {"", "buildhost", 0},
		"buildhost:":               {"", "buildhost:", 0},
		"buildhost:nope":           {"", "buildhost:nope", 0},
	}
	for in, want := range cases {
		login, host, port := splitAddr(in)
		if login != want.login || host != want.host || port != want.port {
			t.Errorf("splitAddr(%q) = %q, %q, %d; want %q, %q, %d",
				in, login, host, port, want.login, want.host, want.port)
		}
	}
}

// Given nothing, the address still has to come out usable: this account, this
// hostname, and a port that is admitted to be a guess.
func TestAnAddressGivenNothingSaysSoAboutThePort(t *testing.T) {
	got, guessed := resolveAddr("", 0)
	if !guessed {
		t.Error("the port was not flagged as a guess, so a forwarded port would be printed as fact")
	}
	if got.Login == "" || got.Host == "" || got.Port == 0 {
		t.Errorf("addr = %+v, want every field filled", got)
	}
	if _, guessed := resolveAddr("host:333", 0); guessed {
		t.Error("a port given in the address was treated as a guess")
	}
	if got, _ := resolveAddr("host", 333); got.Port != 333 {
		t.Errorf("--port was ignored: %+v", got)
	}
}

// A push is ssh, so the address has to name the account the key was installed
// under. Without it ssh uses whoever is running the pipeline, which on a CI
// container is root or a build user and not that account.
func TestThePeerAddressNamesTheAccount(t *testing.T) {
	var out, msg strings.Builder
	writeVars(Env{Stdout: &out, Stderr: &msg}, addr{"macmini", "142.127.69.2", 333}, "")
	if !strings.Contains(out.String(), "KRANQ_PEER=macmini@142.127.69.2:333") {
		t.Errorf("vars = %q, want a peer with the account in it", out.String())
	}
}

// Everything is optional, so the no-argument form has to produce a usable
// address. SSH_CONNECTION names one somebody genuinely reached this machine on,
// which beats a hostname that may resolve nowhere.
func TestTheAddressFallsBackToHowYouGotHere(t *testing.T) {
	t.Setenv("SSH_CONNECTION", "142.127.168.209 30743 192.168.2.55 22")
	got, guessed := resolveAddr("", 0)
	if got.Host != "192.168.2.55" || got.Port != 22 {
		t.Errorf("addr = %+v, want the server side of SSH_CONNECTION", got)
	}
	if !guessed {
		t.Error("the address was not flagged as a guess; a forwarded port would be printed as fact")
	}
	// The client's half is somebody else's address entirely.
	if got.Host == "142.127.168.209" {
		t.Error("the client address was used as the server's")
	}
}

func TestAGivenAddressIsNeverOverriddenByTheConnection(t *testing.T) {
	t.Setenv("SSH_CONNECTION", "1.2.3.4 5 192.168.2.55 22")
	got, guessed := resolveAddr("macmini@142.127.69.2:333", 0)
	if got != (addr{"macmini", "142.127.69.2", 333}) {
		t.Errorf("addr = %+v, want what was asked for", got)
	}
	if guessed {
		t.Error("an address given in full was reported as a guess")
	}
}

// The point of --peer: the registry already holds the address, so there is
// nothing to type and nothing to get wrong.
func TestSettingUpAPeerSendsItsOwnAddress(t *testing.T) {
	got := peerSetupCommand("~/.local/bin/kranq", "macmini@142.127.69.2:333", "ci-dx", false)
	// A leading ~ is expanded by the remote shell, not quoted away.
	want := `"$HOME/.local/bin/kranq" setup 'ci-dx' --host 'macmini@142.127.69.2:333'`
	if got != want {
		t.Errorf("remote command =\n  %s\nwant\n  %s", got, want)
	}
	if !strings.HasSuffix(peerSetupCommand("~/.local/bin/kranq", "h", "ci", true), " --key-only") {
		t.Error("--key-only did not reach the far side")
	}
	// No name means no key, and an empty positional would not say that.
	if got := peerSetupCommand("~/.local/bin/kranq", "h", "", false); strings.Contains(got, "''") {
		t.Errorf("remote command = %s, want no empty argument", got)
	}
}

// Making a machine ready and issuing a credential are different requests. A
// private key printed to a terminal that did not ask for one is scrollback
// nobody wanted, and on a single node the address is already known to whoever
// is standing on it.
func TestSetupIssuesNoCredentialUnlessAKeyIsNamed(t *testing.T) {
	code, out, msg := invoke(t, "setup", "--key-only")
	if code != exitcode.Usage {
		t.Errorf("exit = %d, want a usage error when --key has no name", code)
	}
	if strings.Contains(out+msg, "PRIVATE KEY") {
		t.Error("a private key was printed")
	}
	if !strings.Contains(msg, "name the key") {
		t.Errorf("the error does not say what to do:\n%s", msg)
	}
}

// The closing note describes what was actually printed. It used to promise a
// private key "printed once and kept nowhere" even when the caller had supplied
// the public half and kranq never held one.
func TestTheNoteMatchesWhatWasPrinted(t *testing.T) {
	var out, msg strings.Builder
	writeVars(Env{Stdout: &out, Stderr: &msg}, addr{"macmini", "192.0.2.1", 333}, "")
	if strings.Contains(out.String(), "KRANQ_SSH_KEY") {
		t.Error("a KRANQ_SSH_KEY was emitted for a key kranq did not generate")
	}
	if strings.Contains(msg.String(), "kept nowhere") {
		t.Errorf("the note promises a private key that was never printed:\n%s", msg.String())
	}
	if !strings.Contains(msg.String(), "kranq never had") {
		t.Errorf("the note does not say the key was supplied:\n%s", msg.String())
	}

	out.Reset()
	msg.Reset()
	writeVars(Env{Stdout: &out, Stderr: &msg}, addr{"macmini", "192.0.2.1", 333}, "-----BEGIN OPENSSH PRIVATE KEY-----\n")
	if !strings.Contains(out.String(), "KRANQ_SSH_KEY<<EOF") {
		t.Error("the generated key was not emitted as a heredoc")
	}
	if !strings.Contains(msg.String(), "kept nowhere") {
		t.Error("the note does not say the key is not stored")
	}
}
