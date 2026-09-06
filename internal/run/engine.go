package run

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"

	"github.com/simon-em/kranq/internal/image"
	"github.com/simon-em/kranq/internal/project"
	"github.com/simon-em/kranq/internal/vm"
)

const (
	WorkDir      = "work"
	ArtifactsDir = "ci-artifacts"
)

type KeepPolicy string

const (
	KeepNever     KeepPolicy = "never"
	KeepOnFailure KeepPolicy = "on-failure"
	KeepAlways    KeepPolicy = "always"
)

func ParseKeep(v string) (KeepPolicy, bool) {
	switch KeepPolicy(v) {
	case "", KeepNever:
		return KeepNever, true
	case KeepOnFailure:
		return KeepOnFailure, true
	case KeepAlways:
		return KeepAlways, true
	}
	return KeepNever, false
}

func (k KeepPolicy) keeps(exitCode int, failed bool) bool {
	switch k {
	case KeepAlways:
		return true
	case KeepOnFailure:
		return failed || exitCode != 0
	}
	return false
}

type Request struct {
	TaskID      string
	Repo        string
	Ref         string
	Label       string
	Script      string
	Env         map[string]string
	Checkout    string
	ArtifactDir string
	RemoteBase  string
	Keep        KeepPolicy
	Fence       *FencePlan
	Source      Source
	Kranqfile   string
	// Where the run's own commit is recorded, so whoever pushed can fetch it.
	ResultRepo string
}

type Result struct {
	ExitCode  int
	VMName    string
	Image     string
	Artifacts bool
	ResultRef string
	Kept      bool
	FenceRef  string
	FenceHeld bool
}

type Engine struct {
	Driver vm.Driver
	Images *image.Manager
	Assets map[string][]byte
}

func (e *Engine) Execute(ctx context.Context, req Request, out io.Writer) (res Result, err error) {

	proj, err := project.Load(req.Checkout, req.Kranqfile)
	if err != nil {
		return res, err
	}

	// Claimed before the image work rather than at first push, so a collision
	// costs seconds instead of surfacing forty minutes into a run.
	remote := Remote{Base: req.RemoteBase, Repo: req.Repo, Token: ResolveToken(req.Env)}
	held, err := e.claimFence(ctx, req, remote, out)
	if err != nil {
		return res, err
	}
	defer func() { e.settleFence(context.WithoutCancel(ctx), held, &res, out) }()

	plan, err := e.Images.Ensure(ctx, req.Repo, proj, out)
	if err != nil {
		return res, err
	}
	res.Image = plan.Image()

	name := image.RunName(req.Repo, req.Label, req.TaskID)
	res.VMName = name
	fmt.Fprintf(out, "cloning %s into %s\n", plan.Image(), name)
	if err := e.Driver.Clone(ctx, plan.Image(), name, image.Sized(proj)); err != nil {
		return res, fmt.Errorf("cloning the project image: %w", err)
	}
	failed := true
	defer func() {
		if req.Keep.keeps(res.ExitCode, failed) {
			res.Kept = true
			fmt.Fprintf(out, "keeping %s for inspection; `kranq vm shell %s` to open it, "+
				"`kranq vm rm %s` when done\n", name, req.TaskID, name)
			return
		}
		fmt.Fprintf(out, "destroying %s\n", name)
		if err := e.Images.Destroy(context.WithoutCancel(ctx), name); err != nil {
			fmt.Fprintf(out, "warning: could not destroy %s: %v\n", name, err)
		}
	}()

	if err := e.Driver.Start(ctx, name); err != nil {
		return res, fmt.Errorf("starting %s: %w", name, err)
	}
	if err := e.upload(ctx, name, req); err != nil {
		return res, err
	}

	work := `"$HOME/` + WorkDir + `"`
	clone := remote.CloneCommand(req.Ref, work)
	if req.Source.Pushed() {
		clone = req.Source.CloneCommand(work, remote)
	}
	script := fmt.Sprintf("set -euo pipefail\nrm -rf %s\n%s\ncd %s\nexec bash /tmp/kranq-task.sh\n",
		work, clone, work)

	code, err := e.Driver.Shell(ctx, name, e.exports(req, held)+script, out)
	if err != nil {
		return res, err
	}
	res.ExitCode = code
	failed = false
	res.Artifacts = e.collect(ctx, name, req, out)
	// Only on the pushed path: an incremental bundle needs its base on the
	// other side, and a clone from the git host leaves nothing there to build
	// on.
	if req.Source.Pushed() && req.ResultRepo != "" {
		res.ResultRef = e.commitResult(ctx, name, req, out)
	}
	return res, nil
}

func (e *Engine) exports(req Request, held *heldFence) string {
	env := map[string]string{}
	for k, v := range req.Env {
		env[k] = v
	}
	if held != nil {
		for k, v := range held.env(req.TaskID) {
			env[k] = v
		}
	}
	keys := make([]string, 0, len(env))
	for k := range env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b []byte
	for _, k := range keys {
		b = append(b, fmt.Sprintf("export %s=%s\n", k, shellQuote(env[k]))...)
	}
	if token := ResolveToken(req.Env); token != "" {
		b = append(b, fmt.Sprintf("export KRANQ_GIT_TOKEN=%s\n", shellQuote(token))...)
	}
	return string(b)
}

func (e *Engine) upload(ctx context.Context, name string, req Request) error {
	dir, err := os.MkdirTemp("", "kranq-upload-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)

	taskPath := filepath.Join(dir, "kranq-task.sh")
	if err := os.WriteFile(taskPath, []byte(req.Script), 0o700); err != nil {
		return err
	}
	if err := e.Driver.CopyIn(ctx, name, taskPath, "/tmp/kranq-task.sh", false); err != nil {
		return fmt.Errorf("uploading the task script: %w", err)
	}
	if req.Source.Pushed() {
		if err := e.Driver.CopyIn(ctx, name, req.Source.Bare, GuestSource, true); err != nil {
			return fmt.Errorf("uploading the pushed source: %w", err)
		}
	}

	if len(e.Assets) == 0 {
		return nil
	}
	assetDir := filepath.Join(dir, "assets")
	if err := os.MkdirAll(assetDir, 0o755); err != nil {
		return err
	}
	for name, body := range e.Assets {
		if err := os.WriteFile(filepath.Join(assetDir, name), body, 0o755); err != nil {
			return err
		}
	}
	if _, err := e.Driver.Shell(ctx, name, "mkdir -p /opt/ci/mcp", io.Discard); err != nil {
		return err
	}
	return e.Driver.CopyIn(ctx, name, assetDir+"/.", "/opt/ci/mcp/", true)
}

func (e *Engine) collect(ctx context.Context, name string, req Request, out io.Writer) bool {
	if req.ArtifactDir == "" {
		return false
	}
	guest := "~/" + WorkDir + "/" + ArtifactsDir
	probe := `test -d "$HOME/` + WorkDir + "/" + ArtifactsDir + `"`
	if code, err := e.Driver.Shell(ctx, name, probe, io.Discard); err != nil || code != 0 {
		return false
	}
	if err := os.MkdirAll(req.ArtifactDir, 0o755); err != nil {
		fmt.Fprintf(out, "warning: artifact directory: %v\n", err)
		return false
	}
	if err := e.Driver.CopyOut(ctx, name, guest+"/.", req.ArtifactDir+"/", true); err != nil {
		fmt.Fprintf(out, "warning: artifacts were produced but could not be copied: %v\n", err)
		return false
	}
	fmt.Fprintf(out, "artifacts: %s\n", req.ArtifactDir)
	return true
}
