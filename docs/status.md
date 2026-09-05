# Status

## Done

**Phase 0: the pure core.** New repo, `main.go`, a command table in `internal/cli`.
`internal/task` carried over from ci-runner verbatim, schema and script generation both.
Verified by rendering `infrastructure/ci/tasks/maintenance.yaml` through both the old
`ci-task-validate --script` and `forge render` and diffing: byte-identical, 529 lines.

**Phase 1: one real job, no daemon.** `internal/project`, `internal/image`, `internal/vm`,
`internal/run`, `internal/sshagent`, `internal/sockpath`. `forge run --local` checks out the
repo, builds or reuses both image layers, clones a VM, runs the compiled task, pulls
artifacts, destroys the VM.

Measured on a 16 GiB M-series Mac with Lima 2.2.0:

| Step | Time |
| --- | --- |
| base image build | 153s |
| dx project layer (Ruby, Node, Playwright browsers) | 289s |
| a job against the warm image | 17s |

The 17s figure is the one that matters: `limactl clone` is an APFS copy-on-write clone, so a
warm image costs almost nothing to start from.

**Phase 3: install, VM inspection, and the fence.** `forge install` fetches and
verifies its own Lima and writes the launchd job; `forge auth claude` stores the token
in `$FORGE_HOME/env` at 0600. `--keep-vm` plus `forge vm ls|shell|rm` open a failed
run for inspection. The env-derived checkout credential is in `run.Remote`, so a
forwarded `BITBUCKET_TOKEN` produces an https clone and no forwarded agent is needed.

The fence landed with `spec.effects`, `internal/fence`, the in-VM `forge_push` helper
and its guard hook, and `forge fence ls|show|break`. Two of the plan's assumptions
about git were wrong and are recorded in [fence.md](fence.md).

Still open in phase 3: `forge upgrade|rollback|doctor|deps`, and `forge peer upgrade`
replacing `deploy.sh`.

## Phase 2, in progress

Done so far: `internal/state`, `internal/gate`, `internal/svc.Prepare`, `internal/hostres`,
`internal/sched`. The scheduler carries the `running -> lost` rule, so **the duplicate-PR bug
described below is fixed**, with a test that fails if ci-runner's requeue behaviour is
reintroduced.

Done since: `internal/ipc`, `internal/daemon`, and the CLI for all of it. `forge run` submits
to the daemon by default, follows the log, pulls artifacts and exits with the task's own code;
`--local` remains as a break-glass path that bypasses the queue. `forge ps|logs|cancel|status`
and `forge daemon run|start|stop|status` exist. The daemon autostarts on first use.

Validated on this Mac end to end: autostart, submit, queue, follow, artifacts back over the
socket, correct exit code, 20s against a warm image.

Re-adoption is done. Each job runs as `forge exec <id>` in its own process group
and records its outcome in `result.json`, so a daemon SIGKILL no longer costs the
run. Verified on real hardware: the daemon was killed mid-job, the job kept going
and its VM stayed up, the restarted daemon re-adopted it as attempt 1 and reported
the real exit code. A job that finished while no daemon was running at all was
picked up from its record on the next start instead of being marked lost.

Re-adoption is deliberately last of those. Marking a restarted task `lost` is already correct
and safe; re-adoption only makes it *cheaper*, by not throwing away a 45-minute run. It needs
the job to outlive the daemon, which means an exec shim recording pgid and start time, so it
is a real chunk of work rather than a tweak.

### Original plan for the remaining pieces

1. `internal/state` — the task store. Same on-disk layout as ci-runner's
   (`$FORGE_HOME/tasks/<id>/{task.json,log,artifacts/}`) but single-writer with an in-memory
   index, a per-write random temp name, and `f.Sync()` before rename. Drop `AppendLog` and the
   dead `seen` field. Secrets go in a separate `secrets.json` so a diagnostic dump cannot
   include them.
2. `internal/svc` — the domain API that does not exist in ci-runner. In particular a **pure**
   `Prepare(req, caps, now, id) (Task, error)` holding the repo/branch precedence and the
   Claude precondition, all of which is currently trapped inside an HTTP handler.
3. `internal/sched` — the scheduler. Port ci-runner's `queue.go` policy (creation order, head
   of line blocking on memory, skip-forward on a closed gate) and add: a wake channel so
   submit does not wait up to 5s for the next tick, CPU accounting, named stop reasons.
4. `internal/ipc` — `net/http` over a unix socket. Nothing binds a network interface, so
   authorization is file permissions and the bearer token disappears.
5. `forge daemon run|start|stop|status`, and `forge run` defaulting to submit-and-follow with
   `--local` kept as a supported break-glass path.

## The correctness fixes that belong to phase 2

These are the reason phase 2 is not merely "add a queue".

**The duplicate-PR bug that exists in the running system today.** ci-runner's
`Scheduler.recover()` requeues any task found `running` at startup. The job's process group is
spawned with `Setpgid: true`, so a SIGKILL of the daemon orphans it rather than killing it,
and `launchctl kickstart -k` SIGKILLs. So the VM keeps running, the job pushes its branch and
opens its pull request, and the restarted daemon runs the whole task again. It is currently
masked only by `deploy.sh` refusing to restart while work is in flight.

forge must instead: re-adopt a task whose process group and VM are both still alive, and mark
`lost` one whose executor is gone. **There must be no code path anywhere that transitions
`running -> queued` or `lost -> queued`.** Re-running a lost task is a new task id created by
an explicit human command.

**`retry_after` outliving the Claude gate.** A task's `retry_after`, written when the token
was exhausted, survives a token rotation, so tasks sit idle for up to 40 minutes with the gate
wide open. This bit us twice in one afternoon. Delete the field: replace it with
`BlockedOn string` and have the scheduler consult the live gate every tick.

**The Claude gate is not persisted** and is keyed to nothing, so a restart silently reopens a
genuinely closed gate. Persist it, keyed by a hash of the OAuth token, so a rotated token gets
a fresh gate for free.

**Artifacts collide.** ci-runner writes them to a path shared by every run of the same
repo/label/branch and `rm -rf`s it at the start, so two concurrent runs of one branch stomp
each other. forge already copies straight into the task directory.

**`resources.Fits` fails open.** It returns true when the memory probe fails. Harmless on one
machine, but in a cluster a node with a broken `vm_stat` reports infinite free memory and wins
every placement bid. Must fail closed.

## Later phases

3. The fence (`refs/forge/fence/*`), the env-derived checkout credential, self-install,
   launchd, `forge vm shell|keep`, `forge peer upgrade`.
4. Cutover: convert dx's two shell tasks to specs, switch the pipeline, retire ci-runner.
5. Peers over ssh exec.
6. The HTTP API behind a Cloudflare tunnel, with named API tokens and no stored credentials.

Phases 5 and 6 are each comparable in size to 0-4 combined, are independent of each other, and
can be dropped without stranding anything.
