package task

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

const RateLimitExitCode = 75

const scriptPreamble = `#!/usr/bin/env bash
set -euo pipefail

ci_step() { printf '\n===== %s =====\n' "$1" >&2; }

ci_claude_stream() {
    python3 -c '
import json, os, sys, time

start = time.time()
exhausted = False

def out(msg):
    minutes, seconds = divmod(int(time.time() - start), 60)
    for line in str(msg).rstrip().split("\n"):
        sys.stderr.write("[%02d:%02d] %s\n" % (minutes, seconds, line[:2000]))
    sys.stderr.flush()

def brief(text, head=4, tail=4):
    lines = str(text).rstrip().split("\n")
    if len(lines) <= head + tail + 1:
        return "\n".join(lines)
    dropped = len(lines) - head - tail
    return "\n".join(lines[:head] + ["... %d more lines ..." % dropped] + lines[-tail:])

for raw in sys.stdin:
    raw = raw.strip()
    if not raw:
        continue
    try:
        ev = json.loads(raw)
    except Exception:
        out(raw)
        continue
    kind = ev.get("type")
    if kind == "system" and ev.get("subtype") == "init":
        out("claude %s, model %s" % (ev.get("claude_code_version", "?"), ev.get("model", "?")))
    elif kind == "rate_limit_event":
        info = ev.get("rate_limit_info") or {}
        window = (info.get("unifiedWindows") or {}).get("five_hour") or {}
        out("usage %s, five-hour window at %.0f%%" % (
            info.get("status", "unknown"), 100 * (window.get("utilization") or 0)))
        if info.get("status") not in ("allowed", None):
            exhausted = True
            resets_at = info.get("resetsAt") or window.get("resetsAt") or 0
            out("FORGE-GATE exhausted resets_at=%d window=%s" % (
                int(resets_at), info.get("rateLimitType", "unknown")))
    elif kind in ("assistant", "user"):
        blocks = (ev.get("message") or {}).get("content")
        if not isinstance(blocks, list):
            continue
        for block in blocks:
            btype = block.get("type")
            if btype == "text" and (block.get("text") or "").strip():
                out(block["text"].strip())
            elif btype == "tool_use":
                params = block.get("input") or {}
                detail = (params.get("command") or params.get("file_path")
                          or params.get("pattern") or params.get("path") or "")
                out("> %s %s" % (block.get("name"), str(detail)[:400]))
            elif btype == "tool_result":
                body = block.get("content")
                if isinstance(body, list):
                    body = "".join(b.get("text", "") for b in body if isinstance(b, dict))
                if str(body or "").strip():
                    out("%s %s" % ("!" if block.get("is_error") else "<", brief(body)))
    elif kind == "result":
        spend = ev.get("total_cost_usd")
        out("finished %s after %s turns in %.0fs, %s" % (
            ev.get("subtype"), ev.get("num_turns"), (ev.get("duration_ms") or 0) / 1000.0,
            ("$%.4f" % spend) if isinstance(spend, (int, float)) else "cost unknown"))
        denials = ev.get("permission_denials") or []
        if denials:
            out("%d permission denial(s); claude was blocked from acting" % len(denials))
        text = ev.get("result") or ""
        sys.stdout.write(text + "\n")
        costfile = os.environ.get("CI_CLAUDE_COST_FILE")
        if costfile and ev.get("total_cost_usd") is not None:
            with open(costfile, "a") as fh:
                fh.write("%s\n" % ev["total_cost_usd"])
        lowered = text.lower()
        for marker in ("usage limit reached", "exceeded your usage", "out of credits", "out of usage"):
            if marker in lowered:
                exhausted = True

sys.exit(75 if exhausted else 0)
'
}

ci_claude() {
    local prompt="$1"
    shift
    local raw
    raw="$(mktemp)"
    local statuses
    set +e
    claude -p "$prompt" --output-format stream-json --verbose "$@" 2>&1 | tee "$raw" | ci_claude_stream
    statuses="${PIPESTATUS[0]} ${PIPESTATUS[2]}"
    set -e
    local claude_rc="${statuses%% *}"
    local render_rc="${statuses##* }"
    if [ "$render_rc" != "0" ] && [ "$render_rc" != "75" ]; then
        cat "$raw"
    fi
    rm -f "$raw"
    if [ "$render_rc" = "75" ]; then
        echo "claude reported no remaining usage" >&2
        exit 75
    fi
    return "$claude_rc"
}

ci_mcp_config() {
    python3 -c 'import json, os, sys
spec = json.loads(sys.argv[1])
for server in (spec.get("mcpServers") or {}).values():
    server["env"] = {k: os.path.expandvars(v) for k, v in (server.get("env") or {}).items()}
sys.stdout.write(json.dumps(spec))' "$1"
}
`

func BuildScript(s Spec, env map[string]string) string {
	var b strings.Builder
	b.WriteString(scriptPreamble)
	if s.Fenced() {
		b.WriteString(fencePreamble)
	}

	merged := map[string]string{}
	for k, v := range s.Env {
		merged[k] = v
	}
	for k, v := range env {
		merged[k] = v
	}
	keys := make([]string, 0, len(merged))
	for k := range merged {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	if len(keys) > 0 {
		b.WriteString("\n")
		for _, k := range keys {
			fmt.Fprintf(&b, "export %s=%s\n", k, shellQuote(merged[k]))
		}
	}

	for i, st := range s.Steps {
		name := st.Name
		if name == "" {
			name = fmt.Sprintf("step %d", i+1)
		}
		b.WriteString("\n")
		fmt.Fprintf(&b, "ci_step %s\n", shellQuote(name))
		body := stepBody(st, i)
		if st.ContinueOn {
			fmt.Fprintf(&b, "if ! (\n%s\n); then printf 'step %%s failed, continuing\\n' %s >&2; fi\n", body, shellQuote(name))
			continue
		}
		b.WriteString(body)
		b.WriteString("\n")
	}
	return b.String()
}

func stepBody(st Step, index int) string {
	if st.Run != "" {
		return strings.TrimRight(st.Run, "\n")
	}
	delim := fmt.Sprintf("CI_PROMPT_%d", index)
	mcpVar := fmt.Sprintf("CI_MCP_%d", index)

	var args []string
	if len(st.AllowedTools) > 0 {
		args = append(args, "--allowedTools "+shellQuote(strings.Join(st.AllowedTools, ",")))
	}
	if len(st.DisallowedTools) > 0 {
		args = append(args, "--disallowedTools "+shellQuote(strings.Join(st.DisallowedTools, ",")))
	}
	if st.MaxTurns > 0 {
		args = append(args, fmt.Sprintf("--max-turns %d", st.MaxTurns))
	}
	if st.Effort != "" {
		args = append(args, "--effort "+shellQuote(st.Effort))
	}
	if st.Model != "" {
		args = append(args, "--model "+shellQuote(st.Model))
	}
	if st.PermissionMode == "bypassPermissions" {
		args = append(args, "--dangerously-skip-permissions")
	} else if st.PermissionMode != "" {
		args = append(args, "--permission-mode "+shellQuote(st.PermissionMode))
	}
	if len(st.MCPServers) > 0 {
		args = append(args, fmt.Sprintf("--mcp-config \"$%s\"", mcpVar))
	}

	suffix := ""
	if len(args) > 0 {
		suffix = " " + strings.Join(args, " ")
	}
	prompt := strings.TrimRight(st.Claude, "\n")
	call := fmt.Sprintf("ci_claude \"$(cat <<'%s'\n%s\n%s\n)\"%s", delim, prompt, delim, suffix)

	if len(st.MCPServers) == 0 {
		return call
	}
	config, err := mcpConfigJSON(st.MCPServers)
	if err != nil {
		return call
	}
	return fmt.Sprintf("%s=\"$(mktemp)\"\nci_mcp_config %s > \"$%s\"\n%s\nci_rc=$?\nrm -f \"$%s\"\n(exit $ci_rc)",
		mcpVar, shellQuote(config), mcpVar, call, mcpVar)
}

func mcpConfigJSON(servers map[string]MCPServer) (string, error) {
	type wireServer struct {
		Command string            `json:"command"`
		Args    []string          `json:"args,omitempty"`
		Env     map[string]string `json:"env,omitempty"`
	}
	wire := map[string]map[string]wireServer{"mcpServers": {}}
	for name, srv := range servers {
		wire["mcpServers"][name] = wireServer{Command: srv.Command, Args: srv.Args, Env: srv.Env}
	}
	data, err := json.Marshal(wire)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

func shellQuote(v string) string {
	return "'" + strings.ReplaceAll(v, "'", `'\''`) + "'"
}
