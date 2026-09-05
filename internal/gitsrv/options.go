package gitsrv

import (
	"fmt"
	"sort"
	"strings"
)

// What a caller asks for by pushing. Push options are the channel because they
// are the only one git gives that is neither the URL nor a header: they carry
// arbitrary strings, they do not end up in a proxy's request log, and the hook
// receives them as GIT_PUSH_OPTION_<n>.
type Request struct {
	Task    string
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
		case name == "task":
			req.Task = value
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
	if req.Task == "" {
		return req, fmt.Errorf("no task: push with -o task=<path to the spec inside the repo>")
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
