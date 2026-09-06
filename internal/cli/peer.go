package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/simon-em/kranq/internal/exitcode"
	"github.com/simon-em/kranq/internal/peer"
)

func runPeer(env Env, args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(env.Stderr, "usage: kranq peer add|ls|rm|test|upgrade")
		return exitcode.Usage
	}
	subs := map[string]func(Env, []string) int{
		"add":     peerAdd,
		"ls":      peerList,
		"rm":      peerRemove,
		"test":    peerTest,
		"upgrade": peerUpgrade,
	}
	sub, ok := subs[args[0]]
	if !ok {
		fmt.Fprintf(env.Stderr, "kranq peer: unknown subcommand %q\n", args[0])
		return exitcode.Usage
	}
	return sub(env, args[1:])
}

func peerRegistry(env Env) (*peer.Registry, string, int) {
	path := peer.Path(kranqHome())
	r, err := peer.Load(path)
	if err != nil {
		fmt.Fprintf(env.Stderr, "kranq: %v\n", err)
		return nil, "", exitcode.InternalError
	}
	return r, path, exitcode.OK
}

func resolvePeer(env Env, name string) (peer.Peer, int) {
	r, _, code := peerRegistry(env)
	if code != exitcode.OK {
		return peer.Peer{}, code
	}
	p, err := r.Get(name)
	if err != nil {
		if errors.Is(err, peer.ErrNoPeers) {
			fmt.Fprintln(env.Stderr, "kranq: no peers are registered; `kranq peer add <name> --ssh user@host:port`")
		} else {
			fmt.Fprintf(env.Stderr, "kranq: %v\n", err)
		}
		return peer.Peer{}, exitcode.Misconfigured
	}
	return p, exitcode.OK
}

func peerAdd(env Env, args []string) int {
	fs := flag.NewFlagSet("peer add", flag.ContinueOnError)
	fs.SetOutput(env.Stderr)
	ssh := fs.String("ssh", "", "user@host[:port]")
	bin := fs.String("bin", "", "path to kranq on that machine")
	asDefault := fs.Bool("default", false, "make this the peer commands use with no --peer")
	rest, err := parsePermuted(fs, args)
	if err != nil {
		return exitcode.Usage
	}
	if len(rest) != 1 || *ssh == "" {
		fmt.Fprintln(env.Stderr, "usage: kranq peer add <name> --ssh user@host[:port] [--bin PATH] [--default]")
		return exitcode.Usage
	}
	r, path, code := peerRegistry(env)
	if code != exitcode.OK {
		return code
	}
	if err := r.Add(peer.Peer{Name: rest[0], SSH: *ssh, Bin: *bin, Default: *asDefault}); err != nil {
		fmt.Fprintf(env.Stderr, "kranq: %v\n", err)
		return exitcode.Usage
	}
	if err := r.Save(path); err != nil {
		fmt.Fprintf(env.Stderr, "kranq: %v\n", err)
		return exitcode.InternalError
	}
	fmt.Fprintf(env.Stderr, "peer %s added; `kranq peer test %s` to check it\n", rest[0], rest[0])
	return exitcode.OK
}

func peerList(env Env, args []string) int {
	r, _, code := peerRegistry(env)
	if code != exitcode.OK {
		return code
	}
	if len(r.Peers) == 0 {
		fmt.Fprintln(env.Stderr, "no peers are registered")
		return exitcode.OK
	}
	w := tabwriter.NewWriter(env.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "NAME\tSSH\tKRANQ\tDEFAULT")
	for _, p := range r.Peers {
		star := ""
		if p.Default {
			star = "*"
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", p.Name, p.SSH, p.Bin, star)
	}
	w.Flush()
	return exitcode.OK
}

func peerRemove(env Env, args []string) int {
	if len(args) != 1 {
		fmt.Fprintln(env.Stderr, "usage: kranq peer rm <name>")
		return exitcode.Usage
	}
	r, path, code := peerRegistry(env)
	if code != exitcode.OK {
		return code
	}
	if err := r.Remove(args[0]); err != nil {
		fmt.Fprintf(env.Stderr, "kranq: %v\n", err)
		return exitcode.Misconfigured
	}
	if err := r.Save(path); err != nil {
		fmt.Fprintf(env.Stderr, "kranq: %v\n", err)
		return exitcode.InternalError
	}
	fmt.Fprintf(env.Stderr, "peer %s removed\n", args[0])
	return exitcode.OK
}

func peerTest(env Env, args []string) int {
	p, code := resolvePeer(env, first(args))
	if code != exitcode.OK {
		return code
	}
	target, _ := peer.ParseTarget(p.SSH)
	fmt.Fprintf(env.Stderr, "== %s (%s)\n", p.Name, p.SSH)

	arch, err := sshCapture(target, "uname -m")
	if err != nil {
		fmt.Fprintf(env.Stderr, "kranq: cannot reach %s: %v\n", p.SSH, err)
		return exitcode.Unreachable
	}
	fmt.Fprintf(env.Stderr, "reachable, %s\n", strings.TrimSpace(arch))

	if _, err := sshCapture(target, remotePath(p.Bin)+" version"); err != nil {
		fmt.Fprintf(env.Stderr, "no kranq at %s; `kranq peer upgrade %s` installs one\n", p.Bin, p.Name)
		return exitcode.MissingDep
	}
	out, err := sshCapture(target, remotePath(p.Bin)+" doctor")
	fmt.Fprint(env.Stdout, out)
	if err != nil {
		return exitcode.Misconfigured
	}
	return exitcode.OK
}

func peerUpgrade(env Env, args []string) int {
	fs := flag.NewFlagSet("peer upgrade", flag.ContinueOnError)
	fs.SetOutput(env.Stderr)
	binary := fs.String("binary", "", "kranq binary to send (default: the running one)")
	force := fs.Bool("force", false, "upgrade even while work is in flight there")
	rest, err := parsePermuted(fs, args)
	if err != nil {
		return exitcode.Usage
	}
	p, code := resolvePeer(env, first(rest))
	if code != exitcode.OK {
		return code
	}
	target, _ := peer.ParseTarget(p.SSH)

	source := *binary
	if source == "" {
		self, err := os.Executable()
		if err != nil {
			fmt.Fprintf(env.Stderr, "kranq: %v\n", err)
			return exitcode.InternalError
		}
		source = self
	}

	arch, err := sshCapture(target, "uname -m")
	if err != nil {
		fmt.Fprintf(env.Stderr, "kranq: cannot reach %s: %v\n", p.SSH, err)
		return exitcode.Unreachable
	}
	if *binary == "" {
		if code := archMatches(env, strings.TrimSpace(arch)); code != exitcode.OK {
			return code
		}
	}

	staged := fmt.Sprintf("/tmp/kranq-upgrade-%d", time.Now().UnixNano())
	fmt.Fprintf(env.Stderr, "sending %s to %s\n", filepath.Base(source), p.Name)
	if err := runQuiet("scp", target.SCPArgs(source, staged)...); err != nil {
		fmt.Fprintf(env.Stderr, "kranq: could not copy the binary: %v\n", err)
		return exitcode.Unreachable
	}
	defer sshCapture(target, "rm -f "+shellQuote(staged))

	if _, err := sshCapture(target, remotePath(p.Bin)+" version"); err != nil {
		fmt.Fprintf(env.Stderr, "no kranq at %s yet, installing\n", p.Bin)
		return sshStream(env, target, fmt.Sprintf("chmod 755 %s && %s install", shellQuote(staged), shellQuote(staged)))
	}
	cmd := fmt.Sprintf("%s upgrade %s --target %s", remotePath(p.Bin), shellQuote(staged), remotePath(p.Bin))
	if *force {
		cmd += " --force"
	}
	return sshStream(env, target, cmd)
}

// The running binary is this machine's architecture, so sending it to a peer of
// another one produces a confusing exec error on the far side.
func archMatches(env Env, remote string) int {
	want := map[string]string{"arm64": "arm64", "amd64": "x86_64"}[runtime.GOARCH]
	if want == "" || remote == want {
		return exitcode.OK
	}
	fmt.Fprintf(env.Stderr, "kranq: this binary is %s but that machine is %s\n", runtime.GOARCH, remote)
	fmt.Fprintf(env.Stderr, "build one for it and pass --binary:\n  GOOS=darwin GOARCH=%s go build -o kranq-%s .\n",
		archOf(remote), archOf(remote))
	return exitcode.Misconfigured
}

func archOf(uname string) string {
	if uname == "x86_64" {
		return "amd64"
	}
	return uname
}

func sshCapture(target peer.Target, command string) (string, error) {
	out, err := exec.Command("ssh", target.SSHArgs(command)...).Output()
	return string(out), err
}

func sshStream(env Env, target peer.Target, command string) int {
	cmd := exec.CommandContext(context.Background(), "ssh", target.SSHArgs(command)...)
	cmd.Stdout = env.Stdout
	cmd.Stderr = env.Stderr
	if err := cmd.Run(); err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			return exit.ExitCode()
		}
		fmt.Fprintf(env.Stderr, "kranq: %v\n", err)
		return exitcode.Unreachable
	}
	return exitcode.OK
}

func runQuiet(name string, args ...string) error {
	cmd := exec.Command(name, args...)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if s := strings.TrimSpace(stderr.String()); s != "" {
			return fmt.Errorf("%w: %s", err, s)
		}
		return err
	}
	return nil
}

func first(args []string) string {
	if len(args) == 0 {
		return ""
	}
	return args[0]
}

func shellQuote(v string) string {
	return "'" + strings.ReplaceAll(v, "'", `'\''`) + "'"
}

// A single-quoted tilde is a literal directory named "~", so a path that starts
// with one is passed through $HOME instead. peer.ValidBin keeps the rest of the
// path safe to sit inside double quotes.
func remotePath(v string) string {
	if rest, found := strings.CutPrefix(v, "~/"); found {
		return `"$HOME/` + rest + `"`
	}
	return shellQuote(v)
}
