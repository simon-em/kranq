package project

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func checkout(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, body := range files {
		path := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

const dxSetup = `memory: 3GiB

setup: |
  #!/usr/bin/env bash
  set -euo pipefail
  git clone --depth 1 --branch "$CI_REF" "${CI_GIT_REMOTE}/${CI_REPO}.git" ~/setup
`

func TestLoadReadsTheRealDxShape(t *testing.T) {
	p, err := Load(checkout(t, map[string]string{SetupFile: dxSetup}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p.Setup.Memory != "3GiB" {
		t.Errorf("memory = %q", p.Setup.Memory)
	}
	if p.Setup.CPUs != 0 {
		t.Errorf("cpus = %d, want 0 since dx does not set it", p.Setup.CPUs)
	}
	if !strings.HasPrefix(p.Setup.Script, "#!/usr/bin/env bash") {
		t.Errorf("the setup block lost its shebang: %q", p.Setup.Script)
	}
	if !strings.Contains(p.Setup.Script, "${CI_GIT_REMOTE}") {
		t.Errorf("the setup block was mangled: %q", p.Setup.Script)
	}
}

func TestMemoryKeepsItsUnit(t *testing.T) {
	cases := map[string]float64{"3GiB": 3, "4": 4, "3000MiB": 2.9296875}
	for value, want := range cases {
		p, err := Load(checkout(t, map[string]string{SetupFile: "memory: " + value + "\nsetup: |\n  true\n"}))
		if err != nil {
			t.Fatalf("%s: %v", value, err)
		}
		got, ok := p.Setup.MemoryGiB()
		if !ok || got != want {
			t.Errorf("%s -> %v (ok=%v), want %v; the awk parser it replaces stripped the unit and passed the bare number", value, got, ok, want)
		}
	}
}

func TestLoadRejectsABadSpec(t *testing.T) {
	cases := map[string]string{
		"no setup block": "memory: 3GiB\n",
		"empty setup":    "setup: |\n",
		"bad memory":     "memory: plenty\nsetup: |\n  true\n",
		"not yaml":       "memory: [\n",
	}
	for name, body := range cases {
		if _, err := Load(checkout(t, map[string]string{SetupFile: body})); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
}

func TestMissingSetupFileSaysSo(t *testing.T) {
	_, err := Load(t.TempDir())
	if err == nil || !strings.Contains(err.Error(), SetupFile) {
		t.Errorf("error = %v, want it to name %s", err, SetupFile)
	}
}

func TestBasekeyReadsListedFilesInOrderAndSkipsComments(t *testing.T) {
	p, err := Load(checkout(t, map[string]string{
		SetupFile:       "setup: |\n  true\n",
		BasekeyFile:     "# a comment\n.ruby-version\n\n.nvmrc\ngone.lock\n",
		".ruby-version": "3.4.1\n",
		".nvmrc":        "v22\n",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Basekey) != 3 {
		t.Fatalf("read %d key files, want 3", len(p.Basekey))
	}
	if p.Basekey[0].Path != ".ruby-version" || p.Basekey[1].Path != ".nvmrc" {
		t.Errorf("order was not preserved: %+v", p.Basekey)
	}
	if string(p.Basekey[0].Contents) != "3.4.1\n" {
		t.Errorf("contents not read: %q", p.Basekey[0].Contents)
	}
	if !p.Basekey[2].Missing {
		t.Error("a listed file that does not exist should be recorded as missing, not dropped")
	}
}

func TestNoBasekeyIsFine(t *testing.T) {
	p, err := Load(checkout(t, map[string]string{SetupFile: "setup: |\n  true\n"}))
	if err != nil || p.Basekey != nil {
		t.Errorf("basekey=%v err=%v, want it optional", p.Basekey, err)
	}
}
