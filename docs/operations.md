# Operating forge

## Checking a machine

```sh
forge doctor            # exit 0 if nothing failed, 78 if something did
forge doctor --json
```

It checks what has to be true for a task to run here: the binary is reachable by
name, `$FORGE_HOME` is 0700, the socket path fits in the 104 bytes macOS allows,
git is new enough to have `--atomic`, lima is forge's own copy and can list
instances, there is disk for an image pair, the claude token is present, and the
daemon is the version the CLI expects.

A warning is something to know about. A failure means tasks will not run.

Two things doctor found the first time it ran, both since fixed and both worth
knowing about because they are the class of thing it exists to catch:

- `$FORGE_HOME` was 0755, because installing lima created it as a parent
  directory. The daemon socket has no authentication at all beyond sitting in a
  private directory, so that mattered.
- `OnPath` compared PATH entries as strings, and on macOS `/tmp` and `/var` are
  symlinks into `/private`, so a binary in a directory plainly on PATH was
  reported as missing from it.

## Upgrading

```sh
forge upgrade ./forge              # replace the forge on PATH
forge upgrade ./forge --target /opt/bin/forge
forge rollback
```

The candidate must answer `version --json` before it replaces anything, so a
binary for the wrong architecture or a truncated download fails before it becomes
the installed copy rather than after.

If work is in flight the upgrade is refused, because restarting the daemon marks
running tasks `lost`. `--force` overrides it.

If the daemon does not come back within fifteen seconds, the previous binary is
put back and the daemon restarted from it. The command still exits nonzero: it
tells you the upgrade failed, not that nothing happened.

`forge rollback` keeps the binary it rolled away from, so running it twice
returns you to where you started.

## Build machines

```sh
forge peer add mini-1 --ssh macmini@142.127.69.2:333 --default
forge peer ls
forge peer test mini-1          # reachable, forge present, its doctor output
forge peer upgrade mini-1       # installs forge there, or upgrades it
forge peer rm mini-1
```

`peer upgrade` replaces `deploy.sh`. It copies the running binary over, then runs
`forge upgrade` on the far side, so the far machine does its own verification,
its own in-flight check and its own automatic rollback. On a machine with no
forge yet it runs `forge install` instead.

It refuses to send the running binary to a machine of another architecture and
tells you the `go build` line to produce one, which you then pass with `--binary`.

**Peers are registered but work is not yet dispatched to them.** These commands
manage machines; running a task on a remote peer is phase 5.

## What is stored where

```
$FORGE_HOME/env          secrets, 0600, never in the launchd plist
$FORGE_HOME/peers.json   the peer registry, 0600
$FORGE_HOME/deps/        forge's own lima, never on the global PATH
$FORGE_HOME/fence/       scratch git repos used to read and write fences
$FORGE_HOME/tasks/       one directory per task: task.json, log, artifacts
$FORGE_HOME/forge.sock   the daemon socket, 0600 inside a 0700 directory
```

The socket's permissions are the entire authorization model for the local API.
That is why doctor fails, rather than warns, on a wide `$FORGE_HOME`.
