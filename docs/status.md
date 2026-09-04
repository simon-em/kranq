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

## Next: phase 2, the daemon

In order:

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
