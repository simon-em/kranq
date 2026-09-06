package image

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
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
	MetaDir  string
	Now      func() time.Time
}

// Plan is the whole chain, base first. Every entry but the last is a cache
// candidate for some other project's build.
type Plan struct {
	Base  string
	Chain []string
}

func (p Plan) Image() string { return p.Chain[len(p.Chain)-1] }

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

func (m *Manager) meta() metaStore { return metaStore{dir: m.MetaDir} }

// An image is usable only once its metadata record exists, which is written
// after the build succeeds. A bare limactl instance is not enough: it appears
// the moment the clone starts, so another job would clone a half-built parent.
func (m *Manager) complete(name string) bool {
	if !m.Driver.Exists(name) {
		return false
	}
	_, ok := m.meta().load(name)
	return ok
}

func (m *Manager) PlanFor(p project.Project) Plan {
	base := BaseName(m.Template, m.now(), m.ttl())
	return Plan{Base: base, Chain: Chain(base, p.Build.Disk, p.Layers)}
}

func (m *Manager) Ensure(ctx context.Context, repo string, p project.Project, out io.Writer) (Plan, error) {
	plan := m.PlanFor(p)
	if m.MetaDir == "" {
		return plan, errors.New("image manager has no MetaDir, so it cannot tell a finished layer from a half-built one")
	}
	if err := m.ensureBase(ctx, plan.Base, out); err != nil {
		return plan, err
	}
	for i, l := range p.Layers {
		parent, name := plan.Chain[i], plan.Chain[i+1]
		if err := m.ensureLayer(ctx, repo, parent, name, i+1, l, Sized(p), out); err != nil {
			return plan, err
		}
	}
	m.touchChain(plan, repo)
	return plan, nil
}

func (m *Manager) touchChain(plan Plan, repo string) {
	store := m.meta()
	now := m.now()
	for _, name := range plan.Chain[1:] {
		store.touch(name, repo, now)
	}
}

// once builds name unless it is already a finished image, holding a lock other
// forge processes honour and re-checking inside it. It reports whether build
// ran, which is the difference between "cached" and "built" in the log.
func (m *Manager) once(name string, build func() error) (bool, error) {
	if m.complete(name) {
		return false, nil
	}
	guard, err := m.meta().acquire(name)
	if err != nil {
		return false, err
	}
	defer guard.release()
	if m.complete(name) {
		return false, nil
	}
	return true, build()
}

func (m *Manager) ensureBase(ctx context.Context, name string, out io.Writer) error {
	_, err := m.once(name, func() error {
		if err := m.BuildBase(ctx, name, out); err != nil {
			return err
		}
		return m.meta().save(Meta{Name: name, Step: "base", CreatedAt: m.now(), LastUsedAt: m.now()})
	})
	return err
}

// Sized is what a clone of this project's images asks for. A layer is built at
// the project's size because a 1GiB base cannot run a bundle install, and the
// job's own clone asks for it again because a shared layer may have been built
// by a project that wanted less.
func Sized(p project.Project) vm.Resources {
	mem, _ := p.MemoryGiB()
	disk, _ := p.DiskGiB()
	return vm.Resources{MemoryGiB: mem, CPUs: p.Build.CPUs, DiskGiB: disk}
}

func (m *Manager) ensureLayer(ctx context.Context, repo, parent, name string, depth int, l project.Resolved, size vm.Resources, out io.Writer) error {
	label := fmt.Sprintf("layer %d/%s", depth, l.Summary())
	built, err := m.once(name, func() error {
		fmt.Fprintf(out, "%s building %s from %s\n", label, name, parent)
		if err := m.buildLayer(ctx, parent, name, l, size, out); err != nil {
			return err
		}
		return m.meta().save(Meta{
			Name:       name,
			Parent:     parent,
			Depth:      depth,
			Step:       l.Summary(),
			Copies:     l.Roots,
			Repos:      withRepo(nil, repo),
			CreatedAt:  m.now(),
			LastUsedAt: m.now(),
		})
	})
	if err == nil && !built {
		fmt.Fprintf(out, "%s cached as %s\n", label, name)
	}
	return err
}

func (m *Manager) BuildBase(ctx context.Context, name string, out io.Writer) error {
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

func (m *Manager) buildLayer(ctx context.Context, parent, name string, l project.Resolved, size vm.Resources, out io.Writer) error {
	_ = m.Destroy(ctx, name)
	if err := m.Driver.Clone(ctx, parent, name, size); err != nil {
		return fmt.Errorf("cloning %s: %w", parent, err)
	}
	if err := m.Driver.Start(ctx, name); err != nil {
		_ = m.Destroy(ctx, name)
		return fmt.Errorf("starting %s: %w", name, err)
	}
	if err := m.applyLayer(ctx, name, l, out); err != nil {
		_ = m.Destroy(ctx, name)
		return err
	}
	if err := m.Driver.Stop(ctx, name, false); err != nil {
		return fmt.Errorf("stopping %s: %w", name, err)
	}
	return nil
}

func (m *Manager) applyLayer(ctx context.Context, name string, l project.Resolved, out io.Writer) error {
	workdir := l.Dir()
	if err := m.shell(ctx, name, "preparing "+workdir, mkdirScript(workdir), out); err != nil {
		return err
	}
	if err := m.upload(ctx, name, l, out); err != nil {
		return err
	}
	if strings.TrimSpace(l.Run) == "" {
		return nil
	}
	script := fmt.Sprintf("set -euo pipefail\ncd %s\n%s%s\n", shellQuote(workdir), exports(l.Env), l.Run)
	return m.shell(ctx, name, l.Summary(), script, out)
}

func (m *Manager) shell(ctx context.Context, name, what, script string, out io.Writer) error {
	code, err := m.Driver.Shell(ctx, name, script, out)
	if err != nil {
		return fmt.Errorf("%s: %w", what, err)
	}
	if code != 0 {
		return fmt.Errorf("%s exited %d", what, code)
	}
	return nil
}

func mkdirScript(dirs ...string) string {
	quoted := make([]string, 0, len(dirs))
	for _, d := range dirs {
		quoted = append(quoted, shellQuote(d))
	}
	joined := strings.Join(quoted, " ")
	return fmt.Sprintf("sudo mkdir -p %s && sudo chown \"$(id -u):$(id -g)\" %s", joined, joined)
}

// Files are staged under one directory and moved into place with sudo, because
// a COPY destination can be anywhere, including a directory the build user
// cannot write. They end up owned by the build user, since that is who the RUN
// that reads them is.
func (m *Manager) upload(ctx context.Context, name string, l project.Resolved, out io.Writer) error {
	if len(l.Files) == 0 {
		return nil
	}
	stage, err := os.MkdirTemp("", "forge-layer-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(stage)
	for _, f := range l.Files {
		dest := filepath.Join(stage, filepath.FromSlash(strings.TrimPrefix(f.Dest, "/")))
		if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
			return err
		}
		body, err := os.ReadFile(f.Source)
		if err != nil {
			return fmt.Errorf("COPY %s: %w", f.Dest, err)
		}
		if err := os.WriteFile(dest, body, f.Mode); err != nil {
			return err
		}
	}

	reset := fmt.Sprintf("rm -rf %s && mkdir -p %s", shellQuote(project.CopyStage), shellQuote(project.CopyStage))
	if err := m.shell(ctx, name, "staging the copy", reset, out); err != nil {
		return err
	}
	if err := m.Driver.CopyIn(ctx, name, stage+"/.", project.CopyStage+"/", true); err != nil {
		return fmt.Errorf("copying %d file(s) into the layer: %w", len(l.Files), err)
	}
	return m.shell(ctx, name, "installing the copy", installScript(l.Roots), out)
}

// tar rather than `cp -a`, and --no-overwrite-dir is the whole reason. `cp -a
// stage/. /` applies the stage directory's own ownership to the destination:
// measured, it turned / from root:root 755 into the build user's, which leaves
// an image that boots but whose sshd never answers. tar leaves the metadata of
// directories that already exist alone, and still creates the ones that do not.
func installScript(roots []string) string {
	stage := shellQuote(project.CopyStage)
	return strings.Join([]string{
		"set -euo pipefail",
		fmt.Sprintf("sudo tar -C %s -cf - . | sudo tar -C / --no-overwrite-dir -xf -", stage),
		fmt.Sprintf(`sudo chown -R "$(id -u):$(id -g)" %s`, quoteAll(roots)),
		fmt.Sprintf("rm -rf %s", stage),
	}, "\n")
}

func quoteAll(paths []string) string {
	out := make([]string, 0, len(paths))
	for _, p := range paths {
		out = append(out, shellQuote(p))
	}
	return strings.Join(out, " ")
}

// ENV keeps its order so a value may build on one set before it, which is what
// makes `ENV PATH=/opt/ci/ruby/bin:$PATH` mean what it looks like.
func exports(env []project.EnvVar) string {
	var b strings.Builder
	for _, e := range env {
		fmt.Fprintf(&b, "export %s=\"%s\"\n", e.Name, strings.ReplaceAll(e.Value, `"`, `\"`))
	}
	return b.String()
}

func (m *Manager) Destroy(ctx context.Context, name string) error {
	if !Managed(name) {
		return fmt.Errorf("refusing to destroy %q: not a forge instance", name)
	}
	_ = m.Driver.Stop(ctx, name, true)
	err := m.Driver.Delete(ctx, name)
	if err == nil {
		m.meta().remove(name)
	}
	return err
}

func shellQuote(v string) string {
	return "'" + strings.ReplaceAll(v, "'", `'\''`) + "'"
}

// Prune keeps the most recently used chain heads whole. Deleting an ancestor
// of a chain you still want costs a full rebuild of everything above it, so
// ancestry is protected rather than aged out independently.
func (m *Manager) Prune(ctx context.Context, protect ...string) ([]string, error) {
	instances, err := m.Driver.List(ctx)
	if err != nil {
		return nil, err
	}
	metas := m.meta().all()
	exists := map[string]bool{}
	keep := map[string]bool{}
	for _, name := range protect {
		keep[name] = true
	}
	var layers, bases, legacy []vm.Instance
	for _, inst := range instances {
		exists[inst.Name] = true
		if inst.Running() {
			keep[inst.Name] = true
		}
		switch {
		case IsLayer(inst.Name):
			layers = append(layers, inst)
		case IsBase(inst.Name):
			bases = append(bases, inst)
		case strings.HasPrefix(inst.Name, LegacyProjectPrefix+"-"):
			legacy = append(legacy, inst)
		}
	}

	ranked := heads(layers, metas, exists)
	for _, head := range ranked[:min(len(ranked), m.keep())] {
		for name := head; name != "" && !keep[name]; name = metas[name].Parent {
			keep[name] = true
		}
	}

	sort.Slice(bases, func(i, j int) bool { return bases[i].Name > bases[j].Name })
	for _, b := range bases[:min(len(bases), m.keep())] {
		keep[b.Name] = true
	}

	var pruned []string
	for _, group := range [][]vm.Instance{layers, bases, legacy} {
		for _, inst := range group {
			if keep[inst.Name] {
				continue
			}
			if err := m.Destroy(ctx, inst.Name); err == nil {
				pruned = append(pruned, inst.Name)
			}
		}
	}
	for name := range metas {
		if !exists[name] {
			m.meta().remove(name)
		}
	}
	sort.Strings(pruned)
	return pruned, nil
}

// A head is a layer nothing else is built on, so it is the layer a project
// actually clones for a run. Ordering is by last use; a layer forge has no
// record of sorts oldest and is therefore the first to go.
func heads(layers []vm.Instance, metas map[string]Meta, exists map[string]bool) []string {
	hasChild := map[string]bool{}
	for _, inst := range layers {
		if parent := metas[inst.Name].Parent; parent != "" && exists[parent] {
			hasChild[parent] = true
		}
	}
	var out []string
	for _, inst := range layers {
		if !hasChild[inst.Name] {
			out = append(out, inst.Name)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := metas[out[i]].LastUsedAt, metas[out[j]].LastUsedAt
		if a.Equal(b) {
			return out[i] < out[j]
		}
		return a.After(b)
	})
	return out
}

func (m *Manager) Describe(name string) (Meta, bool) { return m.meta().load(name) }
