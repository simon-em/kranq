package vm

import (
	"context"
	"fmt"
	"io"
	"sort"
	"strings"
	"sync"
)

type Fake struct {
	mu        sync.Mutex
	instances map[string]*Instance
	Calls     []string
	ShellFunc func(name, script string, out io.Writer) (int, error)
	FailOn    map[string]error
}

func NewFake() *Fake {
	return &Fake{instances: map[string]*Instance{}, FailOn: map[string]error{}}
}

func (f *Fake) record(op string) error {
	f.Calls = append(f.Calls, op)
	verb, _, _ := strings.Cut(op, " ")
	if err := f.FailOn[verb]; err != nil {
		return err
	}
	return f.FailOn[op]
}

func (f *Fake) Seed(name, status string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.instances[name] = &Instance{Name: name, Status: status}
}

func (f *Fake) Names() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	names := make([]string, 0, len(f.instances))
	for name := range f.instances {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func (f *Fake) List(ctx context.Context) ([]Instance, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.record("list"); err != nil {
		return nil, err
	}
	var out []Instance
	for _, name := range f.namesLocked() {
		out = append(out, *f.instances[name])
	}
	return out, nil
}

func (f *Fake) namesLocked() []string {
	names := make([]string, 0, len(f.instances))
	for name := range f.instances {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func (f *Fake) Exists(name string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	_, ok := f.instances[name]
	return ok
}

func (f *Fake) CreateFromTemplate(ctx context.Context, name string, template []byte, out io.Writer) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.record("create " + name); err != nil {
		return err
	}
	f.instances[name] = &Instance{Name: name, Status: "Running"}
	return nil
}

func (f *Fake) Clone(ctx context.Context, src, dst string, r Resources) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.record("clone " + src + " " + dst); err != nil {
		return err
	}
	if _, ok := f.instances[src]; !ok {
		return fmt.Errorf("clone: %s does not exist", src)
	}
	if f.instances[src].Status == "Running" {
		return fmt.Errorf("clone: %s must be stopped to be cloned", src)
	}
	f.instances[dst] = &Instance{Name: dst, Status: "Stopped", CPUs: r.CPUs}
	return nil
}

func (f *Fake) Start(ctx context.Context, name string) error {
	return f.transition("start "+name, name, "Running")
}

func (f *Fake) Stop(ctx context.Context, name string, force bool) error {
	return f.transition("stop "+name, name, "Stopped")
}

func (f *Fake) transition(op, name, status string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.record(op); err != nil {
		return err
	}
	inst, ok := f.instances[name]
	if !ok {
		return fmt.Errorf("%s: no such instance", op)
	}
	inst.Status = status
	return nil
}

func (f *Fake) Delete(ctx context.Context, name string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.record("delete " + name); err != nil {
		return err
	}
	delete(f.instances, name)
	return nil
}

func (f *Fake) CopyIn(ctx context.Context, name, hostPath, guestPath string, recursive bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.record("copyin " + name + " " + guestPath)
}

func (f *Fake) CopyOut(ctx context.Context, name, guestPath, hostPath string, recursive bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.record("copyout " + name + " " + guestPath)
}

func (f *Fake) Shell(ctx context.Context, name, script string, out io.Writer) (int, error) {
	f.mu.Lock()
	if err := f.record("shell " + name); err != nil {
		f.mu.Unlock()
		return -1, err
	}
	fn := f.ShellFunc
	f.mu.Unlock()
	if fn != nil {
		return fn(name, script, out)
	}
	return 0, nil
}
