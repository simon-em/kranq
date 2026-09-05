package gitsrv

import (
	"fmt"
	"strings"
)

// Over ssh, git runs `git-receive-pack '<path>'` on the far side. A forced
// command in authorized_keys replaces that with forge, and the original lands
// in SSH_ORIGINAL_COMMAND. This is the only thing standing between a key and
// arbitrary command execution as the forge user, so it refuses anything it does
// not positively recognise rather than trying to sanitise it.
type SSHCommand struct {
	Verb string
	Repo string
}

const (
	VerbReceive = "git-receive-pack"
	VerbUpload  = "git-upload-pack"
)

func (c SSHCommand) Writes() bool { return c.Verb == VerbReceive }

func ParseSSHCommand(original string) (SSHCommand, error) {
	var cmd SSHCommand
	trimmed := strings.TrimSpace(original)
	if trimmed == "" {
		return cmd, fmt.Errorf("this key can only be used for git; it has no shell")
	}

	verb, rest, ok := strings.Cut(trimmed, " ")
	if !ok {
		return cmd, fmt.Errorf("%q is not a git command", firstWord(trimmed))
	}
	// git also spells these "git receive-pack" with a space.
	if verb == "git" {
		sub, remainder, found := strings.Cut(strings.TrimSpace(rest), " ")
		if !found {
			return cmd, fmt.Errorf("%q is not a git command", firstWord(trimmed))
		}
		verb, rest = "git-"+sub, remainder
	}
	if verb != VerbReceive && verb != VerbUpload {
		return cmd, fmt.Errorf("%q is not allowed; this key may only push and fetch", firstWord(trimmed))
	}

	path, err := unquote(strings.TrimSpace(rest))
	if err != nil {
		return cmd, err
	}
	// Unlike an http request, where the repository is only the first segment of
	// a longer path, an ssh path is the repository and nothing else. Anything
	// with more parts is refused rather than having its first segment taken:
	// "/etc/../dx.git" would otherwise quietly mean "etc".
	repo := strings.Trim(path, "/")
	if strings.Contains(repo, "/") {
		return cmd, fmt.Errorf("%q is not a repository name", firstWord(repo))
	}
	if err := ValidRepo(repo); err != nil {
		return cmd, err
	}
	return SSHCommand{Verb: verb, Repo: strings.TrimSuffix(repo, ".git")}, nil
}

func firstWord(v string) string {
	word, _, _ := strings.Cut(v, " ")
	if len(word) > 40 {
		return word[:40] + "…"
	}
	return word
}

// git single-quotes the path and writes an embedded quote as '\”. Anything
// else, including an unquoted path carrying shell syntax, is refused.
func unquote(v string) (string, error) {
	if v == "" {
		return "", fmt.Errorf("no repository given")
	}
	if !strings.HasPrefix(v, "'") {
		if strings.ContainsAny(v, " \t;|&$`<>()\\\"'\n") {
			return "", fmt.Errorf("the repository argument is not quoted as git quotes it")
		}
		return v, nil
	}
	if !strings.HasSuffix(v, "'") || len(v) < 2 {
		return "", fmt.Errorf("the repository argument is not closed")
	}
	inner := v[1 : len(v)-1]
	var b strings.Builder
	for i := 0; i < len(inner); i++ {
		if inner[i] != '\'' {
			b.WriteByte(inner[i])
			continue
		}
		// The only quote sequence git produces is '\'' .
		if strings.HasPrefix(inner[i:], `'\''`) {
			b.WriteByte('\'')
			i += 3
			continue
		}
		return "", fmt.Errorf("the repository argument is quoted in a way git does not produce")
	}
	return b.String(), nil
}
