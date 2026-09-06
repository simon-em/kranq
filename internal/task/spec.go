package task

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

type Resources struct {
	Memory string `yaml:"memory"`
	CPUs   int    `yaml:"cpus"`
	Disk   string `yaml:"disk"`
}

type MCPServer struct {
	Command string            `yaml:"command"`
	Args    []string          `yaml:"args"`
	Env     map[string]string `yaml:"env"`
}

type Step struct {
	Name            string               `yaml:"name"`
	Run             string               `yaml:"run"`
	Claude          string               `yaml:"claude"`
	AllowedTools    []string             `yaml:"allowed_tools"`
	DisallowedTools []string             `yaml:"disallowed_tools"`
	MaxTurns        int                  `yaml:"max_turns"`
	Effort          string               `yaml:"effort"`
	Model           string               `yaml:"model"`
	PermissionMode  string               `yaml:"permission_mode"`
	MCPServers      map[string]MCPServer `yaml:"mcp_servers"`
	ContinueOn      bool                 `yaml:"continue_on_error"`
}

type Effects struct {
	Push  bool   `yaml:"push"`
	Key   string `yaml:"key"`
	Scope string `yaml:"scope"`
}

type Spec struct {
	Name      string            `yaml:"name"`
	Repo      string            `yaml:"repo"`
	Branch    string            `yaml:"branch"`
	Label     string            `yaml:"label"`
	Forgefile string            `yaml:"forgefile"`
	Artifacts string            `yaml:"artifacts"`
	Resources Resources         `yaml:"resources"`
	Env       map[string]string `yaml:"env"`
	Effects   Effects           `yaml:"effects"`
	Steps     []Step            `yaml:"steps"`
}

var validPermissionModes = map[string]bool{
	"acceptEdits":       true,
	"auto":              true,
	"bypassPermissions": true,
	"manual":            true,
	"dontAsk":           true,
	"plan":              true,
}

var memoryPattern = regexp.MustCompile(`^([0-9]+(?:\.[0-9]+)?)\s*(GiB|GB|G|MiB|MB|M)?$`)

func Parse(data []byte) (Spec, error) {
	var s Spec
	if err := yaml.Unmarshal(data, &s); err != nil {
		return s, fmt.Errorf("invalid task yaml: %w", err)
	}
	if err := s.validate(); err != nil {
		return s, err
	}
	if s.Label == "" {
		s.Label = s.Name
	}
	if s.Artifacts == "" {
		s.Artifacts = "ci-artifacts"
	}
	return s, nil
}

func (s Spec) validate() error {
	if s.Name == "" {
		return errors.New("task name is required")
	}
	if len(s.Steps) == 0 {
		return errors.New("task needs at least one step")
	}
	for i, st := range s.Steps {
		if st.Run == "" && st.Claude == "" {
			return fmt.Errorf("step %d (%q) has neither run nor claude", i+1, st.Name)
		}
		if st.Run != "" && st.Claude != "" {
			return fmt.Errorf("step %d (%q) has both run and claude", i+1, st.Name)
		}
	}
	for i, st := range s.Steps {
		if st.PermissionMode != "" && !validPermissionModes[st.PermissionMode] {
			return fmt.Errorf("step %d (%q): unknown permission_mode %q", i+1, st.Name, st.PermissionMode)
		}
		for name, server := range st.MCPServers {
			if server.Command == "" {
				return fmt.Errorf("step %d (%q): mcp server %q has no command", i+1, st.Name, name)
			}
			if st.Claude == "" {
				return fmt.Errorf("step %d (%q): mcp_servers only apply to a claude step", i+1, st.Name)
			}
		}
	}
	if s.Resources.Memory != "" {
		if _, err := ParseMemory(s.Resources.Memory); err != nil {
			return err
		}
	}
	return s.Effects.validate()
}

var validEffectScopes = map[string]bool{"": true, "branch": true, "repo": true}

func (e Effects) validate() error {
	if !validEffectScopes[e.Scope] {
		return fmt.Errorf("effects.scope must be branch or repo, not %q", e.Scope)
	}
	if !e.Push && (e.Key != "" || e.Scope != "") {
		return errors.New("effects.key and effects.scope only apply when effects.push is true")
	}
	return nil
}

func (s Spec) Fenced() bool { return s.Effects.Push }

// FenceKind is the first component of the fence identity. Two tasks sharing a
// kind, a repo and a scope contend for the same fence.
func (s Spec) FenceKind() string {
	if s.Effects.Key != "" {
		return s.Effects.Key
	}
	return s.Name
}

// FenceBranch is empty when the effect is repo-wide, which makes every branch
// of that repo contend for one fence.
func (s Spec) FenceBranch(branch string) string {
	if s.Effects.Scope == "repo" {
		return ""
	}
	return branch
}

func (s Spec) NeedsClaude() bool {
	for _, st := range s.Steps {
		if st.Claude != "" {
			return true
		}
	}
	return false
}

func (s Spec) MemoryBytes() int64 {
	if s.Resources.Memory == "" {
		return 0
	}
	n, err := ParseMemory(s.Resources.Memory)
	if err != nil {
		return 0
	}
	return n
}

func ParseMemory(v string) (int64, error) {
	m := memoryPattern.FindStringSubmatch(strings.TrimSpace(v))
	if m == nil {
		return 0, fmt.Errorf("unrecognised memory value %q", v)
	}
	n, err := strconv.ParseFloat(m[1], 64)
	if err != nil {
		return 0, fmt.Errorf("unrecognised memory value %q", v)
	}
	switch strings.ToLower(m[2]) {
	case "mib", "mb", "m":
		return int64(n * (1 << 20)), nil
	default:
		return int64(n * (1 << 30)), nil
	}
}
