package run

import (
	"strings"
	"testing"
)

const base = "git@bitbucket.org:effetmonstre"

func TestCloneUsesSshWhenNoTokenWasForwarded(t *testing.T) {
	got := Remote{Base: base, Repo: "dx"}.CloneCommand("main", "~/work")
	if !strings.Contains(got, "git@bitbucket.org:effetmonstre/dx.git") {
		t.Errorf("command = %q, want the ssh remote", got)
	}
	if strings.Contains(got, "credential.helper") {
		t.Errorf("no token means no credential helper: %q", got)
	}
}

func TestCloneUsesHttpsWhenATokenWasForwarded(t *testing.T) {
	got := Remote{Base: base, Repo: "dx", Token: "secret"}.CloneCommand("ci/lima", "~/work")
	if !strings.Contains(got, "https://bitbucket.org/effetmonstre/dx.git") {
		t.Errorf("command = %q, want the https remote", got)
	}
	if strings.Contains(got, "secret") {
		t.Errorf("the token must never appear in argv, it goes through the env: %q", got)
	}
	if !strings.Contains(got, "$KRANQ_GIT_TOKEN") {
		t.Errorf("the helper must read the token from the env: %q", got)
	}
	if !strings.Contains(got, "--branch 'ci/lima'") {
		t.Errorf("a branch with a slash must survive quoting: %q", got)
	}
}

func TestResolveTokenPrefersTheMostSpecific(t *testing.T) {
	all := map[string]string{
		"BITBUCKET_STEP_OAUTH_TOKEN": "step",
		"BITBUCKET_API_TOKEN":        "api",
		"BITBUCKET_TOKEN":            "repo",
		"KRANQ_GIT_TOKEN":            "kranq",
	}
	if got := ResolveToken(all); got != "kranq" {
		t.Errorf("ResolveToken = %q, want the explicit kranq one first", got)
	}
	delete(all, "KRANQ_GIT_TOKEN")
	if got := ResolveToken(all); got != "repo" {
		t.Errorf("ResolveToken = %q, want BITBUCKET_TOKEN next", got)
	}
	if got := ResolveToken(map[string]string{"BITBUCKET_TOKEN": ""}); got != "" {
		t.Errorf("an empty value must not count as a token, got %q", got)
	}
}

func TestCloneDestinationIsNotSingleQuoted(t *testing.T) {
	dest := `"$HOME/work"`
	for _, r := range []Remote{{Base: base, Repo: "dx"}, {Base: base, Repo: "dx", Token: "t"}} {
		got := r.CloneCommand("main", dest)
		if !strings.HasSuffix(got, dest) {
			t.Errorf("command = %q, want it to end with an expandable %s", got, dest)
		}
		if strings.Contains(got, `'~/work'`) || strings.Contains(got, `'$HOME`) {
			t.Errorf("the destination was quoted so the shell cannot expand it, which makes git create a directory literally named ~: %q", got)
		}
	}
}
