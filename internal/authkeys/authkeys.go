package authkeys

import (
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

var (
	ErrNotFound = errors.New("no such key")
	ErrExists   = errors.New("a key by that name is already installed")
)

// The marker sits in the comment field, which ssh ignores, so a kranq entry is
// identifiable without a parallel file that could drift out of step with this
// one.
const Marker = "kranq-key:"

var namePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,31}$`)

// The key types worth accepting in 2026. rsa is absent deliberately: a short rsa
// key looks identical to a long one here, and there is no reason to add one now.
var keyTypes = map[string]bool{
	"ssh-ed25519":                        true,
	"sk-ssh-ed25519@openssh.com":         true,
	"ecdsa-sha2-nistp256":                true,
	"ecdsa-sha2-nistp384":                true,
	"ecdsa-sha2-nistp521":                true,
	"sk-ecdsa-sha2-nistp256@openssh.com": true,
}

type Key struct {
	Name    string
	Type    string
	Data    string
	Comment string
}

func (k Key) Fingerprint() string {
	if len(k.Data) < 12 {
		return k.Data
	}
	return k.Data[:12] + "…"
}

func Path() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".ssh", "authorized_keys"), nil
}

// ParsePublicKey accepts what a .pub file contains and refuses anything else,
// including a private key pasted by mistake, which would otherwise be written
// into a world-readable-ish file.
func ParsePublicKey(line string) (Key, error) {
	var k Key
	fields := strings.Fields(strings.TrimSpace(line))
	if len(fields) < 2 {
		return k, errors.New("that does not look like an ssh public key")
	}
	if strings.Contains(line, "PRIVATE KEY") {
		return k, errors.New("that is a private key; pass the .pub file instead")
	}
	if !keyTypes[fields[0]] {
		return k, fmt.Errorf("%q is not a key type kranq accepts; use an ed25519 key", fields[0])
	}
	if _, err := base64.StdEncoding.DecodeString(fields[1]); err != nil {
		return k, errors.New("the key data is not valid base64")
	}
	k.Type, k.Data = fields[0], fields[1]
	if len(fields) > 2 {
		k.Comment = strings.Join(fields[2:], " ")
	}
	return k, nil
}

// restrict turns off agent and port forwarding, pty and X11 in one word. A git
// push needs none of them, and the forced command is what makes the key usable
// for nothing else.
func entry(k Key, name, command string) string {
	return fmt.Sprintf(`restrict,command="%s" %s %s %s%s`,
		strings.ReplaceAll(command, `"`, `\"`), k.Type, k.Data, Marker, name)
}

func markerOf(line string) (string, bool) {
	idx := strings.LastIndex(line, Marker)
	if idx < 0 {
		return "", false
	}
	name := strings.TrimSpace(line[idx+len(Marker):])
	if name == "" {
		return "", false
	}
	return name, true
}

// Add returns the new file content. Lines kranq did not write are returned
// untouched, in order: this file usually holds the key someone administers the
// machine with, and losing it locks them out.
func Add(content string, k Key, name, command string) (string, error) {
	if !namePattern.MatchString(name) {
		return "", fmt.Errorf("%q is not a usable key name: lowercase letters, digits and dashes", name)
	}
	for _, line := range lines(content) {
		if existing, ok := markerOf(line); ok && existing == name {
			return "", fmt.Errorf("%w: %s", ErrExists, name)
		}
		if fields := strings.Fields(line); len(fields) >= 2 && strings.Contains(line, k.Data) {
			return "", fmt.Errorf("that key is already in authorized_keys")
		}
	}
	out := strings.TrimRight(content, "\n")
	if out != "" {
		out += "\n"
	}
	return out + entry(k, name, command) + "\n", nil
}

func Remove(content, name string) (string, error) {
	var kept []string
	found := false
	for _, line := range lines(content) {
		if existing, ok := markerOf(line); ok && existing == name {
			found = true
			continue
		}
		kept = append(kept, line)
	}
	if !found {
		return "", fmt.Errorf("%w: %s", ErrNotFound, name)
	}
	if len(kept) == 0 {
		return "", nil
	}
	return strings.Join(kept, "\n") + "\n", nil
}

func List(content string) []Key {
	var out []Key
	for _, line := range lines(content) {
		name, ok := markerOf(line)
		if !ok {
			continue
		}
		fields := strings.Fields(line)
		k := Key{Name: name}
		for i, f := range fields {
			if keyTypes[f] && i+1 < len(fields) {
				k.Type, k.Data = f, fields[i+1]
				break
			}
		}
		out = append(out, k)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func lines(content string) []string {
	if strings.TrimSpace(content) == "" {
		return nil
	}
	return strings.Split(strings.TrimRight(content, "\n"), "\n")
}

func Read(path string) (string, error) {
	body, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return string(body), nil
}

// sshd refuses to read authorized_keys if it or ~/.ssh is group or world
// writable, and does so silently from the client's point of view.
func Write(path, content string) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return err
	}
	tmp := path + ".kranq-tmp"
	if err := os.WriteFile(tmp, []byte(content), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
