# Every command

Global: `--kranq-home` is read from `$KRANQ_HOME`; settings come from the
environment first and `$KRANQ_HOME/env` second (see [configuration](#configuration)).

Commands marked **internal** are run by git or by the daemon, never by hand.

| | |
| --- | --- |
| [Running tasks](#running-tasks) | `run` `push` `ps` `logs` `fetch` `result` `cancel` `validate` `render` |
| [Inspecting a machine](#inspecting-a-machine) | `status` `doctor` `vm` `image` |
| [The daemon](#the-daemon) | `daemon` `config` `auth` |
| [Receiving pushes](#receiving-pushes) | `setup` `repo` `token` `key` |
| [At-most-once effects](#at-most-once-effects) | `fence` |
| [Installing and updating](#installing-and-updating) | `install` `upgrade` `rollback` `uninstall` `version` `help` |
| [Other machines](#other-machines) | `peer` |
| [Internal](#internal) | `exec` `git-hook` `git-receive` |

---

## Running tasks

### `kranq run <task.yaml> [flags]`

Submit a task and follow it to completion. Exits with the task's own exit code.

```sh
kranq run ci/tasks/spec.yaml --repo dx --branch main
kranq run ci/tasks/review.yaml --artifacts ./review-output
id=$(kranq run ci/tasks/spec.yaml --detach) && kranq logs "$id" -f
```

| Flag | Default | Meaning |
| --- | --- | --- |
| `--repo` | `$CI_REPO`, `$BITBUCKET_REPO_SLUG` | repository slug |
| `--branch` | `$CI_BRANCH`, `$BITBUCKET_BRANCH` | branch to check out |
| `--label` | the task name | names the VM and the artifact directory |
| `--artifacts DIR` | none | copy the spec's `artifacts:` path out into `DIR` |
| `--remote` | `$KRANQ_GIT_REMOTE` | git remote base, e.g. `git@bitbucket.org:effetmonstre` |
| `--env NAME=V`, `-e` | | repeatable; bare `-e NAME` forwards it from here |
| `--keep-vm` | `never` | `never`, `on-failure`, `always` |
| `--kranqfile FILE` | `Kranqfile`, or the spec's `kranqfile:` | build file to read from the repository root |
| `--timeout` | `4h` | ceiling on the run |
| `--detach` | off | print the task id and return |
| `--local` | off | run in this process, bypassing the daemon |

`--local` is a break-glass path: no queue, no admission control, no re-adoption.
It exists so the code path is exercised rather than dead.

### `kranq push <task.yaml> [flags]`

Send **this repository** to a kranq machine and run a task against exactly what
was sent. Nothing is cloned from the git host, so no credential is needed to read
the code, and a commit that exists nowhere else still runs. See [push.md](advanced/push.md).

```sh
KRANQ_ENDPOINT=ssh://macmini@host:333 KRANQ_SSH_KEY=~/.ssh/kranq_push \
  kranq push ci/tasks/spec.yaml --repo dx

KRANQ_ENDPOINT=https://ci.example.com KRANQ_TOKEN=kranq_… \
  kranq push ci/tasks/spec.yaml --repo dx -e BITBUCKET_TOKEN
```

| Flag | Default | Meaning |
| --- | --- | --- |
| `--endpoint` | `$KRANQ_ENDPOINT` | `ssh://user@host:port` or `https://host` |
| `--token` | `$KRANQ_TOKEN` | required for https, ignored for ssh |
| `--ssh-key` | `$KRANQ_SSH_KEY` | identity to push with, for ssh |
| `--receive-pack` | `$HOME/.local/bin/kranq` | kranq on the far side, so no kranq-specific key is needed; `""` to rely on a forced command |
| `--repo` | `$KRANQ_REPO`, `$CI_REPO` | name the repository has on the runner |
| `--branch` | `$CI_BRANCH`, `$BITBUCKET_BRANCH` | branch name to report for the run |
| `--rev` | `HEAD` | what to send |
| `--label`, `--keep-vm`, `--env`/`-e`, `--detach` | | as for `run` |

The token is never passed as an argument: it goes in the URL's userinfo, so it
does not appear in the process list. It is also not defaulted into the flag,
because `flag.PrintDefaults` prints defaults and usage is printed on every misuse.

### `kranq ps [-a]`

List tasks. `-a` includes finished ones.

### `kranq logs <id> [-f]`

Print a task's log; `-f` follows until it finishes.

### `kranq cancel <id>...`

Cancel queued or running tasks. A running job is signalled as a **process
group**, then SIGKILLed if it ignores SIGTERM, so its VM goes with it.

### `kranq result <id> [--wait DURATION]`

```sh
kranq result 20260906T170711-9ee94
KRANQ-RESULT id=20260906T170711-9ee94 status=succeeded exit=0
```

Reprints the line the receive hook prints when a run ends, and exits with the
task's own code. It waits if the task is still going.

**A run outlives the connection that started it.** If ssh drops mid-push the job
keeps going on the machine, finishes, and takes its verdict with it — which is
indistinguishable from a failed build unless you ask again. This is how the push
client rejoins: it captures the id from `task <id> queued` and asks over a fresh
connection rather than guessing.

### `kranq fetch <id> [--out DIR]`

```sh
kranq fetch 20260906T030344-d3c16 --out ./report
ssh macmini@buildhost kranq fetch 20260906T030344-d3c16 | tar xzf - -C ./report
```

Without `--out` the tar.gz goes to stdout, which is how a pipeline gets
artifacts back over ssh without installing anything but `tar`. A task that
produced nothing exits 0 and writes nothing, because dx pulls a test report from
a run that failed and that is not itself a failure.

### `kranq validate <task.yaml|Kranqfile>...` and `kranq render <task.yaml>`

Parse a spec, and print the bash script it compiles to. Neither needs a daemon.
`render` is how you see what a task will actually run.

Given a `Kranqfile` it resolves every `COPY` and prints the layer each `RUN`
produces, which is how you see what an edit would rebuild before paying for it:

```
$ kranq validate Kranqfile
LINE  INSTRUCTION                        FILES  IMAGE
4     RUN sudo apt-get update                0  kranq-layer-01-97ab2ab29342
8     RUN ruby-build "$(cat .ruby-versi...   1  kranq-layer-03-0cf0d6d9e047
11    RUN bundle install                     2  kranq-layer-04-2b3a3377b10b
```

`COPY` paths are relative to the repository root, so run it from there.

---

## Inspecting a machine

### `kranq status [--json]`

Queue depth, what the scheduler is blocked on, free memory and CPUs, and the
Claude gate.

### `kranq doctor [--json]`

Checks everything that has to be true for a task to run here: the binary is on
PATH, `$KRANQ_HOME` is `0700`, the socket path fits macOS's 104-byte limit, git
is new enough for `--atomic`, lima is kranq's own copy and can list instances,
there is disk for an image pair, the Claude token is present, the daemon matches
the CLI, and no VM is orphaned. Exits `78` if anything failed.

### `kranq vm ls|shell|rm`

```sh
kranq vm ls
kranq vm shell <task-id|vm-name>            # open a kept VM
kranq vm shell <task-id> -- cat /tmp/x      # or run one command in it
kranq vm rm <vm-name>...  |  kranq vm rm --all
```

Only useful with `--keep-vm`, which is how you get to look at a failed run.

### `kranq image ls|build|prune`

```sh
kranq image ls
kranq image build --repo dx --ref main                        # warm the whole chain
kranq image build --repo dx --ref main --kranqfile Kranqfile.perf
kranq image prune
```

A shared base image, then one layer per `RUN` in the repository's `Kranqfile`.
Each layer is an APFS copy-on-write clone of the one before it, keyed by a hash
of its parent, its command and the contents of the files it copies — so a
lockfile edit rebuilds only the tail, and two repositories doing identical work
share the layer. `kranq image ls` shows which instruction each image came from
and when it was last used. See [build.md](build.md).

---

## The daemon

### `kranq daemon run|start|stop|status`

`run` stays in the foreground; `start` spawns one and waits for it. `stop`
refuses while work is in flight unless `--force`; the jobs keep running either
way and are re-adopted when the daemon comes back.

Most commands autostart a daemon on first use. `KRANQ_AUTOSTART=0` turns that off.

### `kranq config ls|get|set|unset`

Settings the daemon reads at startup, stored in `$KRANQ_HOME/env`.

```sh
kranq config set KRANQ_MAX_VMS=1
kranq config ls
```

A daemon started by launchd or over ssh has almost no environment, so this file
is how it is configured at all. The environment still wins, for a one-off
override. Secrets are never printed, by `ls` or by `get`.

<a name="configuration"></a>

| Setting | Default | Meaning |
| --- | --- | --- |
| `KRANQ_MAX_VMS` | `2` | job VMs at once |
| `KRANQ_MEMORY_HEADROOM_MB` | `2048` | memory to keep free when admitting |
| `KRANQ_GIT_REMOTE` | | remote base, e.g. `git@bitbucket.org:effetmonstre` |
| `KRANQ_LIMA_HOME` | lima's default | where kranq keeps its VMs |
| `KRANQ_HTTP_ADDR` | off | loopback address for the git endpoint |
| `KRANQ_NODE` | the hostname | what this machine calls itself in a fence record |
| `KRANQ_AUTO_CREATE_REPOS` | on | make a repository on first push |
| `KRANQ_AUTO_INSTALL_DEPS` | on | fetch lima the first time a command needs a VM |
| `KRANQ_HOME` | `~/.kranq` | everything above lives here |
| `KRANQ_AUTOSTART` | on | `0` stops commands starting a daemon |

### `kranq auth claude [--stdin|--show|--clear]`

Store the Claude token in `$KRANQ_HOME/env` at `0600`. `--show` says whether one
is set and its fingerprint, never the token. It is deliberately not in the
launchd plist: `~/Library/LaunchAgents` is world-readable.

---

## Receiving pushes

Setting up the machine other people push to. See [push.md](advanced/push.md).

### `kranq setup [name] [--peer NAME]`

Makes a machine ready to receive work: lima and the daemon. Naming a key also
authorises one and prints what a pusher needs; without a name nothing is issued
and no address is read back, because whoever is standing on a single build
machine already knows how to reach it.

```sh
kranq setup                                  # the machine, and nothing else
kranq setup ci-dx                            # ... and a key for a pipeline
kranq setup ci-dx --key ci-dx.pub            # ... one whose private half stays yours
kranq setup ci-dx --peer mini-1              # from a laptop, which knows the address
```

Safe to re-run; re-running with a name rotates that key.

| Flag | What |
| --- | --- |
| `--peer NAME` | set up a registered build machine over ssh, sending it its own address |
| `--host [user@]host[:port]` | the address a client reaches the machine at |
| `--port N` | the port, if it is not in `--host` |
| `--key FILE` | authorise this public key instead of generating one |
| `--key-only` | just the key: leave lima and the daemon alone |

Variables go to stdout, narration to stderr, so `> vars.env` is a file. The
private key is printed once and kept nowhere.

Nothing is pre-created: a repository comes into being on its first push, unless
`KRANQ_AUTO_CREATE_REPOS` is off, in which case use `kranq repo create`.

The key is installed as a forced command, so it can push and fetch and do
nothing else — no shell, no agent forwarding, no port forwarding. That is also
what removes the client-side `receivepack` and `uploadpack` overrides: ssh puts
what git asked for in `SSH_ORIGINAL_COMMAND` and kranq resolves the repository.

**The address cannot be discovered here.** A machine reached through a forwarded
port sees only what sshd is bound to, so `setup` uses the address you arrived on
and says that it guessed. `--peer` avoids the question: the registry holds it.

### `kranq repo ls|create|rm`

The bare repositories people push into, under `$KRANQ_HOME/repos`.

```sh
kranq repo ls
kranq repo create dx --host 142.127.69.2:333
kranq repo rm dx         # the next push recreates it
```

`create` prints the repository's URL, and that URL is all a client needs when
its key already reaches the machine: pushing to a **real path** runs stock
`git-receive-pack`, and the hooks inside the repository are kranq. No forced
command, no `receivepack` override.

That is also why `create` is needed at all on this path — nothing kranq owns
runs before git does, so there is nothing to create the repository on arrival.
A push to the short `ssh://host/dx.git` form does create it, unless
`KRANQ_AUTO_CREATE_REPOS=false`.

`--host [user@]host[:port]` is the address a client reaches the machine at; the
same guess and the same warning as [`kranq setup`](#kranq-setup-name---peer-name).

### `kranq token create|ls|revoke`

Named tokens for the **https** endpoint. Stored only as a hash, so the secret is
printed once and cannot be shown again.

```sh
kranq token create ci-dx
kranq token ls
kranq token revoke ci-dx
```

### `kranq key add|ls|rm`

ssh keys allowed to push to this machine, written into `~/.ssh/authorized_keys`
as forced-command entries.

```sh
kranq key add ci-laptop ~/.ssh/kranq_push.pub
kranq key add ci-laptop -          # read the key from stdin
kranq key ls
kranq key rm ci-laptop
```

Each entry is `restrict,command="kranq git-receive --name <n>"`, so the key can
push and fetch and do nothing else: no shell, no forwarding. Lines kranq did not
write are never touched, because that file is usually how you administer the
machine.

---

## At-most-once effects

### `kranq fence ls|show|break --repo NAME`

A task declaring `effects.push` holds a fence at the git host for the duration,
so a lost run cannot open a second pull request. See [fence.md](advanced/fence.md).

```sh
kranq fence ls    --repo dx
kranq fence show  --repo dx refs/kranq/fence/<hash>
kranq fence break --repo dx refs/kranq/fence/<hash> --yes
```

`break` requires `--yes` and prints who holds the fence and whether they already
pushed. Breaking a fence whose run is still alive stops its *next* push, not
what it has already done.

---

## Installing and updating

### `kranq install [flags]`

Copies the running binary to `~/.local/bin`, adds it to PATH, and fetches Lima.

| Flag | Meaning |
| --- | --- |
| `--prefix DIR` | where the binary goes |
| `--no-path` | do not touch shell startup files |
| `--skip-deps` | do not install lima |
| `--deps-only` | lima and launchd only: leave the binary where a package manager put it |

Lima is fetched the first time a command actually needs a VM, so `--deps-only`
is for getting that out of the way ahead of time rather than something you have
to run. `KRANQ_AUTO_INSTALL_DEPS=false` turns the automatic fetch off, and then
a command that needs a VM says so instead.
| `--client-only` | binary and PATH only: no lima, no launchd |
| `--with-daemon` | also install and start the launchd job |

Lima is verified twice: against a checksum compiled into the binary **and**
against the published `SHA256SUMS`, which must agree. It is installed under
`$KRANQ_HOME/deps` and invoked by absolute path, so a Homebrew lima appearing or
disappearing cannot change what runs.

### `kranq upgrade <path> [--target PATH] [--force]`

Replace the installed kranq, keeping the previous as `<target>.prev`. The
candidate must answer `version --json` before it replaces anything. If the daemon
does not come back in fifteen seconds, the previous binary is restored and the
daemon restarted from it.

Defaults to the kranq on PATH, not the file being executed: `./kranq upgrade`
from a build directory means "replace what is installed".

### `kranq rollback [--target PATH] [--force]`

Go back to the previous binary. It keeps the one it rolled away from, so running
it twice returns you to where you started.

### `kranq uninstall [--purge]`

Remove the binary, the PATH block and the launchd job. `--purge` also deletes
tasks, artifacts and image metadata.

### `kranq version [--json]`

### `kranq help [command|--all]`

`kranq help` is a short list: setting a machine up, watching it, and reaching
another one. `kranq help --all` is this table. `kranq help <command>`, or
`kranq <command>` with wrong arguments, prints one command's usage — including
the ones the short list leaves out.

---

## Other machines

### `kranq peer add|ls|rm|test|upgrade`

```sh
kranq peer add mini-1 --ssh macmini@142.127.69.2:333 --default
kranq peer ls
kranq peer test mini-1        # reachable, kranq present, its doctor output
kranq peer upgrade mini-1     # install or upgrade kranq there
kranq peer rm mini-1
```

`peer upgrade` replaces the old `deploy.sh`. It copies the running binary over
and runs `kranq upgrade` on the far side, so that machine does its own
verification, its own in-flight check and its own rollback. A machine with no
kranq yet gets `kranq install`. Sending a binary to a machine of another
architecture is refused, with the `go build` line that would produce the right one.

**Peers are managed, not yet used for dispatch.** Running a task on a remote peer
is still ahead.

---

## Internal

Listed because they show up in `ps` and in hook scripts, not because you run them.

| Command | Run by | Purpose |
| --- | --- | --- |
| `kranq exec <task-id>` | the daemon | runs one job in its own process group and records the result, so a job outlives the daemon |
| `kranq git-hook <phase>` | git | validates a push in `pre-receive`, submits and streams in `post-receive` |
| `kranq git-receive` | sshd | the forced command a push key runs; the whole boundary between a key and a shell |

`exec` is deliberately a real listed command: re-adoption identifies a job by
finding `exec` and the task id in its `ps` command line, because a pid alone is
not an identity.

---

## Exit codes

Reserved codes are 64-127. A task's own exit code passes through below that; a
task exiting in the reserved range is reported as `1`.

| Code | Meaning |
| --- | --- |
| 0 | success |
| 1-63 | the task's own exit code |
| 64 | usage error |
| 65 | invalid spec, or a push refused for one |
| 66 | input file not found |
| 69 | daemon or peer unreachable |
| 70 | internal error |
| 75 | never admitted |
| 77 | authentication or scope denied |
| 78 | misconfigured; `doctor` found a failure |
| 124 / 125 | timed out / cancelled |
| 126 / 127 | could not start / missing dependency |
