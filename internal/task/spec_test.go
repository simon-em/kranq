package task

import "testing"

func TestParseRejectsInvalidSpecs(t *testing.T) {
	cases := map[string]string{
		"no name":        "repo: dx\nsteps:\n  - run: true\n",
		"no steps":       "name: x\nrepo: dx\n",
		"empty step":     "name: x\nrepo: dx\nsteps:\n  - name: nothing\n",
		"run and claude": "name: x\nrepo: dx\nsteps:\n  - run: true\n    claude: hi\n",
		"bad memory":     "name: x\nrepo: dx\nresources:\n  memory: plenty\nsteps:\n  - run: true\n",
	}
	for name, spec := range cases {
		if _, err := Parse([]byte(spec)); err == nil {
			t.Errorf("%s: expected an error, got none", name)
		}
	}
}

func TestParseAcceptsASpecWithoutARepo(t *testing.T) {
	s, err := Parse([]byte("name: generic\nsteps:\n  - run: true\n"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if s.Repo != "" {
		t.Errorf("repo = %q, want empty so the submitter supplies it", s.Repo)
	}
}

func TestParseDefaults(t *testing.T) {
	s, err := Parse([]byte("name: spec\nrepo: dx\nsteps:\n  - run: bundle exec rspec\n"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if s.Label != "spec" {
		t.Errorf("label = %q, want the name as fallback", s.Label)
	}
	if s.Artifacts != "ci-artifacts" {
		t.Errorf("artifacts = %q, want the default", s.Artifacts)
	}
	if s.NeedsClaude() {
		t.Error("NeedsClaude = true for a spec with no claude step")
	}
}

func TestNeedsClaude(t *testing.T) {
	s, err := Parse([]byte("name: r\nrepo: dx\nsteps:\n  - run: true\n  - claude: review this\n"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !s.NeedsClaude() {
		t.Error("NeedsClaude = false despite a claude step")
	}
}

func TestParseMemory(t *testing.T) {
	cases := []struct {
		in   string
		want int64
	}{
		{"4GiB", 4 << 30},
		{"4", 4 << 30},
		{"512MiB", 512 << 20},
		{"1.5GiB", 1610612736},
	}
	for _, c := range cases {
		got, err := ParseMemory(c.in)
		if err != nil {
			t.Errorf("ParseMemory(%q) errored: %v", c.in, err)
			continue
		}
		if got != c.want {
			t.Errorf("ParseMemory(%q) = %d, want %d", c.in, got, c.want)
		}
	}
	if _, err := ParseMemory("lots"); err == nil {
		t.Error("ParseMemory accepted a nonsense value")
	}
}

func TestPermissionModeIsValidated(t *testing.T) {
	if _, err := Parse([]byte("name: x\nrepo: dx\nsteps:\n  - claude: hi\n    permission_mode: yolo\n")); err == nil {
		t.Error("expected an unknown permission_mode to be rejected")
	}
	if _, err := Parse([]byte("name: x\nrepo: dx\nsteps:\n  - claude: hi\n    permission_mode: bypassPermissions\n")); err != nil {
		t.Errorf("bypassPermissions should be accepted: %v", err)
	}
}

func TestMCPServersRequireAClaudeStep(t *testing.T) {
	spec := "name: x\nrepo: dx\nsteps:\n  - run: true\n    mcp_servers:\n      bb:\n        command: python3\n"
	if _, err := Parse([]byte(spec)); err == nil {
		t.Error("mcp_servers on a shell step should be rejected")
	}
}

func TestMCPServerNeedsACommand(t *testing.T) {
	spec := "name: x\nrepo: dx\nsteps:\n  - claude: hi\n    mcp_servers:\n      bb:\n        args: [a]\n"
	if _, err := Parse([]byte(spec)); err == nil {
		t.Error("an mcp server without a command should be rejected")
	}
}
