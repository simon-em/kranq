package cli

import (
	"errors"
	"fmt"
	"text/tabwriter"
	"time"

	"github.com/simon-em/kranq/internal/exitcode"
	"github.com/simon-em/kranq/internal/selfinstall"
	"github.com/simon-em/kranq/internal/token"
)

func runToken(env Env, args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(env.Stderr, "usage: kranq token create|ls|revoke")
		return exitcode.Usage
	}
	subs := map[string]func(Env, []string) int{
		"create": tokenCreate,
		"ls":     tokenList,
		"revoke": tokenRevoke,
	}
	sub, ok := subs[args[0]]
	if !ok {
		fmt.Fprintf(env.Stderr, "kranq token: unknown subcommand %q\n", args[0])
		return exitcode.Usage
	}
	return sub(env, args[1:])
}

func tokenSet(env Env) (*token.Set, string, int) {
	path := token.Path(kranqHome())
	set, err := token.Load(path)
	if err != nil {
		fmt.Fprintf(env.Stderr, "kranq: %v\n", err)
		return nil, "", exitcode.InternalError
	}
	return set, path, exitcode.OK
}

func tokenCreate(env Env, args []string) int {
	if len(args) != 1 {
		fmt.Fprintln(env.Stderr, "usage: kranq token create <name>")
		return exitcode.Usage
	}
	if err := selfinstall.EnsureHome(kranqHome()); err != nil {
		fmt.Fprintf(env.Stderr, "kranq: %v\n", err)
		return exitcode.InternalError
	}
	set, path, code := tokenSet(env)
	if code != exitcode.OK {
		return code
	}
	secret, err := set.Create(args[0], time.Now())
	if err != nil {
		fmt.Fprintf(env.Stderr, "kranq: %v\n", err)
		if errors.Is(err, token.ErrExists) {
			fmt.Fprintf(env.Stderr, "revoke it first if you mean to replace it: kranq token revoke %s\n", args[0])
		}
		return exitcode.Usage
	}
	if err := set.Save(path); err != nil {
		fmt.Fprintf(env.Stderr, "kranq: %v\n", err)
		return exitcode.InternalError
	}
	// Only the hash is kept, so this is the one and only time it can be shown.
	fmt.Fprintln(env.Stdout, secret)
	fmt.Fprintf(env.Stderr, "\ntoken %q created. It is not stored and cannot be shown again.\n", args[0])
	return exitcode.OK
}

func tokenList(env Env, args []string) int {
	set, _, code := tokenSet(env)
	if code != exitcode.OK {
		return code
	}
	if len(set.Tokens) == 0 {
		fmt.Fprintln(env.Stderr, "no tokens; `kranq token create <name>`")
		return exitcode.OK
	}
	w := tabwriter.NewWriter(env.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "NAME\tPREFIX\tCREATED\tLAST USED")
	for _, t := range set.Tokens {
		used := "never"
		if t.LastUsed != nil {
			used = t.LastUsed.Local().Format("2006-01-02 15:04")
		}
		fmt.Fprintf(w, "%s\t%s…\t%s\t%s\n", t.Name, t.Display,
			t.Created.Local().Format("2006-01-02"), used)
	}
	w.Flush()
	return exitcode.OK
}

func tokenRevoke(env Env, args []string) int {
	if len(args) != 1 {
		fmt.Fprintln(env.Stderr, "usage: kranq token revoke <name>")
		return exitcode.Usage
	}
	set, path, code := tokenSet(env)
	if code != exitcode.OK {
		return code
	}
	if err := set.Revoke(args[0]); err != nil {
		fmt.Fprintf(env.Stderr, "kranq: %v\n", err)
		return exitcode.Misconfigured
	}
	if err := set.Save(path); err != nil {
		fmt.Fprintf(env.Stderr, "kranq: %v\n", err)
		return exitcode.InternalError
	}
	fmt.Fprintf(env.Stderr, "token %q revoked\n", args[0])
	return exitcode.OK
}
