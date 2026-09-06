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

// Kept are the refs a run leaves behind: the source it was given, and the
// commit it produced.
var Kept = []string{"refs/kranq/src/", ResultRefPrefix}

const ResultRefPrefix = "refs/kranq/result/"

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
