package gitsrv

import (
	"strings"
	"testing"
)

func TestParseOptions(t *testing.T) {
	req, err := ParseOptions([]string{
		"task=ci/tasks/spec.yaml",
		"label=spec",
		"branch=feature/x",
		"keep-vm=on-failure",
		"env.BITBUCKET_TOKEN=s3cr3t",
		"env.MAINTENANCE_SCAN_URL=https://example.test",
		"detach",
	})
	if err != nil {
		t.Fatal(err)
	}
	if req.Task != "ci/tasks/spec.yaml" || req.Label != "spec" || req.Branch != "feature/x" {
		t.Fatalf("%+v", req)
	}
	if req.Keep != "on-failure" || !req.Detach {
		t.Fatalf("%+v", req)
	}
	if req.Env["BITBUCKET_TOKEN"] != "s3cr3t" || len(req.Env) != 2 {
		t.Fatalf("env = %v", req.Env)
	}
}

func TestATaskIsRequired(t *testing.T) {
	_, err := ParseOptions([]string{"label=spec"})
	if err == nil {
		t.Fatal("a push with no task was accepted")
	}
	if !strings.Contains(err.Error(), "-o task=") {
		t.Fatalf("the error does not say how to fix it: %v", err)
	}
}

// A typo'd option would otherwise be silently ignored, and the run would use a
// default the caller did not ask for.
func TestUnknownOptionsAreRefused(t *testing.T) {
	_, err := ParseOptions([]string{"task=t.yaml", "keepvm=always", "artifacts=out"})
	if err == nil {
		t.Fatal("unknown options were ignored")
	}
	for _, want := range []string{"artifacts", "keepvm"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("%q not named in %v", want, err)
		}
	}
}

func TestAnEnvOptionWithNoValueIsRefused(t *testing.T) {
	// -o env.FOO would otherwise set FOO to the empty string, which is not what
	// anyone means by it.
	_, err := ParseOptions([]string{"task=t.yaml", "env.BITBUCKET_TOKEN"})
	if err == nil {
		t.Fatal("a valueless env option was accepted")
	}
	if !strings.Contains(err.Error(), "BITBUCKET_TOKEN") {
		t.Fatalf("%v", err)
	}
	if _, err := ParseOptions([]string{"task=t.yaml", "env.=x"}); err == nil {
		t.Fatal("an env option naming no variable was accepted")
	}
}

func TestAnEmptyEnvValueIsAllowedWhenWritten(t *testing.T) {
	req, err := ParseOptions([]string{"task=t.yaml", "env.MAINTENANCE_SCAN_URL="})
	if err != nil {
		t.Fatal(err)
	}
	if v, ok := req.Env["MAINTENANCE_SCAN_URL"]; !ok || v != "" {
		t.Fatalf("an explicitly empty value was dropped: %v", req.Env)
	}
}

// Push options carry tokens, so anything echoed to the pusher or a log must
// keep the names and drop the values.
func TestRedactKeepsNamesAndDropsValues(t *testing.T) {
	got := Redact([]string{"task=ci/spec.yaml", "env.BITBUCKET_TOKEN=s3cr3t", "detach"})
	joined := strings.Join(got, " ")
	if strings.Contains(joined, "s3cr3t") {
		t.Fatalf("a secret survived redaction: %q", joined)
	}
	for _, want := range []string{"env.BITBUCKET_TOKEN", "task", "detach"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("%q was lost: %q", want, joined)
		}
	}
}
