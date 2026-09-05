package cli

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"text/tabwriter"

	"github.com/effetmonstre/forge/internal/deps"
	"github.com/effetmonstre/forge/internal/doctor"
	"github.com/effetmonstre/forge/internal/exitcode"
	"github.com/effetmonstre/forge/internal/hostres"
	"github.com/effetmonstre/forge/internal/image"
	"github.com/effetmonstre/forge/internal/selfinstall"
	"github.com/effetmonstre/forge/internal/state"
)

// One image pair, base plus project layer, measured at about 24GiB on the mini.
const imagePairBytes = 24 << 30

func runDoctor(env Env, args []string) int {
	fs := flag.NewFlagSet("doctor", flag.ContinueOnError)
	fs.SetOutput(env.Stderr)
	asJSON := fs.Bool("json", false, "emit the checks as json")
	if err := fs.Parse(args); err != nil {
		return exitcode.Usage
	}

	checks := collect(context.Background())
	if *asJSON {
		body, err := json.MarshalIndent(map[string]any{
			"level": doctor.Worst(checks).String(), "checks": checks,
		}, "", "  ")
		if err != nil {
			return exitcode.InternalError
		}
		fmt.Fprintln(env.Stdout, string(body))
	} else {
		printChecks(env, checks)
	}
	if doctor.Worst(checks) == doctor.Fail {
		return exitcode.Misconfigured
	}
	return exitcode.OK
}

func printChecks(env Env, checks []doctor.Check) {
	w := tabwriter.NewWriter(env.Stdout, 0, 0, 2, ' ', 0)
	for _, c := range checks {
		mark := map[doctor.Level]string{doctor.OK: "ok", doctor.Warn: "WARN", doctor.Fail: "FAIL"}[c.Level]
		fmt.Fprintf(w, "%s\t%s\t%s\n", mark, c.Name, c.Detail)
	}
	w.Flush()
	for _, c := range checks {
		if c.Level != doctor.OK && c.Fix != "" {
			fmt.Fprintf(env.Stderr, "\n%s: %s", c.Name, c.Fix)
		}
	}
	fmt.Fprintln(env.Stderr)
}

func collect(ctx context.Context) []doctor.Check {
	home := forgeHome()
	cfg := daemonConfig()
	checks := []doctor.Check{
		binaryCheck(),
		homeCheck(home),
		doctor.SocketPath(cfg.SocketPath()),
		envFileCheck(home),
		gitCheck(ctx),
		limaCheck(ctx, home),
		doctor.DiskSpace(hostres.FreeDisk(home), imagePairBytes),
		claudeCheck(cfg.ClaudeToken),
	}
	daemonVersion, owned, reachable := daemonState(ctx)
	checks = append(checks, doctor.VersionMatch(Version, daemonVersion))
	// Without a daemon there is nothing to own a VM, so every VM would look
	// orphaned. That is a false alarm, not a finding.
	if reachable {
		if vms, err := jobVMs(ctx); err == nil {
			checks = append(checks, doctor.OrphanVMs(vms, owned))
		}
	}
	return checks
}

// What matters is whether the binary being run is reachable by name, not
// whether it sits in the default prefix: --prefix is supported and a custom one
// is not a problem.
func binaryCheck() doctor.Check {
	self, err := os.Executable()
	if err != nil {
		return doctor.Check{Name: "forge", Level: doctor.Warn, Detail: "could not locate the running binary"}
	}
	if resolved, err := filepath.EvalSymlinks(self); err == nil {
		self = resolved
	}
	if selfinstall.OnPath(filepath.Dir(self)) {
		return doctor.Check{Name: "forge", Level: doctor.OK, Detail: fmt.Sprintf("%s at %s", Version, self)}
	}
	fix := "forge install"
	detail := fmt.Sprintf("running %s from %s, which is not on PATH", Version, self)
	if onPath, err := exec.LookPath("forge"); err == nil {
		detail += fmt.Sprintf("; `forge` resolves to %s instead", onPath)
		fix = "forge install, or run the copy already on PATH"
	}
	return doctor.Check{Name: "forge", Level: doctor.Warn, Detail: detail, Fix: fix}
}

func homeCheck(home string) doctor.Check {
	info, err := os.Stat(home)
	if err != nil {
		return doctor.Check{
			Name: "forge home", Level: doctor.Warn, Fix: "it is created on first use",
			Detail: fmt.Sprintf("%s does not exist yet", home),
		}
	}
	return doctor.Permissions("forge home", home, info.Mode(), 0o700)
}

func envFileCheck(home string) doctor.Check {
	path := filepath.Join(home, "env")
	info, err := os.Stat(path)
	if err != nil {
		return doctor.Check{
			Name: "env file", Level: doctor.OK,
			Detail: "none, so nothing is stored on disk",
		}
	}
	return doctor.Permissions("env file", path, info.Mode(), 0o600)
}

func gitCheck(ctx context.Context) doctor.Check {
	out, err := exec.CommandContext(ctx, "git", "--version").Output()
	if err != nil {
		return doctor.Check{
			Name: "git", Level: doctor.Fail, Fix: "install git",
			Detail: fmt.Sprintf("could not run git: %v", err),
		}
	}
	return doctor.GitVersion(string(out))
}

func limaCheck(ctx context.Context, home string) doctor.Check {
	bin := limactlPath(home)
	source := doctor.LimaSource(bin, home)
	if source.Level == doctor.Fail || bin == "" {
		return source
	}
	if err := exec.CommandContext(ctx, bin, "list", "--format", "json").Run(); err != nil {
		return doctor.Check{
			Name: "lima", Level: doctor.Fail, Fix: "forge install",
			Detail: fmt.Sprintf("%s cannot list instances: %v", bin, err),
		}
	}
	if source.Level != doctor.OK {
		return source
	}
	return doctor.Check{Name: "lima", Level: doctor.OK, Detail: deps.LimaVersion + " at " + bin}
}

func claudeCheck(token string) doctor.Check {
	if token == "" {
		return doctor.Check{
			Name: "claude token", Level: doctor.Warn, Fix: "forge auth claude --stdin",
			Detail: "absent, so tasks with a claude step will be refused",
		}
	}
	return doctor.Check{Name: "claude token", Level: doctor.OK, Detail: "present"}
}

func daemonState(ctx context.Context) (version string, ownedVMs []string, reachable bool) {
	client, code := connect(Env{Stdout: os.Stdout, Stderr: devNull{}}, false)
	if code != exitcode.OK || client == nil {
		return "", nil, false
	}
	reachable = true
	if s, err := client.Status(ctx); err == nil {
		version = s.Version
	}
	tasks, err := client.List(ctx)
	if err != nil {
		return version, nil, reachable
	}
	for _, t := range tasks {
		if t.VMName != "" && (t.Status == state.StatusRunning || t.VMKept) {
			ownedVMs = append(ownedVMs, t.VMName)
		}
	}
	return version, ownedVMs, reachable
}

func jobVMs(ctx context.Context) ([]string, error) {
	_, driver := newManager()
	all, err := driver.List(ctx)
	if err != nil {
		return nil, err
	}
	var names []string
	for _, i := range all {
		if strings.HasPrefix(i.Name, image.RunPrefix+"-") {
			names = append(names, i.Name)
		}
	}
	return names, nil
}

type devNull struct{}

func (devNull) Write(p []byte) (int, error) { return len(p), nil }
