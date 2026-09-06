package vm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

const TeardownGrace = 2 * time.Minute

type Lima struct {
	Bin  string
	Home string
}

func (l Lima) bin() string {
	if l.Bin != "" {
		return l.Bin
	}
	return "limactl"
}

func (l Lima) env() []string {
	env := os.Environ()
	if l.Home != "" {
		env = append(env, "LIMA_HOME="+l.Home)
	}
	return env
}

func (l Lima) command(ctx context.Context, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, l.bin(), args...)
	cmd.Env = l.env()
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM) }
	cmd.WaitDelay = TeardownGrace
	return cmd
}

func (l Lima) run(ctx context.Context, out io.Writer, args ...string) error {
	cmd := l.command(ctx, args...)
	if out != nil {
		cmd.Stdout = out
		cmd.Stderr = out
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("limactl %s: %w", args[0], err)
		}
		return nil
	}
	combined, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("limactl %s: %w: %s", args[0], err, strings.TrimSpace(string(combined)))
	}
	return nil
}

func (l Lima) List(ctx context.Context) ([]Instance, error) {
	cmd := l.command(ctx, "list", "--format", "json")
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("limactl list: %w", err)
	}
	var instances []Instance
	dec := json.NewDecoder(strings.NewReader(string(out)))
	for {
		var i Instance
		if err := dec.Decode(&i); err != nil {
			if errors.Is(err, io.EOF) {
				return instances, nil
			}
			return nil, fmt.Errorf("limactl list returned unreadable json: %w", err)
		}
		instances = append(instances, i)
	}
}

func (l Lima) home() string {
	if l.Home != "" {
		return l.Home
	}
	if h := os.Getenv("LIMA_HOME"); h != "" {
		return h
	}
	return filepath.Join(os.Getenv("HOME"), ".lima")
}

func (l Lima) Exists(name string) bool {
	_, err := os.Stat(filepath.Join(l.home(), name, "lima.yaml"))
	return err == nil
}

func (l Lima) CreateFromTemplate(ctx context.Context, name string, template []byte, out io.Writer) error {
	file, err := os.CreateTemp("", "kranq-lima-*.yaml")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	if _, err := file.Write(template); err != nil {
		file.Close()
		return err
	}
	file.Close()
	return l.run(ctx, out, "start", "--name="+name, "--tty=false", file.Name())
}

func (l Lima) Clone(ctx context.Context, src, dst string, r Resources) error {
	args := []string{"clone", src, dst}
	if r.MemoryGiB > 0 {
		args = append(args, "--memory", strconv.FormatFloat(r.MemoryGiB, 'f', -1, 64))
	}
	if r.CPUs > 0 {
		args = append(args, "--cpus", strconv.Itoa(r.CPUs))
	}
	if r.DiskGiB > 0 {
		args = append(args, "--disk", strconv.FormatFloat(r.DiskGiB, 'f', -1, 64))
	}
	return l.run(ctx, nil, args...)
}

func (l Lima) Start(ctx context.Context, name string) error {
	return l.run(ctx, nil, "start", name)
}

func (l Lima) Stop(ctx context.Context, name string, force bool) error {
	args := []string{"stop"}
	if force {
		args = append(args, "-f")
	}
	return l.run(ctx, nil, append(args, name)...)
}

func (l Lima) Delete(ctx context.Context, name string) error {
	return l.run(ctx, nil, "delete", "-f", name)
}

func (l Lima) CopyIn(ctx context.Context, name, hostPath, guestPath string, recursive bool) error {
	return l.copy(ctx, hostPath, name+":"+guestPath, recursive)
}

func (l Lima) CopyOut(ctx context.Context, name, guestPath, hostPath string, recursive bool) error {
	return l.copy(ctx, name+":"+guestPath, hostPath, recursive)
}

func (l Lima) copy(ctx context.Context, src, dst string, recursive bool) error {
	args := []string{"copy"}
	if recursive {
		args = append(args, "-r")
	}
	return l.run(ctx, nil, append(args, src, dst)...)
}

func (l Lima) Shell(ctx context.Context, name, script string, out io.Writer) (int, error) {
	cmd := l.command(ctx, "shell", name, "--", "bash", "-lc", script)
	cmd.Stdout = out
	cmd.Stderr = out
	err := cmd.Run()
	if err == nil {
		return 0, nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return exitErr.ExitCode(), nil
	}
	return -1, err
}
