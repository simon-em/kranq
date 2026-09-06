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

func TestParseResult(t *testing.T) {
	out := `remote: kranq: task 20260904T150405-abc succeeded (exit 0)
remote: ` + ResultMarker + ` id=20260904T150405-abc status=failed exit=7
To http://127.0.0.1:8420/git/dx.git
`
	res := ParseResult(out)
	if !res.Found {
		t.Fatal("the result line was not seen")
	}
	if res.ID != "20260904T150405-abc" || res.Status != "failed" || res.ExitCode != 7 {
		t.Fatalf("%+v", res)
	}
}

// Without a result line the caller must not assume success: the run may still
// be going, or the connection may have dropped.
func TestParseResultReportsAbsence(t *testing.T) {
	for _, out := range []string{"", "remote: kranq: task queued\nTo http://x\n", "KRANQ-RESULTS id=x"} {
		if ParseResult(out).Found && !strings.Contains(out, ResultMarker+" ") {
			t.Fatalf("a result was invented from %q", out)
		}
	}
	if ParseResult("no marker here").Found {
		t.Fatal("a result was found in output that has none")
	}
}

func TestTheLastResultLineWins(t *testing.T) {
	out := ResultMarker + " id=a status=succeeded exit=0\n" +
		ResultMarker + " id=b status=failed exit=3\n"
	res := ParseResult(out)
	if res.ID != "b" || res.ExitCode != 3 {
		t.Fatalf("%+v", res)
	}
}

// A shared task lives in a submodule, which is a gitlink: its files are not in
// the commit being pushed, so the spec has to travel with the push.
func TestASpecCanTravelWithThePush(t *testing.T) {
	spec := []byte("name: maintenance\nsteps:\n  - run: echo hi\n")
	encoded, err := EncodeSpec(spec)
	if err != nil {
		t.Fatal(err)
	}
	if strings.ContainsAny(encoded, "\n\x00") {
		t.Errorf("encoded spec carries bytes an environment variable cannot: %q", encoded)
	}
	req, err := ParseOptions([]string{"spec=" + encoded})
	if err != nil {
		t.Fatalf("ParseOptions: %v", err)
	}
	if string(req.Spec) != string(spec) {
		t.Errorf("spec came back as %q", req.Spec)
	}
}

// gzip is what keeps a real task well inside the 64KiB a push option carries.
func TestAnInlineSpecStaysSmallEnoughToSend(t *testing.T) {
	spec := []byte(strings.Repeat("  - name: a step\n    run: bundle exec rspec\n", 500))
	encoded, err := EncodeSpec(spec)
	if err != nil {
		t.Fatal(err)
	}
	if len(encoded) >= 60000 {
		t.Errorf("a %d byte spec encodes to %d bytes, too close to git's packet limit", len(spec), len(encoded))
	}
}

func TestAMangledInlineSpecIsRefusedClearly(t *testing.T) {
	for name, value := range map[string]string{
		"not base64": "spec=!!!!",
		"not gzip":   "spec=aGVsbG8=",
		"empty":      "spec=",
	} {
		if _, err := ParseOptions([]string{value}); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
}

func TestATaskPathIsStillEnoughOnItsOwn(t *testing.T) {
	req, err := ParseOptions([]string{"task=ci/tasks/spec.yaml"})
	if err != nil || req.Task != "ci/tasks/spec.yaml" || len(req.Spec) != 0 {
		t.Errorf("plain git push must keep working: %+v %v", req, err)
	}
}
