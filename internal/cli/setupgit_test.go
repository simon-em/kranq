package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
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
