package gitsrv

import (
	"strings"
	"testing"
)

func TestParseSSHCommandAcceptsWhatGitActuallySends(t *testing.T) {
	cases := map[string]SSHCommand{
		`git-receive-pack '/dx.git'`:      {Verb: VerbReceive, Repo: "dx"},
		`git-receive-pack 'dx.git'`:       {Verb: VerbReceive, Repo: "dx"},
		`git-upload-pack '/dx.git'`:       {Verb: VerbUpload, Repo: "dx"},
		`git receive-pack '/dx.git'`:      {Verb: VerbReceive, Repo: "dx"},
		`git-receive-pack '/my-repo.git'`: {Verb: VerbReceive, Repo: "my-repo"},
		`git-receive-pack /dx.git`:        {Verb: VerbReceive, Repo: "dx"},
	}
	for in, want := range cases {
		got, err := ParseSSHCommand(in)
		if err != nil {
			t.Fatalf("%q: %v", in, err)
		}
		if got != want {
			t.Fatalf("%q -> %+v, want %+v", in, got, want)
		}
	}
}

// This is the whole security boundary of the ssh path: a forced command runs as
// the kranq user, and anything that gets past here runs with it.
func TestParseSSHCommandRefusesEverythingElse(t *testing.T) {
	for _, in := range []string{
		"",
		"   ",
		"sh",
		"bash -i",
		"/bin/sh",
		"scp -t /tmp",
		"rsync --server",
		"git-receive-pack",
		"git-upload-archive '/dx.git'",
		`git-receive-pack '/dx.git'; rm -rf /`,
		`git-receive-pack '/dx.git' && curl evil`,
		"git-receive-pack '/../../../etc/passwd'",
		"git-receive-pack '/etc/../dx.git'",
		"git-receive-pack $(whoami)",
		"git-receive-pack `id`",
		"git-receive-pack '/dx.git",
		"git-receive-pack ''",
		"git",
		"git shell",
	} {
		if got, err := ParseSSHCommand(in); err == nil {
			t.Fatalf("%q was accepted as %+v", in, got)
		}
	}
}

// A refusal is read by whoever pushed, so it must not echo back a command
// somebody chose in full.
func TestARefusalDoesNotEchoTheWholeCommand(t *testing.T) {
	long := "evil" + strings.Repeat("A", 500)
	_, err := ParseSSHCommand(long + " x")
	if err == nil {
		t.Fatal("accepted")
	}
	if len(err.Error()) > 200 {
		t.Fatalf("the refusal quotes too much back: %d chars", len(err.Error()))
	}
}

func TestWritesDistinguishesPushFromFetch(t *testing.T) {
	push, _ := ParseSSHCommand(`git-receive-pack '/dx.git'`)
	fetch, _ := ParseSSHCommand(`git-upload-pack '/dx.git'`)
	if !push.Writes() {
		t.Fatal("receive-pack is a write")
	}
	if fetch.Writes() {
		t.Fatal("upload-pack is not a write")
	}
}

func TestUnquoteHandlesGitsOwnEscaping(t *testing.T) {
	// git writes an embedded quote as '\'' and nothing else.
	got, err := unquote(`'it'\''s.git'`)
	if err != nil {
		t.Fatal(err)
	}
	if got != "it's.git" {
		t.Fatalf("got %q", got)
	}
	for _, bad := range []string{`'a'b'`, `'unclosed`, `'`, ""} {
		if _, err := unquote(bad); err == nil {
			t.Fatalf("%q was accepted", bad)
		}
	}
}

// An ssh path is the repository and nothing else, so taking only its first
// segment would let "/etc/../dx.git" quietly mean a repository called "etc".
func TestAnSSHPathMustBeExactlyOneSegment(t *testing.T) {
	for _, in := range []string{
		"git-receive-pack '/etc/../dx.git'",
		"git-receive-pack '/a/b.git'",
		"git-receive-pack '/dx.git/info/refs'",
	} {
		if got, err := ParseSSHCommand(in); err == nil {
			t.Fatalf("%q was accepted as %+v", in, got)
		}
	}
	// Leading slashes are trimmed, so this one is unambiguous and accepted.
	// What matters is that nothing has an interior slash left.
	if got, err := ParseSSHCommand("git-receive-pack '//dx.git'"); err != nil || got.Repo != "dx" {
		t.Fatalf("got %+v, %v", got, err)
	}
}

// The other way in: `git push --receive-pack="kranq git-receive"` makes git run
// kranq with the repository as an argument, so no forced command and no
// kranq-specific key are involved. Anyone who can already ssh here can push.
func TestRepoFromPath(t *testing.T) {
	cases := map[string]string{
		"dx.git":                             "dx",
		"/dx.git":                            "dx",
		"dx":                                 "dx",
		"/Users/macmini/.kranq/repos/dx.git": "dx",
		"~/.kranq/repos/my-repo.git":         "my-repo",
	}
	for in, want := range cases {
		got, err := RepoFromPath(in)
		if err != nil {
			t.Fatalf("%q: %v", in, err)
		}
		if got != want {
			t.Fatalf("%q -> %q, want %q", in, got, want)
		}
	}
	for _, bad := range []string{"", "  ", "/", "///", ".git", "../x.git"} {
		if got, err := RepoFromPath(bad); err == nil {
			t.Fatalf("%q was accepted as %q", bad, got)
		}
	}
}

// A client set up for the no-setup path, pushing to a key whose forced command
// already decides what runs, must not be refused by the parser.
func TestAForcedCommandAcceptsKranqsOwnReceivePack(t *testing.T) {
	got, err := ParseSSHCommand(`/Users/macmini/.local/bin/kranq git-receive 'dx.git'`)
	if err != nil {
		t.Fatal(err)
	}
	if got.Verb != VerbReceive || got.Repo != "dx" {
		t.Fatalf("%+v", got)
	}
	up, err := ParseSSHCommand(`/Users/macmini/.local/bin/kranq git-upload 'dx.git'`)
	if err != nil {
		t.Fatal(err)
	}
	if up.Verb != VerbUpload {
		t.Fatalf("%+v", up)
	}
	// But kranq is not a way to run anything else.
	for _, bad := range []string{
		`/Users/macmini/.local/bin/kranq daemon stop`,
		`/Users/macmini/.local/bin/kranq exec some-task`,
		`/Users/macmini/.local/bin/kranq auth claude --show`,
		`kranq config set X=1`,
	} {
		if _, err := ParseSSHCommand(bad); err == nil {
			t.Fatalf("%q was accepted", bad)
		}
	}
}
