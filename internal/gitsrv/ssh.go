package gitsrv

import (
	"fmt"
	"strings"
)

// Over ssh, git runs `git-receive-pack '<path>'` on the far side. A forced
// command in authorized_keys replaces that with kranq, and the original lands
// in SSH_ORIGINAL_COMMAND. This is the only thing standing between a key and
// arbitrary command execution as the kranq user, so it refuses anything it does
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

// RepoFromPath handles the other way in: `git push --receive-pack="kranq
// git-receive"` makes git run kranq directly with the repository as an
// argument, so no forced command and no kranq-specific key is involved at all.
// Whoever can already ssh to the machine can push.
func RepoFromPath(path string) (string, error) {
	trimmed := strings.Trim(strings.TrimSpace(path), "/")
	if trimmed == "" {
		return "", fmt.Errorf("no repository given")
	}
	// A full path is accepted so that pushing to the repository's real location
	// works too; only its last part names the repository, and the store is
	// where it resolves either way. A ".." segment cannot reach outside the
	// store, but it means the caller intended somewhere else, and quietly
	// reinterpreting that is worse than refusing it.
	for _, segment := range strings.Split(trimmed, "/") {
		if segment == ".." {
			return "", fmt.Errorf("%q is not a repository name", path)
		}
	}
	if i := strings.LastIndex(trimmed, "/"); i >= 0 {
		trimmed = trimmed[i+1:]
	}
	if err := ValidRepo(trimmed); err != nil {
		return "", err
	}
	return strings.TrimSuffix(trimmed, ".git"), nil
}

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
	// A client may ask for kranq itself as the receive-pack. Under a forced
	// command that arrives here rather than as an argument, and refusing it
	// would break the combination of a locked-down key and a client set up for
	// the no-setup path.
	if strings.HasSuffix(verb, "/kranq") || verb == "kranq" {
		sub, remainder, found := strings.Cut(strings.TrimSpace(rest), " ")
		if !found {
			return cmd, fmt.Errorf("%q is not a git command", firstWord(trimmed))
		}
		switch sub {
		case "git-receive":
			verb, rest = VerbReceive, remainder
		case "git-upload":
			verb, rest = VerbUpload, remainder
		default:
			return cmd, fmt.Errorf("%q is not allowed here", firstWord(sub))
		}
		// kranq's own flags come before the path, and a client configured for
		// the other way in sends them: remote.<name>.uploadpack is
		// "kranq git-receive --upload". Under a forced command the whole
		// string arrives here, so the flags have to be read rather than taken
		// for part of the repository -- which made every fetch fail with "the
		// repository argument is not quoted as git quotes it". Unknown flags
		// are refused, because this is the boundary between a key and a shell.
		verb, rest, err := takeFlags(verb, rest)
		if err != nil {
			return cmd, err
		}
		return finish(verb, rest)
	}
	if verb != VerbReceive && verb != VerbUpload {
		return cmd, fmt.Errorf("%q is not allowed; this key may only push and fetch", firstWord(trimmed))
	}
	return finish(verb, rest)
}

func takeFlags(verb, rest string) (string, string, error) {
	for {
		trimmed := strings.TrimSpace(rest)
		if !strings.HasPrefix(trimmed, "-") {
			return verb, trimmed, nil
		}
		word, remainder, _ := strings.Cut(trimmed, " ")
		switch {
		case word == "--upload":
			verb, rest = VerbUpload, remainder
		case strings.HasPrefix(word, "--name="):
			rest = remainder
		case word == "--name":
			_, after, found := strings.Cut(strings.TrimSpace(remainder), " ")
			if !found {
				return verb, "", fmt.Errorf("--name has no value")
			}
			rest = after
		default:
			return verb, "", fmt.Errorf("%q is not allowed here", firstWord(word))
		}
	}
}

func finish(verb, rest string) (SSHCommand, error) {
	var cmd SSHCommand
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
