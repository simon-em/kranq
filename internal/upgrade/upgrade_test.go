package upgrade

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// A shell script standing in for a kranq binary of a given version, so the
// tests exercise the real Verify path rather than a stub.
func fakeKranq(t *testing.T, path, version string) string {
	t.Helper()
	body := fmt.Sprintf("#!/bin/sh\n[ \"$1\" = version ] && echo '{\"version\":\"%s\"}' && exit 0\nexit 1\n", version)
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func versionAt(t *testing.T, path string) string {
	t.Helper()
	out, err := exec.Command(path, "version", "--json").Output()
	if err != nil {
		t.Fatalf("running %s: %v", path, err)
	}
	return strings.TrimSpace(string(out))
}

func TestInstallReplacesAndKeepsThePrevious(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "kranq")
	fakeKranq(t, target, "0.1.0")
	source := fakeKranq(t, filepath.Join(dir, "new"), "0.2.0")

	r, err := Install(context.Background(), source, target)
	if err != nil {
		t.Fatal(err)
	}
	if r.From != "0.1.0" || r.To != "0.2.0" {
		t.Fatalf("report says %s -> %s", r.From, r.To)
	}
	if !strings.Contains(versionAt(t, target), "0.2.0") {
		t.Fatal("the target was not replaced")
	}
	if !strings.Contains(versionAt(t, Previous(target)), "0.1.0") {
		t.Fatal("the previous binary was not kept")
	}
}

func TestInstallOntoNothingIsAFreshInstall(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "kranq")
	source := fakeKranq(t, filepath.Join(dir, "new"), "0.2.0")

	r, err := Install(context.Background(), source, target)
	if err != nil {
		t.Fatal(err)
	}
	if r.From != "" || r.Previous != "" {
		t.Fatalf("reported a previous version that never existed: %+v", r)
	}
	if !strings.Contains(versionAt(t, target), "0.2.0") {
		t.Fatal("the binary was not installed")
	}
}

// The whole point of verifying first: a binary for the wrong architecture, or a
// truncated download, must not become the installed copy.
func TestInstallRefusesABinaryThatDoesNotRun(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "kranq")
	fakeKranq(t, target, "0.1.0")
	broken := filepath.Join(dir, "broken")
	if err := os.WriteFile(broken, []byte("\x7fELF not really\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := Install(context.Background(), broken, target); err == nil {
		t.Fatal("a binary that cannot run was installed")
	}
	if !strings.Contains(versionAt(t, target), "0.1.0") {
		t.Fatal("the working binary was replaced by one that does not run")
	}
	if _, err := os.Stat(target + ".new"); err == nil {
		t.Fatal("a staged file was left behind")
	}
}

func TestInstallRefusesSomethingThatIsNotKranq(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "kranq")
	fakeKranq(t, target, "0.1.0")
	other := filepath.Join(dir, "ls")
	if err := os.WriteFile(other, []byte("#!/bin/sh\necho hello\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := Install(context.Background(), other, target); err == nil {
		t.Fatal("a program that is not kranq was installed")
	}
	if !strings.Contains(versionAt(t, target), "0.1.0") {
		t.Fatal("the working binary was replaced")
	}
}

func TestRollbackIsItsOwnUndo(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "kranq")
	fakeKranq(t, target, "0.1.0")
	source := fakeKranq(t, filepath.Join(dir, "new"), "0.2.0")
	if _, err := Install(context.Background(), source, target); err != nil {
		t.Fatal(err)
	}

	if _, err := Rollback(context.Background(), target); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(versionAt(t, target), "0.1.0") {
		t.Fatal("rollback did not restore the previous binary")
	}

	r, err := Rollback(context.Background(), target)
	if err != nil {
		t.Fatalf("rolling back a rollback: %v", err)
	}
	if r.To != "0.2.0" {
		t.Fatalf("a second rollback went to %s, not back to 0.2.0", r.To)
	}
	if !strings.Contains(versionAt(t, target), "0.2.0") {
		t.Fatal("rollback is a one-way door")
	}
}

func TestRollbackWithNothingToReturnTo(t *testing.T) {
	dir := t.TempDir()
	target := fakeKranq(t, filepath.Join(dir, "kranq"), "0.1.0")
	if _, err := Rollback(context.Background(), target); !errors.Is(err, ErrNoPrevious) {
		t.Fatalf("expected ErrNoPrevious, got %v", err)
	}
	if !strings.Contains(versionAt(t, target), "0.1.0") {
		t.Fatal("a failed rollback disturbed the installed binary")
	}
}

func TestInstalledBinaryIsExecutable(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "kranq")
	source := fakeKranq(t, filepath.Join(dir, "new"), "0.2.0")
	if _, err := Install(context.Background(), source, target); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(target)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm()&0o111 == 0 {
		t.Fatalf("the installed binary is %04o", info.Mode().Perm())
	}
}

// Replacing a file a package manager owns leaves its record of that file wrong,
// and the next `brew upgrade` silently undoes whatever was put there.
func TestManagedRecognisesAPackageManagersBinary(t *testing.T) {
	for _, path := range []string{
		"/opt/homebrew/Cellar/kranq/1.0.0/bin/kranq",
		"/usr/local/Cellar/kranq/1.0.0/bin/kranq",
		"/home/linuxbrew/.linuxbrew/bin/kranq",
		"/nix/store/abc123-kranq/bin/kranq",
	} {
		manager, managed := Managed(path)
		if !managed {
			t.Fatalf("%q was not recognised as managed", path)
		}
		// The manager and the command it answers to are different words, and
		// printing the wrong one hands somebody a line that does not run.
		if manager.Name == "homebrew" && manager.Command != "brew" {
			t.Fatalf("%q would be told to run %q", path, manager.Command)
		}
		if manager.Command == "" {
			t.Fatalf("%q has no command to suggest", path)
		}
	}
	for _, path := range []string{
		"/Users/x/.local/bin/kranq",
		"/usr/local/bin/kranq",
		"/tmp/kranq",
	} {
		if manager, managed := Managed(path); managed {
			t.Fatalf("%q was called %s-managed", path, manager.Name)
		}
	}
}

// brew puts a symlink in bin pointing into the Cellar, and that symlink is what
// is on PATH, so the check has to look through it.
func TestManagedSeesThroughABrewSymlink(t *testing.T) {
	root := t.TempDir()
	cellar := filepath.Join(root, "Cellar", "kranq", "1.0.0", "bin")
	if err := os.MkdirAll(cellar, 0o755); err != nil {
		t.Fatal(err)
	}
	real := filepath.Join(cellar, "kranq")
	if err := os.WriteFile(real, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(root, "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(bin, "kranq")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	if _, managed := Managed(link); !managed {
		t.Fatal("a symlink into a Cellar was not recognised as managed")
	}
}
