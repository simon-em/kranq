package cli

import (
	"fmt"
	"os/exec"
	"strings"

	"github.com/effetmonstre/forge/internal/exitcode"
	"github.com/effetmonstre/forge/internal/task"
)

func runValidate(env Env, args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(env.Stderr, "usage: forge validate <task.yaml>...")
		return exitcode.Usage
	}
	worst := exitcode.OK
	for _, path := range args {
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
