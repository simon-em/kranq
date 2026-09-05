package cli

import (
	"errors"
	"fmt"
	"io"
	"os"
	"text/tabwriter"

	"github.com/effetmonstre/forge/internal/authkeys"
	"github.com/effetmonstre/forge/internal/exitcode"
)

func runKey(env Env, args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(env.Stderr, "usage: forge key add|ls|rm")
		return exitcode.Usage
	}
	subs := map[string]func(Env, []string) int{
		"add": keyAdd,
		"ls":  keyList,
		"rm":  keyRemove,
	}
	sub, ok := subs[args[0]]
	if !ok {
		fmt.Fprintf(env.Stderr, "forge key: unknown subcommand %q\n", args[0])
		return exitcode.Usage
	}
	return sub(env, args[1:])
}

func keyAdd(env Env, args []string) int {
	if len(args) != 2 {
		fmt.Fprintln(env.Stderr, "usage: forge key add <name> <path-to-public-key>")
		fmt.Fprintln(env.Stderr, "       forge key add <name> -   to read it from stdin")
		return exitcode.Usage
	}
	name, source := args[0], args[1]

	var raw []byte
	var err error
	if source == "-" {
		raw, err = io.ReadAll(os.Stdin)
	} else {
		raw, err = os.ReadFile(source)
	}
	if err != nil {
		fmt.Fprintf(env.Stderr, "forge: %v\n", err)
		return exitcode.NoSuchFile
	}
	key, err := authkeys.ParsePublicKey(string(raw))
	if err != nil {
		fmt.Fprintf(env.Stderr, "forge: %v\n", err)
		return exitcode.Usage
	}

	self, err := os.Executable()
	if err != nil {
		fmt.Fprintf(env.Stderr, "forge: %v\n", err)
		return exitcode.InternalError
	}
	path, err := authkeys.Path()
	if err != nil {
		fmt.Fprintf(env.Stderr, "forge: %v\n", err)
		return exitcode.InternalError
	}
	before, err := authkeys.Read(path)
	if err != nil {
		fmt.Fprintf(env.Stderr, "forge: %v\n", err)
		return exitcode.InternalError
	}
	after, err := authkeys.Add(before, key, name, self+" git-receive --name "+name)
	if err != nil {
		fmt.Fprintf(env.Stderr, "forge: %v\n", err)
		if errors.Is(err, authkeys.ErrExists) {
			fmt.Fprintf(env.Stderr, "remove it first if you mean to replace it: forge key rm %s\n", name)
		}
		return exitcode.Usage
	}
	if err := authkeys.Write(path, after); err != nil {
		fmt.Fprintf(env.Stderr, "forge: %v\n", err)
		return exitcode.InternalError
	}
	fmt.Fprintf(env.Stderr, "key %q added to %s\n", name, path)
	fmt.Fprintln(env.Stderr, "it can push to forge and do nothing else: no shell, no forwarding")
	return exitcode.OK
}

func keyList(env Env, args []string) int {
	path, err := authkeys.Path()
	if err != nil {
		fmt.Fprintf(env.Stderr, "forge: %v\n", err)
		return exitcode.InternalError
	}
	content, err := authkeys.Read(path)
	if err != nil {
		fmt.Fprintf(env.Stderr, "forge: %v\n", err)
		return exitcode.InternalError
	}
	keys := authkeys.List(content)
	if len(keys) == 0 {
		fmt.Fprintf(env.Stderr, "no forge keys in %s\n", path)
		return exitcode.OK
	}
	w := tabwriter.NewWriter(env.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "NAME\tTYPE\tKEY")
	for _, k := range keys {
		fmt.Fprintf(w, "%s\t%s\t%s\n", k.Name, k.Type, k.Fingerprint())
	}
	w.Flush()
	return exitcode.OK
}

func keyRemove(env Env, args []string) int {
	if len(args) != 1 {
		fmt.Fprintln(env.Stderr, "usage: forge key rm <name>")
		return exitcode.Usage
	}
	path, err := authkeys.Path()
	if err != nil {
		fmt.Fprintf(env.Stderr, "forge: %v\n", err)
		return exitcode.InternalError
	}
	before, err := authkeys.Read(path)
	if err != nil {
		fmt.Fprintf(env.Stderr, "forge: %v\n", err)
		return exitcode.InternalError
	}
	after, err := authkeys.Remove(before, args[0])
	if err != nil {
		fmt.Fprintf(env.Stderr, "forge: %v\n", err)
		return exitcode.Misconfigured
	}
	if err := authkeys.Write(path, after); err != nil {
		fmt.Fprintf(env.Stderr, "forge: %v\n", err)
		return exitcode.InternalError
	}
	fmt.Fprintf(env.Stderr, "key %q removed\n", args[0])
	return exitcode.OK
}
