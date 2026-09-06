package sockpath

import (
	"strings"
	"testing"
)

func TestShortPathsAreLeftAlone(t *testing.T) {
	want := "/Users/me/.kranq/agent.sock"
	if got := For(want); got != want {
		t.Errorf("For(%q) = %q, want it unchanged", want, got)
	}
}

func TestALongPathIsShortenedBelowTheMacOSLimit(t *testing.T) {
	long := "/var/folders/59/" + strings.Repeat("deep/", 30) + "agent.sock"
	got := For(long)
	if len(got) > Max {
		t.Fatalf("For returned %d bytes: %q; macOS refuses a unix socket path over 104", len(got), got)
	}
	if got == long {
		t.Error("the long path was not shortened")
	}
}

func TestShorteningIsStableAndDistinct(t *testing.T) {
	a := "/var/folders/" + strings.Repeat("x/", 60) + "agent.sock"
	b := "/var/folders/" + strings.Repeat("y/", 60) + "agent.sock"
	if For(a) != For(a) {
		t.Error("shortening must be deterministic, or a second call starts a second agent")
	}
	if For(a) == For(b) {
		t.Error("two different roots must not collide onto one socket")
	}
}
