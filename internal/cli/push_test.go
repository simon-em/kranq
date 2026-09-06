package cli

import (
	"strings"
	"testing"

	"github.com/effetmonstre/forge/internal/exitcode"
)

func TestPushURL(t *testing.T) {
	got, err := pushURL("https://ci.example.com", "dx", "forge_abc123")
	if err != nil {
		t.Fatal(err)
	}
	if got != "https://forge:forge_abc123@ci.example.com/git/dx.git" {
		t.Fatalf("got %q", got)
	}
}

func TestPushURLToleratesTrailingSlashesAndAnExplicitGitPath(t *testing.T) {
	for _, endpoint := range []string{
		"https://ci.example.com/",
		"https://ci.example.com/git",
		"https://ci.example.com/git/",
	} {
		got, err := pushURL(endpoint, "dx", "t")
		if err != nil {
			t.Fatalf("%q: %v", endpoint, err)
		}
		if !strings.HasSuffix(got, "/git/dx.git") {
			t.Fatalf("%q -> %q", endpoint, got)
		}
		if strings.Contains(got, "//git") || strings.Contains(got, "git/git") {
			t.Fatalf("%q -> %q", endpoint, got)
		}
	}
}

func TestPushURLRejectsSomethingThatIsNotAURL(t *testing.T) {
	for _, bad := range []string{"", "ci.example.com", "/not/a/url", "::::"} {
		if _, err := pushURL(bad, "dx", "t"); err == nil {
			t.Fatalf("%q was accepted as an endpoint", bad)
		}
	}
}

// The token is the password. It must not end up as a command-line argument,
// where anything else on the machine can read it out of the process list.
func TestTheTokenTravelsInTheURLNotAnArgument(t *testing.T) {
	got, err := pushURL("https://ci.example.com", "dx", "forge_s3cr3t")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "forge_s3cr3t@") {
		t.Fatalf("the token is not in the userinfo: %q", got)
	}
}

// flag.PrintDefaults prints each flag's default value, and usage is printed on
// every misuse, so a token defaulted from the environment would be echoed into
// the terminal and into CI logs.
func TestUsageNeverPrintsTheToken(t *testing.T) {
	t.Setenv("FORGE_TOKEN", "forge_s3cr3tvalue")
	t.Setenv("FORGE_ENDPOINT", "https://ci.example.com")
	_, out, errb := invoke(t, "push")
	if strings.Contains(out+errb, "forge_s3cr3tvalue") {
		t.Fatalf("the token was printed:\n%s%s", out, errb)
	}
}

func TestEnvShorthandIsAccepted(t *testing.T) {
	t.Setenv("FORGE_TOKEN", "")
	code, _, errb := invoke(t, "push", "spec.yaml", "-e", "FOO=bar", "--repo", "dx")
	// It must fail for the missing endpoint, not for an unknown flag.
	if strings.Contains(errb, "flag provided but not defined") {
		t.Fatalf("-e was not accepted: %s", errb)
	}
	if code == exitcode.OK {
		t.Fatal("a push with no endpoint succeeded")
	}
}

// A rejected token is the pipeline's configuration; a rejected spec is the code
// being pushed. A CI step should be able to tell them apart from the exit code.
func TestRefusalCodeTellsAuthFromEverythingElse(t *testing.T) {
	cases := map[string]int{
		"fatal: Authentication failed for 'https://ci/git/dx.git'":  exitcode.Unauthorized,
		"remote: a forge token is required":                         exitcode.Unauthorized,
		"fatal: could not resolve host: ci.example.com":             exitcode.Unreachable,
		"fatal: unable to access: Failed to connect to ci port 443": exitcode.Unreachable,
		"remote: forge: ci/spec.yaml is not in the pushed commit":   exitcode.InvalidSpec,
		// What ssh actually says when the key is not installed: it never uses
		// the word "authentication", so matching on that alone missed it.
		"macmini@host: Permission denied (publickey).":                  exitcode.Unauthorized,
		"fatal: Could not read from remote repository.":                 exitcode.Unauthorized,
		`forge: "cat" is not allowed; this key may only push and fetch`: exitcode.Unauthorized,
		"": exitcode.InvalidSpec,
	}
	for output, want := range cases {
		if got := refusalCode(output); got != want {
			t.Fatalf("%q -> %d, want %d", output, got, want)
		}
	}
}

// Over ssh the key is the credential, so there is no token to place anywhere,
// and the path is the repository because a forced command decides where it goes.
func TestPushURLOverSSH(t *testing.T) {
	got, err := pushURL("ssh://macmini@142.127.69.2:333", "dx", "")
	if err != nil {
		t.Fatal(err)
	}
	if got != "ssh://macmini@142.127.69.2:333/dx.git" {
		t.Fatalf("got %q", got)
	}
	// A token must not be smuggled into an ssh url, where it would sit in the
	// process list for no benefit.
	with, err := pushURL("ssh://macmini@host:333", "dx", "forge_s3cr3t")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(with, "s3cr3t") {
		t.Fatalf("the token ended up in an ssh url: %q", with)
	}
}

func TestAnHTTPEndpointStillNeedsAToken(t *testing.T) {
	if _, err := pushURL("https://ci.example.com", "dx", ""); err == nil {
		t.Fatal("an http endpoint was accepted with no token")
	}
}

// The push key is a forced-command key that can do nothing but push, and it
// sits beside an ordinary key for the same host. Without IdentitiesOnly, ssh
// offers the ordinary key first and the forced command never runs.
func TestSSHCommandPinsTheIdentity(t *testing.T) {
	got := sshCommand("/Users/x/.ssh/forge_push")
	for _, want := range []string{"-i '/Users/x/.ssh/forge_push'", "IdentitiesOnly=yes", "IdentityAgent=none"} {
		if !strings.Contains(got, want) {
			t.Fatalf("%q missing from %q", want, got)
		}
	}
}

func TestSSHCommandQuotesAPathWithSpaces(t *testing.T) {
	got := sshCommand("/Users/some one/.ssh/k")
	if !strings.Contains(got, `'/Users/some one/.ssh/k'`) {
		t.Fatalf("a path with a space was not quoted: %q", got)
	}
}

// Asking for forge as the receive-pack is what lets an ordinary ssh key push:
// git runs forge on the far side itself, so nothing has to be set up there.
func TestSSHPushAsksForForgeAsTheReceivePack(t *testing.T) {
	if defaultReceivePack == "" {
		t.Fatal("there is no default, so a plain ssh endpoint needs server-side setup")
	}
	if !strings.Contains(defaultReceivePack, "forge") {
		t.Fatalf("the default does not name forge: %q", defaultReceivePack)
	}
	// $HOME rather than a literal path: the far side expands it, and forge does
	// not know that machine's home directory.
	if !strings.HasPrefix(defaultReceivePack, "$HOME/") {
		t.Fatalf("the default assumes a path forge cannot know: %q", defaultReceivePack)
	}
}

func TestOrElse(t *testing.T) {
	if got := orElse("", "fallback"); got != "fallback" {
		t.Fatalf("got %q", got)
	}
	if got := orElse("set", "fallback"); got != "set" {
		t.Fatalf("got %q", got)
	}
}

// Pushing the same commit twice must run the task twice: retrying a failed
// pipeline step is the ordinary way to re-run a job, and git runs no hook at
// all for a ref that is already where it would put it.
func TestEveryPushGoesToItsOwnRef(t *testing.T) {
	first, second := pushRef(), pushRef()
	if first == second {
		t.Fatalf("two pushes share the ref %q, so the second would be a no-op", first)
	}
	for _, ref := range []string{first, second} {
		if !strings.HasPrefix(ref, "refs/forge/push/") {
			t.Errorf("ref = %q, want it outside refs/heads so it is not mistaken for a branch", ref)
		}
		if strings.Count(ref, "/") < 2 {
			t.Errorf("ref = %q; git refuses a single-segment refname remotely", ref)
		}
	}
}

func TestABranchIsReportedEvenWhenTheRefIsANonce(t *testing.T) {
	if got := branchFromRef("refs/heads/ci/lima"); got != "ci/lima" {
		t.Errorf("branchFromRef = %q, want the branch", got)
	}
	if got := branchFromRef(pushRef()); got != "forge-push" {
		t.Errorf("branchFromRef = %q, want a readable fallback, not a nonce", got)
	}
}
