# forge

A single Go binary that runs CI jobs in disposable Lima VMs on macOS build machines.
Replaces `ci-runner` (a Go daemon + ~400 lines of bash) and the two bash clients in
`infrastructure/ci/`.

**The full design and phasing lives at**
`/Users/effetmonstre/.claude/plans/going-in-another-directtion-stateless-journal.md`.
Read it before making architectural decisions; it records what was decided and why, and
several decisions there reverse earlier drafts.

## Where this is now

Phases 0 and 1 are done and validated on real hardware. See [docs/status.md](docs/status.md)
for the phase-by-phase state and what is next.

```sh
forge run <spec.yaml> --repo dx --branch ci/lima --artifacts ./out   # works today
forge image ls|build|prune
forge validate|render|version
```

## Commands

```sh
go test ./...
go build -o /tmp/forge .
GOOS=linux GOARCH=amd64 go build -o /tmp/forge-linux .   # the pipeline container is Linux
```

## Non-obvious facts

**The pipeline container is Linux, the runners are macOS.** dx's `bitbucket-pipelines.yml`
uses `image: ruby:4.0.5`. forge must cross-compile a `linux/amd64` client. This is why the
bash clients kept breaking: that image has no `python3` and no `pgrep`.

**There are no virtiofs mounts in the VM.** The base template inherits `_images/debian-13`
and defines no `mounts:`, so the guest cannot see the host filesystem. Anything the job needs
must be copied in with `limactl copy` or cloned from a real remote. This rules out testing
with a `file://` remote on the host.

**A path passed to the guest must not be single-quoted if it needs expansion.** Quoting the
clone destination as `'~/work'` makes git create a directory literally named `~`. forge uses
`"$HOME/work"` in double quotes. There is a regression test in `internal/run/checkout_test.go`.

**macOS refuses a unix socket path over 104 bytes, and this has bitten three times.** The ssh
agent socket and the daemon socket both go through `internal/sockpath`, which shortens
deterministically. The third case is subtler: **a Lima instance name is part of a socket path**,
because Lima builds `~/.lima/<name>/ssh.sock.<16 digits>`. So `image.RunName` caps names at
`MaxRunName` (40) with a hash suffix rather than embedding the whole task id. A smoke test
passed at exactly 104 before this was fixed, so the failure was latent and length-dependent.
Any new path under `~/.lima` or `$FORGE_HOME` needs the same arithmetic.

**`ssh -T git@bitbucket.org` succeeding does not mean the agent has identities.** A key on
disk works for the host, but Lima's `forwardAgent` forwards an *agent*, so an empty agent
forwards nothing. `internal/sshagent` starts one and adds a key, and only runs when no token
was forwarded.

**`limactl list --format json` emits NDJSON**, one object per line, not a JSON array. The
decoder in `internal/vm/lima.go` reads a stream of values. Real output is checked in at
`internal/vm/testdata/list.json`.

**An image must be Stopped to be cloned**, and a newly provisioned VM needs a restart before
its `docker` group applies, because Lima multiplexes ssh and the session predates `usermod`.
`BuildBase` does stop/start/stop for this reason.

**forge only ever destroys instances whose name starts with `forge-`.** `image.Managed` gates
every destructive operation, which is what lets forge and the system it replaces coexist on
the same machine. A test asserts a `ci-run-*` instance is not claimed.

**All of a task's steps compile into ONE bash script**, so an `export` in step 1 is visible in
step 3. Real tasks depend on this: `maintenance.yaml` sets `PR_BRANCH` in step 1 and reads it
in step 4. `internal/task` has a test pinning it.

**Every Claude stream carries a `rate_limit_event`, even a perfectly healthy one.** Detecting
exhaustion by matching the text `rate_limit` would therefore mark every task exhausted and
wedge the gate permanently. Match on `rate_limit_info.status != "allowed"` instead. The
renderer emits `FORGE-GATE exhausted resets_at=<unix> window=<name>` when it happens, and
`gate.ParseExhaustion` reads it back out of the task log so the scheduler can wait for the
actual reset rather than polling. There is a test for the healthy case specifically.

**The usage gate retries at a flat interval (default 1 minute), not an exponential backoff.**
Usage can return at any moment, so doubling to hours leaves the machine idle long after it
could have run. When Claude tells us when the window resets, the gate waits for that instead,
because retrying every minute for three hours would boot a VM each time.

**Memory admission is conservative, and on a busy workstation it will block tasks.**
`vm_stat` free+inactive+speculative+purgeable on a laptop with a browser open can be 3GiB of
16GiB, and the default 2GiB headroom then leaves room for almost nothing. This is correct on a
dedicated build machine and surprising on a dev Mac. `FORGE_MEMORY_HEADROOM_MB` tunes it, and
`forge status` always says which task is waiting and why.

**A task's own reported status outranks its exit code.** `claude -p` exits 0 even when it
stops to ask a question, so both real tasks write a status file and a later step reads that
file. Never "simplify" this to trusting the exit code.

**A defer that records into the result needs named return values.** `Execute` returns
`(res Result, err error)` for exactly this reason: the teardown defer decides whether the VM
was kept, and with unnamed returns `return res, nil` copies the value before the defer runs.

**Secrets never go in the launchd plist.** `~/Library/LaunchAgents` is world-readable. The
daemon reads `$FORGE_HOME/env` (0600) at startup instead, and the plist carries only
`FORGE_HOME` and `PATH`. A test asserts the rendered plist contains no credential at all.

**The daemon under launchd has almost no environment.** Anything it needs must come from
`$FORGE_HOME/env` or the plist, not from your shell. `claudeToken()` reads the environment
first so a one-off override works, then falls back to the env file, which is the path that
actually matters in production.

## Layout

```
main.go                  os.Exit(cli.Main(os.Args))
assets/                  lima.yaml + mcp/*.py, go:embed'd into the binary
internal/cli/            subcommand dispatch, flag parsing, terminal output
internal/task/           task schema + compiling a spec to one bash script (ported verbatim)
internal/project/        ci/setup.yaml and ci/basekey.txt parsing
internal/image/          content-addressed naming, the two-layer cache, build, prune
internal/vm/             the limactl Driver interface, the Lima impl, and a Fake for tests
internal/run/            one job end to end
internal/sshagent/       the ensure_agent port
internal/sockpath/       the 104-byte unix socket workaround
internal/exitcode/       the exit code contract
```

## Conventions

Stdout is data, stderr is narration, on every command.

Exit codes 64-127 are reserved for forge so a caller can tell an infrastructure failure from
a test failure. A task's own code passes through below that. See README.md.

Tests use `vm.Fake` rather than a real VM. `internal/vm/lima_test.go` tests the real driver
against a stub `limactl` on PATH and checked-in real output.

**An unforced `git push` is not a compare-and-swap.** It rejects a non-fast-forward
but accepts any fast-forward, and accepts a create unconditionally. Every fence
advance therefore uses `--force-with-lease=<ref>:<exact oid>`. Do not "simplify" it
back to a plain push. See [docs/fence.md](docs/fence.md), which records what was
probed and what it showed.

**`--atomic` is load-bearing on a fenced push.** Without it a rejected fence update
still lets the branch land, and the push merely reports failure afterwards.

**A single-segment ref is a "funny refname" and git rejects it remotely.** Fence refs
are `refs/forge/fence/<hash>`, three segments.

**The host cannot track the fence by object id**, because the job advances it from
inside the VM. Ownership is read back out of the record's JSON (`fence.Adopt`).

**Writes to `refs/forge/*` are unconfirmed against Bitbucket Cloud.** Everything in
the fence was probed against a local bare repo. Whether the host allows a custom ref
namespace is hosting policy; confirm before the first fenced task runs for real.

**A job runs in its own process, not in the daemon.** The daemon starts
`forge exec <id>` with `Setpgid`, and everything it learns about the run comes
from `result.json` in the task directory. That is what lets a job outlive a
daemon SIGKILL and a restarted daemon pick the outcome back up. `forge exec` is
listed in the command table on purpose: re-adoption identifies a job by finding
`exec` and the task id in its `ps` command line, because a pid alone is not an
identity.

**A job's deadline is its own, not the daemon's.** The per-task context comes
from `context.Background()`, so shutting the daemon down does not cancel work in
flight; only a cancel or the task's own timeout does. Deriving it from the
daemon context would make every restart kill the runs it is supposed to preserve.
On shutdown the daemon waits `TeardownGrace` and then leaves whatever is left
running, to be re-adopted.

**A re-adopted job keeps the deadline it started against**, measured from
`StartedAt`, so a restart cannot be used to extend a run indefinitely.

**`net/http` buffers before writing to the wire, and `cgi.Handler` does not
flush.** A pushed build streamed live over ssh and arrived in clumps over http
until every write was flushed. This is invisible from reading the code: it was
found by timing a task that prints a line every three seconds. Any future
handler that streams needs the same treatment.

**Push options need `receive.advertisePushOptions` on the receiving repo**, and a
shallow push needs `receive.shallowUpdate`. A CI container starts with a shallow
clone, so without the second, every real pipeline push is rejected with "shallow
update not allowed". Both are set by `gitsrv.Store.Ensure`; a repository created
before those existed keeps the old config, so `forge repo rm` and push again.

**Git quarantines pushed objects during `pre-receive`.** The hook itself can read
them; no separate process can. That is why validation is in `pre-receive` (which
can still refuse the push) and the run is in `post-receive` (which cannot).

**A `git push` can never carry the task's exit code.** `post-receive` runs after
the ref is accepted. The `FORGE-RESULT` line is the contract instead; `forge
push` exits on it.

**Every setting falls back to `$FORGE_HOME/env`, and the CLI must use the same
lookup as the daemon.** `forge image ls` once came back empty on a machine that
was running VMs, because the CLI read `FORGE_LIMA_HOME` from the environment
while the daemon read it from the file. Anything reading a setting goes through
`daemonConfig()`.

**Shutdown must not wait for running jobs.** They are deliberately left alive to
be re-adopted, and their contexts are not tied to the daemon's, so a wait can
only time out while the old process sits on the lock and the next daemon fails
to start.

**Lima is fetched on first use, not by an install step.** `ensureLima` is what
every command that drives a VM calls; `limactlPath` only reports and never
installs, because doctor uses it and a check that fixes what it is checking is
not a check. The download is behind a flock, since two commands starting at once
would otherwise both fetch it.

**`limaAsset` is keyed by architecture and every entry is a macOS build.** Guard
on `GOOS` before installing, or the linux client downloads a Darwin tarball and
reports success. `deps.Supported()` is that guard.
