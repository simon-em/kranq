package image

import (
	"context"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/effetmonstre/forge/internal/project"
	"github.com/effetmonstre/forge/internal/vm"
)

const DefaultTTL = 14 * 24 * time.Hour

type Manager struct {
	Driver   vm.Driver
	Template []byte
	TTL      time.Duration
	Keep     int
	Now      func() time.Time

	mu    sync.Mutex
	locks map[string]*sync.Mutex
}

type Plan struct {
	Base    string
	Project string
}

func (m *Manager) now() time.Time {
	if m.Now != nil {
		return m.Now()
	}
	return time.Now()
}

func (m *Manager) ttl() time.Duration {
	if m.TTL > 0 {
		return m.TTL
	}
	return DefaultTTL
}

func (m *Manager) keep() int {
	if m.Keep > 0 {
		return m.Keep
	}
	return 3
}

func (m *Manager) lockFor(key string) *sync.Mutex {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.locks == nil {
		m.locks = map[string]*sync.Mutex{}
	}
	if _, ok := m.locks[key]; !ok {
		m.locks[key] = &sync.Mutex{}
	}
	return m.locks[key]
}

func (m *Manager) PlanFor(repo string, p project.Project) Plan {
	base := BaseName(m.Template, m.now(), m.ttl())
	return Plan{Base: base, Project: ProjectName(repo, base, p)}
}

func (m *Manager) Ensure(ctx context.Context, repo, ref string, p project.Project, out io.Writer) (Plan, error) {
	plan := m.PlanFor(repo, p)
	if m.Driver.Exists(plan.Project) {
		fmt.Fprintf(out, "reusing project image %s\n", plan.Project)
		return plan, nil
	}

	lock := m.lockFor(plan.Project)
	lock.Lock()
	defer lock.Unlock()

	if m.Driver.Exists(plan.Project) {
		fmt.Fprintf(out, "reusing project image %s\n", plan.Project)
		return plan, nil
	}
	if !m.Driver.Exists(plan.Base) {
		if err := m.buildBase(ctx, plan.Base, out); err != nil {
			return plan, err
		}
	}
	return plan, m.buildProject(ctx, plan, repo, ref, p, out)
}

func (m *Manager) buildBase(ctx context.Context, name string, out io.Writer) error {
	fmt.Fprintf(out, "building base image %s\n", name)
	_ = m.Destroy(ctx, name)
	if err := m.Driver.CreateFromTemplate(ctx, name, m.Template, out); err != nil {
		_ = m.Destroy(ctx, name)
		return fmt.Errorf("provisioning %s: %w", name, err)
	}
	for _, step := range []func() error{
		func() error { return m.Driver.Stop(ctx, name, false) },
		func() error { return m.Driver.Start(ctx, name) },
		func() error { return m.Driver.Stop(ctx, name, false) },
	} {
		if err := step(); err != nil {
			_ = m.Destroy(ctx, name)
			return fmt.Errorf("settling %s: %w", name, err)
		}
	}
	fmt.Fprintf(out, "base image %s ready\n", name)
	return nil
}

func (m *Manager) buildProject(ctx context.Context, plan Plan, repo, ref string, p project.Project, out io.Writer) error {
	mem, _ := p.Setup.MemoryGiB()
	disk, _ := p.Setup.DiskGiB()
	fmt.Fprintf(out, "building project image %s from %s\n", plan.Project, plan.Base)
	_ = m.Destroy(ctx, plan.Project)
	res := vm.Resources{MemoryGiB: mem, CPUs: p.Setup.CPUs, DiskGiB: disk}
	if err := m.Driver.Clone(ctx, plan.Base, plan.Project, res); err != nil {
		return fmt.Errorf("cloning %s: %w", plan.Base, err)
	}
	if err := m.Driver.Start(ctx, plan.Project); err != nil {
		_ = m.Destroy(ctx, plan.Project)
		return fmt.Errorf("starting %s: %w", plan.Project, err)
	}

	script, err := os.CreateTemp("", "forge-setup-*.sh")
	if err != nil {
		return err
	}
	defer os.Remove(script.Name())
	if _, err := script.WriteString(p.Setup.Script); err != nil {
		script.Close()
		return err
	}
	script.Close()

	if err := m.Driver.CopyIn(ctx, plan.Project, script.Name(), "/tmp/forge-setup.sh", false); err != nil {
		_ = m.Destroy(ctx, plan.Project)
		return fmt.Errorf("uploading the setup script: %w", err)
	}
	cmd := fmt.Sprintf("CI_REPO=%s CI_REF=%s CI_GIT_REMOTE=%s bash /tmp/forge-setup.sh",
		shellQuote(repo), shellQuote(ref), shellQuote(gitRemoteBase()))
	code, err := m.Driver.Shell(ctx, plan.Project, cmd, out)
	if err != nil || code != 0 {
		_ = m.Destroy(ctx, plan.Project)
		if err != nil {
			return fmt.Errorf("running %s: %w", project.SetupFile, err)
		}
		return fmt.Errorf("%s exited %d", project.SetupFile, code)
	}
	if err := m.Driver.Stop(ctx, plan.Project, false); err != nil {
		return fmt.Errorf("stopping %s: %w", plan.Project, err)
	}
	fmt.Fprintf(out, "project image %s ready\n", plan.Project)
	return nil
}

func (m *Manager) Destroy(ctx context.Context, name string) error {
	if !Managed(name) {
		return fmt.Errorf("refusing to destroy %q: not a forge instance", name)
	}
	_ = m.Driver.Stop(ctx, name, true)
	return m.Driver.Delete(ctx, name)
}

func (m *Manager) Prune(ctx context.Context, protect ...string) ([]string, error) {
	instances, err := m.Driver.List(ctx)
	if err != nil {
		return nil, err
	}
	keep := map[string]bool{}
	for _, name := range protect {
		keep[name] = true
	}
	var pruned []string
	for _, prefix := range []string{BasePrefix, ProjectPrefix} {
		var group []vm.Instance
		for _, inst := range instances {
			if hasPrefix(inst.Name, prefix+"-") && !keep[inst.Name] && !inst.Running() {
				group = append(group, inst)
			}
		}
		sort.Slice(group, func(i, j int) bool { return group[i].Name > group[j].Name })
		for _, inst := range group[min(len(group), m.keep()):] {
			if err := m.Destroy(ctx, inst.Name); err == nil {
				pruned = append(pruned, inst.Name)
			}
		}
	}
	return pruned, nil
}

func hasPrefix(s, prefix string) bool { return len(s) >= len(prefix) && s[:len(prefix)] == prefix }

func gitRemoteBase() string {
	if v := os.Getenv("FORGE_GIT_REMOTE"); v != "" {
		return v
	}
	return "git@bitbucket.org:effetmonstre"
}

func shellQuote(v string) string {
	return "'" + strings.ReplaceAll(v, "'", `'\''`) + "'"
}
