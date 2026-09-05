# Every command

Global: `--forge-home` is read from `$FORGE_HOME`; settings come from the
environment first and `$FORGE_HOME/env` second (see [configuration](#configuration)).

Commands marked **internal** are run by git or by the daemon, never by hand.

| | |
| --- | --- |
| [Running tasks](#running-tasks) | `run` `push` `ps` `logs` `cancel` `validate` `render` |
| [Inspecting a machine](#inspecting-a-machine) | `status` `doctor` `vm` `image` |
| [The daemon](#the-daemon) | `daemon` `config` `auth` |
| [Receiving pushes](#receiving-pushes) | `repo` `token` `key` |
| [At-most-once effects](#at-most-once-effects) | `fence` |
| [Installing and updating](#installing-and-updating) | `install` `upgrade` `rollback` `uninstall` `version` `help` |
| [Other machines](#other-machines) | `peer` |
| [Internal](#internal) | `exec` `git-hook` `git-receive` |

---

## Running tasks

### `forge run <task.yaml> [flags]`

Submit a task and follow it to completion. Exits with the task's own exit code.

```sh
forge run ci/tasks/spec.yaml --repo dx --branch main
forge run ci/tasks/review.yaml --artifacts ./review-output
id=$(forge run ci/tasks/spec.yaml --detach) && forge logs "$id" -f
```

| Flag | Default | Meaning |
| --- | --- | --- |
| `--repo` | `$CI_REPO`, `$BITBUCKET_REPO_SLUG` | repository slug |
| `--branch` | `$CI_BRANCH`, `$BITBUCKET_BRANCH` | branch to check out |
| `--label` | the task name | names the VM and the artifact directory |
| `--artifacts DIR` | none | copy `ci-artifacts/` out into `DIR` |
| `--remote` | `$FORGE_GIT_REMOTE` | git remote base, e.g. `git@bitbucket.org:effetmonstre` |
| `--env NAME=V`, `-e` | | repeatable; bare `-e NAME` forwards it from here |
| `--keep-vm` | `never` | `never`, `on-failure`, `always` |
| `--timeout` | `4h` | ceiling on the run |
| `--detach` | off | print the task id and return |
| `--local` | off | run in this process, bypassing the daemon |

`--local` is a break-glass path: no queue, no admission control, no re-adoption.
It exists so the code path is exercised rather than dead.

### `forge push <task.yaml> [flags]`

Send **this repository** to a forge machine and run a task against exactly what
was sent. Nothing is cloned from the git host, so no credential is needed to read
the code, and a commit that exists nowhere else still runs. See [push.md](push.md).

```sh
FORGE_ENDPOINT=ssh://macmini@host:333 FORGE_SSH_KEY=~/.ssh/forge_push \
  forge push ci/tasks/spec.yaml --repo dx

FORGE_ENDPOINT=https://ci.example.com FORGE_TOKEN=forge_… \
  forge push ci/tasks/spec.yaml --repo dx -e BITBUCKET_TOKEN
```

| Flag | Default | Meaning |
| --- | --- | --- |
| `--endpoint` | `$FORGE_ENDPOINT` | `ssh://user@host:port` or `https://host` |
| `--token` | `$FORGE_TOKEN` | required for https, ignored for ssh |
| `--ssh-key` | `$FORGE_SSH_KEY` | identity to push with, for ssh |
| `--receive-pack` | `$HOME/.local/bin/forge` | forge on the far side, so no forge-specific key is needed; `""` to rely on a forced command |
| `--repo` | `$FORGE_REPO`, `$CI_REPO` | name the repository has on the runner |
| `--branch` | `$CI_BRANCH`, `$BITBUCKET_BRANCH` | branch name to report for the run |
| `--rev` | `HEAD` | what to send |
| `--label`, `--keep-vm`, `--env`/`-e`, `--detach` | | as for `run` |

The token is never passed as an argument: it goes in the URL's userinfo, so it
does not appear in the process list. It is also not defaulted into the flag,
because `flag.PrintDefaults` prints defaults and usage is printed on every misuse.

### `forge ps [-a]`

List tasks. `-a` includes finished ones.

### `forge logs <id> [-f]`

Print a task's log; `-f` follows until it finishes.

### `forge cancel <id>...`

Cancel queued or running tasks. A running job is signalled as a **process
group**, then SIGKILLed if it ignores SIGTERM, so its VM goes with it.

### `forge validate <task.yaml>...` and `forge render <task.yaml>`

Parse a spec, and print the bash script it compiles to. Neither needs a daemon.
`render` is how you see what a task will actually run.

---

## Inspecting a machine

### `forge status [--json]`

Queue depth, what the scheduler is blocked on, free memory and CPUs, and the
Claude gate.

### `forge doctor [--json]`

Checks everything that has to be true for a task to run here: the binary is on
PATH, `$FORGE_HOME` is `0700`, the socket path fits macOS's 104-byte limit, git
is new enough for `--atomic`, lima is forge's own copy and can list instances,
there is disk for an image pair, the Claude token is present, the daemon matches
the CLI, and no VM is orphaned. Exits `78` if anything failed.

### `forge vm ls|shell|rm`

```sh
forge vm ls
forge vm shell <task-id|vm-name>            # open a kept VM
forge vm shell <task-id> -- cat /tmp/x      # or run one command in it
forge vm rm <vm-name>...  |  forge vm rm --all
```

Only useful with `--keep-vm`, which is how you get to look at a failed run.

### `forge image ls|build|prune`

```sh
forge image ls
forge image build --repo dx --ref main     # warm both layers ahead of time
forge image prune
```

Two layers: a shared base, and a per-project layer keyed by the repo's
`ci/setup.yaml`. A job clones the project layer, which is an APFS
copy-on-write clone and effectively free.

---

## The daemon

### `forge daemon run|start|stop|status`

`run` stays in the foreground; `start` spawns one and waits for it. `stop`
refuses while work is in flight unless `--force`; the jobs keep running either
way and are re-adopted when the daemon comes back.

Most commands autostart a daemon on first use. `FORGE_AUTOSTART=0` turns that off.

### `forge config ls|get|set|unset`

Settings the daemon reads at startup, stored in `$FORGE_HOME/env`.

```sh
forge config set FORGE_MAX_VMS=1
forge config ls
```

A daemon started by launchd or over ssh has almost no environment, so this file
is how it is configured at all. The environment still wins, for a one-off
override. Secrets are never printed, by `ls` or by `get`.

<a name="configuration"></a>

| Setting | Default | Meaning |
| --- | --- | --- |
| `FORGE_MAX_VMS` | `2` | job VMs at once |
| `FORGE_MEMORY_HEADROOM_MB` | `2048` | memory to keep free when admitting |
| `FORGE_GIT_REMOTE` | | remote base, e.g. `git@bitbucket.org:effetmonstre` |
| `FORGE_LIMA_HOME` | lima's default | where forge keeps its VMs |
| `FORGE_HTTP_ADDR` | off | loopback address for the git endpoint |
| `FORGE_NODE` | the hostname | what this machine calls itself in a fence record |
| `FORGE_AUTO_CREATE_REPOS` | on | make a repository on first push |
| `FORGE_AUTO_INSTALL_DEPS` | on | fetch lima the first time a command needs a VM |
| `FORGE_HOME` | `~/.forge` | everything above lives here |
| `FORGE_AUTOSTART` | on | `0` stops commands starting a daemon |

### `forge auth claude [--stdin|--show|--clear]`

Store the Claude token in `$FORGE_HOME/env` at `0600`. `--show` says whether one
is set and its fingerprint, never the token. It is deliberately not in the
launchd plist: `~/Library/LaunchAgents` is world-readable.

---

## Receiving pushes

Only needed on a machine that receives `forge push`. See [push.md](push.md).

### `forge repo ls|create|rm`

The bare repositories people push into, under `$FORGE_HOME/repos`.

```sh
forge repo ls
forge repo create dx     # so a plain git push to its path works
forge repo rm dx         # the next push recreates it
```

A push creates the repository on arrival unless
`FORGE_AUTO_CREATE_REPOS=false`. `create` is for that case, and for pushing
straight to a repository's real path, where git runs the real `git-receive-pack`
and no forge code is in the loop to create anything.

### `forge token create|ls|revoke`

Named tokens for the **https** endpoint. Stored only as a hash, so the secret is
printed once and cannot be shown again.

```sh
forge token create ci-dx
forge token ls
forge token revoke ci-dx
```

### `forge key add|ls|rm`

ssh keys allowed to push to this machine, written into `~/.ssh/authorized_keys`
as forced-command entries.

```sh
forge key add ci-laptop ~/.ssh/forge_push.pub
forge key add ci-laptop -          # read the key from stdin
forge key ls
forge key rm ci-laptop
```

Each entry is `restrict,command="forge git-receive --name <n>"`, so the key can
push and fetch and do nothing else: no shell, no forwarding. Lines forge did not
write are never touched, because that file is usually how you administer the
machine.

---

## At-most-once effects

### `forge fence ls|show|break --repo NAME`

A task declaring `effects.push` holds a fence at the git host for the duration,
so a lost run cannot open a second pull request. See [fence.md](fence.md).

```sh
forge fence ls    --repo dx
forge fence show  --repo dx refs/forge/fence/<hash>
forge fence break --repo dx refs/forge/fence/<hash> --yes
```

`break` requires `--yes` and prints who holds the fence and whether they already
pushed. Breaking a fence whose run is still alive stops its *next* push, not
what it has already done.

---

## Installing and updating

### `forge install [flags]`

Copies the running binary to `~/.local/bin`, adds it to PATH, and fetches Lima.

| Flag | Meaning |
| --- | --- |
| `--prefix DIR` | where the binary goes |
| `--no-path` | do not touch shell startup files |
| `--skip-deps` | do not install lima |
| `--deps-only` | lima and launchd only: leave the binary where a package manager put it |

Lima is fetched the first time a command actually needs a VM, so `--deps-only`
is for getting that out of the way ahead of time rather than something you have
to run. `FORGE_AUTO_INSTALL_DEPS=false` turns the automatic fetch off, and then
a command that needs a VM says so instead.
| `--client-only` | binary and PATH only: no lima, no launchd |
| `--with-daemon` | also install and start the launchd job |

Lima is verified twice: against a checksum compiled into the binary **and**
against the published `SHA256SUMS`, which must agree. It is installed under
`$FORGE_HOME/deps` and invoked by absolute path, so a Homebrew lima appearing or
disappearing cannot change what runs.

### `forge upgrade <path> [--target PATH] [--force]`

Replace the installed forge, keeping the previous as `<target>.prev`. The
candidate must answer `version --json` before it replaces anything. If the daemon
does not come back in fifteen seconds, the previous binary is restored and the
daemon restarted from it.

Defaults to the forge on PATH, not the file being executed: `./forge upgrade`
from a build directory means "replace what is installed".

### `forge rollback [--target PATH] [--force]`

Go back to the previous binary. It keeps the one it rolled away from, so running
it twice returns you to where you started.

### `forge uninstall [--purge]`

Remove the binary, the PATH block and the launchd job. `--purge` also deletes
tasks, artifacts and image metadata.

### `forge version [--json]`

### `forge help [command]`

The command table, or one command's usage. `forge <command>` with wrong
arguments prints the same thing.

---

## Other machines

### `forge peer add|ls|rm|test|upgrade`

```sh
forge peer add mini-1 --ssh macmini@142.127.69.2:333 --default
forge peer ls
forge peer test mini-1        # reachable, forge present, its doctor output
forge peer upgrade mini-1     # install or upgrade forge there
forge peer rm mini-1
```

`peer upgrade` replaces the old `deploy.sh`. It copies the running binary over
and runs `forge upgrade` on the far side, so that machine does its own
verification, its own in-flight check and its own rollback. A machine with no
forge yet gets `forge install`. Sending a binary to a machine of another
architecture is refused, with the `go build` line that would produce the right one.

**Peers are managed, not yet used for dispatch.** Running a task on a remote peer
is still ahead.

---

## Internal

Listed because they show up in `ps` and in hook scripts, not because you run them.

| Command | Run by | Purpose |
| --- | --- | --- |
| `forge exec <task-id>` | the daemon | runs one job in its own process group and records the result, so a job outlives the daemon |
| `forge git-hook <phase>` | git | validates a push in `pre-receive`, submits and streams in `post-receive` |
| `forge git-receive` | sshd | the forced command a push key runs; the whole boundary between a key and a shell |

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
