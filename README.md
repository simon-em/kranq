# forge

One binary that runs CI jobs in disposable [Lima](https://lima-vm.io) VMs on a macOS build
machine. It installs itself, installs its own dependencies, keeps state in a local daemon,
reaches other build machines over ssh without opening a port anywhere, and exposes all of it
through a CLI.

It replaces `ci-runner` plus the two bash clients in `infrastructure/ci/`.

## Status

Phase 0. The pure core is in place and the CLI can parse, validate and compile a task spec.
Nothing runs a job yet.

```sh
forge validate ci/tasks/maintenance.yaml
forge render   ci/tasks/maintenance.yaml    # the bash a spec compiles to
forge version
```

`render` is byte-identical to `ci-runner`'s `ci-task-validate --script`, which is how the
port of `internal/task` was verified against the real maintenance task.

## Layout

```
main.go                  os.Exit(cli.Main(os.Args))
internal/cli/            subcommand dispatch and terminal output
internal/task/           the task schema, and compiling a spec to one bash script
internal/exitcode/       the exit code contract
```

## The exit code contract

A caller has to be able to tell an infrastructure failure from a test failure, which the
system this replaces could not do: it exited 1 for both. So 64 to 127 are reserved for forge
and a task's own exit code passes through below that.

| Code | Meaning |
| --- | --- |
| 0 | the task reached a passing terminal status |
| 1-63 | the task's own exit code, verbatim |
| 64 | usage error |
| 65 | the spec is invalid |
| 66 | an input file does not exist |
| 69 | the peer or daemon is unreachable |
| 75 | the task was never admitted before its queue deadline |
| 77 | authentication or scope denied |
| 124 / 125 | timed out / cancelled |
| 126 / 127 | could not start / a dependency is missing |

A task exit code that would collide with the reserved band is reported as 1, with the true
value carried in the result record.

Stdout is data, stderr is narration. Every command follows this, so output can be piped.

## One property worth not breaking

A spec's steps compile into **one** bash script, so an `export` in step 1 is visible in step
3. Real tasks depend on this: `maintenance.yaml` sets `PR_BRANCH` in its first step and reads
it in its last. `internal/task` has a test asserting it, so a refactor that isolates steps
fails loudly rather than in production.

## Build

```sh
go test ./...
go build -ldflags "-X github.com/effetmonstre/forge/internal/cli.Version=$(git describe --tags --always)" -o forge .
```
