# kranq

One binary that runs CI jobs in disposable [Lima](https://lima-vm.io) VMs on a
macOS build machine. A pipeline pushes its code with git and reads the result
with git; there is nothing to install on the client.

```yaml
- source infrastructure/ci/kranq-ci
- git push kranq -o task_file=ci/tasks/spec.yaml "${KRANQ_OPTS[@]}"
- git pull --ff-only kranq "$KRANQ_TASK"   # what the run produced
- git fetch kranq "$KRANQ_OK"              # missing if the run failed
```

## Set it up

On the build machine:

```sh
brew tap simon-em/kranq https://github.com/simon-em/kranq
brew install simon-em/kranq/kranq
kranq setup-git ci-dx --host 142.127.69.2:333 --repo dx
```

The tap needs the URL because the repository is not named `homebrew-kranq`.
Lima is fetched and verified the first time a command needs a VM.

`setup-git` authorises an ssh key and prints what the pipeline needs:

```
KRANQ_PEER=macmini@142.127.69.2:333
KRANQ_HOST_KEY=[142.127.69.2]:333 ssh-ed25519 AAAAC3Nz…
KRANQ_SSH_KEY<<EOF
-----BEGIN OPENSSH PRIVATE KEY-----
…
EOF
```

`KRANQ_PEER` is the only one that must be set; the other two are for a caller
with no ssh identity of its own. Variables go to stdout and everything else to
stderr, so `kranq setup-git … > vars.env` is a file. The private key is printed
once and kept nowhere — run it again to rotate.

That key is a forced command: it can push and fetch and nothing else, and it is
what lets the client be a plain URL with no git configuration.

```
$ ssh -i ci-dx macmini@142.127.69.2 -p 333 id
kranq: "id" is not a git command
```

## How a run works

A repository's `Kranqfile` describes the environment, and reads like a
Dockerfile:

```
RUN sudo apt-get install -y --no-install-recommends default-jdk libvips

COPY Gemfile Gemfile.lock .
RUN bundle install
```

Each `RUN` is a layer, named by a hash of its parent, its command and the
**contents** of the files it copies — no repository, no branch. Editing a
lockfile rebuilds the tail and nothing before it, and two projects installing
the same packages share that layer instead of each building it.

A push runs the task against exactly what was sent. Nothing is cloned, so
nothing needs a credential to read the code, and a commit that exists nowhere
else still runs. The log streams back to the pushing terminal as it goes.

**A run is a branch.** The name you push to is the name you pull from: when the
job finishes, `task/<run>` points at a commit whose parent is the commit you
pushed. Pulling it brings back files the job changed and whatever it wrote into
its `artifacts:` path, at the paths it wrote them — so a report ends up where
the tool that made it already put it, and there is nothing to unpack.

**The verdict is a second ref.** A push exits 0 whenever it was accepted,
whatever the task did, so `ok/<run>` exists only if the task succeeded and
`git fetch` of a missing ref exits 128. It is separate from the run's own ref
because a report is worth having precisely when the run failed.

## Watching it

```sh
kranq status        # queue, machine and claude state
kranq ps            # tasks
kranq logs <id> -f  # follow one
kranq doctor        # can this machine run jobs at all
```

From a laptop, against another build machine:

```sh
kranq peer add mini-1 --ssh macmini@142.127.69.2:333 --default
kranq peer upgrade mini-1
kranq peer test mini-1
```

`kranq help --all` lists everything else.

## Documentation

[Wiring it into a pipeline](docs/pipelines.md) ·
[Writing a task](docs/tasks.md) ·
[The Kranqfile](docs/build.md) ·
[Every command](docs/commands.md)

Deeper, in [docs/advanced](docs/advanced):
[pushing work to kranq](docs/advanced/push.md) ·
[at-most-once effects](docs/advanced/fence.md) ·
[operating it](docs/advanced/operations.md) ·
[installing with Homebrew](docs/advanced/homebrew.md) ·
[testing](docs/advanced/testing.md) ·
[killing a job](docs/advanced/teardown.md) ·
[where things stand](docs/advanced/status.md)

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
internal/run/        one job end to end, including the pushed-source path
internal/project/    the Kranqfile: parsing it, and resolving what COPY selects
internal/image/      content-addressed layers, the chain cache, build, prune
internal/vm/         the limactl driver, behind an interface with a fake
internal/gitsrv/     receiving a git push over http and over ssh
internal/fence/      at-most-once effects, as a compare-and-swap at the git host
internal/peer/       other build machines
```

The rest — `jobproc ipc authkeys token gate upgrade selfinstall deps doctor
hostres sockpath exitcode` — is named for what it does. About 12,000 lines of
Go and 443 tests; `gopkg.in/yaml.v3` is the only dependency.

`go test ./...` needs no VM and no network: the `limactl` surface sits behind an
interface a fake satisfies, the fence and the git endpoint run against real
local bare repositories, and the ssh path is driven by a fake `ssh` on PATH.

Things that have cost time to discover are in `CLAUDE.md`.
