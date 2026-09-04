package selfinstall

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAddToProfileIsIdempotent(t *testing.T) {
	profile := filepath.Join(t.TempDir(), ".zprofile")
	if err := os.WriteFile(profile, []byte("# mine\nexport EDITOR=vim\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for range 3 {
		if err := AddToProfile(profile, "/opt/bin", "/bin/zsh", io.Discard); err != nil {
			t.Fatal(err)
		}
	}
	body, _ := os.ReadFile(profile)
	if n := strings.Count(string(body), markerStart); n != 1 {
		t.Errorf("the block was added %d times; running install twice must not duplicate it", n)
	}
	if !strings.Contains(string(body), "export EDITOR=vim") {
		t.Error("the user's own profile content was lost")
	}
}

func TestRemoveFromProfileLeavesEverythingElse(t *testing.T) {
	profile := filepath.Join(t.TempDir(), ".zprofile")
	if err := os.WriteFile(profile, []byte("export EDITOR=vim\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := AddToProfile(profile, "/opt/bin", "/bin/zsh", io.Discard); err != nil {
		t.Fatal(err)
	}
	if err := RemoveFromProfile(profile, io.Discard); err != nil {
		t.Fatal(err)
	}
	body, _ := os.ReadFile(profile)
	if strings.Contains(string(body), "forge") || strings.Contains(string(body), "/opt/bin") {
		t.Errorf("uninstall left something behind:\n%s", body)
	}
	if !strings.Contains(string(body), "export EDITOR=vim") {
		t.Errorf("uninstall ate the user's own content:\n%s", body)
	}
}

func TestRemoveFromAProfileWeNeverTouchedIsSafe(t *testing.T) {
	profile := filepath.Join(t.TempDir(), ".zprofile")
	if err := os.WriteFile(profile, []byte("export EDITOR=vim\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := RemoveFromProfile(profile, io.Discard); err != nil {
		t.Fatal(err)
	}
	body, _ := os.ReadFile(profile)
	if string(body) != "export EDITOR=vim\n" {
		t.Errorf("a profile with no forge block was modified:\n%q", body)
	}
	if err := RemoveFromProfile(filepath.Join(t.TempDir(), "absent"), io.Discard); err != nil {
		t.Errorf("removing from a nonexistent profile should be a no-op, got %v", err)
	}
}

func TestProfileAndExportLineMatchTheShell(t *testing.T) {
	cases := map[string]struct{ profile, export string }{
		"/bin/zsh":      {".zprofile", "export PATH="},
		"/bin/bash":     {".bash_profile", "export PATH="},
		"/opt/bin/fish": {"config.fish", "fish_add_path"},
		"/usr/bin/ksh":  {".profile", "export PATH="},
	}
	for shell, want := range cases {
		got, err := ProfileFor(shell)
		if err != nil {
			t.Fatal(err)
		}
		if filepath.Base(got) != want.profile {
			t.Errorf("%s -> %s, want %s", shell, filepath.Base(got), want.profile)
		}
		if !strings.HasPrefix(ExportLine("/opt/bin", shell), want.export) {
			t.Errorf("%s export line = %q, want it to start %q", shell, ExportLine("/opt/bin", shell), want.export)
		}
	}
}

func TestInstallBinaryIsAtomicAndExecutable(t *testing.T) {
	prefix := filepath.Join(t.TempDir(), "bin")
	dest, err := InstallBinary(prefix, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(dest)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm()&0o111 == 0 {
		t.Errorf("installed binary is not executable: %v", info.Mode())
	}
	if _, err := os.Stat(dest + ".new"); err == nil {
		t.Error("the temporary file was left behind")
	}
}

func TestInstallingOverItselfIsANoOp(t *testing.T) {
	prefix := filepath.Join(t.TempDir(), "bin")
	dest, err := InstallBinary(prefix, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(dest)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := InstallBinary(prefix, io.Discard); err != nil {
		t.Fatalf("installing twice must work: %v", err)
	}
	after, _ := os.Stat(dest)
	if !after.ModTime().Equal(before.ModTime()) && after.Size() != before.Size() {
		t.Error("reinstalling produced a different file")
	}
}
