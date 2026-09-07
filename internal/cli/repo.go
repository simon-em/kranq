package cli

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"sort"
	"strings"
	"text/tabwriter"

	"github.com/simon-em/kranq/internal/exitcode"
	"github.com/simon-em/kranq/internal/gitsrv"
	"github.com/simon-em/kranq/internal/selfinstall"
)

func runRepo(env Env, args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(env.Stderr, "usage: kranq repo ls|create|rm")
		return exitcode.Usage
	}
	subs := map[string]func(Env, []string) int{
		"ls":     repoList,
		"create": repoCreate,
		"rm":     repoRemove,
	}
	sub, ok := subs[args[0]]
	if !ok {
		fmt.Fprintf(env.Stderr, "kranq repo: unknown subcommand %q\n", args[0])
		return exitcode.Usage
	}
	return sub(env, args[1:])
}

func repoStore() *gitsrv.Store {
	cfg := daemonConfig()
	self, _ := os.Executable()
	return &gitsrv.Store{
		Root:       filepath.Join(cfg.Home, "repos"),
		KranqBin:   self,
		SocketPath: cfg.SocketPath(),
	}
}

func repoList(env Env, args []string) int {
	store := repoStore()
	entries, err := os.ReadDir(store.Root)
	if os.IsNotExist(err) {
		fmt.Fprintln(env.Stderr, "nothing has been pushed to this machine yet")
		return exitcode.OK
	}
	if err != nil {
		fmt.Fprintf(env.Stderr, "kranq: %v\n", err)
		return exitcode.InternalError
	}
	var names []string
	for _, e := range entries {
		if e.IsDir() && strings.HasSuffix(e.Name(), ".git") {
			names = append(names, strings.TrimSuffix(e.Name(), ".git"))
		}
	}
	sort.Strings(names)
	if len(names) == 0 {
		fmt.Fprintln(env.Stderr, "nothing has been pushed to this machine yet")
		return exitcode.OK
	}
	w := tabwriter.NewWriter(env.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "REPO\tSIZE\tPUSH URL")
	for _, name := range names {
		fmt.Fprintf(w, "%s\t%s\t%s\n", name, humanSize(dirSize(store.Dir(name))), store.Dir(name))
	}
	w.Flush()
	return exitcode.OK
}

// Pre-creating a repository is what lets someone push to its path directly with
// the ssh key they already administer the machine with. The forced command
// creates one on first push; a plain push to a path cannot.
func repoCreate(env Env, args []string) int {
	fs := flag.NewFlagSet("repo create", flag.ContinueOnError)
	fs.SetOutput(env.Stderr)
	host := fs.String("host", "", "address a client reaches this machine at, [user@]host[:port]")
	port := fs.Int("port", 0, "the port, if it is not in --host")
	rest, err := parsePermuted(fs, args)
	if err != nil {
		return exitcode.Usage
	}
	if len(rest) != 1 {
		fmt.Fprintln(env.Stderr, "usage: kranq repo create <name> [--host ADDR]")
		return exitcode.Usage
	}
	if err := selfinstall.EnsureHome(kranqHome()); err != nil {
		fmt.Fprintf(env.Stderr, "kranq: %v\n", err)
		return exitcode.InternalError
	}
	store := repoStore()
	dir, err := store.Ensure(context.Background(), rest[0])
	if err != nil {
		fmt.Fprintf(env.Stderr, "kranq: %v\n", err)
		return exitcode.Usage
	}
	fmt.Fprintf(env.Stdout, "%s\n", dir)

	// A repository given by its real path needs nothing intercepting the
	// connection: stock git-receive-pack runs and the hooks in it are kranq.
	// So any key that can already ssh here can push, with no forced command and
	// no receive-pack override -- the URL is the whole of the setup.
	where, guessed := resolveAddr(*host, *port)
	fmt.Fprintf(env.Stderr, "\nAny key that can already ssh here can push to it, with nothing "+
		"configured\non either side:\n\n")
	fmt.Fprintf(env.Stderr, "  git remote add kranq ssh://%s@%s:%d/~/%s\n",
		where.Login, where.Host, where.Port, homeRelative(dir))
	fmt.Fprintln(env.Stderr, "  git push kranq HEAD:refs/heads/task/$RUN -o task_file=ci/tasks/spec.yaml")
	fmt.Fprintln(env.Stderr, "  git pull --ff-only kranq task/$RUN")
	fmt.Fprintln(env.Stderr, "  git fetch kranq ok/$RUN")
	if guessed {
		fmt.Fprintf(env.Stderr, "\nThe address is the one this machine was reached on. Pass the outside "+
			"one\nif a client comes in through a forwarded port: --host addr:port\n")
	}
	return exitcode.OK
}

// The path is printed relative to the home directory so the URL survives a
// machine whose home is somewhere else, and stays short enough to read.
func homeRelative(dir string) string {
	home, err := os.UserHomeDir()
	if err != nil {
		return strings.TrimPrefix(dir, "/")
	}
	if rel, ok := strings.CutPrefix(dir, home+"/"); ok {
		return rel
	}
	return strings.TrimPrefix(dir, "/")
}

func repoRemove(env Env, args []string) int {
	if len(args) != 1 {
		fmt.Fprintln(env.Stderr, "usage: kranq repo rm <name>")
		return exitcode.Usage
	}
	if err := gitsrv.ValidRepo(args[0]); err != nil {
		fmt.Fprintf(env.Stderr, "kranq: %v\n", err)
		return exitcode.Usage
	}
	dir := repoStore().Dir(args[0])
	if _, err := os.Stat(dir); err != nil {
		fmt.Fprintf(env.Stderr, "kranq: no repository called %q here\n", args[0])
		return exitcode.Misconfigured
	}
	if err := os.RemoveAll(dir); err != nil {
		fmt.Fprintf(env.Stderr, "kranq: %v\n", err)
		return exitcode.InternalError
	}
	fmt.Fprintf(env.Stderr, "%s removed; the next push recreates it\n", args[0])
	return exitcode.OK
}

// user.Current first, because $USER is not set in every environment kranq runs
// in -- a launchd job among them, and that is where setup-git is often run
// from. The name ends up in an ssh address, so getting it wrong is not cosmetic.
func currentUser() string {
	if u, err := user.Current(); err == nil && u.Username != "" {
		return u.Username
	}
	if v := os.Getenv("USER"); v != "" {
		return v
	}
	return "you"
}

func dirSize(root string) int64 {
	var total int64
	filepath.WalkDir(root, func(_ string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if info, err := d.Info(); err == nil {
			total += info.Size()
		}
		return nil
	})
	return total
}

func humanSize(n int64) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.1fGiB", float64(n)/(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%dMiB", n/(1<<20))
	}
	return fmt.Sprintf("%dKiB", n/(1<<10))
}
