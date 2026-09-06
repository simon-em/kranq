package peer

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

var (
	ErrNotFound = errors.New("no such peer")
	ErrNoPeers  = errors.New("no peers are registered")
)

const DefaultBin = "~/.local/bin/kranq"

type Peer struct {
	Name    string `json:"name"`
	SSH     string `json:"ssh"`
	Bin     string `json:"bin"`
	Default bool   `json:"default,omitempty"`
}

type Registry struct {
	Peers []Peer `json:"peers"`
}

var namePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,31}$`)

type Target struct {
	User string
	Host string
	Port int
}

// user@host[:port]. Deliberately strict: a typo here surfaces as a confusing ssh
// failure minutes later, against a machine that may not be the intended one.
func ParseTarget(v string) (Target, error) {
	var t Target
	rest := strings.TrimSpace(v)
	if rest == "" {
		return t, errors.New("empty ssh target")
	}
	if user, host, found := strings.Cut(rest, "@"); found {
		if user == "" {
			return t, fmt.Errorf("%q has an empty user", v)
		}
		t.User, rest = user, host
	}
	if host, port, found := strings.Cut(rest, ":"); found {
		n, err := strconv.Atoi(port)
		if err != nil || n < 1 || n > 65535 {
			return t, fmt.Errorf("%q has an invalid port %q", v, port)
		}
		t.Host, t.Port = host, n
	} else {
		t.Host = rest
	}
	if t.Host == "" {
		return t, fmt.Errorf("%q has no host", v)
	}
	return t, nil
}

func (t Target) Address() string {
	if t.User == "" {
		return t.Host
	}
	return t.User + "@" + t.Host
}

// BatchMode so a missing key fails immediately instead of hanging on a password
// prompt nobody is watching, which is the difference between a clear error and
// a stuck deploy.
func (t Target) SSHArgs(extra ...string) []string {
	args := []string{"-o", "BatchMode=yes"}
	if t.Port != 0 {
		args = append(args, "-p", strconv.Itoa(t.Port))
	}
	return append(append(args, t.Address()), extra...)
}

func (t Target) SCPArgs(source, dest string) []string {
	args := []string{"-o", "BatchMode=yes"}
	if t.Port != 0 {
		args = append(args, "-P", strconv.Itoa(t.Port))
	}
	return append(args, source, t.Address()+":"+dest)
}

// A leading ~ has to survive to the remote shell, so the path is interpolated
// rather than quoted. These characters would change what that shell runs.
func ValidBin(v string) error {
	if v == "" {
		return errors.New("empty kranq path")
	}
	if !strings.HasPrefix(v, "/") && !strings.HasPrefix(v, "~/") {
		return fmt.Errorf("%q must be absolute or start with ~/", v)
	}
	if i := strings.IndexAny(v, "\"$`\\\n"); i >= 0 {
		return fmt.Errorf("%q contains %q, which is not allowed in a remote path", v, v[i])
	}
	return nil
}

func Path(home string) string { return filepath.Join(home, "peers.json") }

func Load(path string) (*Registry, error) {
	r := &Registry{}
	body, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return r, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(body, r); err != nil {
		return nil, fmt.Errorf("%s is not readable as a peer list: %w", path, err)
	}
	return r, nil
}

func (r *Registry) Save(path string) error {
	body, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(body, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func (r *Registry) Add(p Peer) error {
	if !namePattern.MatchString(p.Name) {
		return fmt.Errorf("%q is not a usable peer name: lowercase letters, digits and dashes", p.Name)
	}
	if _, err := ParseTarget(p.SSH); err != nil {
		return err
	}
	if p.Bin == "" {
		p.Bin = DefaultBin
	}
	if err := ValidBin(p.Bin); err != nil {
		return err
	}
	for i, existing := range r.Peers {
		if existing.Name != p.Name {
			continue
		}
		// Re-adding a peer edits its address, not its role: dropping the flag
		// here would leave a one-peer registry with no default at all.
		p.Default = p.Default || existing.Default
		r.Peers[i] = p
		r.applyDefault(p)
		return nil
	}
	if len(r.Peers) == 0 {
		p.Default = true
	}
	r.Peers = append(r.Peers, p)
	r.applyDefault(p)
	sort.Slice(r.Peers, func(i, j int) bool { return r.Peers[i].Name < r.Peers[j].Name })
	return nil
}

func (r *Registry) applyDefault(p Peer) {
	if !p.Default {
		return
	}
	for i := range r.Peers {
		r.Peers[i].Default = r.Peers[i].Name == p.Name
	}
}

func (r *Registry) Remove(name string) error {
	for i, p := range r.Peers {
		if p.Name != name {
			continue
		}
		r.Peers = append(r.Peers[:i], r.Peers[i+1:]...)
		// The list must never be left without a default, or a bare `--peer`
		// would silently resolve to nothing.
		if p.Default && len(r.Peers) > 0 {
			r.Peers[0].Default = true
		}
		return nil
	}
	return fmt.Errorf("%w: %s", ErrNotFound, name)
}

func (r *Registry) Get(name string) (Peer, error) {
	if name == "" {
		return r.Default()
	}
	for _, p := range r.Peers {
		if p.Name == name {
			return p, nil
		}
	}
	return Peer{}, fmt.Errorf("%w: %s", ErrNotFound, name)
}

func (r *Registry) Default() (Peer, error) {
	if len(r.Peers) == 0 {
		return Peer{}, ErrNoPeers
	}
	for _, p := range r.Peers {
		if p.Default {
			return p, nil
		}
	}
	return r.Peers[0], nil
}
