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
	"github.com/simon-em/kranq/internal/peer"
)

// kranq setup makes a machine ready to receive work and authorises a key to
// send it, which used to be `install` and then `setup-git`. Everything is
// optional: run it with no arguments on the build machine, or with --peer from
// a laptop, where the address is already known.
//
// The far side of a push is provisioned so the near side needs nothing: no
// receive-pack override, no upload-pack override, no shell on the key. A
// pipeline ends up with a URL and a key, which is what any git remote needs.
//
// The forced command in authorized_keys is what makes that work. ssh puts what
// git asked for in SSH_ORIGINAL_COMMAND and runs kranq instead, so kranq
// resolves the repository rather than the client having to say where it lives.
func runSetup(env Env, args []string) int {
	fs := flag.NewFlagSet("setup", flag.ContinueOnError)
	fs.SetOutput(env.Stderr)
	on := fs.String("peer", "", "set up this registered build machine rather than the local one")
	host := fs.String("host", "", "address a client reaches the machine at, [user@]host[:port]")
	port := fs.Int("port", 0, "the port, if it is not in --host")
	pub := fs.String("key", "", "authorise this public key instead of generating one")
	keyOnly := fs.Bool("key-only", false, "only authorise a key; leave lima and the daemon alone")
	rest, err := parsePermuted(fs, args)
	if err != nil {
		return exitcode.Usage
	}
	if len(rest) > 1 {
		fmt.Fprintln(env.Stderr, "usage: kranq setup [name] [--peer NAME] [--host ADDR] [--key FILE]")
		return exitcode.Usage
	}
	name := "ci"
	if len(rest) == 1 {
		name = rest[0]
	}

	if *on != "" {
		return setupOnPeer(env, *on, name, *pub, *keyOnly)
	}

	if !*keyOnly {
		// deps-only: whatever put this binary here owns it, and a package
		// manager's copy must not be duplicated somewhere else on PATH.
		if code := runInstall(env, []string{"--deps-only", "--with-daemon"}); code != exitcode.OK {
			return code
		}
		fmt.Fprintln(env.Stderr)
	}

	where, guessed := resolveAddr(*host, *port)
	dir, err := os.MkdirTemp("", "kranq-setup-*")
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
	if code := installKey(env, name, *pub); code != exitcode.OK {
		return code
	}
	keys, _ := authkeys.Path()
	fmt.Fprintf(env.Stderr, "key %q installed in %s: it can push and fetch here, and nothing else\n\n", name, keys)
	writeVars(env, where, private)
	if guessed {
		fmt.Fprintf(env.Stderr, "\nThe address is the one this machine was reached on (%s:%d). If a client\n", where.Host, where.Port)
		fmt.Fprintln(env.Stderr, "comes in through a forwarded port or another name, pass it: --host addr:port")
	}
	return exitcode.OK
}

// From a laptop there is nothing to type: the peer registry already holds the
// address, which is the one thing the machine itself cannot work out.
func setupOnPeer(env Env, peerName, name, pub string, keyOnly bool) int {
	p, code := resolvePeer(env, peerName)
	if code != exitcode.OK {
		return code
	}
	target, err := peer.ParseTarget(p.SSH)
	if err != nil {
		fmt.Fprintf(env.Stderr, "kranq: %v\n", err)
		return exitcode.Misconfigured
	}
	if pub != "" {
		fmt.Fprintln(env.Stderr, "kranq: --key is for a key already on that machine; run setup there instead")
		return exitcode.Usage
	}
	fmt.Fprintf(env.Stderr, "setting up %s\n", p.Name)
	return sshStream(env, target, peerSetupCommand(p.Bin, p.SSH, name, keyOnly))
}

// --host is the peer's own registered address, which is the one thing the
// machine on the far side cannot work out for itself.
func peerSetupCommand(bin, ssh, name string, keyOnly bool) string {
	cmd := fmt.Sprintf("%s setup %s --host %s", remotePath(bin), shellQuote(name), shellQuote(ssh))
	if keyOnly {
		cmd += " --key-only"
	}
	return cmd
}

type addr struct {
	Login, Host string
	Port        int
}

// A machine cannot see the address it is reached at. The mini answers on a
// forwarded 333 and sees only its own 192.168.x.x:22, so what is guessed here
// is right for a client on the same network and wrong for one outside it --
// which is why the caller is told that it was guessed rather than having it
// printed as though it were known.
//
// SSH_CONNECTION is the best of the bad options: "<client ip> <client port>
// <server ip> <server port>", so it names an address someone genuinely reached
// this machine on, unlike a hostname that may resolve nowhere.
func resolveAddr(given string, port int) (addr, bool) {
	login, host, p := splitAddr(given)
	if port == 0 {
		port = p
	}
	if login == "" {
		login = currentUser()
	}
	if host != "" && port != 0 {
		return addr{login, host, port}, false
	}
	guessHost, guessPort := connectedAddr()
	if host == "" {
		host = guessHost
	}
	if port == 0 {
		port = guessPort
	}
	return addr{login, host, port}, true
}

func connectedAddr() (string, int) {
	fields := strings.Fields(os.Getenv("SSH_CONNECTION"))
	if len(fields) == 4 {
		if n, err := strconv.Atoi(fields[3]); err == nil && n > 0 {
			return fields[2], n
		}
	}
	return hostname(), sshdPort()
}

func writeVars(env Env, a addr, private string) {
	// The login name belongs in the address: a push is ssh, and ssh with no
	// user takes whoever is running the pipeline, which on a CI container is
	// not the account the key was installed under.
	peer := fmt.Sprintf("%s@%s:%d", a.Login, a.Host, a.Port)
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
	fmt.Fprintln(env.Stderr, "Those three lines are the pipeline's variables.")
	fmt.Fprintln(env.Stderr, "KRANQ_PEER is the only one it must have. The other two are for a")
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
