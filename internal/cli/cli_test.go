package cli

import (
	"bytes"
	"flag"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/effetmonstre/forge/internal/daemon"
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
	isolate(t)
	spec := write(t, "t.yaml", goodSpec)

	code, _, errb := invoke(t, "run", spec, "--repo", "dx", "--branch", "main")

	if code == exitcode.Misconfigured {
		t.Fatalf("exit = %d: --repo and --branch after the file name were ignored, which is how "+
			"Go's flag package behaves without permutation\nstderr: %s", code, errb)
	}
	if code != exitcode.Unreachable {
		t.Errorf("exit = %d, want %d (no daemon), which means the flags were parsed", code, exitcode.Unreachable)
	}
}

func TestParsePermutedCollectsPositionalsAroundFlags(t *testing.T) {
	cases := map[string][]string{
		"flags first":       {"--repo", "dx", "a.yaml"},
		"flags last":        {"a.yaml", "--repo", "dx"},
		"flags either side": {"--branch", "main", "a.yaml", "--repo", "dx"},
	}
	for name, args := range cases {
		fs := flag.NewFlagSet("t", flag.ContinueOnError)
		fs.SetOutput(io.Discard)
		repo := fs.String("repo", "", "")
		branch := fs.String("branch", "", "")
		positional, err := parsePermuted(fs, args)
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		if len(positional) != 1 || positional[0] != "a.yaml" {
			t.Errorf("%s: positional = %v, want [a.yaml]", name, positional)
		}
		if *repo != "dx" && strings.Contains(name, "repo") {
			t.Errorf("%s: repo = %q", name, *repo)
		}
		if name == "flags either side" && (*repo != "dx" || *branch != "main") {
			t.Errorf("%s: repo=%q branch=%q, want both", name, *repo, *branch)
		}
	}
}

func isolate(t *testing.T) {
	t.Helper()
	t.Setenv("FORGE_HOME", t.TempDir())
	t.Setenv("FORGE_AUTOSTART", "0")
}

func TestRunNeedsARepoAndBranch(t *testing.T) {
	isolate(t)
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
	isolate(t)
	code, _, _ := invoke(t, "run", filepath.Join(t.TempDir(), "gone.yaml"), "--repo", "dx", "--branch", "main")
	if code != exitcode.NoSuchFile {
		t.Errorf("exit = %d, want %d", code, exitcode.NoSuchFile)
	}
}

func TestSettingsFallBackToTheStoredEnvFile(t *testing.T) {
	home := t.TempDir()
	t.Setenv("FORGE_HOME", home)
	t.Setenv("CLAUDE_CODE_OAUTH_TOKEN", "")

	if got := settings(home)("CLAUDE_CODE_OAUTH_TOKEN"); got != "" {
		t.Fatalf("token = %q with nothing configured", got)
	}
	if err := daemon.SetEnv(home, "CLAUDE_CODE_OAUTH_TOKEN", "stored-token"); err != nil {
		t.Fatal(err)
	}
	if got := settings(home)("CLAUDE_CODE_OAUTH_TOKEN"); got != "stored-token" {
		t.Errorf("token = %q, want the one from the env file; the daemon is started by launchd "+
			"with no token in its environment, so this fallback is the only way it gets one", got)
	}

	t.Setenv("CLAUDE_CODE_OAUTH_TOKEN", "from-environment")
	if got := settings(home)("CLAUDE_CODE_OAUTH_TOKEN"); got != "from-environment" {
		t.Errorf("token = %q, want the environment to win for a one-off override", got)
	}
}

// The same fallback has to cover every setting, not just the token: a daemon
// started over ssh or by launchd has almost no environment at all.
func TestEverySettingComesFromTheEnvFileToo(t *testing.T) {
	home := t.TempDir()
	t.Setenv("FORGE_HOME", home)
	for _, name := range []string{"FORGE_MAX_VMS", "FORGE_GIT_REMOTE", "FORGE_HTTP_ADDR", "FORGE_LIMA_HOME"} {
		t.Setenv(name, "")
	}
	for _, kv := range [][2]string{
		{"FORGE_MAX_VMS", "1"},
		{"FORGE_GIT_REMOTE", "git@bitbucket.org:effetmonstre"},
		{"FORGE_HTTP_ADDR", "127.0.0.1:8420"},
		{"FORGE_LIMA_HOME", home + "/lima"},
	} {
		if err := daemon.SetEnv(home, kv[0], kv[1]); err != nil {
			t.Fatal(err)
		}
	}
	cfg := daemonConfig()
	if cfg.MaxVMs != 1 {
		t.Errorf("MaxVMs = %d, want 1 from the env file", cfg.MaxVMs)
	}
	if cfg.GitRemote != "git@bitbucket.org:effetmonstre" {
		t.Errorf("GitRemote = %q", cfg.GitRemote)
	}
	if cfg.HTTPAddr != "127.0.0.1:8420" {
		t.Errorf("HTTPAddr = %q", cfg.HTTPAddr)
	}
	if cfg.LimaHome != home+"/lima" {
		t.Errorf("LimaHome = %q", cfg.LimaHome)
	}
}

// A malformed number must not silently become zero, which would mean no job is
// ever admitted.
func TestABadNumberFallsBackToTheDefault(t *testing.T) {
	home := t.TempDir()
	t.Setenv("FORGE_HOME", home)
	t.Setenv("FORGE_MAX_VMS", "not-a-number")
	if got := daemonConfig().MaxVMs; got != 2 {
		t.Errorf("MaxVMs = %d, want the default 2", got)
	}
}

// forge config ls prints values, so a secret must never be one of them.
func TestConfigNeverPrintsASecret(t *testing.T) {
	home := t.TempDir()
	t.Setenv("FORGE_HOME", home)
	if err := daemon.SetEnv(home, "CLAUDE_CODE_OAUTH_TOKEN", "sk-ant-oat01-verysecret"); err != nil {
		t.Fatal(err)
	}
	if err := daemon.SetEnv(home, "FORGE_MAX_VMS", "1"); err != nil {
		t.Fatal(err)
	}
	_, out, errb := invoke(t, "config", "ls")
	if strings.Contains(out+errb, "verysecret") {
		t.Fatalf("a secret was printed:\n%s%s", out, errb)
	}
	if !strings.Contains(out, "FORGE_MAX_VMS") || !strings.Contains(out, "1") {
		t.Fatalf("an ordinary setting was not shown:\n%s", out)
	}
	if code, out, _ := invoke(t, "config", "get", "CLAUDE_CODE_OAUTH_TOKEN"); code == exitcode.OK || strings.Contains(out, "verysecret") {
		t.Fatalf("config get printed a secret: %d %q", code, out)
	}
}

// The daemon may be running VMs in a lima home set in $FORGE_HOME/env. A CLI
// that only read the environment would list, and offer to delete, a different
// set of VMs than the ones actually running.
func TestLimaHomeComesFromTheConfigFileToo(t *testing.T) {
	home := t.TempDir()
	t.Setenv("FORGE_HOME", home)
	t.Setenv("FORGE_LIMA_HOME", "")
	if got := limaHomeEnv(); got != nil {
		t.Fatalf("got %v with nothing configured", got)
	}
	if err := daemon.SetEnv(home, "FORGE_LIMA_HOME", home+"/lima"); err != nil {
		t.Fatal(err)
	}
	got := limaHomeEnv()
	if len(got) != 1 || got[0] != "LIMA_HOME="+home+"/lima" {
		t.Fatalf("got %v, want the value from the env file", got)
	}
	t.Setenv("FORGE_LIMA_HOME", "/tmp/override")
	if got := limaHomeEnv(); got[0] != "LIMA_HOME=/tmp/override" {
		t.Fatalf("got %v, want the environment to win", got)
	}
}

// A setting forge actually reads must be in the known list, or `config set`
// warns that it is unused and the person reasonably assumes it does nothing.
func TestEverySettingTheDaemonReadsIsDocumented(t *testing.T) {
	for _, name := range []string{
		"FORGE_MAX_VMS", "FORGE_MEMORY_HEADROOM_MB", "FORGE_GIT_REMOTE",
		"FORGE_LIMA_HOME", "FORGE_HTTP_ADDR", "FORGE_NODE", "FORGE_AUTO_CREATE_REPOS",
	} {
		if _, ok := knownSettings[name]; !ok {
			t.Errorf("%s is read by the daemon but `forge config set` calls it unknown", name)
		}
	}
}

func TestAutoCreateReposDefaultsOn(t *testing.T) {
	home := t.TempDir()
	t.Setenv("FORGE_HOME", home)
	t.Setenv("FORGE_AUTO_CREATE_REPOS", "")
	if !daemonConfig().AutoCreateRepos {
		t.Fatal("the simple case should not need a setting to work")
	}
	for _, off := range []string{"false", "0", "no", "off", "FALSE"} {
		if err := daemon.SetEnv(home, "FORGE_AUTO_CREATE_REPOS", off); err != nil {
			t.Fatal(err)
		}
		if daemonConfig().AutoCreateRepos {
			t.Errorf("%q did not turn it off", off)
		}
	}
	if err := daemon.SetEnv(home, "FORGE_AUTO_CREATE_REPOS", "true"); err != nil {
		t.Fatal(err)
	}
	if !daemonConfig().AutoCreateRepos {
		t.Error("true did not turn it back on")
	}
}

// When a package manager installed forge it owns the binary and PATH, and
// copying it elsewhere would leave two copies that upgrade separately.
func TestDepsOnlyInstallLeavesTheBinaryAlone(t *testing.T) {
	home := t.TempDir()
	prefix := filepath.Join(t.TempDir(), "bin")
	t.Setenv("FORGE_HOME", home)
	profile := filepath.Join(t.TempDir(), ".zprofile")
	t.Setenv("HOME", filepath.Dir(profile))

	code, _, errb := invoke(t, "install", "--deps-only", "--skip-deps", "--prefix", prefix)
	if code != exitcode.OK {
		t.Fatalf("code %d: %s", code, errb)
	}
	if _, err := os.Stat(filepath.Join(prefix, "forge")); err == nil {
		t.Fatal("--deps-only copied the binary anyway")
	}
	if body, err := os.ReadFile(profile); err == nil && strings.Contains(string(body), "forge") {
		t.Fatalf("--deps-only edited a shell profile:\n%s", body)
	}
}
