package gate

import (
	"testing"
	"time"
)

func TestParseExhaustionFindsTheResetTime(t *testing.T) {
	log := "[00:04] usage rejected, five-hour window at 100%\n" +
		"[00:04] KRANQ-GATE exhausted resets_at=1788468000 window=five_hour\n"
	at, window, found := ParseExhaustion(log)
	if !found || window != "five_hour" {
		t.Fatalf("found=%v window=%q", found, window)
	}
	if !at.Equal(time.Unix(1788468000, 0)) {
		t.Errorf("resets at %v, want the unix time from the stream", at)
	}
}

func TestParseExhaustionIgnoresAHealthyLog(t *testing.T) {
	if _, _, found := ParseExhaustion("[00:04] usage allowed, five-hour window at 27%\n"); found {
		t.Error("a healthy run must not look like exhaustion; every stream carries a rate_limit_event")
	}
}

func TestParseExhaustionTakesTheLastOccurrence(t *testing.T) {
	log := "KRANQ-GATE exhausted resets_at=100 window=five_hour\n" +
		"KRANQ-GATE exhausted resets_at=200 window=seven_day\n"
	at, window, _ := ParseExhaustion(log)
	if !at.Equal(time.Unix(200, 0)) || window != "seven_day" {
		t.Errorf("got %v %q, want the most recent", at, window)
	}
}

func TestParseExhaustionToleratesAMissingTimestamp(t *testing.T) {
	at, _, found := ParseExhaustion("KRANQ-GATE exhausted resets_at=0 window=unknown\n")
	if !found {
		t.Fatal("exhaustion should still be detected")
	}
	if !at.IsZero() {
		t.Errorf("resets = %v, want zero so the caller falls back to polling", at)
	}
}
