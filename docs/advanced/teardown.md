# Killing a job, and why it is fiddly

Three facts, each learned by getting it wrong in the system kranq replaces. None of them is
rediscoverable from reading the code, which is why they are written down here.

## The signal has to reach the process group

`exec.CommandContext` kills a cancelled command with `SIGKILL` by default. That is wrong
here: the VM is destroyed by the teardown path of the process being killed, so a `SIGKILL`
skips it entirely and the VM keeps running. A leaked VM is not merely untidy, it permanently
consumes a concurrency slot, because slots are counted from running instances.

Signalling only the parent is also wrong. The parent sits in a foreground `limactl shell`,
and a shell defers a trap until the foreground command returns, so the signal is not acted on
until the job finishes on its own, which is the thing we were trying to prevent.

So: `SysProcAttr{Setpgid: true}` to give the child its own group, `cmd.Cancel` sending
`SIGTERM` to `-pid`, and `cmd.WaitDelay` as the bounded fallback before `SIGKILL`. All three
are load-bearing; the first two are in `internal/vm/lima.go`.

This was verified against the real build machine: cancel destroys the VM in about ten
seconds, and the same test fails against the previous behaviour.

## A `SIGKILL` of the daemon still leaks

Nothing can be done about it in the signal path, because `SIGKILL` cannot be trapped and the
child is in its own process group, so it is orphaned rather than killed. The answer is not a
better signal, it is reconciliation: VMs are named after the task that owns them
(`kranq-run-<repo>-<label>-<task-id>`), so a reaper can list instances, cross-reference the
store, and destroy any whose task is finished or unknown.

The old naming used the shell's `$$`, which cannot survive a restart and therefore made this
impossible.

## kranq only ever destroys what it owns

`image.Managed` gates every destructive operation on the `kranq-` prefix. This is what lets
kranq run on the same machine as the system it replaces without the two fighting over each
other's instances, and it is covered by a test that asserts a `ci-run-*` instance is *not*
claimed.
