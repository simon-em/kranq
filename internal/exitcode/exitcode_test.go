package exitcode

import "testing"

func TestFromTaskPassesThroughOrdinaryCodes(t *testing.T) {
	for _, code := range []int{0, 1, 2, 63} {
		if got := FromTask(code); got != code {
			t.Errorf("FromTask(%d) = %d, want it verbatim", code, got)
		}
	}
}

func TestFromTaskClampsTheReservedBand(t *testing.T) {
	for _, code := range []int{64, 75, 126, 127} {
		if got := FromTask(code); got != 1 {
			t.Errorf("FromTask(%d) = %d, want 1 so it cannot be mistaken for a forge failure", code, got)
		}
	}
}

func TestFromTaskLeavesCodesAboveTheBandAlone(t *testing.T) {
	if got := FromTask(137); got != 137 {
		t.Errorf("FromTask(137) = %d, want it verbatim", got)
	}
}
