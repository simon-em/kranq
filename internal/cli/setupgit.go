package cli

import (
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/simon-em/kranq/internal/authkeys"
	"github.com/simon-em/kranq/internal/exitcode"
)

// kranq setup-git provisions the far side of a push so that the near side needs
// nothing: no receive-pack override, no upload-pack override, no shell on the
// key. A pipeline ends up with a URL and a key, which is what any git remote
// needs anyway.
//
// The forced command in authorized_keys is what makes that work. ssh puts what
// git asked for in SSH_ORIGINAL_COMMAND and runs kranq instead, so kranq resolves
// the repository rather than the client having to say where it lives.
func runSetupGit(env Env, args []string) int {
	fs := flag.NewFlagSet("setup-git", flag.ContinueOnError)
	fs.SetOutput(env.Stderr)
	host := fs.String("host", "", "address the pipeline reaches this machine at, [user@]host[:port]")
	port := fs.Int("port", 0, "ssh port it is reached on (default: what sshd is bound to)")
	repo := fs.String("repo", "", "create this repository now rather than on first push")
	pub := fs.String("key", "", "public key to authorise (default: generate a new one)")
	rest, err := parsePermuted(fs, args)
	if err != nil {
		return exitcode.Usage
	}
	name := "ci"
	if len(rest) == 1 {
		name = rest[0]
	} else if len(rest) > 1 {
		fmt.Fprintln(env.Stderr, "usage: kranq setup-git [name] [--host ADDR] [--port N] [--repo NAME] [--key FILE]")
		return exitcode.Usage
	}

	addr, guessed := resolveAddr(*host, *port)

	dir, err := os.MkdirTemp("", "kranq-setup-git-*")
	if err != nil {
		fmt.Fprintf(env.Stderr, "kranq: %v\n", err)
		return exitcode.InternalError
	}
	defer os.RemoveAll(dir)

	var private string
	if *pub == "" {
		*pub, private, err = generateKey(dir, name)
		if err != nil {
			fmt.Fprintf(env.Stderr, "kranq: %v\n", err)
			return exitcode.InternalError
		}
	}
	if *repo != "" {
		if code := runRepo(quiet(env), []string{"create", *repo}); code != exitcode.OK {
			return code
		}
		fmt.Fprintf(env.Stderr, "repository %q is ready\n", *repo)
	}
	if code := installKey(env, name, *pub); code != exitcode.OK {
		return code
	}
	keys, _ := authkeys.Path()
	fmt.Fprintf(env.Stderr, "key %q installed in %s: it can push and fetch here, and nothing else\n\n", name, keys)
	writeVars(env, addr, private)
	if guessed {
		fmt.Fprintf(env.Stderr, "\nThe port is what sshd is bound to here (%d). If this machine is reached\n", addr.Port)
		fmt.Fprintln(env.Stderr, "through a forwarded port, pass the outside one: --host addr:port")
	}
	return exitcode.OK
}

// The pipeline needs exactly three things, and two of them are optional if it
// already has an ssh identity that reaches this machine.
type addr struct {
	Login, Host string
	Port        int
}

// A machine cannot see the address it is reached at: a host behind a forwarded
// port sees only the port sshd is bound to. So sshd's is a starting point and
// nothing more, and the caller is told when that is what it got rather than
// having it printed as though it were known.
func resolveAddr(given string, port int) (addr, bool) {
	login, host, p := splitAddr(given)
	if port == 0 {
		port = p
	}
	if login == "" {
		login = currentUser()
	}
	if host == "" {
		host = hostname()
	}
	if port == 0 {
		return addr{login, host, sshdPort()}, true
	}
	return addr{login, host, port}, false
}

func writeVars(env Env, a addr, private string) {
	// The login name belongs in the address: a push is ssh, and ssh with no
	// user takes whoever is running the pipeline, which on a CI container is
	// not the account the key was installed under.
	peer := fmt.Sprintf("%s@%s:%d", a.Login, a.Host, a.Port)
	fmt.Fprintln(env.Stderr, "Set these in the pipeline:")
	fmt.Fprintln(env.Stdout, "KRANQ_PEER="+peer)
	if line := hostKeyLine(a.Host, a.Port); line != "" {
		fmt.Fprintln(env.Stdout, "KRANQ_HOST_KEY="+line)
	}
	if private != "" {
		fmt.Fprintln(env.Stdout, "KRANQ_SSH_KEY<<EOF")
		fmt.Fprint(env.Stdout, private)
		fmt.Fprintln(env.Stdout, "EOF")
	}
	fmt.Fprintln(env.Stderr)
	fmt.Fprintln(env.Stderr, "KRANQ_PEER is the only one a pipeline must have. The other two are for a")
	fmt.Fprintln(env.Stderr, "caller with no ssh identity of its own; set KRANQ_SSH_KEY secured. The")
	fmt.Fprintln(env.Stderr, "private key is printed once and kept nowhere, so run this again to rotate.")
	fmt.Fprintln(env.Stderr)
	fmt.Fprintln(env.Stderr, "Nothing else is needed on the client: no receive-pack override, no")
	fmt.Fprintln(env.Stderr, "upload-pack override. The key runs kranq as its forced command, so the")
	fmt.Fprintln(env.Stderr, "far side resolves the repository itself.")
}

// Only the variables reach stdout, so that `kranq setup-git > vars.env` is a
// file you can read straight into a pipeline. The repository command writes its
// own path there, and explains how to push to it by path -- which is the other
// way in, not the one being set up here.
func quiet(Env) Env {
	return Env{Stdout: io.Discard, Stderr: io.Discard}
}

// [user@]host[:port], the form the address is written in everywhere else,
// KRANQ_PEER included. The port is taken from the right so a user name
// containing no colon cannot be mistaken for one.
func splitAddr(v string) (login, host string, port int) {
	if user, rest, found := strings.Cut(v, "@"); found {
		login, v = user, rest
	}
	if i := strings.LastIndex(v, ":"); i >= 0 {
		if n, err := strconv.Atoi(v[i+1:]); err == nil && n > 0 {
			return login, v[:i], n
		}
	}
	return login, v, 0
}

func installKey(env Env, name, pub string) int {
	silent := quiet(env)
	_ = runKey(silent, []string{"rm", name})
	if code := runKey(silent, []string{"add", name, pub}); code != exitcode.OK {
		return runKey(env, []string{"add", name, pub})
	}
	return exitcode.OK
}

func generateKey(dir, name string) (pub, private string, err error) {
	path := filepath.Join(dir, "id_ed25519")
	cmd := exec.Command("ssh-keygen", "-q", "-t", "ed25519", "-f", path, "-N", "", "-C", "kranq-"+name)
	if out, err := cmd.CombinedOutput(); err != nil {
		return "", "", fmt.Errorf("ssh-keygen: %w: %s", err, strings.TrimSpace(string(out)))
	}
	body, err := os.ReadFile(path)
	if err != nil {
		return "", "", err
	}
	return path + ".pub", string(body), nil
}

// A known_hosts entry for a non-default port is written [host]:port, and an
// entry in the plain form simply never matches -- ssh looks the port form up and
// finds nothing, so verification fails with a message about an unknown host
// rather than about the format.
func hostKeyLine(host string, port int) string {
	return hostKeyFrom(hostKeyPath, host, port)
}

const hostKeyPath = "/etc/ssh/ssh_host_ed25519_key.pub"

func hostKeyFrom(path, host string, port int) string {
	body, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	fields := strings.Fields(string(body))
	if len(fields) < 2 {
		return ""
	}
	name := host
	if port != 22 {
		name = fmt.Sprintf("[%s]:%d", host, port)
	}
	return fmt.Sprintf("%s %s %s", name, fields[0], fields[1])
}

// Read from sshd's own configuration rather than assumed, because a machine
// reachable on a custom port is exactly the case this command exists for.
func sshdPort() int {
	body, err := os.ReadFile("/etc/ssh/sshd_config")
	if err != nil {
		return 22
	}
	for _, line := range strings.Split(string(body), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 && strings.EqualFold(fields[0], "Port") {
			if n, err := strconv.Atoi(fields[1]); err == nil && n > 0 {
				return n
			}
		}
	}
	return 22
}

func hostname() string {
	name, err := os.Hostname()
	if err != nil || name == "" {
		return "<this machine>"
	}
	return name
}
