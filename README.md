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
kranq setup
```

The tap needs the URL because the repository is not named `homebrew-kranq`.
`setup` installs lima and starts the daemon. That is the machine done.

## Letting something push to it

`setup` leaves one repository that takes every codebase, at a path any key
already in `authorized_keys` can reach:

```
KRANQ_URL=ssh://macmini@142.127.69.2:333/~/kranq.git
```

That is the whole of the client's configuration. No forced command, no
`receivepack` override, no new credential, and nothing to create per project —
pushing to a **real path** runs stock `git-receive-pack`, and the hooks inside
the repository are kranq.

**One repository is enough because a layer's name does not contain one.** A
layer is a hash of its parent, its command and the contents of what it copies,
so two projects with the same Kranqfile steps already share layers wherever
they arrive.

**Most runs need no name at all.** A push carries the code, so the commit
already says what ran, and that is what `kranq ps` shows. A name is only an
address at the git host, so kranq asks for one — `-o repo=<name>` — in the two
cases that go there: a task holding a fence, and a task that arrived with no
commit and has to clone. Anything else is identified by what it is.

On port 22 the short form works too, since git resolves a bare name by
appending `.git`:

```sh
git remote add kranq macmini@142.127.69.2:kranq
```

Not on any other port: scp-style syntax has no field for one, and git reads
`host:333:kranq` as a path called `333:kranq`. Use the `ssh://` form there.

A key that should be able to push and nothing else — no shell, no agent
forwarding — can have one issued instead. That is
[`kranq setup <name>`](docs/commands.md#kranq-setup-name---peer-name), and
nothing here needs it: a key already in `authorized_keys` is enough.

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
kranq peer upgrade mini-1   # install or replace kranq there
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
