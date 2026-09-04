# forge

One binary that runs CI jobs in disposable [Lima](https://lima-vm.io) VMs on a macOS build
machine. It installs itself, installs its own dependencies, keeps state in a local daemon,
reaches other build machines over ssh without opening a port anywhere, and exposes all of it
through a CLI.

It replaces `ci-runner` plus the two bash clients in `infrastructure/ci/`.

## Status

It runs real jobs. There is no daemon or queue yet, so a run happens in the foreground of the
CLI process. See [docs/status.md](docs/status.md).

```sh
forge run <spec.yaml> --repo dx --branch ci/lima --artifacts ./out
forge image ls|build|prune
forge validate|render|version
```

Measured on a 16 GiB M-series Mac: a base image builds in 153s, dx's project layer in 289s,
and a job against the warm image runs in 17s, because `limactl clone` is a copy-on-write
clone.

## Layout

```
main.go                  os.Exit(cli.Main(os.Args))
assets/                  lima.yaml + mcp/*.py, embedded in the binary
internal/cli/            subcommand dispatch and terminal output
internal/task/           the task schema, and compiling a spec to one bash script
internal/project/        ci/setup.yaml and ci/basekey.txt
internal/image/          content-addressed naming, the two-layer cache, build, prune
internal/vm/             the limactl driver, behind an interface with a fake
internal/run/            one job end to end
internal/sshagent/       ssh agent handling, when no token was forwarded
internal/sockpath/       the macOS 104-byte unix socket limit
internal/exitcode/       the exit code contract
```

## Two image layers

The **base** is the same for every project: Debian 13, Docker, libvips, overmind, the Claude
CLI. It is rebuilt when `assets/lima.yaml` changes or when it is older than the TTL, which is
folded into the image name so an expiry invalidates everything built on it.

The **project layer** is the repo's `ci/setup.yaml` run once on a clone of the base. This is
where language versions live, because that is what actually differs between projects. It is
keyed by the base name, the setup script, and every file listed in `ci/basekey.txt`, so
changing a lockfile rebuilds just that layer and changing nothing costs nothing.

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
