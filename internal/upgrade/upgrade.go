package upgrade

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

var ErrNoPrevious = errors.New("no previous binary to roll back to")

func Previous(target string) string { return target + ".prev" }

// Manager is a package manager that owns forge's binary, and the command it
// answers to. The two differ: the manager is called homebrew and the command is
// brew, and printing the wrong one gives somebody a line that does not run.
type Manager struct {
	Name    string
	Command string
}

// Managed reports whether a package manager owns this path. Replacing a file
// under a Cellar leaves brew's own record of it wrong, and the next brew
// upgrade silently undoes whatever was put there.
func Managed(target string) (Manager, bool) {
	resolved := target
	if real, err := filepath.EvalSymlinks(target); err == nil {
		resolved = real
	}
	for _, marker := range []string{"/Cellar/", "/homebrew/", "/linuxbrew/"} {
		if strings.Contains(resolved, marker) {
			return Manager{Name: "homebrew", Command: "brew"}, true
		}
	}
	if strings.HasPrefix(resolved, "/nix/store/") {
		return Manager{Name: "nix", Command: "nix"}, true
	}
	return Manager{}, false
}

type Report struct {
	Target   string
	From     string
	To       string
	Previous string
}

// Verify runs the candidate before it is allowed to replace anything. A binary
// for the wrong architecture, or a truncated download, fails here rather than
// after it has become the installed copy.
func Verify(ctx context.Context, path string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, path, "version", "--json").Output()
	if err != nil {
		return "", fmt.Errorf("%s does not run on this machine: %w", filepath.Base(path), err)
	}
	var v struct {
		Version string `json:"version"`
	}
	if err := json.Unmarshal(out, &v); err != nil || v.Version == "" {
		return "", fmt.Errorf("%s did not report a version, so it is not a forge binary", filepath.Base(path))
	}
	return v.Version, nil
}

// Install replaces target with source, keeping what was there as target.prev.
// The rename is atomic, so a target is never briefly absent or half-written.
func Install(ctx context.Context, source, target string) (Report, error) {
	var r Report
	r.Target = target
	to, err := Verify(ctx, source)
	if err != nil {
		return r, err
	}
	r.To = to
	if from, err := Verify(ctx, target); err == nil {
		r.From = from
	}

	staged := target + ".new"
	if err := copyExecutable(source, staged); err != nil {
		return r, err
	}
	defer os.Remove(staged)

	if _, err := os.Stat(target); err == nil {
		if err := copyExecutable(target, Previous(target)); err != nil {
			return r, fmt.Errorf("keeping the previous binary: %w", err)
		}
		r.Previous = Previous(target)
	}
	if err := os.Rename(staged, target); err != nil {
		return r, err
	}
	return r, nil
}

func Rollback(ctx context.Context, target string) (Report, error) {
	r := Report{Target: target}
	prev := Previous(target)
	if _, err := os.Stat(prev); err != nil {
		return r, ErrNoPrevious
	}
	to, err := Verify(ctx, prev)
	if err != nil {
		return r, err
	}
	r.To = to

	// Kept aside first, so the binary being rolled away becomes what a second
	// rollback returns to. The command is its own undo, not a one-way door.
	held := target + ".rolled"
	if from, err := Verify(ctx, target); err == nil {
		r.From = from
		if err := copyExecutable(target, held); err != nil {
			return r, fmt.Errorf("keeping the binary being rolled away: %w", err)
		}
		defer os.Remove(held)
	}

	staged := target + ".new"
	if err := copyExecutable(prev, staged); err != nil {
		return r, err
	}
	if err := os.Rename(staged, target); err != nil {
		os.Remove(staged)
		return r, err
	}
	if r.From != "" {
		if err := os.Rename(held, prev); err != nil {
			return r, fmt.Errorf("rolled back, but could not keep %s: %w", r.From, err)
		}
		r.Previous = prev
	}
	return r, nil
}

func copyExecutable(source, dest string) (err error) {
	in, err := os.Open(source)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.OpenFile(dest, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o755)
	if err != nil {
		return err
	}
	defer func() {
		if closeErr := out.Close(); err == nil {
			err = closeErr
		}
	}()
	if _, err := io.Copy(out, in); err != nil {
		return err
	}
	return out.Sync()
}
