package cli

import (
	"context"
	"fmt"
	"os"
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
	if len(args) != 1 {
		fmt.Fprintln(env.Stderr, "usage: kranq repo create <name>")
		return exitcode.Usage
	}
	if err := selfinstall.EnsureHome(kranqHome()); err != nil {
		fmt.Fprintf(env.Stderr, "kranq: %v\n", err)
		return exitcode.InternalError
	}
	store := repoStore()
	dir, err := store.Ensure(context.Background(), args[0])
	if err != nil {
		fmt.Fprintf(env.Stderr, "kranq: %v\n", err)
		return exitcode.Usage
	}
	fmt.Fprintf(env.Stdout, "%s\n", dir)
	fmt.Fprintf(env.Stderr, "push to it with the key you already use:\n"+
		"  git push ssh://%s@<this machine>%s -o task=<spec> HEAD:refs/heads/run\n",
		currentUser(), dir)
	return exitcode.OK
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

func currentUser() string {
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
