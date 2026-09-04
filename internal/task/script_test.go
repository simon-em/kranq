package task

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"strings"
	"testing"
)

func build(t *testing.T, yaml string, env map[string]string) string {
	t.Helper()
	s, err := Parse([]byte(yaml))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	return BuildScript(s, env)
}

func TestScriptIsValidBash(t *testing.T) {
	script := build(t, "name: x\nrepo: dx\nenv:\n  A: one\nsteps:\n  - name: shell\n    run: |\n      echo hi\n  - name: think\n    claude: |\n      Review 'this' and \"that\"\n    allowed_tools: [Read, Grep]\n    max_turns: 12\n", map[string]string{"B": "two"})
	cmd := exec.Command("bash", "-n")
	cmd.Stdin = strings.NewReader(script)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("generated script is not valid bash: %v\n%s\n---\n%s", err, out, script)
	}
	for _, want := range []string{"export A='one'", "export B='two'", "--allowedTools 'Read,Grep'", "--max-turns 12", "exit 75"} {
		if !strings.Contains(script, want) {
			t.Errorf("script missing %q\n%s", want, script)
		}
	}
}

func TestScriptQuotesHostileValues(t *testing.T) {
	script := build(t, "name: x\nrepo: dx\nsteps:\n  - run: true\n", map[string]string{
		"EVIL": "'; rm -rf /tmp/pwned; echo '",
	})
	cmd := exec.Command("bash", "-n")
	cmd.Stdin = strings.NewReader(script)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("hostile env broke the script: %v\n%s", err, out)
	}
	if strings.Contains(script, "rm -rf /tmp/pwned; echo ''\n") {
		t.Errorf("env value was not quoted:\n%s", script)
	}
}

func TestScriptRunsAndPropagatesExit(t *testing.T) {
	script := build(t, "name: x\nrepo: dx\nsteps:\n  - name: ok\n    run: echo first\n  - name: boom\n    run: exit 3\n", nil)
	cmd := exec.Command("bash")
	cmd.Stdin = strings.NewReader(script)
	out, err := cmd.CombinedOutput()
	if !strings.Contains(string(out), "first") {
		t.Errorf("first step did not run: %s", out)
	}
	var exitErr *exec.ExitError
	if err == nil {
		t.Fatal("expected a non-zero exit")
	}
	if ok := asExitError(err, &exitErr); !ok || exitErr.ExitCode() != 3 {
		t.Errorf("exit code = %v, want 3", err)
	}
}

func TestContinueOnErrorKeepsGoing(t *testing.T) {
	script := build(t, "name: x\nrepo: dx\nsteps:\n  - name: soft\n    run: exit 4\n    continue_on_error: true\n  - name: after\n    run: echo reached\n", nil)
	cmd := exec.Command("bash")
	cmd.Stdin = strings.NewReader(script)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("script should have succeeded: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "reached") {
		t.Errorf("later step did not run: %s", out)
	}
}

func asExitError(err error, target **exec.ExitError) bool {
	e, ok := err.(*exec.ExitError)
	if ok {
		*target = e
	}
	return ok
}

func TestScriptEmitsPermissionAndMCPFlags(t *testing.T) {
	script := build(t, "name: r\nrepo: dx\nsteps:\n  - name: review\n    claude: review it\n    permission_mode: bypassPermissions\n    disallowed_tools: [WebFetch]\n    mcp_servers:\n      bitbucket:\n        command: python3\n        args: [ci/mcp/bitbucket-mcp.py]\n        env:\n          BITBUCKET_TOKEN: $BITBUCKET_TOKEN\n", nil)

	cmd := exec.Command("bash", "-n")
	cmd.Stdin = strings.NewReader(script)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("script is not valid bash: %v\n%s\n---\n%s", err, out, script)
	}
	for _, want := range []string{
		"--dangerously-skip-permissions",
		"--disallowedTools 'WebFetch'",
		"--mcp-config \"$CI_MCP_0\"",
		"ci_mcp_config ",
		"rm -f \"$CI_MCP_0\"",
	} {
		if !strings.Contains(script, want) {
			t.Errorf("script missing %q\n%s", want, script)
		}
	}
}

func TestMCPConfigExpandsEnvAtRuntime(t *testing.T) {
	dir := t.TempDir()
	fake := `#!/usr/bin/env bash
while [ $# -gt 0 ]; do
  if [ "$1" = "--mcp-config" ]; then cat "$2" > ` + dir + `/captured.json; fi
  shift
done
echo '{"type":"result","subtype":"success","result":"ok"}'
`
	if err := os.WriteFile(dir+"/claude", []byte(fake), 0o755); err != nil {
		t.Fatal(err)
	}

	script := build(t, "name: r\nrepo: dx\nsteps:\n  - name: dump\n    claude: x\n    mcp_servers:\n      bitbucket:\n        command: python3\n        args: [server.py]\n        env:\n          BITBUCKET_TOKEN: $BITBUCKET_TOKEN\n", nil)

	cmd := exec.Command("bash")
	cmd.Stdin = strings.NewReader(script)
	cmd.Env = append(os.Environ(), "BITBUCKET_TOKEN=super-secret", "PATH="+dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("script failed: %v\n%s", err, out)
	}

	raw, err := os.ReadFile(dir + "/captured.json")
	if err != nil {
		t.Fatalf("claude never received an --mcp-config file: %v", err)
	}
	var parsed struct {
		MCPServers map[string]struct {
			Command string            `json:"command"`
			Args    []string          `json:"args"`
			Env     map[string]string `json:"env"`
		} `json:"mcpServers"`
	}
	if err := json.Unmarshal(bytes.TrimSpace(raw), &parsed); err != nil {
		t.Fatalf("generated config is not json: %v\n%s", err, raw)
	}
	server, ok := parsed.MCPServers["bitbucket"]
	if !ok {
		t.Fatalf("bitbucket server missing from config: %s", raw)
	}
	if server.Env["BITBUCKET_TOKEN"] != "super-secret" {
		t.Errorf("env expansion = %q, want the real value", server.Env["BITBUCKET_TOKEN"])
	}
	if server.Command != "python3" || len(server.Args) != 1 {
		t.Errorf("command/args wrong: %+v", server)
	}
}

func TestOtherPermissionModesUseTheFlag(t *testing.T) {
	script := build(t, "name: r\nrepo: dx\nsteps:\n  - claude: x\n    permission_mode: acceptEdits\n", nil)
	if !strings.Contains(script, "--permission-mode 'acceptEdits'") {
		t.Errorf("acceptEdits should use --permission-mode:\n%s", script)
	}
	if strings.Contains(script, "--dangerously-skip-permissions") {
		t.Error("only bypassPermissions should use the dangerous flag")
	}
}

func TestMCPTempFileIsCleanedUp(t *testing.T) {
	script := build(t, "name: r\nrepo: dx\nsteps:\n  - claude: x\n    mcp_servers:\n      bb:\n        command: true\n", nil)
	if !strings.Contains(script, "rm -f \"$CI_MCP_0\"") {
		t.Errorf("generated script does not remove the mcp config:\n%s", script)
	}
}

func TestClaudeCostIsRecordedWhenAskedFor(t *testing.T) {
	dir := t.TempDir()
	fake := "#!/usr/bin/env bash\necho '{\"type\":\"result\",\"subtype\":\"success\",\"result\":\"done\",\"total_cost_usd\":0.4213}'\n"
	if err := os.WriteFile(dir+"/claude", []byte(fake), 0o755); err != nil {
		t.Fatal(err)
	}
	costFile := dir + "/cost.txt"

	script := build(t, "name: c\nrepo: dx\nsteps:\n  - claude: hello\n  - claude: again\n",
		map[string]string{"CI_CLAUDE_COST_FILE": costFile})

	cmd := exec.Command("bash")
	cmd.Stdin = strings.NewReader(script)
	cmd.Env = append(os.Environ(), "PATH="+dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("script failed: %v\n%s", err, out)
	}

	raw, err := os.ReadFile(costFile)
	if err != nil {
		t.Fatalf("no cost was recorded: %v", err)
	}
	if got := strings.Fields(string(raw)); len(got) != 2 || got[0] != "0.4213" {
		t.Errorf("cost file = %q, want one line per claude call", raw)
	}
}

func TestNoCostFileMeansNoRecording(t *testing.T) {
	dir := t.TempDir()
	fake := "#!/usr/bin/env bash\necho '{\"type\":\"result\",\"subtype\":\"success\",\"result\":\"done\",\"total_cost_usd\":0.5}'\n"
	if err := os.WriteFile(dir+"/claude", []byte(fake), 0o755); err != nil {
		t.Fatal(err)
	}
	script := build(t, "name: c\nrepo: dx\nsteps:\n  - claude: hello\n", nil)
	cmd := exec.Command("bash")
	cmd.Stdin = strings.NewReader(script)
	cmd.Env = append(os.Environ(), "PATH="+dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("script failed: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "done") {
		t.Errorf("claude result was not printed: %s", out)
	}
}

func TestHealthyRateLimitEventDoesNotLookLikeExhaustion(t *testing.T) {
	dir := t.TempDir()
	fake := "#!/usr/bin/env bash\n" +
		"echo '{\"type\":\"rate_limit_event\",\"rate_limit_info\":{\"status\":\"allowed\",\"unifiedWindows\":{\"five_hour\":{\"utilization\":0.99}}}}'\n" +
		"echo '{\"type\":\"result\",\"subtype\":\"success\",\"result\":\"all good\"}'\n"
	if err := os.WriteFile(dir+"/claude", []byte(fake), 0o755); err != nil {
		t.Fatal(err)
	}
	script := build(t, "name: x\nrepo: dx\nsteps:\n  - claude: hi\n", nil)
	cmd := exec.Command("bash")
	cmd.Stdin = strings.NewReader(script)
	cmd.Env = append(os.Environ(), "PATH="+dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("a healthy stream must not exit 75: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "five-hour window at 99%") {
		t.Errorf("usage was not reported in the log: %s", out)
	}
}

func TestExhaustedRateLimitEventExits75(t *testing.T) {
	dir := t.TempDir()
	fake := "#!/usr/bin/env bash\n" +
		"echo '{\"type\":\"rate_limit_event\",\"rate_limit_info\":{\"status\":\"rejected\",\"unifiedWindows\":{\"five_hour\":{\"utilization\":1}}}}'\n" +
		"echo '{\"type\":\"result\",\"subtype\":\"success\",\"result\":\"stopped\"}'\n"
	if err := os.WriteFile(dir+"/claude", []byte(fake), 0o755); err != nil {
		t.Fatal(err)
	}
	script := build(t, "name: x\nrepo: dx\nsteps:\n  - claude: hi\n", nil)
	cmd := exec.Command("bash")
	cmd.Stdin = strings.NewReader(script)
	cmd.Env = append(os.Environ(), "PATH="+dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	err := cmd.Run()
	var exitErr *exec.ExitError
	if !asExitError(err, &exitErr) || exitErr.ExitCode() != RateLimitExitCode {
		t.Fatalf("exit = %v, want %d so the daemon holds the task", err, RateLimitExitCode)
	}
}

func TestStreamedToolCallsReachTheLog(t *testing.T) {
	dir := t.TempDir()
	fake := "#!/usr/bin/env bash\n" +
		"echo '{\"type\":\"assistant\",\"message\":{\"content\":[{\"type\":\"text\",\"text\":\"bumping rails\"}]}}'\n" +
		"echo '{\"type\":\"assistant\",\"message\":{\"content\":[{\"type\":\"tool_use\",\"name\":\"Bash\",\"input\":{\"command\":\"bundle update rails\"}}]}}'\n" +
		"echo '{\"type\":\"user\",\"message\":{\"content\":[{\"type\":\"tool_result\",\"content\":\"Bundle updated!\"}]}}'\n" +
		"echo '{\"type\":\"result\",\"subtype\":\"success\",\"result\":\"done\",\"num_turns\":3,\"duration_ms\":5000,\"total_cost_usd\":0.1}'\n"
	if err := os.WriteFile(dir+"/claude", []byte(fake), 0o755); err != nil {
		t.Fatal(err)
	}
	script := build(t, "name: x\nrepo: dx\nsteps:\n  - claude: hi\n", nil)
	cmd := exec.Command("bash")
	cmd.Stdin = strings.NewReader(script)
	cmd.Env = append(os.Environ(), "PATH="+dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("script failed: %v\n%s", err, out)
	}
	for _, want := range []string{"bumping rails", "> Bash bundle update rails", "< Bundle updated!", "finished success after 3 turns"} {
		if !strings.Contains(string(out), want) {
			t.Errorf("log is missing %q:\n%s", want, out)
		}
	}
}

func TestExhaustionEmitsTheResetTimeForTheScheduler(t *testing.T) {
	dir := t.TempDir()
	fake := "#!/usr/bin/env bash\n" +
		"echo '{\"type\":\"rate_limit_event\",\"rate_limit_info\":{\"status\":\"rejected\",\"resetsAt\":1788468000,\"rateLimitType\":\"five_hour\",\"unifiedWindows\":{\"five_hour\":{\"utilization\":1}}}}'\n" +
		"echo '{\"type\":\"result\",\"subtype\":\"success\",\"result\":\"stopped\"}'\n"
	if err := os.WriteFile(dir+"/claude", []byte(fake), 0o755); err != nil {
		t.Fatal(err)
	}
	script := build(t, "name: x\nrepo: dx\nsteps:\n  - claude: hi\n", nil)
	cmd := exec.Command("bash")
	cmd.Stdin = strings.NewReader(script)
	cmd.Env = append(os.Environ(), "PATH="+dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	out, _ := cmd.CombinedOutput()

	if !strings.Contains(string(out), "FORGE-GATE exhausted resets_at=1788468000 window=five_hour") {
		t.Errorf("the reset time was not surfaced, so the scheduler can only poll blindly:\n%s", out)
	}
}
