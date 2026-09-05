package selfinstall

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

const (
	markerStart = "# >>> forge >>>"
	markerEnd   = "# <<< forge <<<"
)

type Plan struct {
	Prefix string
	Home   string
	Shell  string
}

func DefaultPrefix() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return "/usr/local/bin"
	}
	return filepath.Join(home, ".local", "bin")
}

func InstallBinary(prefix string, out io.Writer) (string, error) {
	self, err := os.Executable()
	if err != nil {
		return "", err
	}
	self, err = filepath.EvalSymlinks(self)
	if err != nil {
		return "", err
	}
	dest := filepath.Join(prefix, "forge")
	if same, _ := sameFile(self, dest); same {
		fmt.Fprintf(out, "already installed at %s\n", dest)
		return dest, nil
	}
	if err := os.MkdirAll(prefix, 0o755); err != nil {
		return "", err
	}
	body, err := os.ReadFile(self)
	if err != nil {
		return "", err
	}
	tmp := dest + ".new"
	if err := os.WriteFile(tmp, body, 0o755); err != nil {
		return "", fmt.Errorf("writing %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, dest); err != nil {
		return "", err
	}
	fmt.Fprintf(out, "installed %s\n", dest)
	return dest, nil
}

func sameFile(a, b string) (bool, error) {
	ai, err := os.Stat(a)
	if err != nil {
		return false, err
	}
	bi, err := os.Stat(b)
	if err != nil {
		return false, err
	}
	return os.SameFile(ai, bi), nil
}

// Compared after resolving symlinks, because on macOS /tmp and /var are links
// into /private and a string comparison reports a directory as absent from a
// PATH that plainly contains it.
func OnPath(prefix string) bool {
	want := resolve(prefix)
	for _, dir := range filepath.SplitList(os.Getenv("PATH")) {
		if dir != "" && resolve(dir) == want {
			return true
		}
	}
	return false
}

func resolve(dir string) string {
	if abs, err := filepath.Abs(dir); err == nil {
		dir = abs
	}
	if real, err := filepath.EvalSymlinks(dir); err == nil {
		return real
	}
	return dir
}

func ProfileFor(shell string) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	switch {
	case strings.Contains(shell, "zsh"):
		return filepath.Join(home, ".zprofile"), nil
	case strings.Contains(shell, "bash"):
		return filepath.Join(home, ".bash_profile"), nil
	case strings.Contains(shell, "fish"):
		return filepath.Join(home, ".config", "fish", "config.fish"), nil
	}
	return filepath.Join(home, ".profile"), nil
}

func ExportLine(prefix, shell string) string {
	if strings.Contains(shell, "fish") {
		return fmt.Sprintf("fish_add_path %s", prefix)
	}
	return fmt.Sprintf("export PATH=%q:$PATH", prefix)
}

func AddToProfile(profile, prefix, shell string, out io.Writer) error {
	existing, err := os.ReadFile(profile)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	if strings.Contains(string(existing), markerStart) {
		fmt.Fprintf(out, "%s already has a forge block\n", profile)
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(profile), 0o755); err != nil {
		return err
	}
	block := fmt.Sprintf("\n%s\n%s\n%s\n", markerStart, ExportLine(prefix, shell), markerEnd)
	f, err := os.OpenFile(profile, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	if _, err := f.WriteString(block); err != nil {
		return err
	}
	fmt.Fprintf(out, "added %s to PATH in %s\n", prefix, profile)
	return nil
}

func RemoveFromProfile(profile string, out io.Writer) error {
	body, err := os.ReadFile(profile)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	text := string(body)
	start := strings.Index(text, markerStart)
	end := strings.Index(text, markerEnd)
	if start < 0 || end < 0 || end < start {
		return nil
	}
	trimmed := strings.TrimRight(text[:start], "\n") + text[end+len(markerEnd):]
	if err := os.WriteFile(profile, []byte(trimmed), 0o644); err != nil {
		return err
	}
	fmt.Fprintf(out, "removed the forge block from %s\n", profile)
	return nil
}

// The daemon socket is authorized by nothing but file permissions, so the home
// has to be 0700. It is created that way, but an older install, an umask, or a
// dependency that made it as a parent directory can leave it wider.
func EnsureHome(home string) error {
	info, err := os.Stat(home)
	if os.IsNotExist(err) {
		return os.MkdirAll(home, 0o700)
	}
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("%s is not a directory", home)
	}
	if info.Mode().Perm()&^0o700 != 0 {
		return os.Chmod(home, 0o700)
	}
	return nil
}
