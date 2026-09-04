package cli

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"text/tabwriter"

	"github.com/effetmonstre/forge/internal/exitcode"
	"github.com/effetmonstre/forge/internal/image"
	"github.com/effetmonstre/forge/internal/state"
	"github.com/effetmonstre/forge/internal/vm"
)

func runVM(env Env, args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(env.Stderr, "usage: forge vm ls|shell|rm")
		return exitcode.Usage
	}
	subs := map[string]func(Env, []string) int{
		"ls":    vmList,
		"shell": vmShell,
		"rm":    vmRemove,
	}
	sub, ok := subs[args[0]]
	if !ok {
		fmt.Fprintf(env.Stderr, "forge vm: unknown subcommand %q\n", args[0])
		return exitcode.Usage
	}
	return sub(env, args[1:])
}

func runVMs(env Env) ([]vm.Instance, int) {
	_, driver := newManager()
	all, err := driver.List(context.Background())
	if err != nil {
		fmt.Fprintf(env.Stderr, "forge: %v\n", err)
		return nil, exitcode.MissingDep
	}
	var out []vm.Instance
	for _, i := range all {
		if strings.HasPrefix(i.Name, image.RunPrefix+"-") {
			out = append(out, i)
		}
	}
	return out, exitcode.OK
}

func vmList(env Env, args []string) int {
	instances, code := runVMs(env)
	if code != exitcode.OK {
		return code
	}
	owners := map[string]state.Task{}
	if client, _ := connect(env, false); client != nil {
		if tasks, err := client.List(context.Background()); err == nil {
			for _, t := range tasks {
				if t.VMName != "" {
					owners[t.VMName] = t
				}
			}
		}
	}
	w := tabwriter.NewWriter(env.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "VM\tSTATUS\tTASK\tOWNER STATUS")
	for _, i := range instances {
		owner, task := "-", "-"
		if t, ok := owners[i.Name]; ok {
			owner, task = string(t.Status), t.ID
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", i.Name, i.Status, task, owner)
	}
	if len(instances) == 0 {
		fmt.Fprintln(env.Stderr, "no job VMs are present")
	}
	return flushed(w)
}

func resolveVM(env Env, ref string) (string, int) {
	if strings.HasPrefix(ref, image.RunPrefix+"-") {
		return ref, exitcode.OK
	}
	client, code := connect(env, false)
	if client == nil {
		return "", code
	}
	t, err := client.Get(context.Background(), ref)
	if err != nil {
		fmt.Fprintf(env.Stderr, "forge: %v\n", err)
		return "", exitcode.NoSuchFile
	}
	if t.VMName == "" {
		fmt.Fprintf(env.Stderr, "forge: task %s has no VM recorded\n", t.ID)
		return "", exitcode.NoSuchFile
	}
	if !t.VMKept {
		fmt.Fprintf(env.Stderr, "forge: task %s ran in %s, which was destroyed when it finished\n", t.ID, t.VMName)
		fmt.Fprintln(env.Stderr, "forge: re-run it with --keep-vm on-failure to be able to look inside")
		return "", exitcode.NoSuchFile
	}
	return t.VMName, exitcode.OK
}

func vmShell(env Env, args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(env.Stderr, "usage: forge vm shell <task-id|vm-name> [-- command...]")
		return exitcode.Usage
	}
	name, code := resolveVM(env, args[0])
	if name == "" {
		return code
	}
	rest := args[1:]
	if len(rest) > 0 && rest[0] == "--" {
		rest = rest[1:]
	}
	limactl := append([]string{"shell", name}, appendSeparator(rest)...)
	cmd := exec.Command("limactl", limactl...)
	cmd.Env = append(os.Environ(), limaHomeEnv()...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		var exitErr *exec.ExitError
		if ok := asExit(err, &exitErr); ok {
			return exitErr.ExitCode()
		}
		fmt.Fprintf(env.Stderr, "forge: %v\n", err)
		return exitcode.InternalError
	}
	return exitcode.OK
}

func appendSeparator(rest []string) []string {
	if len(rest) == 0 {
		return nil
	}
	return append([]string{"--"}, rest...)
}

func limaHomeEnv() []string {
	if v := os.Getenv("FORGE_LIMA_HOME"); v != "" {
		return []string{"LIMA_HOME=" + v}
	}
	return nil
}

func vmRemove(env Env, args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(env.Stderr, "usage: forge vm rm <vm-name>... | --all")
		return exitcode.Usage
	}
	m, _ := newManager()
	targets := args
	if args[0] == "--all" {
		instances, code := runVMs(env)
		if code != exitcode.OK {
			return code
		}
		targets = nil
		for _, i := range instances {
			targets = append(targets, i.Name)
		}
	}
	worst := exitcode.OK
	for _, name := range targets {
		if err := m.Destroy(context.Background(), name); err != nil {
			fmt.Fprintf(env.Stderr, "forge: %v\n", err)
			worst = exitcode.InternalError
			continue
		}
		fmt.Fprintln(env.Stdout, name)
	}
	return worst
}

func asExit(err error, target **exec.ExitError) bool {
	e, ok := err.(*exec.ExitError)
	if ok {
		*target = e
	}
	return ok
}
