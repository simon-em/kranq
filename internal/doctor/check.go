package doctor

import (
	"fmt"
	"io/fs"
	"regexp"
	"strconv"
	"strings"
)

type Level int

const (
	OK Level = iota
	Warn
	Fail
)

func (l Level) String() string {
	switch l {
	case OK:
		return "ok"
	case Warn:
		return "warn"
	}
	return "fail"
}

type Check struct {
	Name   string
	Level  Level
	Detail string
	Fix    string
}

func ok(name, format string, args ...any) Check {
	return Check{Name: name, Level: OK, Detail: fmt.Sprintf(format, args...)}
}

func warn(name, fix, format string, args ...any) Check {
	return Check{Name: name, Level: Warn, Detail: fmt.Sprintf(format, args...), Fix: fix}
}

func fail(name, fix, format string, args ...any) Check {
	return Check{Name: name, Level: Fail, Detail: fmt.Sprintf(format, args...), Fix: fix}
}

func Worst(checks []Check) Level {
	worst := OK
	for _, c := range checks {
		if c.Level > worst {
			worst = c.Level
		}
	}
	return worst
}

// macOS caps a unix socket path at 104 bytes including the terminator, and the
// failure mode is a bind error nobody connects to path length.
const MaxSocketPath = 103

func SocketPath(path string) Check {
	const name = "socket path"
	if len(path) > MaxSocketPath {
		return fail(name, "set FORGE_HOME to something shorter",
			"%s is %d bytes; macOS refuses to bind a unix socket longer than %d",
			path, len(path), MaxSocketPath)
	}
	if len(path) > MaxSocketPath-16 {
		return warn(name, "consider a shorter FORGE_HOME",
			"%s is %d bytes, within %d of the %d-byte limit",
			path, len(path), MaxSocketPath-len(path), MaxSocketPath)
	}
	return ok(name, "%d of %d bytes", len(path), MaxSocketPath)
}

func Permissions(name, path string, mode fs.FileMode, want fs.FileMode) Check {
	got := mode.Perm()
	if got&^want != 0 {
		return fail(name, fmt.Sprintf("chmod %04o %s", want, path),
			"%s is %04o, which is wider than %04o", path, got, want)
	}
	return ok(name, "%s is %04o", path, got)
}

var gitVersionPattern = regexp.MustCompile(`git version (\d+)\.(\d+)`)

// --atomic arrived in git 2.4; the fence is unsafe without it.
func GitVersion(output string) Check {
	const name = "git"
	m := gitVersionPattern.FindStringSubmatch(output)
	if m == nil {
		return fail(name, "install git", "could not read a version out of %q", strings.TrimSpace(output))
	}
	major, _ := strconv.Atoi(m[1])
	minor, _ := strconv.Atoi(m[2])
	if major < 2 || (major == 2 && minor < 4) {
		return fail(name, "upgrade git to 2.4 or newer",
			"git %d.%d has no --atomic push, so fenced tasks cannot be run safely", major, minor)
	}
	return ok(name, "%d.%d", major, minor)
}

// forge invokes its own limactl by absolute path so a Homebrew lima appearing or
// disappearing cannot change what runs.
func LimaSource(binary, root string) Check {
	const name = "lima"
	if binary == "" {
		return fail(name, "forge install", "no limactl; forge installs its own under %s/deps", root)
	}
	if !strings.HasPrefix(binary, root) {
		return warn(name, "forge install", "using %s, which forge does not manage", binary)
	}
	return ok(name, "%s", binary)
}

func DiskSpace(free, want int64) Check {
	const name = "disk"
	switch {
	case free <= 0:
		return warn(name, "", "could not measure free space")
	case free < want:
		return fail(name, "free some space or prune images with `forge image prune`",
			"%s free, and one image pair needs about %s", human(free), human(want))
	case free < want*2:
		return warn(name, "`forge image prune` reclaims old layers",
			"%s free, enough for about one image pair", human(free))
	}
	return ok(name, "%s free", human(free))
}

func human(n int64) string {
	const gib = 1 << 30
	if n >= gib {
		return fmt.Sprintf("%.0fGiB", float64(n)/gib)
	}
	return fmt.Sprintf("%dMiB", n/(1<<20))
}

func OrphanVMs(vms, owned []string) Check {
	const name = "orphan vms"
	live := map[string]bool{}
	for _, n := range owned {
		live[n] = true
	}
	var orphans []string
	for _, n := range vms {
		if !live[n] {
			orphans = append(orphans, n)
		}
	}
	if len(orphans) == 0 {
		return ok(name, "none")
	}
	return warn(name, "`forge vm rm "+orphans[0]+"` for each, once you have looked at them",
		"%d job VM(s) belong to no live task: %s", len(orphans), strings.Join(orphans, ", "))
}

func VersionMatch(cli, daemon string) Check {
	const name = "daemon version"
	if daemon == "" {
		return warn(name, "forge daemon start", "the daemon is not running")
	}
	if cli != daemon {
		return warn(name, "forge daemon restart", "cli is %s, the running daemon is %s", cli, daemon)
	}
	return ok(name, "%s", daemon)
}
