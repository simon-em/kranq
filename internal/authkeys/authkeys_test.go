package authkeys

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const (
	pubA = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIGVsb25nZW5vdWdoa2V5ZGF0YWhlcmVvaw== ci@dx"
	pubB = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIGFub3RoZXJrZXlkYXRhdGhhdGRpZmZlcnM= other@host"
	// What someone actually administers this machine with.
	human = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIHVtYW5rZXlmb3JhZG1pbmlzdHJhdGlvbg== me@laptop"
)

const command = "/Users/macmini/.local/bin/kranq git-receive --name ci-dx"

func mustKey(t *testing.T, line string) Key {
	t.Helper()
	k, err := ParsePublicKey(line)
	if err != nil {
		t.Fatal(err)
	}
	return k
}

// This file usually holds the key someone administers the machine with. Losing
// it locks them out of their own build machine.
func TestAddLeavesEveryOtherLineExactlyAsItWas(t *testing.T) {
	before := "# my keys\n" + human + "\n\n" + "ssh-rsa AAAAB3legacy old@key\n"
	after, err := Add(before, mustKey(t, pubA), "ci-dx", command)
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range []string{"# my keys", human, "ssh-rsa AAAAB3legacy old@key"} {
		if !strings.Contains(after, line) {
			t.Fatalf("%q was lost:\n%s", line, after)
		}
	}
	if !strings.Contains(after, Marker+"ci-dx") {
		t.Fatalf("the new key was not added:\n%s", after)
	}
}

func TestAddedEntryIsLockedDown(t *testing.T) {
	after, err := Add("", mustKey(t, pubA), "ci-dx", command)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(after, "restrict,") {
		t.Fatalf("the entry does not restrict the key:\n%s", after)
	}
	if !strings.Contains(after, `command="`+command+`"`) {
		t.Fatalf("the forced command is missing, so the key would get a shell:\n%s", after)
	}
	// The forced command must come before the key, or ssh reads it as a comment.
	if strings.Index(after, "command=") > strings.Index(after, "ssh-ed25519") {
		t.Fatalf("the options are on the wrong side of the key:\n%s", after)
	}
}

func TestRemoveTakesOnlyItsOwn(t *testing.T) {
	content := "# comment\n" + human + "\n"
	content, _ = Add(content, mustKey(t, pubA), "ci-dx", command)
	content, _ = Add(content, mustKey(t, pubB), "ci-other", command)

	after, err := Remove(content, "ci-dx")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(after, Marker+"ci-dx") {
		t.Fatal("the key was not removed")
	}
	for _, keep := range []string{human, "# comment", Marker + "ci-other"} {
		if !strings.Contains(after, keep) {
			t.Fatalf("%q was removed too:\n%s", keep, after)
		}
	}
}

func TestRemoveTheOnlyLineLeavesAnEmptyFileNotABrokenOne(t *testing.T) {
	content, _ := Add("", mustKey(t, pubA), "ci-dx", command)
	after, err := Remove(content, "ci-dx")
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(after) != "" {
		t.Fatalf("got %q", after)
	}
}

func TestRemoveAKeyThatIsNotThere(t *testing.T) {
	if _, err := Remove(human+"\n", "ci-dx"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestAddingTheSameNameTwiceIsRefused(t *testing.T) {
	content, _ := Add("", mustKey(t, pubA), "ci-dx", command)
	if _, err := Add(content, mustKey(t, pubB), "ci-dx", command); !errors.Is(err, ErrExists) {
		t.Fatalf("expected ErrExists, got %v", err)
	}
}

// Two entries for one key means revoking one name leaves it working.
func TestAddingTheSameKeyUnderAnotherNameIsRefused(t *testing.T) {
	content, _ := Add("", mustKey(t, pubA), "ci-dx", command)
	if _, err := Add(content, mustKey(t, pubA), "ci-two", command); err == nil {
		t.Fatal("the same key was installed twice")
	}
}

func TestListReportsOnlyKranqEntries(t *testing.T) {
	content := human + "\n"
	content, _ = Add(content, mustKey(t, pubA), "ci-dx", command)
	content, _ = Add(content, mustKey(t, pubB), "ci-other", command)

	got := List(content)
	if len(got) != 2 {
		t.Fatalf("%d keys: %+v", len(got), got)
	}
	if got[0].Name != "ci-dx" || got[1].Name != "ci-other" {
		t.Fatalf("%+v", got)
	}
	if got[0].Type != "ssh-ed25519" || got[0].Data == "" {
		t.Fatalf("the key itself was not read back: %+v", got[0])
	}
}

func TestParsePublicKeyRefusesWhatIsNotOne(t *testing.T) {
	for _, bad := range []string{
		"",
		"not a key",
		"ssh-ed25519",
		"ssh-ed25519 not-base64!!",
		"ssh-rsa AAAAB3NzaC1yc2E= old@key",
		"-----BEGIN OPENSSH PRIVATE KEY-----",
	} {
		if _, err := ParsePublicKey(bad); err == nil {
			t.Fatalf("%q was accepted", bad)
		}
	}
}

// Pasting a private key by mistake would write it into a file meant for public
// ones, so it is called out specifically.
func TestAPrivateKeyIsCalledOut(t *testing.T) {
	_, err := ParsePublicKey("-----BEGIN OPENSSH PRIVATE KEY-----\nb3BlbnNza\n")
	if err == nil || !strings.Contains(err.Error(), "private key") {
		t.Fatalf("got %v", err)
	}
}

func TestBadNamesAreRefused(t *testing.T) {
	for _, name := range []string{"", "CI", "ci_dx", "-ci", strings.Repeat("c", 33)} {
		if _, err := Add("", mustKey(t, pubA), name, command); err == nil {
			t.Fatalf("%q was accepted", name)
		}
	}
}

// sshd silently ignores authorized_keys if it or ~/.ssh is too permissive, and
// the client just sees the key rejected.
func TestWriteLeavesPermissionsSshdWillAccept(t *testing.T) {
	dir := filepath.Join(t.TempDir(), ".ssh")
	if err := os.MkdirAll(dir, 0o777); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "authorized_keys")
	if err := Write(path, human+"\n"); err != nil {
		t.Fatal(err)
	}
	fileInfo, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if fileInfo.Mode().Perm()&0o077 != 0 {
		t.Fatalf("authorized_keys is %04o", fileInfo.Mode().Perm())
	}
	dirInfo, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if dirInfo.Mode().Perm()&0o022 != 0 {
		t.Fatalf(".ssh is %04o, and sshd will ignore what is in it", dirInfo.Mode().Perm())
	}
}

func TestReadingAnAbsentFileIsNotAnError(t *testing.T) {
	got, err := Read(filepath.Join(t.TempDir(), "authorized_keys"))
	if err != nil || got != "" {
		t.Fatalf("got %q, %v", got, err)
	}
}
