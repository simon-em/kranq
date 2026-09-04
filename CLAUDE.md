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

**macOS refuses a unix socket path over 104 bytes.** `internal/sockpath` shortens
deterministically. This bites the ssh agent socket and will bite the daemon socket.

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
