package project

import (
	"bufio"
	"fmt"
	"strings"
)

// lex turns lines into instructions, joining backslash continuations and
// reading heredoc bodies verbatim. A real setup script is dozens of lines, so
// `RUN <<EOF` is not a nicety: without it every line needs a trailing backslash.
func lex(scan *bufio.Scanner, file string) ([]instruction, error) {
	var out []instruction
	var lines []string
	for scan.Scan() {
		lines = append(lines, strings.TrimRight(scan.Text(), "\r"))
	}
	if err := scan.Err(); err != nil {
		return nil, err
	}

	for i := 0; i < len(lines); i++ {
		text := lines[i]
		if strings.TrimSpace(text) == "" || strings.HasPrefix(strings.TrimSpace(text), "#") {
			continue
		}
		start := i + 1
		for strings.HasSuffix(text, "\\") && i+1 < len(lines) {
			i++
			text = strings.TrimSuffix(text, "\\") + strings.TrimSpace(lines[i])
		}
		verb, args, _ := strings.Cut(strings.TrimSpace(text), " ")
		verb = strings.ToUpper(verb)
		args = strings.TrimSpace(args)

		if tag, ok := heredocTag(args); ok {
			body, next, err := heredoc(lines, i+1, tag)
			if err != nil {
				return nil, fmt.Errorf("%s:%d: %w", file, start, err)
			}
			out = append(out, instruction{verb: verb, body: body, line: start})
			i = next
			continue
		}
		out = append(out, instruction{verb: verb, args: args, line: start})
	}
	return out, nil
}

func heredocTag(args string) (string, bool) {
	if !strings.HasPrefix(args, "<<") {
		return "", false
	}
	tag := strings.TrimSpace(strings.TrimPrefix(args, "<<"))
	tag = strings.Trim(tag, `"'`)
	if tag == "" {
		return "", false
	}
	return tag, true
}

func heredoc(lines []string, from int, tag string) (string, int, error) {
	var body []string
	for i := from; i < len(lines); i++ {
		if strings.TrimSpace(lines[i]) == tag {
			return strings.Join(body, "\n") + "\n", i, nil
		}
		body = append(body, lines[i])
	}
	return "", 0, fmt.Errorf("heredoc opened with <<%s is never closed", tag)
}

// fields splits on whitespace the way a shell does for quoting, so a path with
// a space can be copied and an ENV value can contain one.
func fields(s string) []string {
	var out []string
	var cur strings.Builder
	var quote rune
	held := false
	for _, r := range s {
		switch {
		case quote != 0 && r == quote:
			quote = 0
		case quote == 0 && (r == '\'' || r == '"'):
			quote = r
			held = true
		case quote == 0 && (r == ' ' || r == '\t'):
			if cur.Len() > 0 || held {
				out = append(out, cur.String())
				cur.Reset()
				held = false
			}
		default:
			cur.WriteRune(r)
		}
	}
	if cur.Len() > 0 || held {
		out = append(out, cur.String())
	}
	return out
}

func parseCopy(args string) (Copy, error) {
	parts := fields(args)
	if len(parts) < 2 {
		return Copy{}, fmt.Errorf("COPY needs at least one source and a destination")
	}
	return Copy{Sources: parts[:len(parts)-1], Dest: parts[len(parts)-1]}, nil
}

// ENV takes either `ENV K=V K2=V2` or the older `ENV K the rest of the line`.
func parseEnv(args string) ([]EnvVar, error) {
	parts := fields(args)
	if len(parts) == 0 {
		return nil, fmt.Errorf("ENV needs a name")
	}
	if !strings.Contains(parts[0], "=") {
		if len(parts) < 2 {
			return nil, fmt.Errorf("ENV %s has no value", parts[0])
		}
		name, value, _ := strings.Cut(strings.TrimSpace(args), " ")
		return []EnvVar{{Name: name, Value: strings.TrimSpace(value)}}, nil
	}
	out := make([]EnvVar, 0, len(parts))
	for _, part := range parts {
		name, value, ok := strings.Cut(part, "=")
		if !ok || name == "" {
			return nil, fmt.Errorf("ENV %q is not NAME=VALUE", part)
		}
		out = append(out, EnvVar{Name: name, Value: value})
	}
	return out, nil
}
