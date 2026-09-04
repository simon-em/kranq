package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/effetmonstre/forge/internal/exitcode"
)

func invoke(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	var out, errb bytes.Buffer
	code := dispatch(Env{Stdout: &out, Stderr: &errb}, args)
	return code, out.String(), errb.String()
}

func write(t *testing.T, name, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

const goodSpec = "name: demo\nsteps:\n  - name: greet\n    run: echo hello\n"

func TestNoArgumentsIsAUsageError(t *testing.T) {
	code, _, errb := invoke(t)
	if code != exitcode.Usage {
		t.Errorf("exit = %d, want %d", code, exitcode.Usage)
	}
	if !strings.Contains(errb, "usage: forge") {
		t.Errorf("stderr did not carry usage:\n%s", errb)
	}
}

func TestUnknownCommandIsAUsageError(t *testing.T) {
	code, _, errb := invoke(t, "wat")
	if code != exitcode.Usage {
		t.Errorf("exit = %d, want %d", code, exitcode.Usage)
	}
	if !strings.Contains(errb, `unknown command "wat"`) {
		t.Errorf("stderr did not name the command:\n%s", errb)
	}
}

func TestHelpGoesToStdoutAndSucceeds(t *testing.T) {
	code, out, _ := invoke(t, "--help")
	if code != exitcode.OK {
		t.Errorf("exit = %d, want 0", code)
	}
	if !strings.Contains(out, "validate") {
		t.Errorf("help did not list the commands:\n%s", out)
	}
}

func TestVersionIsDataOnStdout(t *testing.T) {
	code, out, errb := invoke(t, "version")
	if code != exitcode.OK || strings.TrimSpace(out) != Version {
		t.Errorf("exit=%d out=%q, want 0 and %q", code, out, Version)
	}
	if errb != "" {
		t.Errorf("version wrote to stderr: %q", errb)
	}
}

func TestValidateAcceptsAGoodSpec(t *testing.T) {
	code, out, errb := invoke(t, "validate", write(t, "ok.yaml", goodSpec))
	if code != exitcode.OK {
		t.Fatalf("exit = %d, want 0; stderr:\n%s", code, errb)
	}
	if !strings.Contains(out, "ok (1 steps, claude=false") {
		t.Errorf("unexpected summary: %q", out)
	}
}

func TestValidateRejectsAnInvalidSpec(t *testing.T) {
	code, _, errb := invoke(t, "validate", write(t, "bad.yaml", "steps:\n  - run: true\n"))
	if code != exitcode.InvalidSpec {
		t.Errorf("exit = %d, want %d", code, exitcode.InvalidSpec)
	}
	if !strings.Contains(errb, "name is required") {
		t.Errorf("stderr did not explain: %q", errb)
	}
}

func TestValidateReportsAMissingFileDistinctly(t *testing.T) {
	code, _, _ := invoke(t, "validate", filepath.Join(t.TempDir(), "absent.yaml"))
	if code != exitcode.NoSuchFile {
		t.Errorf("exit = %d, want %d so a caller can tell it apart from a bad spec", code, exitcode.NoSuchFile)
	}
}

func TestValidateChecksEveryFileAndReturnsTheWorst(t *testing.T) {
	good := write(t, "a.yaml", goodSpec)
	bad := write(t, "b.yaml", "steps: []\n")
	code, out, _ := invoke(t, "validate", good, bad)
	if code == exitcode.OK {
		t.Error("a bad file among good ones must not exit 0")
	}
	if !strings.Contains(out, "a.yaml: ok") {
		t.Errorf("the good file was not reported: %q", out)
	}
}

func TestRenderCompilesToRunnableBash(t *testing.T) {
	code, out, errb := invoke(t, "render", write(t, "ok.yaml", goodSpec))
	if code != exitcode.OK {
		t.Fatalf("exit = %d, want 0; stderr:\n%s", code, errb)
	}
	if !strings.HasPrefix(out, "#!/usr/bin/env bash") {
		t.Errorf("rendered script has no shebang:\n%s", out)
	}
	if !strings.Contains(out, "echo hello") {
		t.Errorf("rendered script lost the step body:\n%s", out)
	}
	if err := checkBash(out); err != nil {
		t.Errorf("rendered script is not valid bash: %v", err)
	}
}

func TestRenderNeedsExactlyOneFile(t *testing.T) {
	if code, _, _ := invoke(t, "render"); code != exitcode.Usage {
		t.Errorf("exit = %d, want %d", code, exitcode.Usage)
	}
}

func TestRunAcceptsFlagsAfterTheFileName(t *testing.T) {
	spec := write(t, "t.yaml", goodSpec)
	code, _, errb := invoke(t, "run", spec, "--repo", "dx", "--branch", "main", "--local=false")
	if code != exitcode.Misconfigured {
		t.Fatalf("exit = %d, want %d; flags after the positional must be parsed, not ignored", code, exitcode.Misconfigured)
	}
	if !strings.Contains(errb, "only --local is implemented") {
		t.Errorf("--local=false after the file name was not seen: %q", errb)
	}
}

func TestRunNeedsARepoAndBranch(t *testing.T) {
	t.Setenv("CI_REPO", "")
	t.Setenv("BITBUCKET_REPO_SLUG", "")
	t.Setenv("CI_BRANCH", "")
	t.Setenv("BITBUCKET_BRANCH", "")
	code, _, errb := invoke(t, "run", write(t, "t.yaml", goodSpec))
	if code != exitcode.Misconfigured {
		t.Errorf("exit = %d, want %d", code, exitcode.Misconfigured)
	}
	if !strings.Contains(errb, "--repo") {
		t.Errorf("stderr should name the missing flag: %q", errb)
	}
}

func TestRunReportsAMissingSpecFileBeforeDoingAnything(t *testing.T) {
	code, _, _ := invoke(t, "run", filepath.Join(t.TempDir(), "gone.yaml"), "--repo", "dx", "--branch", "main")
	if code != exitcode.NoSuchFile {
		t.Errorf("exit = %d, want %d", code, exitcode.NoSuchFile)
	}
}
