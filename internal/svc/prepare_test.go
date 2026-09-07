package svc

import (
	"strings"
	"testing"
	"time"

	"github.com/simon-em/kranq/internal/exitcode"
	"github.com/simon-em/kranq/internal/state"
)

var caps = Capabilities{HasClaudeToken: true, TotalMemory: 16 << 30, TotalCPUs: 8}

func prepare(t *testing.T, yaml string, req SubmitRequest, c Capabilities) (state.Task, error) {
	t.Helper()
	req.SpecYAML = []byte(yaml)
	return Prepare(req, c, time.Unix(1_780_000_000, 0), "id-1")
}

const minimal = "name: spec\nsteps:\n  - run: true\n"

func TestTheRequestRepoWinsOverTheSpec(t *testing.T) {
	got, err := prepare(t, "name: spec\nrepo: dx\nsteps:\n  - run: true\n",
		SubmitRequest{Repo: "refrabec", Branch: "main"}, caps)
	if err != nil {
		t.Fatal(err)
	}
	if got.Repo != "refrabec" {
		t.Errorf("repo = %q, want the request to win so a shared task runs against the caller", got.Repo)
	}
}

func TestASpecWithoutARepoTakesItFromTheRequest(t *testing.T) {
	got, err := prepare(t, minimal, SubmitRequest{Repo: "dx", Branch: "main"}, caps)
	if err != nil || got.Repo != "dx" {
		t.Fatalf("task = %+v, err = %v", got, err)
	}
}

func TestNoRepoAnywhereIsAConfigurationError(t *testing.T) {
	_, err := prepare(t, minimal, SubmitRequest{Branch: "main"}, caps)
	var e *Error
	if !asError(err, &e) || e.Code != exitcode.Misconfigured {
		t.Fatalf("err = %v, want a Misconfigured Error", err)
	}
	if !strings.Contains(e.Msg, "no repo") {
		t.Errorf("message = %q", e.Msg)
	}
}

func TestNoBranchAnywhereIsAConfigurationError(t *testing.T) {
	_, err := prepare(t, minimal, SubmitRequest{Repo: "dx"}, caps)
	if err == nil || !strings.Contains(err.Error(), "no branch") {
		t.Errorf("err = %v, want it to name the missing branch", err)
	}
}

func TestAnInvalidSpecIsDistinctFromAMisconfiguration(t *testing.T) {
	_, err := prepare(t, "steps: []\n", SubmitRequest{Repo: "dx", Branch: "main"}, caps)
	var e *Error
	if !asError(err, &e) || e.Code != exitcode.InvalidSpec {
		t.Fatalf("err = %v (code %v), want InvalidSpec", err, codeOf(err))
	}
}

func TestAClaudeTaskIsRejectedAtSubmitWhenTheRunnerHasNoToken(t *testing.T) {
	_, err := prepare(t, "name: r\nsteps:\n  - claude: review this\n",
		SubmitRequest{Repo: "dx", Branch: "main"}, Capabilities{TotalMemory: 16 << 30})
	if err == nil {
		t.Fatal("expected a rejection")
	}
	if !strings.Contains(err.Error(), "CLAUDE_CODE_OAUTH_TOKEN") {
		t.Errorf("message = %q, want it to say what is missing rather than failing 40 minutes later", err)
	}
}

func TestATaskTooBigForTheMachineIsRejectedRatherThanQueuedForever(t *testing.T) {
	_, err := prepare(t, "name: big\nresources:\n  memory: 64GiB\nsteps:\n  - run: true\n",
		SubmitRequest{Repo: "dx", Branch: "main"}, caps)
	if err == nil {
		t.Fatal("expected a rejection; ci-runner queues this forever")
	}
	for _, want := range []string{"64.0GiB", "16.0GiB", "never be admitted"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("message %q is missing %q", err, want)
		}
	}
}

func TestTooManyCPUsIsRejected(t *testing.T) {
	_, err := prepare(t, "name: big\nresources:\n  cpus: 32\nsteps:\n  - run: true\n",
		SubmitRequest{Repo: "dx", Branch: "main"}, caps)
	if err == nil || !strings.Contains(err.Error(), "32 cpus") {
		t.Errorf("err = %v, want a cpu rejection", err)
	}
}

func TestLabelFallsBackThroughRequestSpecThenName(t *testing.T) {
	got, _ := prepare(t, minimal, SubmitRequest{Repo: "dx", Branch: "main", Label: "explicit"}, caps)
	if got.Label != "explicit" {
		t.Errorf("label = %q, want the request's", got.Label)
	}
	got, _ = prepare(t, minimal, SubmitRequest{Repo: "dx", Branch: "main"}, caps)
	if got.Label != "spec" {
		t.Errorf("label = %q, want it to fall back to the name", got.Label)
	}
}

func TestPrepareIsPureAndStartsQueued(t *testing.T) {
	got, err := prepare(t, minimal, SubmitRequest{Repo: "dx", Branch: "main"}, caps)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != state.StatusQueued {
		t.Errorf("status = %q, want queued", got.Status)
	}
	if got.CreatedAt != time.Unix(1_780_000_000, 0) {
		t.Error("Prepare must use the clock it is given, not time.Now")
	}
	if got.SpecYAML != minimal {
		t.Error("the original spec text must be kept verbatim for re-execution")
	}
}

func asError(err error, target **Error) bool {
	e, ok := err.(*Error)
	if ok {
		*target = e
	}
	return ok
}

func codeOf(err error) any {
	if e, ok := err.(*Error); ok {
		return e.Code
	}
	return nil
}

func TestTheSpecCanNameItsBuildFileAndTheRequestOverridesIt(t *testing.T) {
	spec := []byte("name: spec\nrepo: dx\nbranch: main\nkranqfile: Kranqfile.staging\nsteps:\n  - run: true\n")
	t1, err := Prepare(SubmitRequest{SpecYAML: spec}, Capabilities{}, time.Now(), "id")
	if err != nil {
		t.Fatal(err)
	}
	if t1.Kranqfile != "Kranqfile.staging" {
		t.Errorf("setup file = %q, want the one the spec named", t1.Kranqfile)
	}
	t2, err := Prepare(SubmitRequest{SpecYAML: spec, Kranqfile: "Kranqfile.perf"}, Capabilities{}, time.Now(), "id")
	if err != nil {
		t.Fatal(err)
	}
	if t2.Kranqfile != "Kranqfile.perf" {
		t.Errorf("setup file = %q, want the request to win over the spec", t2.Kranqfile)
	}
}

// One repository can hold every codebase, so where the objects landed and what
// the code is are two different names now. Conflating them sent the runner to
// look for the source in a repository that does not exist -- measured:
// "fatal: not a git repository: '.../alpha.git'" for code pushed to kranq.git.
func TestWhereTheObjectsAreIsNotWhatTheCodeIs(t *testing.T) {
	spec := []byte("name: t\nsteps:\n  - run: true\n")
	got, err := Prepare(SubmitRequest{
		SpecYAML:   spec,
		Repo:       "alpha",
		SourceRepo: "kranq",
		Branch:     "main",
	}, Capabilities{}, time.Now(), "id-1")
	if err != nil {
		t.Fatal(err)
	}
	if got.Repo != "alpha" {
		t.Errorf("Repo = %q, want what the pusher said the code is", got.Repo)
	}
	if got.SourceRepo != "kranq" {
		t.Errorf("SourceRepo = %q, want where the push landed", got.SourceRepo)
	}
}

// A push that named no repository is the old shape, where the two were the
// same. Nothing should start depending on SourceRepo being set.
func TestARequestWithOneNameStillWorks(t *testing.T) {
	got, err := Prepare(SubmitRequest{
		SpecYAML: []byte("name: t\nsteps:\n  - run: true\n"),
		Repo:     "dx",
		Branch:   "main",
	}, Capabilities{}, time.Now(), "id-2")
	if err != nil {
		t.Fatal(err)
	}
	if got.Repo != "dx" || got.SourceRepo != "" {
		t.Errorf("Repo=%q SourceRepo=%q, want dx and empty", got.Repo, got.SourceRepo)
	}
}
