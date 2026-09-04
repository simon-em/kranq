package cli

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/effetmonstre/forge/internal/exitcode"
	"github.com/effetmonstre/forge/internal/ipc"
	"github.com/effetmonstre/forge/internal/state"
)

func runPS(env Env, args []string) int {
	fs := flag.NewFlagSet("ps", flag.ContinueOnError)
	fs.SetOutput(env.Stderr)
	all := fs.Bool("a", false, "include finished tasks")
	if _, err := parsePermuted(fs, args); err != nil {
		return exitcode.Usage
	}
	client, code := connect(env, false)
	if client == nil {
		return code
	}
	tasks, err := client.List(context.Background())
	if err != nil {
		fmt.Fprintf(env.Stderr, "forge: %v\n", err)
		return exitcode.Unreachable
	}
	w := tabwriter.NewWriter(env.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "ID\tTASK\tREPO\tBRANCH\tSTATUS\tAGE")
	for _, t := range tasks {
		if !*all && t.Terminal() {
			continue
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n",
			t.ID, t.Name, t.Repo, t.Branch, describe(t), age(t))
	}
	return flushed(w)
}

func describe(t state.Task) string {
	switch {
	case t.Status == state.StatusBlocked && t.BlockedOn != "":
		return "blocked/" + string(t.BlockedOn)
	case t.Status == state.StatusFailed && t.ExitCode > 0:
		return fmt.Sprintf("failed(%d)", t.ExitCode)
	case t.Status == state.StatusLost:
		return "lost"
	}
	return string(t.Status)
}

func age(t state.Task) string {
	end := time.Now()
	if t.FinishedAt != nil {
		end = *t.FinishedAt
	}
	return end.Sub(t.CreatedAt).Truncate(time.Second).String()
}

func runLogs(env Env, args []string) int {
	fs := flag.NewFlagSet("logs", flag.ContinueOnError)
	fs.SetOutput(env.Stderr)
	follow := fs.Bool("f", false, "follow until the task finishes")
	positional, err := parsePermuted(fs, args)
	if err != nil {
		return exitcode.Usage
	}
	if len(positional) != 1 {
		fmt.Fprintln(env.Stderr, "usage: forge logs <id> [-f]")
		return exitcode.Usage
	}
	client, code := connect(env, false)
	if client == nil {
		return code
	}
	if err := client.Logs(context.Background(), positional[0], *follow, env.Stdout); err != nil {
		fmt.Fprintf(env.Stderr, "forge: %v\n", err)
		return codeOf(err, exitcode.Unreachable)
	}
	return exitcode.OK
}

func runCancel(env Env, args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(env.Stderr, "usage: forge cancel <id>...")
		return exitcode.Usage
	}
	client, code := connect(env, false)
	if client == nil {
		return code
	}
	worst := exitcode.OK
	for _, id := range args {
		t, err := client.Cancel(context.Background(), id)
		if err != nil {
			fmt.Fprintf(env.Stderr, "forge: %s: %v\n", id, err)
			worst = codeOf(err, exitcode.Unreachable)
			continue
		}
		fmt.Fprintf(env.Stdout, "%s %s\n", t.ID, t.Status)
	}
	return worst
}

func codeOf(err error, fallback int) int {
	var re *ipc.RemoteError
	if ok := asRemote(err, &re); ok && re.Code != 0 {
		return re.Code
	}
	return fallback
}

func asRemote(err error, target **ipc.RemoteError) bool {
	e, ok := err.(*ipc.RemoteError)
	if ok {
		*target = e
	}
	return ok
}

func fetchArtifacts(client *ipc.Client, id, dest string, env Env) {
	if dest == "" {
		return
	}
	var buf strings.Builder
	ok, err := client.Artifacts(context.Background(), id, &buf)
	if err != nil {
		fmt.Fprintf(env.Stderr, "forge: artifacts could not be fetched: %v\n", err)
		return
	}
	if !ok {
		fmt.Fprintln(env.Stderr, "forge: this task produced no artifacts")
		return
	}
	n, err := untarInto(strings.NewReader(buf.String()), dest)
	if err != nil {
		fmt.Fprintf(env.Stderr, "forge: artifacts arrived but could not be written: %v\n", err)
		return
	}
	fmt.Fprintf(env.Stderr, "forge: %d artifact file(s) in %s\n", n, dest)
}

func untarInto(r io.Reader, dest string) (int, error) {
	gz, err := gzip.NewReader(r)
	if err != nil {
		return 0, err
	}
	defer gz.Close()
	if err := os.MkdirAll(dest, 0o755); err != nil {
		return 0, err
	}
	count := 0
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return count, nil
		}
		if err != nil {
			return count, err
		}
		clean := filepath.Clean(hdr.Name)
		if strings.HasPrefix(clean, "..") || filepath.IsAbs(clean) {
			return count, fmt.Errorf("refusing a tar entry that escapes the destination: %q", hdr.Name)
		}
		target := filepath.Join(dest, clean)
		if hdr.FileInfo().IsDir() {
			if err := os.MkdirAll(target, 0o755); err != nil {
				return count, err
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return count, err
		}
		f, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
		if err != nil {
			return count, err
		}
		if _, err := io.Copy(f, tr); err != nil {
			f.Close()
			return count, err
		}
		f.Close()
		count++
	}
}
