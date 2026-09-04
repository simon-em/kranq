package gate

import (
	"path/filepath"
	"testing"
	"time"
)

const retry = time.Minute

func gateAt(t *testing.T, path, token string, clock *time.Time) *Gate {
	t.Helper()
	g := New(path, token, retry)
	g.now = func() time.Time { return *clock }
	return g
}

func TestAFreshGateWithATokenIsOpen(t *testing.T) {
	now := time.Now()
	g := gateAt(t, filepath.Join(t.TempDir(), "gate.json"), "tok", &now)
	if !g.Available() || !g.Present() {
		t.Error("a fresh gate with a token must be open")
	}
}

func TestExhaustionRetriesAtAFixedInterval(t *testing.T) {
	now := time.Now()
	g := gateAt(t, filepath.Join(t.TempDir(), "gate.json"), "tok", &now)
	for range 5 {
		until := g.MarkExhausted()
		if got := until.Sub(now); got != retry {
			t.Fatalf("next check in %v, want a flat %v every time; usage can return at any moment, "+
				"so doubling to hours leaves the machine idle long after it could have run", got, retry)
		}
		now = now.Add(retry)
	}
}

func TestTheGateReopensWhenTheIntervalElapses(t *testing.T) {
	now := time.Now()
	g := gateAt(t, filepath.Join(t.TempDir(), "gate.json"), "tok", &now)
	g.MarkExhausted()
	now = now.Add(retry - time.Second)
	if g.Available() {
		t.Error("the gate opened early")
	}
	now = now.Add(2 * time.Second)
	if !g.Available() {
		t.Error("the gate did not reopen after the retry interval")
	}
}

func TestAKnownResetTimeIsPreferredOverPollingBlindly(t *testing.T) {
	now := time.Now()
	g := gateAt(t, filepath.Join(t.TempDir(), "gate.json"), "tok", &now)
	resets := now.Add(2 * time.Hour)

	until := g.MarkExhaustedUntil(resets, "five_hour window exhausted")
	if !until.Equal(resets) {
		t.Errorf("next check at %v, want %v: when claude tells us when the window resets, "+
			"retrying every minute until then just burns VM boots", until, resets)
	}
	if got := g.State().LastReason; got != "five_hour window exhausted" {
		t.Errorf("reason = %q, want it surfaced so an operator knows why", got)
	}
}

func TestAResetTimeInThePastDoesNotShortenTheInterval(t *testing.T) {
	now := time.Now()
	g := gateAt(t, filepath.Join(t.TempDir(), "gate.json"), "tok", &now)
	until := g.MarkExhaustedUntil(now.Add(-time.Hour), "stale")
	if got := until.Sub(now); got != retry {
		t.Errorf("next check in %v, want the %v floor; a stale reset time must not cause a hot loop", got, retry)
	}
}

func TestACleanRunReopensTheGate(t *testing.T) {
	now := time.Now()
	g := gateAt(t, filepath.Join(t.TempDir(), "gate.json"), "tok", &now)
	g.MarkExhausted()
	g.MarkHealthy()
	if !g.Available() || g.State().LastReason != "" {
		t.Error("a clean run must reopen the gate and clear the reason")
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
	gateAt(t, path, "old-token", &now).MarkExhaustedUntil(now.Add(6*time.Hour), "exhausted")

	rotated := gateAt(t, path, "new-token", &now)
	if !rotated.Available() {
		t.Error("a new token must get a fresh gate; honouring the old token's wait left tasks idle " +
			"for forty minutes with usage available")
	}
}

func TestResetForcesTheGateOpen(t *testing.T) {
	now := time.Now()
	g := gateAt(t, filepath.Join(t.TempDir(), "gate.json"), "tok", &now)
	g.MarkExhaustedUntil(now.Add(6*time.Hour), "exhausted")
	g.Reset()
	if !g.Available() || g.State().Exhaustions != 0 {
		t.Error("Reset must fully clear the gate, so an operator can act on knowledge forge does not have")
	}
}

func TestNoTokenMeansNotPresent(t *testing.T) {
	now := time.Now()
	g := gateAt(t, filepath.Join(t.TempDir(), "gate.json"), "", &now)
	if g.Present() {
		t.Error("Present must be false with no token, so a claude task is rejected at submit rather than later")
	}
	if TokenID("") != "" {
		t.Error("an absent token has no id")
	}
}

func TestTokenIDDoesNotLeakTheToken(t *testing.T) {
	id := TokenID("sk-ant-oat01-supersecret")
	if len(id) != 16 || id == "sk-ant-oat01-supersecret" {
		t.Errorf("TokenID = %q, want a short hash", id)
	}
}
