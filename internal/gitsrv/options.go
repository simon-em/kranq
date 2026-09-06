package gitsrv

import (
	"bytes"
	"compress/gzip"
	"encoding/base64"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
)

// What a caller asks for by pushing. Push options are the channel because they
// are the only one git gives that is neither the URL nor a header: they carry
// arbitrary strings, they do not end up in a proxy's request log, and the hook
// receives them as GIT_PUSH_OPTION_<n>.
type Request struct {
	Task string
	// Spec carries the task itself, for a caller whose spec is not in the
	// commit being pushed: a shared task in a submodule is a gitlink, so its
	// contents are simply not there, and neither is an edit you have not
	// committed yet.
	Spec    []byte
	Label   string
	Branch  string
	Keep    string
	Detach  bool
	Env     map[string]string
	Unknown []string
}

const EnvPrefix = "env."

func ParseOptions(options []string) (Request, error) {
	req := Request{Env: map[string]string{}}
	for _, opt := range options {
		name, value, hasValue := strings.Cut(opt, "=")
		switch {
		case strings.HasPrefix(name, EnvPrefix):
			key := strings.TrimPrefix(name, EnvPrefix)
			if key == "" {
				return req, fmt.Errorf("push option %q names no variable", redact(opt))
			}
			if !hasValue {
				return req, fmt.Errorf("push option env.%s has no value; use -o env.%s=VALUE", key, key)
			}
			req.Env[key] = value
		case name == "task_file":
			req.Task = value
		case name == "task":
			// Inline, because a task worth running is not always a file in the
			// repository being pushed. git refuses a push option containing a
			// newline outright, so \n is spelled out and unescaped here.
			req.Spec = []byte(unescape(value))
		case name == "spec":
			decoded, err := decodeSpec(value)
			if err != nil {
				return req, err
			}
			req.Spec = decoded
		case name == "label":
			req.Label = value
		case name == "branch":
			req.Branch = value
		case name == "keep-vm":
			req.Keep = value
		case name == "detach":
			req.Detach = !hasValue || value == "true"
		default:
			req.Unknown = append(req.Unknown, name)
		}
	}
	if req.Task == "" && len(req.Spec) == 0 {
		return req, fmt.Errorf("no task: push with -o task_file=<path in the repo>, " +
			"or -o task=<the spec itself, with \\n for line breaks>")
	}
	if len(req.Unknown) > 0 {
		sort.Strings(req.Unknown)
		return req, fmt.Errorf("unknown push option(s): %s", strings.Join(req.Unknown, ", "))
	}
	return req, nil
}

// A value is never shown, because push options carry tokens. Only the name of
// the option is ever safe to echo back to the pusher or into a log.
func redact(opt string) string {
	name, _, found := strings.Cut(opt, "=")
	if !found {
		return name
	}
	return name + "=<redacted>"
}

func Redact(options []string) []string {
	out := make([]string, 0, len(options))
	for _, opt := range options {
		out = append(out, redact(opt))
	}
	return out
}

// A push's exit status only says whether the push was accepted, never what the
// run did, because post-receive runs after the ref has already moved. The hook
// prints this line instead and the client exits on it.
const ResultMarker = "KRANQ-RESULT"

// StatusRefused marks a run that never started, so its code is kranq's own and
// passes through instead of being folded into a generic failure the way a
// task's code in the reserved range is.
const StatusRefused = "refused"

type Result struct {
	ID       string
	Status   string
	ExitCode int
	Found    bool
}

func ParseResult(output string) Result {
	var res Result
	for _, line := range strings.Split(output, "\n") {
		// The trailing space matters: without it KRANQ-RESULTS, or any longer
		// word starting the same way, would be read as a result line.
		idx := strings.Index(line, ResultMarker+" ")
		if idx < 0 {
			continue
		}
		res = Result{Found: true}
		for _, field := range strings.Fields(line[idx+len(ResultMarker):]) {
			key, value, ok := strings.Cut(field, "=")
			if !ok {
				continue
			}
			switch key {
			case "id":
				res.ID = value
			case "status":
				res.Status = value
			case "exit":
				if n, err := strconv.Atoi(value); err == nil {
					res.ExitCode = n
				}
			}
		}
	}
	return res
}

// MaxSpec is generous against any real task and far below the 64KiB a single
// push option can carry, which is what the gzip buys.
const MaxSpec = 1 << 20

// EncodeSpec is the wire form of an inline task: gzipped, then base64, because
// a push option reaches the hook as an environment variable and a spec is
// YAML full of newlines.
func EncodeSpec(spec []byte) (string, error) {
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	if _, err := gz.Write(spec); err != nil {
		return "", err
	}
	if err := gz.Close(); err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(buf.Bytes()), nil
}

func decodeSpec(value string) ([]byte, error) {
	packed, err := base64.StdEncoding.DecodeString(value)
	if err != nil {
		return nil, fmt.Errorf("the inline spec is not base64: %w", err)
	}
	gz, err := gzip.NewReader(bytes.NewReader(packed))
	if err != nil {
		return nil, fmt.Errorf("the inline spec is not gzipped: %w", err)
	}
	defer gz.Close()
	spec, err := io.ReadAll(io.LimitReader(gz, MaxSpec+1))
	if err != nil {
		return nil, fmt.Errorf("the inline spec could not be read: %w", err)
	}
	if len(spec) > MaxSpec {
		return nil, fmt.Errorf("the inline spec is larger than %d bytes", MaxSpec)
	}
	if len(spec) == 0 {
		return nil, fmt.Errorf("the inline spec is empty")
	}
	return spec, nil
}

// git rejects a push option containing a literal newline, so an inline task
// spells its line breaks. Only \n and \\ are recognised: a spec is YAML, where
// a stray backslash is ordinary text, and rewriting anything else would corrupt
// a shell command inside a run: block.
func unescape(v string) string {
	var b strings.Builder
	for i := 0; i < len(v); i++ {
		if v[i] != '\\' || i+1 >= len(v) {
			b.WriteByte(v[i])
			continue
		}
		switch v[i+1] {
		case 'n':
			b.WriteByte('\n')
			i++
		case '\\':
			b.WriteByte('\\')
			i++
		default:
			b.WriteByte(v[i])
		}
	}
	return b.String()
}
