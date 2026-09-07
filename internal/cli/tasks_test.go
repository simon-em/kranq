package cli

import (
	"testing"

	"github.com/simon-em/kranq/internal/state"
)

// A run that named no repository is identified by the commit it arrived as,
// which is the truer answer anyway: it says exactly what ran, where a name
// borrowed from the repository the objects landed in says nothing at all.
func TestARunWithNoNameIsShownByItsCommit(t *testing.T) {
	got := source(state.Task{SourceCommit: "34a81a770e5244b86e5b9cd3e845190d5beea4c7"})
	if got != "34a81a770e52" {
		t.Errorf("source = %q, want the short commit", got)
	}
	if got := source(state.Task{Repo: "dx", SourceCommit: "34a81a770e52"}); got != "dx" {
		t.Errorf("source = %q, want the name when there is one", got)
	}
	if got := source(state.Task{}); got != "-" {
		t.Errorf("source = %q, want a dash rather than an empty column", got)
	}
}
