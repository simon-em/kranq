package cli

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"text/tabwriter"
	"time"

	"github.com/effetmonstre/forge/internal/exitcode"
	"github.com/effetmonstre/forge/internal/fence"
	"github.com/effetmonstre/forge/internal/run"
)

func runFence(env Env, args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(env.Stderr, "usage: forge fence ls|show|break --repo NAME")
		return exitcode.Usage
	}
	subs := map[string]func(Env, []string) int{
		"ls":    fenceList,
		"show":  fenceShow,
		"break": fenceBreak,
	}
	sub, ok := subs[args[0]]
	if !ok {
		fmt.Fprintf(env.Stderr, "forge fence: unknown subcommand %q\n", args[0])
		return exitcode.Usage
	}
	return sub(env, args[1:])
}

func fenceClient(fs *flag.FlagSet, env Env, args []string) (*fence.Client, []string, int) {
	repo := fs.String("repo", envOr("FORGE_REPO", "CI_REPO", "BITBUCKET_REPO_SLUG"), "repository the fence belongs to")
	remote := fs.String("remote", envOr("FORGE_GIT_REMOTE", "CI_GIT_REMOTE", defaultRemote), "git remote base")
	rest, err := parsePermuted(fs, args)
	if err != nil {
		return nil, nil, exitcode.Usage
	}
	if *repo == "" {
		fmt.Fprintln(env.Stderr, "forge fence: --repo is required")
		return nil, nil, exitcode.Usage
	}
	token := run.ResolveToken(envMap())
	spec := run.Remote{Base: *remote, Repo: *repo, Token: token}
	return &fence.Client{
		Dir:   filepath.Join(forgeHome(), "fence"),
		URL:   spec.URL(),
		Token: token,
		Node:  nodeName(),
	}, rest, exitcode.OK
}

func envMap() map[string]string {
	out := map[string]string{}
	for _, name := range []string{"FORGE_GIT_TOKEN", "BITBUCKET_TOKEN", "BITBUCKET_API_TOKEN", "BITBUCKET_STEP_OAUTH_TOKEN"} {
		if v := os.Getenv(name); v != "" {
			out[name] = v
		}
	}
	return out
}

func fenceList(env Env, args []string) int {
	client, _, code := fenceClient(flag.NewFlagSet("fence ls", flag.ContinueOnError), env, args)
	if code != exitcode.OK {
		return code
	}
	entries, err := client.List(context.Background())
	if err != nil {
		fmt.Fprintf(env.Stderr, "forge: %v\n", err)
		return exitcode.InternalError
	}
	if len(entries) == 0 {
		fmt.Fprintln(env.Stderr, "no fences are held")
		return exitcode.OK
	}
	w := tabwriter.NewWriter(env.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "REF\tKIND\tBRANCH\tTASK\tNODE\tPHASE\tHELD FOR")
	for _, e := range entries {
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
			e.Ref, dash(e.Holder.Kind), dash(e.Holder.Branch), dash(e.Holder.Task),
			dash(e.Holder.Node), dash(e.Holder.Phase), heldFor(e.Holder.ClaimedAt))
	}
	w.Flush()
	return exitcode.OK
}

func fenceShow(env Env, args []string) int {
	fs := flag.NewFlagSet("fence show", flag.ContinueOnError)
	client, rest, code := fenceClient(fs, env, args)
	if code != exitcode.OK {
		return code
	}
	if len(rest) != 1 {
		fmt.Fprintln(env.Stderr, "usage: forge fence show --repo NAME <ref>")
		return exitcode.Usage
	}
	entry, err := client.Show(context.Background(), rest[0])
	if err != nil {
		fmt.Fprintf(env.Stderr, "forge: %v\n", err)
		return exitcode.InternalError
	}
	w := tabwriter.NewWriter(env.Stdout, 0, 0, 2, ' ', 0)
	for _, row := range [][2]string{
		{"ref", entry.Ref}, {"object", entry.OID}, {"task", entry.Holder.Task},
		{"node", entry.Holder.Node}, {"kind", entry.Holder.Kind},
		{"repo", entry.Holder.Repo}, {"branch", dash(entry.Holder.Branch)},
		{"phase", entry.Holder.Phase}, {"claimed", entry.Holder.ClaimedAt.Local().Format(time.RFC1123)},
		{"held for", heldFor(entry.Holder.ClaimedAt)},
	} {
		fmt.Fprintf(w, "%s\t%s\n", row[0], row[1])
	}
	w.Flush()
	if entry.Holder.Phase == "pushed" {
		fmt.Fprintln(env.Stderr, "\nthis run already pushed something. Check what landed before breaking the fence.")
	}
	return exitcode.OK
}

func fenceBreak(env Env, args []string) int {
	fs := flag.NewFlagSet("fence break", flag.ContinueOnError)
	yes := fs.Bool("yes", false, "skip the confirmation")
	client, rest, code := fenceClient(fs, env, args)
	if code != exitcode.OK {
		return code
	}
	if len(rest) != 1 {
		fmt.Fprintln(env.Stderr, "usage: forge fence break --repo NAME <ref>")
		return exitcode.Usage
	}
	ref := rest[0]
	entry, err := client.Show(context.Background(), ref)
	if err != nil {
		fmt.Fprintf(env.Stderr, "forge: %v\n", err)
		return exitcode.InternalError
	}
	fmt.Fprintf(env.Stderr, "%s is held by %s on %s, claimed %s (%s ago), phase %s.\n",
		ref, entry.Holder.Task, entry.Holder.Node,
		entry.Holder.ClaimedAt.Local().Format(time.RFC1123), heldFor(entry.Holder.ClaimedAt), entry.Holder.Phase)
	fmt.Fprintln(env.Stderr,
		"Breaking it lets another run take this effect. Confirm that run is really gone first:\n"+
			"forge cannot stop a machine it has lost contact with, and a still-running one\n"+
			"will be refused at its next push but has already done whatever it did before now.")
	if !*yes {
		fmt.Fprintln(env.Stderr, "\nre-run with --yes to break it")
		return exitcode.Usage
	}
	if err := client.Break(context.Background(), ref, entry.OID); err != nil {
		fmt.Fprintf(env.Stderr, "forge: %v\n", err)
		return exitcode.InternalError
	}
	fmt.Fprintf(env.Stderr, "%s broken\n", ref)
	return exitcode.OK
}

func heldFor(since time.Time) string {
	if since.IsZero() {
		return "-"
	}
	d := time.Since(since).Round(time.Minute)
	if d < time.Minute {
		return "under a minute"
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm", int(d.Minutes()))
	}
	return fmt.Sprintf("%dh%dm", int(d.Hours()), int(d.Minutes())%60)
}

func dash(v string) string {
	if v == "" {
		return "-"
	}
	return v
}
