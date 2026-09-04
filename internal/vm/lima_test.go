package vm

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"testing"
)

func TestListParsesRealLimactlOutput(t *testing.T) {
	raw, err := os.ReadFile("testdata/list.json")
	if err != nil {
		t.Fatal(err)
	}
	stub := t.TempDir()
	script := "#!/usr/bin/env bash\ncat " + mustAbs(t, "testdata/list.json") + "\n"
	writeExec(t, stub+"/limactl", script)

	got, err := Lima{Bin: stub + "/limactl"}.List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 6 {
		t.Fatalf("parsed %d instances from %d bytes of real output, want 6", len(got), len(raw))
	}
	first := got[0]
	if first.Name != "ci-base-25f7e1ce3d-1478" || first.Status != "Stopped" {
		t.Errorf("first instance = %+v", first)
	}
	if first.CPUs != 3 || first.Memory != 1073741824 {
		t.Errorf("resources not parsed: cpus=%d memory=%d", first.CPUs, first.Memory)
	}
	if first.Running() {
		t.Error("a Stopped instance must not report Running")
	}
}

func TestListSurfacesAFailure(t *testing.T) {
	stub := t.TempDir()
	writeExec(t, stub+"/limactl", "#!/usr/bin/env bash\necho boom >&2\nexit 1\n")
	if _, err := (Lima{Bin: stub + "/limactl"}).List(context.Background()); err == nil {
		t.Error("a failing limactl must surface as an error, not an empty list")
	}
}

func TestShellReturnsTheGuestExitCode(t *testing.T) {
	stub := t.TempDir()
	writeExec(t, stub+"/limactl", "#!/usr/bin/env bash\nexit 7\n")
	code, err := Lima{Bin: stub + "/limactl"}.Shell(context.Background(), "vm", "true", os.Stderr)
	if err != nil || code != 7 {
		t.Errorf("code=%d err=%v, want 7 and no error: a failing job is a result, not a driver fault", code, err)
	}
}

func TestShellPassesTheScriptAsOneArgument(t *testing.T) {
	stub := t.TempDir()
	out := stub + "/argv"
	writeExec(t, stub+"/limactl", "#!/usr/bin/env bash\nfor a in \"$@\"; do echo \"[$a]\"; done > "+out+"\n")
	script := "echo 'hello world' && ls | wc -l"
	if _, err := (Lima{Bin: stub + "/limactl"}).Shell(context.Background(), "vm", script, os.Stderr); err != nil {
		t.Fatal(err)
	}
	argv, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(argv), "["+script+"]") {
		t.Errorf("the script was split across arguments, which is how the bash version broke:\n%s", argv)
	}
}

func TestExistsReadsTheInstanceDirectory(t *testing.T) {
	home := t.TempDir()
	l := Lima{Home: home}
	if l.Exists("nope") {
		t.Error("Exists must be false for an unknown instance")
	}
	if err := os.MkdirAll(home+"/vm", 0o755); err != nil {
		t.Fatal(err)
	}
	if l.Exists("vm") {
		t.Error("a directory without lima.yaml is not an instance")
	}
	if err := os.WriteFile(home+"/vm/lima.yaml", []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if !l.Exists("vm") {
		t.Error("Exists must be true once lima.yaml is present")
	}
}

func mustAbs(t *testing.T, p string) string {
	t.Helper()
	abs, err := exec.Command("pwd").Output()
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(abs)) + "/" + p
}

func writeExec(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
}
