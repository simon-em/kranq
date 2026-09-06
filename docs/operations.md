# Operating kranq

## Checking a machine

```sh
kranq doctor            # exit 0 if nothing failed, 78 if something did
kranq doctor --json
```

It checks what has to be true for a task to run here: the binary is reachable by
name, `$KRANQ_HOME` is 0700, the socket path fits in the 104 bytes macOS allows,
git is new enough to have `--atomic`, lima is kranq's own copy and can list
instances, there is disk for an image pair, the claude token is present, and the
daemon is the version the CLI expects.

A warning is something to know about. A failure means tasks will not run.

Two things doctor found the first time it ran, both since fixed and both worth
knowing about because they are the class of thing it exists to catch:

- `$KRANQ_HOME` was 0755, because installing lima created it as a parent
  directory. The daemon socket has no authentication at all beyond sitting in a
  private directory, so that mattered.
- `OnPath` compared PATH entries as strings, and on macOS `/tmp` and `/var` are
  symlinks into `/private`, so a binary in a directory plainly on PATH was
  reported as missing from it.

## Upgrading

```sh
kranq upgrade ./kranq              # replace the kranq on PATH
kranq upgrade ./kranq --target /opt/bin/kranq
kranq rollback
```

The candidate must answer `version --json` before it replaces anything, so a
binary for the wrong architecture or a truncated download fails before it becomes
the installed copy rather than after.

If work is in flight the upgrade is refused, because restarting the daemon marks
running tasks `lost`. `--force` overrides it.

If the daemon does not come back within fifteen seconds, the previous binary is
put back and the daemon restarted from it. The command still exits nonzero: it
tells you the upgrade failed, not that nothing happened.

`kranq rollback` keeps the binary it rolled away from, so running it twice
returns you to where you started.

### Upgrading onto layered images

The first job after this upgrade rebuilds the base image and every layer, once
per machine. A layer counts as usable only when `$KRANQ_HOME/layers` has a record
saying its build finished, and images built before that existed have no record.
Adopting them instead would mean trusting an image kranq cannot prove is
complete, which is the failure the record exists to prevent.

The `kranq-proj-*` images from the single-layer scheme are dead on arrival —
nothing can clone them any more. `kranq image prune` sweeps them.

## Build machines

```sh
kranq peer add mini-1 --ssh macmini@142.127.69.2:333 --default
kranq peer ls
kranq peer test mini-1          # reachable, kranq present, its doctor output
kranq peer upgrade mini-1       # installs kranq there, or upgrades it
kranq peer rm mini-1
```

`peer upgrade` replaces `deploy.sh`. It copies the running binary over, then runs
`kranq upgrade` on the far side, so the far machine does its own verification,
its own in-flight check and its own automatic rollback. On a machine with no
kranq yet it runs `kranq install` instead.

It refuses to send the running binary to a machine of another architecture and
tells you the `go build` line to produce one, which you then pass with `--binary`.

**Peers are registered but work is not yet dispatched to them.** These commands
manage machines; running a task on a remote peer is phase 5.

## What is stored where

```
$KRANQ_HOME/env          secrets, 0600, never in the launchd plist
$KRANQ_HOME/peers.json   the peer registry, 0600
$KRANQ_HOME/deps/        kranq's own lima, never on the global PATH
$KRANQ_HOME/fence/       scratch git repos used to read and write fences
$KRANQ_HOME/layers/      one record per built image, and the build locks
$KRANQ_HOME/tasks/       one directory per task: task.json, log, artifacts
$KRANQ_HOME/kranq.sock   the daemon socket, 0600 inside a 0700 directory
```

The socket's permissions are the entire authorization model for the local API.
That is why doctor fails, rather than warns, on a wide `$KRANQ_HOME`.
