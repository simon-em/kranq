package project

import (
	"bufio"
	"fmt"
	"strconv"
	"strings"
)

type instruction struct {
	verb string
	args string
	body string
	line int
}

// Verbs a Dockerfile has that a build environment has no use for. Refusing them
// by name beats "unknown instruction", because what someone pasted a Dockerfile
// in for is usually one of these.
var refused = map[string]string{
	"FROM":        "every Kranqfile starts from kranq's own base image; there is nothing to choose",
	"CMD":         "a layer is an environment, not a service; what runs in it is the task",
	"ENTRYPOINT":  "a layer is an environment, not a service; what runs in it is the task",
	"EXPOSE":      "the VM publishes no ports to the host at all",
	"VOLUME":      "a layer is a whole disk; there is nothing to mount",
	"USER":        "every RUN is the build user, which has passwordless sudo",
	"ADD":         "use COPY; ADD also unpacks archives and fetches URLs, which hides what a layer contains",
	"LABEL":       "nothing reads labels",
	"HEALTHCHECK": "nothing runs a health check",
	"ONBUILD":     "there is nothing to trigger it",
	"SHELL":       "RUN is bash, always",
	"STOPSIGNAL":  "nothing to stop",
}

func parse(src, file string) (Build, error) {
	var b Build
	var pending Layer
	scan := bufio.NewScanner(strings.NewReader(src))
	scan.Buffer(make([]byte, 0, 64<<10), 4<<20)

	instructions, err := lex(scan, file)
	if err != nil {
		return b, err
	}
	for _, in := range instructions {
		if why, no := refused[in.verb]; no {
			return b, fmt.Errorf("%s:%d: %s is not a Kranqfile instruction: %s", file, in.line, in.verb, why)
		}
		if err := b.apply(&pending, in, file); err != nil {
			return b, err
		}
	}
	// Anything left after the last RUN is still part of the image, so it gets
	// a layer of its own rather than being silently dropped.
	if len(pending.Copies) > 0 {
		b.Layers = append(b.Layers, pending)
	}
	if len(b.Layers) == 0 {
		return b, fmt.Errorf("%s builds nothing: it has no RUN or COPY", file)
	}
	return b, nil
}

func (b *Build) size(in instruction, file string) error {
	value := strings.TrimSpace(in.args)
	if value == "" {
		return fmt.Errorf("%s:%d: %s needs a value", file, in.line, in.verb)
	}
	switch in.verb {
	case "MEMORY":
		b.Memory = value
	case "DISK":
		b.Disk = value
	case "CPUS":
		n, err := strconv.Atoi(value)
		if err != nil {
			return fmt.Errorf("%s:%d: CPUS %q is not a number", file, in.line, value)
		}
		b.CPUs = n
	}
	return nil
}

func (b *Build) apply(pending *Layer, in instruction, file string) error {
	switch in.verb {
	case "RUN":
		script := in.args
		if in.body != "" {
			script = in.body
		}
		if strings.TrimSpace(script) == "" {
			return fmt.Errorf("%s:%d: RUN has no command", file, in.line)
		}
		layer := Layer{
			Copies:  pending.Copies,
			Workdir: pending.Workdir,
			Env:     append([]EnvVar(nil), pending.Env...),
			Args:    append([]Arg(nil), pending.Args...),
			Secrets: append([]string(nil), pending.Secrets...),
			Run:     script,
			Line:    in.line,
		}
		b.Layers = append(b.Layers, layer)
		pending.Copies = nil
		pending.Line = 0
		return nil

	case "COPY":
		copies, err := parseCopy(in.args)
		if err != nil {
			return fmt.Errorf("%s:%d: %w", file, in.line, err)
		}
		copies.Workdir = pending.Workdir
		pending.Copies = append(pending.Copies, copies)
		if pending.Line == 0 {
			pending.Line = in.line
		}
		return nil

	case "WORKDIR":
		dir := strings.TrimSpace(in.args)
		if !strings.HasPrefix(dir, "/") {
			return fmt.Errorf("%s:%d: WORKDIR %q must be absolute", file, in.line, dir)
		}
		pending.Workdir = dir
		return nil

	case "ENV":
		vars, err := parseEnv(in.args)
		if err != nil {
			return fmt.Errorf("%s:%d: %w", file, in.line, err)
		}
		pending.Env = append(pending.Env, vars...)
		return nil

	// A build sees nothing it has not asked for. Forwarding the caller's whole
	// environment would put BITBUCKET_COMMIT in the hash and rebuild every
	// layer on every push, so a Kranqfile names the few values it actually
	// builds differently for.
	case "ARG":
		name, value, err := parseArg(in.args)
		if err != nil {
			return fmt.Errorf("%s:%d: %w", file, in.line, err)
		}
		pending.Args = append(pending.Args, Arg{Name: name, Default: value})
		return nil

	// Same channel, opposite hashing. A credential is not what makes a layer
	// different -- `bundle install` produces the same gems whoever fetched
	// them -- so hashing one would rebuild everything the day it is rotated.
	// The name is hashed, because a RUN that can suddenly see a token may do
	// something else; the value never is, and is never written down.
	case "SECRET":
		name, value, err := parseArg(in.args)
		if err != nil {
			return fmt.Errorf("%s:%d: %w", file, in.line, err)
		}
		if value != "" {
			return fmt.Errorf("%s:%d: SECRET %s cannot have a default; a secret in the Kranqfile is not a secret", file, in.line, name)
		}
		pending.Secrets = append(pending.Secrets, name)
		return nil

	case "MEMORY", "DISK", "CPUS":
		return b.size(in, file)
	}
	return fmt.Errorf("%s:%d: unknown instruction %s", file, in.line, in.verb)
}
