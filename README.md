# kranq

One binary that runs CI jobs in disposable [Lima](https://lima-vm.io) VMs on a
macOS build machine. It installs itself, installs its own dependencies, keeps
state in a local daemon, receives work over ssh or a git push, and exposes all of
it through a CLI.

It replaces `ci-runner` and the two bash clients in `infrastructure/ci/`.

```sh
kranq install                                    # binary, PATH, lima, launchd
kranq run ci/tasks/spec.yaml --repo dx --branch main
kranq push ci/tasks/spec.yaml --repo dx          # send this repo, run it there
kranq doctor                                     # is this machine able to run jobs
```

**[Every command](docs/commands.md)** · [Writing a task](docs/tasks.md) ·
[The Kranqfile](docs/build.md) ·
[Pushing work to kranq](docs/push.md) · [Wiring it into a pipeline](docs/pipelines.md) ·
[At-most-once effects](docs/fence.md) · [Operating it](docs/operations.md) ·
[Installing with Homebrew](docs/homebrew.md) ·
[Where things stand](docs/status.md)

## Why a VM per job

Concurrent CI jobs on one machine collide on ports, Compose project names and
working trees. The usual fix is to allocate all three per job, which is fiddly
and has to be redone for every project.

A VM has its own network namespace, so none of it is shared. Each job uses
whatever ports the project normally uses, `docker compose up` works unmodified,
and two jobs cannot see each other. Cloning is nearly free: `limactl clone` is an
APFS copy-on-write clone.

Measured on a 16 GiB M-series Mac: the shared base image builds in 153s, dx's
layers in 289s, and a job against a warm image runs in about 20s.

## The environment is a Kranqfile

A repository's `Kranqfile` reads like a Dockerfile, and each `RUN` is a layer:

```
RUN sudo apt-get install -y --no-install-recommends default-jdk libvips

COPY Gemfile Gemfile.lock .
RUN bundle install
```

A layer is named by a hash of its parent, its command and the **contents** of the
files it copies — and by nothing else. No repository, no branch. So editing a
lockfile rebuilds the tail and nothing before it, and two projects installing the
same packages share that layer rather than each building it.

Layers are diffs, not copies: `limactl clone` is an APFS copy-on-write clone, so
a layer is charged only for the blocks it writes. Measured on the 2.9 GB base,
cloning it costs 0.10s and zero bytes. See [build.md](docs/build.md).

## Two ways to get work to it

**Clone.** The VM fetches the repository from the git host, using a forwarded
token or ssh agent. This is what a pipeline that already has credentials does.

**Push.** You send the code to kranq and it already has it:

```sh
git remote add kranq ssh://macmini@buildhost:333/dx.git
git config remote.kranq.receivepack '$HOME/.local/bin/kranq git-receive'
git push kranq main:refs/heads/run -o task=ci/tasks/spec.yaml
```

If your ssh key already reaches the machine, that is the whole setup: the
repository is created on the first push, and no kranq-specific key is involved.

The build log streams back to your terminal as it runs. Nothing is cloned, so
nothing needs a credential to read the code, and a commit that exists nowhere
else still runs. See [push.md](docs/push.md).

## What it guarantees

**A job outlives its daemon.** Each job runs as its own process group and records
its result in the task directory. Kill the daemon mid-run and the job keeps
going; the daemon that comes back re-adopts it and reports what happened.

**A lost job never silently runs twice.** There is no path from `running` back to
`queued`. A task whose executor is gone becomes `lost`, and re-running it is a
human decision under a new task id.

**An effect lands at most once.** A task declaring `effects.push` holds a fence
at the git host, and its branch push is atomic with advancing that fence, so a
partitioned attempt cannot open a second pull request. What that does and does
not cover is set out in [fence.md](docs/fence.md).

**Claude usage exhaustion is not a failure.** The task is held and resumes when
usage returns. Rotating the token opens the gate immediately.

## Install

```sh
brew tap simon-em/kranq https://github.com/simon-em/kranq
brew install simon-em/kranq/kranq
```

That is all of it: lima is fetched and verified the first time a command needs a
VM.

See [homebrew.md](docs/homebrew.md). Or from a binary you already have:

```sh
./kranq install --with-daemon
kranq auth claude --stdin < token.txt
kranq doctor
```

`install` verifies Lima against a checksum compiled into the binary *and* the
published `SHA256SUMS`, which must agree, then keeps it under `$KRANQ_HOME/deps`
and calls it by absolute path. A Homebrew lima appearing or disappearing cannot
change what runs.

To set up another build machine from your laptop:

```sh
kranq peer add mini-1 --ssh macmini@buildhost:333 --default
kranq peer upgrade mini-1
kranq peer test mini-1
```

## Layout

```
main.go              os.Exit(cli.Main(os.Args))
assets/              lima.yaml + mcp/*.py, embedded in the binary

internal/cli/        subcommand dispatch and terminal output
internal/task/       the task schema, and compiling a spec to one bash script
internal/svc/        the pure domain API every entry point goes through
internal/state/      the task store
internal/sched/      admission, re-adoption, the lost-task rule
internal/daemon/     the daemon, the job supervisor, the git endpoint
internal/jobproc/    the handover between a job process and the daemon
internal/ipc/        http over a unix socket
internal/run/        one job end to end, including the pushed-source path
internal/project/    the Kranqfile: parsing it, and resolving what COPY selects
internal/image/      content-addressed layers, the chain cache, build, prune
internal/vm/         the limactl driver, behind an interface with a fake
internal/gitsrv/     receiving a git push over http and over ssh
internal/authkeys/   forced-command entries in ~/.ssh/authorized_keys
internal/token/      named tokens, stored only as hashes
internal/fence/      at-most-once effects, as a compare-and-swap at the git host
internal/gate/       the Claude usage gate, persisted and keyed by token
internal/peer/       other build machines
internal/upgrade/    replacing the binary, and going back
internal/selfinstall/ install, PATH, launchd
internal/deps/       fetching and verifying lima
internal/doctor/     the checks, as pure functions
internal/hostres/    memory, cpu and disk probes
internal/sockpath/   the macOS 104-byte unix socket limit
internal/exitcode/   the exit code contract
```

About 9,900 lines of Go and 354 tests. `gopkg.in/yaml.v3` is the only dependency.

## Testing

`go test ./...` needs no VM and no network. The `limactl` surface sits behind an
interface a fake satisfies, the fence and the git endpoint run against real local
bare repositories, and the ssh path is driven by a fake `ssh` on PATH.

What that cannot cover is run against real hardware, and
[status.md](docs/status.md) records which of those have actually been done.

## Things that will bite you

**macOS caps a unix socket path at 104 bytes.** This has cost time three
separate ways: the ssh-agent socket, the daemon socket, and Lima instance names.
`doctor` checks it.

**Non-login ssh on macOS gets a minimal PATH.** Nothing kranq runs on a peer
relies on PATH; everything uses an absolute path.

**An unforced `git push` is not a compare-and-swap.** It accepts any
fast-forward, and accepts a create unconditionally. Fence updates use
`--force-with-lease` with an exact expected object. See [fence.md](docs/fence.md).

More of these, with what they cost to discover, are in `CLAUDE.md`.
