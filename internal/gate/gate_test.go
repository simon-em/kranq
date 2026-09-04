package gate

import (
	"path/filepath"
	"testing"
	"time"
)

const (
	base = 10 * time.Minute
	max  = 6 * time.Hour
)

func gateAt(t *testing.T, path, token string, clock *time.Time) *Gate {
	g := New(path, token, base, max)
	g.now = func() time.Time { return *clock }
	return g
}

func TestAGateWithNoExhaustionIsOpen(t *testing.T) {
	now := time.Now()
	g := gateAt(t, filepath.Join(t.TempDir(), "gate.json"), "tok", &now)
	if !g.Available() || !g.Present() {
		t.Error("a fresh gate with a token must be open")
	}
}

func TestBackoffDoublesAndIsCapped(t *testing.T) {
	now := time.Now()
	g := gateAt(t, filepath.Join(t.TempDir(), "gate.json"), "tok", &now)
	for _, want := range []time.Duration{base, 2 * base, 4 * base} {
		g.MarkExhausted()
		if got := g.State().Backoff; got != want {
			t.Fatalf("backoff = %v, want %v", got, want)
		}
	}
	for range 20 {
		g.MarkExhausted()
	}
	if got := g.State().Backoff; got != max {
		t.Errorf("backoff = %v, want it capped at %v", got, max)
	}
}

func TestAHealthyRunResetsTheBackoff(t *testing.T) {
	now := time.Now()
	g := gateAt(t, filepath.Join(t.TempDir(), "gate.json"), "tok", &now)
	g.MarkExhausted()
	if g.Available() {
		t.Fatal("the gate should be shut")
	}
	g.MarkHealthy()
	if !g.Available() || g.State().Backoff != 0 {
		t.Error("a clean run must reopen the gate and clear the backoff")
	}
}

func TestTheGateReopensOnItsOwnWhenTheBackoffElapses(t *testing.T) {
	now := time.Now()
	g := gateAt(t, filepath.Join(t.TempDir(), "gate.json"), "tok", &now)
	g.MarkExhausted()
	now = now.Add(base - time.Second)
	if g.Available() {
		t.Error("the gate opened early")
	}
	now = now.Add(2 * time.Second)
	if !g.Available() {
		t.Error("the gate did not reopen after the backoff")
	}
}

func TestAClosedGateSurvivesARestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "gate.json")
	now := time.Now()
	gateAt(t, path, "tok", &now).MarkExhausted()

	restarted := gateAt(t, path, "tok", &now)
	if restarted.Available() {
		t.Error("a restart silently reopened a genuinely closed gate, which burns a VM start to rediscover it")
	}
	if restarted.State().Exhaustions != 1 {
		t.Errorf("exhaustions = %d, want the count to survive", restarted.State().Exhaustions)
	}
}

func TestRotatingTheTokenOpensAFreshGate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "gate.json")
	now := time.Now()
	gateAt(t, path, "old-token", &now).MarkExhausted()

	rotated := gateAt(t, path, "new-token", &now)
	if !rotated.Available() {
		t.Error("a new token must get a fresh gate; honouring the old token's backoff left tasks idle for 40 minutes with usage available")
	}
	if rotated.State().Backoff != 0 {
		t.Error("the old backoff carried over to the new token")
	}
}

func TestResetForcesTheGateOpen(t *testing.T) {
	now := time.Now()
	g := gateAt(t, filepath.Join(t.TempDir(), "gate.json"), "tok", &now)
	g.MarkExhausted()
	g.Reset()
	if !g.Available() || g.State().Exhaustions != 0 {
		t.Error("Reset must fully clear the gate, so an operator can act on knowledge forge does not have")
	}
}

func TestNoTokenMeansNotPresent(t *testing.T) {
	now := time.Now()
	g := gateAt(t, filepath.Join(t.TempDir(), "gate.json"), "", &now)
	if g.Present() {
		t.Error("Present must be false with no token, so a claude task is rejected at submit rather than failing later")
	}
	if TokenID("") != "" {
		t.Error("an absent token has no id")
	}
}

func TestTokenIDDoesNotLeakTheToken(t *testing.T) {
	id := TokenID("sk-ant-oat01-supersecret")
	if len(id) != 16 {
		t.Errorf("TokenID = %q, want 16 hex chars", id)
	}
	if id == "sk-ant-oat01-supersecret" {
		t.Error("TokenID returned the token itself")
	}
}
