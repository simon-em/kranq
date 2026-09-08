# Where things stand

## Done

**Phase 0, the pure core.** The command table, and `internal/task` carried over
from ci-runner verbatim. Verified by rendering `maintenance.yaml` through both
the old `ci-task-validate --script` and `kranq render` and diffing: byte
identical, 529 lines.

**Phase 1, one real job.** `kranq run --local` checks out the repo, builds or
reuses both image layers, clones a VM, runs the task, pulls artifacts, destroys
the VM. Base image 153s, dx's layers 289s, a job against the warm image
17s.

**Phase 2, the daemon.** `internal/state`, `svc.Prepare`, `sched`, `gate`, `ipc`,
and the daemon behind a unix socket. `kranq run` submits and follows by default;
`--local` stays as break-glass so that path is exercised rather than dead.

Re-adoption is done: each job runs as `kranq exec <id>` in its own process group
and records its outcome, so a daemon restart no longer costs the run.

**Phase 3, the fence and operations.** `spec.effects`, `internal/fence`, the
in-VM `kranq_push` and its guard hook, `kranq fence ls|show|break`. The
env-derived checkout credential. `kranq install|upgrade|rollback|doctor|config`,
`kranq vm shell|keep|rm`, and `kranq peer`, which retires `deploy.sh`.

**The push endpoint** (a reshaped phase 6). Source arrives by `git push` over
https or ssh, push options carry the task and the forwarded env, and the run
streams back to the pushing terminal. Named tokens for https, forced-command ssh
keys for ssh. See [push.md](push.md).

**`files:`**, not part of the original phase plan. A task-level `path`/`mode`/
`content` list, staged before any step runs, so a task can carry its own MCP
server (or any other tool) instead of depending on the embedded
`assets/mcp/*`. Added to unblock an external consumer (`kman`) that needs to
inject its own per-run tooling without a kranq release for every one; see
[tasks.md](../tasks.md#files-for-a-task-that-brings-its-own-tools).

## Verified on real hardware

Not merely tested. These were run against actual machines.

| | Where |
| --- | --- |
| a real dx job, clone from Bitbucket, VM, docker | the mac mini |
| the fence, all six cases including both release policies | this laptop, real VM |
| re-adoption through a SIGKILLed daemon | both |
| re-adoption through a *failed upgrade*, by accident | the mac mini |
| a result recorded while no daemon was running at all | this laptop |
| cancel tearing down the process group and its VM | this laptop |
| `kranq push` over https, code that exists in no git repo | this laptop |
| `kranq push` over ssh, likewise | the mac mini |
| a plain `git push` doing the same, three ways | the mac mini |
| a forced-command key getting no shell and reading no files | the mac mini |
| `kranq install` with no `limactl` on PATH at all | this laptop |
| `peer upgrade` installing kranq from nothing | the mac mini |

## The mac mini, as it stands

kranq is installed at `~/.local/bin/kranq`, state in `~/.kranq`, VMs in
`~/.kranq/lima`, `KRANQ_MAX_VMS=1`. `kranq doctor` is green except for the Claude
token, which is not set.

**ci-runner is untouched and still running.** The two share nothing: different
binaries, different homes, different lima homes, different VM name prefixes.

## Left to do

**Cutover.** Convert dx's `spec.sh` and `playwright.sh` to YAML specs in their
own commit, land the submodule and dx commits, watch a week, then delete the old
clients. This is the step that makes any of it matter day to day.

**Two external facts.** Whether Bitbucket accepts writes to `refs/kranq/*`, which
the fence needs; and whether a first push of dx fits under Cloudflare's request
body cap, which only matters for the https path since ssh has no such limit.

**Peer dispatch.** Peers can be managed but work is not distributed to them. Note
that peers add no Claude capacity, since the subscription is shared: they buy
parallel spec and playwright runs.

**CLI conveniences** the plan listed and that do not exist: `inspect`, `attach`,
`wait`, `rerun`, `daemon restart|logs`, `audit`.

## Risks worth keeping in view

**`ci-runner` has no git remote.** It exists only on one laptop, and it is what
currently runs CI. kranq itself is at
[github.com/simon-em/kranq](https://github.com/simon-em/kranq).

**`ci-runner` is still at `CI_MAX_VMS=2`** while kranq is at 1. Three VMs at
~3GiB on a 16GiB machine is tight. kranq only runs when asked, so nothing runs
away on its own, but the plan calls for dropping both to 1 during the overlap.

**kranq cannot stop a partitioned machine from running a job.** It can only stop
the *effect* landing twice. This is a property of the world, not a gap to close.
