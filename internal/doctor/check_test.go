package doctor

import (
	"strings"
	"testing"
)

func TestSocketPathAgainstTheMacOSLimit(t *testing.T) {
	cases := []struct {
		name string
		n    int
		want Level
	}{
		{"short", 40, OK},
		{"close", MaxSocketPath - 4, Warn},
		{"over", MaxSocketPath + 1, Fail},
		{"exactly at the limit", MaxSocketPath, Warn},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := SocketPath(strings.Repeat("x", c.n)).Level; got != c.want {
				t.Fatalf("%d bytes: got %v, want %v", c.n, got, c.want)
			}
		})
	}
}

func TestPermissionsRejectAnythingWider(t *testing.T) {
	if got := Permissions("env", "/x", 0o600, 0o600).Level; got != OK {
		t.Fatalf("0600 against 0600: %v", got)
	}
	if got := Permissions("env", "/x", 0o644, 0o600).Level; got != Fail {
		t.Fatalf("a world-readable secret was accepted: %v", got)
	}
	if got := Permissions("env", "/x", 0o400, 0o600).Level; got != OK {
		t.Fatalf("narrower than required should pass: %v", got)
	}
}

func TestGitVersionGatesOnAtomicPushSupport(t *testing.T) {
	cases := []struct {
		out  string
		want Level
	}{
		{"git version 2.50.1 (Apple Git-155)", OK},
		{"git version 2.4.0", OK},
		{"git version 2.3.9", Fail},
		{"git version 1.9.5", Fail},
		{"not a version at all", Fail},
	}
	for _, c := range cases {
		if got := GitVersion(c.out).Level; got != c.want {
			t.Fatalf("%q: got %v, want %v", c.out, got, c.want)
		}
	}
}

func TestGitVersionSaysWhyItMatters(t *testing.T) {
	c := GitVersion("git version 2.3.0")
	if !strings.Contains(c.Detail, "--atomic") {
		t.Fatalf("the failure does not explain the consequence: %q", c.Detail)
	}
}

func TestLimaSourceFlagsALimaForgeDoesNotOwn(t *testing.T) {
	root := "/Users/x/.forge"
	if got := LimaSource(root+"/deps/current/bin/limactl", root).Level; got != OK {
		t.Fatalf("forge's own lima: %v", got)
	}
	if got := LimaSource("/opt/homebrew/bin/limactl", root).Level; got != Warn {
		t.Fatalf("a homebrew lima should warn: %v", got)
	}
	if got := LimaSource("", root).Level; got != Fail {
		t.Fatalf("no lima at all should fail: %v", got)
	}
}

func TestDiskSpace(t *testing.T) {
	const pair = 24 << 30
	if got := DiskSpace(200<<30, pair).Level; got != OK {
		t.Fatalf("plenty of space: %v", got)
	}
	if got := DiskSpace(30<<30, pair).Level; got != Warn {
		t.Fatalf("room for one pair: %v", got)
	}
	if got := DiskSpace(5<<30, pair).Level; got != Fail {
		t.Fatalf("not enough for one pair: %v", got)
	}
	if got := DiskSpace(0, pair).Level; got != Warn {
		t.Fatalf("an unmeasurable disk should warn, not fail: %v", got)
	}
}

func TestOrphanVMsAreTheOnesNoTaskOwns(t *testing.T) {
	c := OrphanVMs([]string{"forge-run-dx-spec-aa", "forge-run-dx-spec-bb"}, []string{"forge-run-dx-spec-aa"})
	if c.Level != Warn {
		t.Fatalf("an orphan should warn: %v", c.Level)
	}
	if !strings.Contains(c.Detail, "forge-run-dx-spec-bb") {
		t.Fatalf("the orphan is not named: %q", c.Detail)
	}
	if strings.Contains(c.Detail, "forge-run-dx-spec-aa") {
		t.Fatalf("an owned VM was reported as an orphan: %q", c.Detail)
	}
	if OrphanVMs([]string{"forge-run-dx-spec-aa"}, []string{"forge-run-dx-spec-aa"}).Level != OK {
		t.Fatal("a fully owned set should pass")
	}
}

func TestVersionMatch(t *testing.T) {
	if VersionMatch("0.1.0", "0.1.0").Level != OK {
		t.Fatal("matching versions should pass")
	}
	if VersionMatch("0.2.0", "0.1.0").Level != Warn {
		t.Fatal("a stale daemon should warn")
	}
	if VersionMatch("0.1.0", "").Level != Warn {
		t.Fatal("no daemon should warn")
	}
}

func TestWorstIsTheReportedOutcome(t *testing.T) {
	checks := []Check{ok("a", ""), warn("b", "", ""), ok("c", "")}
	if Worst(checks) != Warn {
		t.Fatal("a warning must not be lost among passes")
	}
	if Worst(append(checks, fail("d", "", ""))) != Fail {
		t.Fatal("a failure must dominate")
	}
	if Worst(nil) != OK {
		t.Fatal("no checks is not a failure")
	}
}
