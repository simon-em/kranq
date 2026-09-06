package cli

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/effetmonstre/forge/assets"
	"github.com/effetmonstre/forge/internal/exitcode"
	"github.com/effetmonstre/forge/internal/image"
	"github.com/effetmonstre/forge/internal/project"
	"github.com/effetmonstre/forge/internal/task"
)

func runValidate(env Env, args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(env.Stderr, "usage: forge validate <task.yaml|Forgefile>...")
		return exitcode.Usage
	}
	worst := exitcode.OK
	for _, path := range args {
		if isBuildFile(path) {
			worst = max(worst, validateBuild(env, path))
			continue
		}
		spec, code, err := loadSpec(path)
		if err != nil {
			fmt.Fprintf(env.Stderr, "%s: %v\n", path, err)
			worst = max(worst, code)
			continue
		}
		if err := checkBash(task.BuildScript(spec, nil)); err != nil {
			fmt.Fprintf(env.Stderr, "%s: %v\n", path, err)
			worst = max(worst, exitcode.InvalidSpec)
			continue
		}
		fmt.Fprintf(env.Stdout, "%s: ok (%d steps, claude=%v, memory=%s)\n",
			path, len(spec.Steps), spec.NeedsClaude(), memoryOf(spec))
	}
	return worst
}

func runRender(env Env, args []string) int {
	if len(args) != 1 {
		fmt.Fprintln(env.Stderr, "usage: forge render <task.yaml>")
		return exitcode.Usage
	}
	spec, code, err := loadSpec(args[0])
	if err != nil {
		fmt.Fprintf(env.Stderr, "%s: %v\n", args[0], err)
		return code
	}
	fmt.Fprint(env.Stdout, task.BuildScript(spec, nil))
	return exitcode.OK
}

// A task is yaml and a Forgefile is not, so the two never need telling apart by
// content.
func isBuildFile(path string) bool {
	return filepath.Base(path) == project.DefaultFile ||
		strings.HasPrefix(filepath.Base(path), project.DefaultFile+".")
}

// Copy patterns are relative to the repository root, so validation has to run
// from it; a file named by a path below the root is still resolved against it.
func validateBuild(env Env, path string) int {
	root, err := os.Getwd()
	if err != nil {
		fmt.Fprintf(env.Stderr, "%s: %v\n", path, err)
		return exitcode.InternalError
	}
	rel, err := filepath.Rel(root, absOr(root, path))
	if err != nil || strings.HasPrefix(rel, "..") {
		fmt.Fprintf(env.Stderr, "%s: run forge validate from the repository root\n", path)
		return exitcode.Usage
	}
	p, err := project.Load(root, rel)
	if err != nil {
		fmt.Fprintf(env.Stderr, "%s: %v\n", path, err)
		return exitcode.InvalidSpec
	}
	base := image.BaseName(assets.LimaTemplate, time.Now(), image.DefaultTTL)
	chain := image.Chain(base, p.Build.Disk, p.Layers)
	w := tabwriter.NewWriter(env.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "LINE\tINSTRUCTION\tFILES\tIMAGE")
	for i, l := range p.Layers {
		fmt.Fprintf(w, "%d\t%s\t%d\t%s\n", l.Line, l.Summary(), len(l.Files), chain[i+1])
	}
	return flushed(w)
}

func absOr(root, path string) string {
	if filepath.IsAbs(path) {
		return path
	}
	return filepath.Join(root, path)
}

func loadSpec(path string) (task.Spec, int, error) {
	data, code, err := readSpecFile(path)
	if err != nil {
		return task.Spec{}, code, err
	}
	spec, err := task.Parse(data)
	if err != nil {
		return task.Spec{}, exitcode.InvalidSpec, err
	}
	return spec, exitcode.OK, nil
}

func checkBash(script string) error {
	cmd := exec.Command("bash", "-n")
	cmd.Stdin = strings.NewReader(script)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("the compiled script is not valid bash: %s", trimForDisplay(string(out)))
	}
	return nil
}

func memoryOf(s task.Spec) string {
	if s.Resources.Memory == "" {
		return "default"
	}
	return s.Resources.Memory
}
