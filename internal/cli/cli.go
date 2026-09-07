package cli

import (
	"fmt"
	"io"
	"os"
	"sort"
	"strings"

	"github.com/simon-em/kranq/internal/exitcode"
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
		"auth":        {"auth claude [--stdin|--show|--clear]", "store the claude token", runAuth},
		"cancel":      {"cancel <id>...", "cancel queued or running tasks", runCancel},
		"install":     {"install [--with-daemon]", "install kranq and its dependencies", runInstall},
		"uninstall":   {"uninstall [--purge]", "remove kranq", runUninstall},
		"daemon":      {"daemon run|start|stop|status", "manage the local daemon", runDaemon},
		"image":       {"image ls|build|prune", "manage the cached VM images", runImage},
		"logs":        {"logs <id> [-f]", "print or follow a task's log", runLogs},
		"ps":          {"ps [-a]", "list tasks", runPS},
		"status":      {"status [--json]", "queue, machine and claude state", runStatus},
		"run":         {"run <task.yaml> [flags]", "run a task in a disposable VM", runRun},
		"version":     {"version [--json]", "print the kranq version", runVersion},
		"validate":    {"validate <task.yaml>...", "check that a task spec parses and compiles", runValidate},
		"vm":          {"vm ls|shell|rm", "inspect or remove job VMs", runVM},
		"fence":       {"fence ls|show|break", "inspect the at-most-once fences held on a repo", runFence},
		"fetch":       {"fetch <id> [--out DIR]", "get a task's artifacts, as a tar.gz or unpacked", runFetch},
		"doctor":      {"doctor [--json]", "check this machine can run tasks", runDoctor},
		"config":      {"config ls|get|set|unset", "settings the daemon reads at startup", runConfig},
		"upgrade":     {"upgrade <path> [--force]", "replace the installed kranq, keeping the previous", runUpgrade},
		"rollback":    {"rollback [--force]", "go back to the previous kranq binary", runRollback},
		"peer":        {"peer add|ls|rm|test|upgrade", "manage the other build machines", runPeer},
		"exec":        {"exec <task-id>", "run one job (started by the daemon, not by hand)", runExec},
		"token":       {"token create|ls|revoke", "named tokens for the git push endpoint", runToken},
		"key":         {"key add|ls|rm", "ssh keys allowed to push to this machine", runKey},
		"repo":        {"repo ls|create|rm", "the repositories people push here", runRepo},
		"setup-git":   {"setup-git [name]", "authorise a key so a client needs no git config", runSetupGit},
		"git-receive": {"git-receive", "the ssh entry point (run by sshd, not by hand)", runGitReceive},
		"git-hook":    {"git-hook <phase>", "run a receive hook (invoked by git, not by hand)", runGitHook},
		"push":        {"push <task.yaml> [flags]", "send this repo to kranq and run a task against it", runPush},
		"result":      {"result <id> [--wait D]", "reprint a run's outcome, waiting if it is still going", runResult},
		"render":      {"render <task.yaml>", "print the bash script a spec compiles to", runRender},
		"help":        {"help [command]", "show usage", runHelp},
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
		fmt.Fprintf(env.Stderr, "kranq: unknown command %q\n\n", name)
		usage(env.Stderr)
		return exitcode.Usage
	}
	return cmd.Run(env, args[1:])
}

func usage(w io.Writer) {
	fmt.Fprintf(w, "kranq %s\n\nusage: kranq <command> [arguments]\n\n", Version)
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
		fmt.Fprintf(env.Stderr, "kranq: unknown command %q\n", args[0])
		return exitcode.Usage
	}
	fmt.Fprintf(env.Stdout, "usage: kranq %s\n\n%s\n", cmd.Usage, cmd.Summary)
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
