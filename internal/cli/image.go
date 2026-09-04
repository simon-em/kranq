package cli

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/effetmonstre/forge/assets"
	"github.com/effetmonstre/forge/internal/exitcode"
	"github.com/effetmonstre/forge/internal/image"
	"github.com/effetmonstre/forge/internal/project"
	"github.com/effetmonstre/forge/internal/run"
	"github.com/effetmonstre/forge/internal/vm"
)

func newManager() (*image.Manager, vm.Driver) {
	driver := vm.Lima{Home: os.Getenv("FORGE_LIMA_HOME")}
	return &image.Manager{Driver: driver, Template: assets.LimaTemplate}, driver
}

func runImage(env Env, args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(env.Stderr, "usage: forge image ls|build|prune")
		return exitcode.Usage
	}
	subs := map[string]func(Env, []string) int{
		"ls":    imageList,
		"build": imageBuild,
		"prune": imagePrune,
	}
	sub, ok := subs[args[0]]
	if !ok {
		fmt.Fprintf(env.Stderr, "forge image: unknown subcommand %q\n", args[0])
		return exitcode.Usage
	}
	return sub(env, args[1:])
}

func imageList(env Env, args []string) int {
	_, driver := newManager()
	instances, err := driver.List(context.Background())
	if err != nil {
		fmt.Fprintf(env.Stderr, "forge: %v\n", err)
		return exitcode.MissingDep
	}
	w := tabwriter.NewWriter(env.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "NAME\tSTATUS\tCPUS\tMEMORY")
	for _, i := range instances {
		if !image.Managed(i.Name) {
			continue
		}
		fmt.Fprintf(w, "%s\t%s\t%d\t%.1fGiB\n", i.Name, i.Status, i.CPUs, float64(i.Memory)/(1<<30))
	}
	return flushed(w)
}

func imageBuild(env Env, args []string) int {
	fs := flag.NewFlagSet("image build", flag.ContinueOnError)
	fs.SetOutput(env.Stderr)
	repo := fs.String("repo", "", "repository slug; omit to build only the base image")
	ref := fs.String("ref", "main", "ref to read ci/setup.yaml from")
	remote := fs.String("remote", envOr("FORGE_GIT_REMOTE"), "git remote base")
	if _, err := parsePermuted(fs, args); err != nil {
		return exitcode.Usage
	}
	if *remote == "" {
		*remote = defaultRemote
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	m, _ := newManager()

	if *repo == "" {
		name := image.BaseName(assets.LimaTemplate, time.Now(), image.DefaultTTL)
		if m.Driver.Exists(name) {
			fmt.Fprintf(env.Stdout, "%s already exists\n", name)
			return exitcode.OK
		}
		if err := m.BuildBase(ctx, name, env.Stderr); err != nil {
			fmt.Fprintf(env.Stderr, "forge: %v\n", err)
			return exitcode.CouldNotStart
		}
		fmt.Fprintln(env.Stdout, name)
		return exitcode.OK
	}

	work, err := os.MkdirTemp("", "forge-image-*")
	if err != nil {
		fmt.Fprintf(env.Stderr, "forge: %v\n", err)
		return exitcode.InternalError
	}
	defer os.RemoveAll(work)
	checkout := work + "/repo"
	if err := run.HostCheckout(ctx, run.Remote{Base: *remote, Repo: *repo}, *ref, checkout); err != nil {
		fmt.Fprintf(env.Stderr, "forge: %v\n", err)
		return exitcode.CouldNotStart
	}
	proj, err := project.Load(checkout)
	if err != nil {
		fmt.Fprintf(env.Stderr, "forge: %v\n", err)
		return exitcode.InvalidSpec
	}
	plan, err := m.Ensure(ctx, *repo, *ref, proj, env.Stderr)
	if err != nil {
		fmt.Fprintf(env.Stderr, "forge: %v\n", err)
		return exitcode.CouldNotStart
	}
	fmt.Fprintln(env.Stdout, plan.Project)
	return exitcode.OK
}

func imagePrune(env Env, args []string) int {
	m, _ := newManager()
	pruned, err := m.Prune(context.Background())
	if err != nil {
		fmt.Fprintf(env.Stderr, "forge: %v\n", err)
		return exitcode.InternalError
	}
	for _, name := range pruned {
		fmt.Fprintln(env.Stdout, name)
	}
	fmt.Fprintf(env.Stderr, "pruned %d image(s)\n", len(pruned))
	return exitcode.OK
}

func flushed(w *tabwriter.Writer) int {
	if err := w.Flush(); err != nil {
		return exitcode.InternalError
	}
	return exitcode.OK
}
