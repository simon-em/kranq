package selfinstall

import (
	"strings"
	"testing"
)

func TestPlistCarriesTheDaemonCommandAndHome(t *testing.T) {
	p := Service{Binary: "/Users/me/.local/bin/kranq", Home: "/Users/me/.kranq"}.Plist()
	for _, want := range []string{
		"<string>/Users/me/.local/bin/kranq</string>",
		"<string>daemon</string>",
		"<string>run</string>",
		"<key>KRANQ_HOME</key>",
		"<string>/Users/me/.kranq</string>",
		"/Users/me/.kranq/daemon.log",
	} {
		if !strings.Contains(p, want) {
			t.Errorf("plist is missing %q:\n%s", want, p)
		}
	}
}

func TestPlistRestartsOnCrashButNotOnACleanExit(t *testing.T) {
	p := Service{Binary: "/f", Home: "/h"}.Plist()
	if !strings.Contains(p, "<key>SuccessfulExit</key>\n    <false/>") {
		t.Errorf("KeepAlive must not restart after a clean stop, or `kranq daemon stop` fights launchd:\n%s", p)
	}
}

func TestThePlistCannotCarryASecret(t *testing.T) {
	p := Service{Binary: "/f", Home: "/h", Path: "/usr/bin"}.Plist()
	if strings.Contains(strings.ToLower(p), "token") || strings.Contains(strings.ToLower(p), "secret") {
		t.Errorf("~/Library/LaunchAgents is world-readable, so the plist must carry no credential "+
			"at all; the daemon reads them from a 0600 env file instead:\n%s", p)
	}
	if !strings.Contains(p, "<key>KRANQ_HOME</key>") {
		t.Error("the plist still needs to say where state lives")
	}
}

func TestPlistEscapesXML(t *testing.T) {
	p := Service{Binary: "/path/with & <angle>", Home: "/h"}.Plist()
	if strings.Contains(p, "with & <angle>") {
		t.Errorf("unescaped XML would produce an unparseable plist:\n%s", p)
	}
	if !strings.Contains(p, "&amp;") || !strings.Contains(p, "&lt;angle&gt;") {
		t.Errorf("expected escaped output:\n%s", p)
	}
}
