package gitsrv

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// DefaultTTL is how long a run's commit stays fetchable. Two days covers a
// pipeline that failed on Friday afternoon and is looked at on Monday morning,
// and not much more: these refs hold whole working trees, including artifacts
// that git cannot compress.
const DefaultTTL = 2 * 24 * time.Hour

// Kept are the refs a run leaves behind: the source it was given, the commit
// it produced, and the two legacy namespaces, which are listed only so that
// what is already on disk ages out instead of living forever.
var Kept = []string{
	"refs/kranq/src/", TaskRefPrefix, OKRefPrefix,
	legacyResultPrefix, legacyPassedPrefix,
}

// A run is a branch, and the name you push to is the name you pull from.
//
// It has to be under refs/heads or that symmetry does not exist: git's short
// form resolves against refs/heads, so `git pull kranq task/<run>` finds
// refs/heads/task/<run> and would not find the same run under refs/kranq/*.
// A plain clone does not see refs/kranq/* either.
const (
	TaskRefPrefix = "refs/heads/task/"
	// OKRefPrefix marks a run that passed. A push cannot carry an exit code --
	// post-receive runs after the ref is accepted -- so the verdict is the
	// presence or absence of a ref, and `git fetch` of a missing one exits 128.
	//
	// It is a separate name rather than something under the run's own, because
	// refs/heads/task/<run> and refs/heads/task/<run>/ok cannot both exist.
	OKRefPrefix = "refs/heads/ok/"
	// AnonRef is where a push that has not chosen a run name lands, so that a
	// person can push without inventing one and be told what it was called.
	//
	// "tasks" rather than "task" is the whole reason this works: a ref is a
	// path, so refs/heads/task and refs/heads/task/<run> cannot coexist --
	// tested, git refuses the second with "cannot lock ref 'refs/heads/task':
	// 'refs/heads/task/<run>' exists", and refuses it in the other order too.
	// refs/heads/tasks is a sibling of refs/heads/task/, not a parent.
	AnonRef = "refs/heads/tasks"
)

const (
	legacyResultPrefix = "refs/kranq/result/"
	legacyPassedPrefix = "refs/kranq/passed/"
)

// ResultRefPrefix is where a run's commit is first recorded, keyed by task id
// rather than by run name, because that is the only name the runner knows. The
// hook then publishes it under the run's own name, which is what the pusher
// knows.
const ResultRefPrefix = legacyResultPrefix

// TaskRef and OKRef name the two refs a run publishes.
func TaskRef(run string) string { return TaskRefPrefix + run }
func OKRef(run string) string   { return OKRefPrefix + run }

// ShortRef is what a caller types: git resolves it against refs/heads, so the
// prefix is noise everywhere except inside this package.
func ShortRef(ref string) string { return strings.TrimPrefix(ref, "refs/heads/") }

// RunName is the last segment of the ref a push landed on, which is what a
// client knows in advance: it chose it. Both the result and the pass ref are
// published under it as well as under the task id, because the task id is only
// known afterwards and a pipeline needs the name before it pushes.
func RunName(ref string) string {
	i := strings.LastIndex(ref, "/")
	if i < 0 || i == len(ref)-1 {
		return ""
	}
	return ref[i+1:]
}

// Expired picks the refs to drop. Retention goes by task id, not by commit
// date: the id records when the run happened, while a commit date is whatever
// the job's clock said, and on a src ref it is the date of somebody else's
// commit entirely.
func Expired(refs []string, now time.Time, ttl time.Duration) []string {
	cutoff := now.Add(-ttl).UTC().Format("20060102T150405")
	var out []string
	for _, ref := range refs {
		id := ref[strings.LastIndex(ref, "/")+1:]
		if len(id) < len(cutoff) {
			continue
		}
		if id[:len(cutoff)] < cutoff {
			out = append(out, ref)
		}
	}
	return out
}

// Sweep is called after a push, because that is the moment a repository is
// known to be in use and nothing is reading it yet.
func Sweep(ctx context.Context, gitDir string, now time.Time, ttl time.Duration) ([]string, error) {
	args := append([]string{"--git-dir", gitDir, "for-each-ref", "--format=%(refname)"}, Kept...)
	out, err := exec.CommandContext(ctx, "git", args...).Output()
	if err != nil {
		return nil, fmt.Errorf("listing run refs: %w", err)
	}
	var refs []string
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if line != "" {
			refs = append(refs, line)
		}
	}
	expired := Expired(refs, now, ttl)
	for _, ref := range expired {
		if err := exec.CommandContext(ctx, "git", "--git-dir", gitDir,
			"update-ref", "-d", ref).Run(); err != nil {
			return nil, fmt.Errorf("dropping %s: %w", ref, err)
		}
	}
	return expired, nil
}
