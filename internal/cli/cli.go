package cli

import (
	"fmt"
	"io"
	"os"
	"sort"
	"strings"

	"github.com/effetmonstre/forge/internal/exitcode"
)

var Version = "dev"

type Env struct {
	Stdout io.Writer
	Stderr io.Writer
}

type Command struct {
	Usage   string
	Summary string
	Run     func(env Env, args []string) int
}

var commands map[string]Command

func init() {
	commands = map[string]Command{
		"auth":      {"auth claude [--stdin|--show|--clear]", "store the claude token", runAuth},
		"cancel":    {"cancel <id>...", "cancel queued or running tasks", runCancel},
		"install":   {"install [--with-daemon]", "install forge and its dependencies", runInstall},
		"uninstall": {"uninstall [--purge]", "remove forge", runUninstall},
		"daemon":    {"daemon run|start|stop|status", "manage the local daemon", runDaemon},
		"image":     {"image ls|build|prune", "manage the cached VM images", runImage},
		"logs":      {"logs <id> [-f]", "print or follow a task's log", runLogs},
		"ps":        {"ps [-a]", "list tasks", runPS},
		"status":    {"status [--json]", "queue, machine and claude state", runStatus},
		"run":       {"run <task.yaml> [flags]", "run a task in a disposable VM", runRun},
		"version":   {"version [--json]", "print the forge version", runVersion},
		"validate":  {"validate <task.yaml>...", "check that a task spec parses and compiles", runValidate},
		"vm":        {"vm ls|shell|rm", "inspect or remove job VMs", runVM},
		"fence":     {"fence ls|show|break", "inspect the at-most-once fences held on a repo", runFence},
		"doctor":    {"doctor [--json]", "check this machine can run tasks", runDoctor},
		"upgrade":   {"upgrade <path> [--force]", "replace the installed forge, keeping the previous", runUpgrade},
		"rollback":  {"rollback [--force]", "go back to the previous forge binary", runRollback},
		"peer":      {"peer add|ls|rm|test|upgrade", "manage the other build machines", runPeer},
		"exec":      {"exec <task-id>", "run one job (started by the daemon, not by hand)", runExec},
		"token":     {"token create|ls|revoke", "named tokens for the git push endpoint", runToken},
		"git-hook":  {"git-hook <phase>", "run a receive hook (invoked by git, not by hand)", runGitHook},
		"push":      {"push <task.yaml> [flags]", "send this repo to forge and run a task against it", runPush},
		"render":    {"render <task.yaml>", "print the bash script a spec compiles to", runRender},
		"help":      {"help [command]", "show usage", runHelp},
	}
}

func Main(argv []string) int {
	return dispatch(Env{Stdout: os.Stdout, Stderr: os.Stderr}, argv[1:])
}

func dispatch(env Env, args []string) int {
	if len(args) == 0 {
		usage(env.Stderr)
		return exitcode.Usage
	}
	name := args[0]
	if name == "-h" || name == "--help" {
		usage(env.Stdout)
		return exitcode.OK
	}
	cmd, ok := commands[name]
	if !ok {
		fmt.Fprintf(env.Stderr, "forge: unknown command %q\n\n", name)
		usage(env.Stderr)
		return exitcode.Usage
	}
	return cmd.Run(env, args[1:])
}

func usage(w io.Writer) {
	fmt.Fprintf(w, "forge %s\n\nusage: forge <command> [arguments]\n\n", Version)
	names := make([]string, 0, len(commands))
	width := 0
	for name := range commands {
		names = append(names, name)
		if n := len(commands[name].Usage); n > width {
			width = n
		}
	}
	sort.Strings(names)
	for _, name := range names {
		fmt.Fprintf(w, "  %-*s  %s\n", width, commands[name].Usage, commands[name].Summary)
	}
}

func runHelp(env Env, args []string) int {
	if len(args) == 0 {
		usage(env.Stdout)
		return exitcode.OK
	}
	cmd, ok := commands[args[0]]
	if !ok {
		fmt.Fprintf(env.Stderr, "forge: unknown command %q\n", args[0])
		return exitcode.Usage
	}
	fmt.Fprintf(env.Stdout, "usage: forge %s\n\n%s\n", cmd.Usage, cmd.Summary)
	return exitcode.OK
}

func runVersion(env Env, args []string) int {
	if len(args) > 0 && args[0] == "--json" {
		fmt.Fprintf(env.Stdout, "{\"version\":%q}\n", Version)
		return exitcode.OK
	}
	fmt.Fprintln(env.Stdout, Version)
	return exitcode.OK
}

func readSpecFile(path string) ([]byte, int, error) {
	data, err := os.ReadFile(path)
	if err == nil {
		return data, exitcode.OK, nil
	}
	if os.IsNotExist(err) {
		return nil, exitcode.NoSuchFile, err
	}
	return nil, exitcode.InternalError, err
}

func trimForDisplay(s string) string {
	return strings.TrimRight(s, "\n")
}
