package cli

import (
	"testing"

	"github.com/simon-em/kranq/internal/doctor"
)

// doctor reported the health of the shell it was run in, not the daemon's. On
// the build machine the token was exported from .zshrc, so a login shell said
// "claude token present" while every claude task was refused with "the runner
// has no CLAUDE_CODE_OAUTH_TOKEN". They do not read the same environment, and
// the daemon's answer is the one that decides.
func TestClaudeCheckReportsTheDaemonNotTheShell(t *testing.T) {
	// No daemon reachable: fall back to what this process can see.
	if got := claudeCheck(t.Context(), "tok", false); got.Level != doctor.OK {
		t.Errorf("with no daemon and a token here, level = %v, want OK", got.Level)
	}
	if got := claudeCheck(t.Context(), "", false); got.Level != doctor.Warn {
		t.Errorf("with no daemon and no token, level = %v, want a warning", got.Level)
	}
}

// The wording has to name the difference, or the reader fixes the shell again.
func TestTheWarningSaysWhereTheTokenIsMissingFrom(t *testing.T) {
	got := absentClaude()
	if got.Level != doctor.Warn || got.Fix == "" {
		t.Errorf("check = %+v, want a warning with a fix", got)
	}
}
