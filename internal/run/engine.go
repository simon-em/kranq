package run

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"

	"github.com/effetmonstre/forge/internal/image"
	"github.com/effetmonstre/forge/internal/project"
	"github.com/effetmonstre/forge/internal/vm"
)

const (
	WorkDir      = "work"
	ArtifactsDir = "ci-artifacts"
)

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
}

type Result struct {
	ExitCode  int
	VMName    string
	Image     string
	Artifacts bool
}

type Engine struct {
	Driver vm.Driver
	Images *image.Manager
	Assets map[string][]byte
}

func (e *Engine) Execute(ctx context.Context, req Request, out io.Writer) (Result, error) {
	var res Result

	proj, err := project.Load(req.Checkout)
	if err != nil {
		return res, err
	}

	plan, err := e.Images.Ensure(ctx, req.Repo, req.Ref, proj, out)
	if err != nil {
		return res, err
	}
	res.Image = plan.Project

	name := image.RunName(req.Repo, req.Label, req.TaskID)
	res.VMName = name
	fmt.Fprintf(out, "cloning %s into %s\n", plan.Project, name)
	if err := e.Driver.Clone(ctx, plan.Project, name, vm.Resources{}); err != nil {
		return res, fmt.Errorf("cloning the project image: %w", err)
	}
	defer func() {
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

	remote := Remote{Base: req.RemoteBase, Repo: req.Repo, Token: ResolveToken(req.Env)}
	script := fmt.Sprintf("set -euo pipefail\nrm -rf ~/%s\n%s\ncd ~/%s\nexec bash /tmp/forge-task.sh\n",
		WorkDir, remote.CloneCommand(req.Ref, "~/"+WorkDir), WorkDir)

	code, err := e.Driver.Shell(ctx, name, e.exports(req)+script, out)
	if err != nil {
		return res, err
	}
	res.ExitCode = code
	res.Artifacts = e.collect(ctx, name, req, out)
	return res, nil
}

func (e *Engine) exports(req Request) string {
	keys := make([]string, 0, len(req.Env))
	for k := range req.Env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b []byte
	for _, k := range keys {
		b = append(b, fmt.Sprintf("export %s=%s\n", k, shellQuote(req.Env[k]))...)
	}
	if token := ResolveToken(req.Env); token != "" {
		b = append(b, fmt.Sprintf("export FORGE_GIT_TOKEN=%s\n", shellQuote(token))...)
	}
	return string(b)
}

func (e *Engine) upload(ctx context.Context, name string, req Request) error {
	dir, err := os.MkdirTemp("", "forge-upload-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)

	taskPath := filepath.Join(dir, "forge-task.sh")
	if err := os.WriteFile(taskPath, []byte(req.Script), 0o700); err != nil {
		return err
	}
	if err := e.Driver.CopyIn(ctx, name, taskPath, "/tmp/forge-task.sh", false); err != nil {
		return fmt.Errorf("uploading the task script: %w", err)
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
	if code, err := e.Driver.Shell(ctx, name, "test -d "+guest, io.Discard); err != nil || code != 0 {
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
