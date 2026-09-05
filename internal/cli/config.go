package cli

import (
	"fmt"
	"sort"
	"strings"
	"text/tabwriter"

	"github.com/effetmonstre/forge/internal/daemon"
	"github.com/effetmonstre/forge/internal/exitcode"
	"github.com/effetmonstre/forge/internal/selfinstall"
)

// Settings a person is expected to set. Anything else is still readable and
// writable, but only these are suggested, because a typo in a setting name is
// otherwise silent: the daemon just uses the default.
var knownSettings = map[string]string{
	"FORGE_MAX_VMS":            "how many job VMs may run at once",
	"FORGE_MEMORY_HEADROOM_MB": "memory to keep free when admitting a job",
	"FORGE_GIT_REMOTE":         "git remote base, e.g. git@bitbucket.org:effetmonstre",
	"FORGE_LIMA_HOME":          "where forge keeps its VMs, if not the lima default",
	"FORGE_HTTP_ADDR":          "loopback address for the git push endpoint, e.g. 127.0.0.1:8420",
	"FORGE_NODE":               "what this machine calls itself in a fence record",
	"FORGE_AUTO_CREATE_REPOS":  "make a repository on first push (default true)",
	"FORGE_AUTO_INSTALL_DEPS":  "fetch lima on first use (default true)",
	"CLAUDE_CODE_OAUTH_TOKEN":  "set it with `forge auth claude` instead",
}

// Values that must never be echoed back, even to the person who set them.
func isSecret(name string) bool {
	upper := strings.ToUpper(name)
	for _, marker := range []string{"TOKEN", "SECRET", "PASSWORD", "KEY"} {
		if strings.Contains(upper, marker) {
			return true
		}
	}
	return false
}

func runConfig(env Env, args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(env.Stderr, "usage: forge config ls|get|set|unset")
		return exitcode.Usage
	}
	subs := map[string]func(Env, []string) int{
		"ls":    configList,
		"get":   configGet,
		"set":   configSet,
		"unset": configUnset,
	}
	sub, ok := subs[args[0]]
	if !ok {
		fmt.Fprintf(env.Stderr, "forge config: unknown subcommand %q\n", args[0])
		return exitcode.Usage
	}
	return sub(env, args[1:])
}

func configList(env Env, args []string) int {
	stored, err := daemon.LoadEnv(forgeHome())
	if err != nil {
		fmt.Fprintf(env.Stderr, "forge: %v\n", err)
		return exitcode.InternalError
	}
	names := make([]string, 0, len(stored))
	for name := range stored {
		names = append(names, name)
	}
	sort.Strings(names)

	w := tabwriter.NewWriter(env.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "SETTING\tVALUE")
	for _, name := range names {
		value := stored[name]
		if isSecret(name) {
			value = fmt.Sprintf("<set, %d chars>", len(value))
		}
		fmt.Fprintf(w, "%s\t%s\n", name, value)
	}
	w.Flush()
	if len(names) == 0 {
		fmt.Fprintf(env.Stderr, "nothing set in %s\n", daemon.EnvPath(forgeHome()))
	}
	fmt.Fprintln(env.Stderr, "\nsettings forge knows about:")
	known := make([]string, 0, len(knownSettings))
	for name := range knownSettings {
		known = append(known, name)
	}
	sort.Strings(known)
	for _, name := range known {
		fmt.Fprintf(env.Stderr, "  %-26s %s\n", name, knownSettings[name])
	}
	return exitcode.OK
}

func configGet(env Env, args []string) int {
	if len(args) != 1 {
		fmt.Fprintln(env.Stderr, "usage: forge config get <name>")
		return exitcode.Usage
	}
	if isSecret(args[0]) {
		fmt.Fprintf(env.Stderr, "forge: %s is a secret and is not printed\n", args[0])
		return exitcode.Usage
	}
	stored, err := daemon.LoadEnv(forgeHome())
	if err != nil {
		fmt.Fprintf(env.Stderr, "forge: %v\n", err)
		return exitcode.InternalError
	}
	value, ok := stored[args[0]]
	if !ok {
		fmt.Fprintf(env.Stderr, "forge: %s is not set\n", args[0])
		return exitcode.Misconfigured
	}
	fmt.Fprintln(env.Stdout, value)
	return exitcode.OK
}

func configSet(env Env, args []string) int {
	if len(args) != 1 || !strings.Contains(args[0], "=") {
		fmt.Fprintln(env.Stderr, "usage: forge config set NAME=VALUE")
		return exitcode.Usage
	}
	name, value, _ := strings.Cut(args[0], "=")
	if name == "" {
		fmt.Fprintln(env.Stderr, "forge: no setting name")
		return exitcode.Usage
	}
	if err := selfinstall.EnsureHome(forgeHome()); err != nil {
		fmt.Fprintf(env.Stderr, "forge: %v\n", err)
		return exitcode.InternalError
	}
	if err := daemon.SetEnv(forgeHome(), name, value); err != nil {
		fmt.Fprintf(env.Stderr, "forge: %v\n", err)
		return exitcode.InternalError
	}
	if _, known := knownSettings[name]; !known {
		fmt.Fprintf(env.Stderr, "note: forge does not use a setting called %s; it is stored and passed through\n", name)
	}
	fmt.Fprintf(env.Stderr, "%s set; restart the daemon for it to take effect\n", name)
	return exitcode.OK
}

func configUnset(env Env, args []string) int {
	if len(args) != 1 {
		fmt.Fprintln(env.Stderr, "usage: forge config unset <name>")
		return exitcode.Usage
	}
	if err := daemon.SetEnv(forgeHome(), args[0], ""); err != nil {
		fmt.Fprintf(env.Stderr, "forge: %v\n", err)
		return exitcode.InternalError
	}
	fmt.Fprintf(env.Stderr, "%s unset; restart the daemon for it to take effect\n", args[0])
	return exitcode.OK
}
