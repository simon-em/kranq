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
	if strings.Contains(string(body), "kranq") || strings.Contains(string(body), "/opt/bin") {
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
		t.Errorf("a profile with no kranq block was modified:\n%q", body)
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

func TestEnsureHomeCreatesItPrivate(t *testing.T) {
	home := filepath.Join(t.TempDir(), "kranq")
	if err := EnsureHome(home); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(home)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o700 {
		t.Fatalf("a fresh home is %04o, not 0700", info.Mode().Perm())
	}
}

func TestEnsureHomeNarrowsAWideOne(t *testing.T) {
	home := filepath.Join(t.TempDir(), "kranq")
	if err := os.MkdirAll(home, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := EnsureHome(home); err != nil {
		t.Fatal(err)
	}
	info, _ := os.Stat(home)
	if info.Mode().Perm() != 0o700 {
		t.Fatalf("a world-readable home was left at %04o", info.Mode().Perm())
	}
}

func TestEnsureHomeRefusesAFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "kranq")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := EnsureHome(path); err == nil {
		t.Fatal("a plain file was accepted as the kranq home")
	}
}

func TestOnPathSeesThroughSymlinks(t *testing.T) {
	root := t.TempDir()
	real := filepath.Join(root, "real")
	if err := os.MkdirAll(real, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "link")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", link+":/usr/bin")
	if !OnPath(real) {
		t.Fatal("a directory on PATH through a symlink was reported as absent")
	}
	t.Setenv("PATH", real+":/usr/bin")
	if !OnPath(link) {
		t.Fatal("a symlink to a directory on PATH was reported as absent")
	}
	if OnPath(filepath.Join(root, "elsewhere")) {
		t.Fatal("an unrelated directory was reported as on PATH")
	}
}
