package gitsrv

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

var repoNamePattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]{0,63}$`)

// The repo name arrives in a URL path from an authenticated but untrusted
// caller, and becomes a directory under the kranq home. Anything that is not a
// single plain segment is refused rather than cleaned, because a cleaned path
// is a path someone reasoned about wrongly.
func ValidRepo(name string) error {
	name = strings.TrimSuffix(name, ".git")
	if !repoNamePattern.MatchString(name) {
		return fmt.Errorf("%q is not a usable repository name", name)
	}
	if name == "." || name == ".." || strings.Contains(name, "..") {
		return fmt.Errorf("%q is not a usable repository name", name)
	}
	return nil
}

func RepoName(urlPath string) (string, error) {
	trimmed := strings.Trim(urlPath, "/")
	segment, _, _ := strings.Cut(trimmed, "/")
	if err := ValidRepo(segment); err != nil {
		return "", err
	}
	return strings.TrimSuffix(segment, ".git"), nil
}

type Store struct {
	Root       string
	KranqBin   string
	SocketPath string
}

func (s *Store) Dir(repo string) string { return filepath.Join(s.Root, repo+".git") }

// Settings that are not defaults and that the probes showed are each load
// bearing:
//
//   - receive.shallowUpdate, or a push from the shallow clone a CI container
//     starts with is rejected outright with "shallow update not allowed"
//   - receive.advertisePushOptions, or the options never reach the hook
//   - http.receivepack, or pushing over http is refused
var repoConfig = [][2]string{
	{"receive.shallowUpdate", "true"},
	{"receive.advertisePushOptions", "true"},
	{"http.receivepack", "true"},
	{"receive.denyCurrentBranch", "ignore"},
	{"gc.auto", "0"},
}

func (s *Store) Ensure(ctx context.Context, repo string) (string, error) {
	if err := ValidRepo(repo); err != nil {
		return "", err
	}
	dir := s.Dir(repo)
	if _, err := os.Stat(filepath.Join(dir, "HEAD")); err != nil {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return "", err
		}
		if out, err := exec.CommandContext(ctx, "git", "init", "--bare", "--quiet", dir).CombinedOutput(); err != nil {
			return "", fmt.Errorf("creating %s: %w: %s", dir, err, out)
		}
	}
	for _, kv := range repoConfig {
		if out, err := exec.CommandContext(ctx, "git", "--git-dir", dir, "config", kv[0], kv[1]).CombinedOutput(); err != nil {
			return "", fmt.Errorf("configuring %s: %w: %s", kv[0], err, out)
		}
	}
	if err := s.writeHooks(dir); err != nil {
		return "", err
	}
	return dir, nil
}

const hookTemplate = `#!/bin/sh
# Written by kranq. Edits are overwritten.
exec %s git-hook %s --repo %s --socket %s
`

func (s *Store) writeHooks(dir string) error {
	hooks := filepath.Join(dir, "hooks")
	if err := os.MkdirAll(hooks, 0o700); err != nil {
		return err
	}
	repo := strings.TrimSuffix(filepath.Base(dir), ".git")
	for _, phase := range []string{"pre-receive", "post-receive"} {
		body := fmt.Sprintf(hookTemplate, shellQuote(s.KranqBin), phase, shellQuote(repo), shellQuote(s.SocketPath))
		if err := os.WriteFile(filepath.Join(hooks, phase), []byte(body), 0o700); err != nil {
			return err
		}
	}
	return nil
}

func shellQuote(v string) string {
	return "'" + strings.ReplaceAll(v, "'", `'\''`) + "'"
}
