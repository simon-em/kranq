# The fence

A task that pushes a branch and opens a pull request must do it at most once, even
when kranq loses track of the machine running it. kranq cannot stop a partitioned
machine from continuing to run a job — no mechanism over any transport can halt a
process on a machine you cannot reach. What it can do is make the *effect*
conditional on a compare-and-swap at a service both attempts must reach anyway:
the git server. A ref update is already a CAS.

The fence is a ref, `refs/kranq/fence/<16 hex>`, hashed from the effect's kind, the
repo and the branch. Its object is a parentless commit whose message is a JSON
record naming the run that holds it.

## The protocol

| Step | Command |
| --- | --- |
| claim, at job start | `git push --force-with-lease=<ref>: <url> <new>:<ref>` |
| push an effect | `git push --atomic --force-with-lease=<ref>:<held> <url> <refspecs> <next>:<ref>` |
| release | `git push --force-with-lease=<ref>:<held> <url> :<ref>` |

The claim is taken **at job start, not at first push**, so a collision costs seconds
instead of surfacing forty minutes into a run.

## What the probes actually showed

Two of these were assumed in the design and turned out to be wrong. They are the
reason this file exists.

**An unforced push is not a compare-and-swap.** The original design advanced the
fence with a plain `git push`, reasoning that it "succeeds only if the fence is
still where we left it". It does not: an unforced push rejects a non-fast-forward,
but happily accepts *any* fast-forward, and accepts a create unconditionally. So a
run whose fence had been deleted would recreate it and push its branch, which is
exactly the duplicate the fence exists to prevent. Every advance uses
`--force-with-lease=<ref>:<exact oid>`, which is a true exact-value CAS: it fails
when the ref moved, and it fails when the ref is gone.

**`--force-with-lease` with an empty expect means "must not exist".** Verified: the
second claimant is rejected with `(stale info)`. This is the one the design got
right. Note it only holds with an *explicit* expect value; the bare
`--force-with-lease` form consults remote-tracking refs instead and is not safe here.

**`--atomic` propagates the rejection, and it is load-bearing.** Without it, a push
of `HEAD:refs/heads/feature` plus a rejected fence update lands the branch anyway
and reports failure. With it, both are rejected together. A test asserts the branch
does not appear.

**git rejects a single-segment ref as a "funny refname".** `refs/kranq/fence/<h>`
is three segments and fine; a fence ref must never be shortened to `refs/<h>`.

**git fails loudly when the server does not support `--atomic`** ("the receiving end
does not support --atomic push"), rather than silently degrading. kranq maps that to
`ErrNoAtomic` and refuses to push rather than pushing unfenced.

Probed against git 2.50.1 over a local bare repo, which is the same receive-pack
path a remote uses. **Not yet confirmed against Bitbucket Cloud**: whether it accepts
writes to a custom `refs/kranq/*` namespace is a hosting policy, not a git behaviour.
Confirm before the first fenced task runs for real.

## Verified end to end

Run against a real Lima VM, with a `git daemon` on the host standing in for the
git host so nothing external is touched:

| | |
| --- | --- |
| host claims before any image work | claimed, then the layers built |
| a raw `git push` inside the VM | refused by the guard hook |
| `kranq_push` | fence advanced and branch created in one push |
| clean finish | fence released, branch present |
| a second run against a held fence | refused after checkout, no VM, no branch |
| pushed then failed | fence held, phase `pushed`, holder named |
| failed having pushed nothing | fence released |

That last row happened by accident: a rerun failed on a non-fast-forward before
reaching `kranq_push`, and the fence was released exactly as intended, so an
ordinary failure needs no human.

## Ownership is by content, not by object

The host claims the fence, but the job advances it from **inside the VM**, so the
host never learns the new object id. `Adopt` therefore reads the record back and
compares the task id in it. That is what lets the host release a fence its own job
moved.

## Releasing, and the one asymmetry

| Run ended | Fence moved? | Outcome |
| --- | --- | --- |
| any | no | released — nothing landed, so a retry is safe |
| exit 0 | yes | released |
| nonzero | yes | **held**, and reported |

A fence held by mistake costs one human command. A fence released by mistake costs
a second pull request. A task that merely failed its tests pushed nothing and is
released, so an ordinary failure never needs a human.

## Inside the VM

When a spec sets `effects.push`, the script preamble gains two things:

- `kranq_push <refspec>...` — verifies the fence still names this run, then does the
  atomic push above.
- a `pre-push` hook that **fails any plain `git push`**, naming `kranq_push` in the
  error. Without it the helper would be advisory, and one `git push` anywhere in a
  task would bypass the whole mechanism.

`KRANQ_FENCE_BYPASS=1` is how `kranq_push` gets past its own hook.

## Residual holes

Listed rather than papered over.

- The gap between a fenced push and creating the pull request is unfenced.
- A `run:` step that curls an API is unfenced. The fence covers git, nothing else.
- `kranq fence break` is a loaded gun. It asks for `--yes` and says so.
- Breaking the fence of a run that is still alive stops its *next* push, not what it
  already did.

## Schema

```yaml
effects:
  push: true          # take a fence; without this nothing above happens
  key: maintenance    # the contended identity, defaults to the task name
  scope: branch       # branch (default), or repo for a repo-wide effect
```

## Commands

```sh
kranq fence ls    --repo dx
kranq fence show  --repo dx <ref>
kranq fence break --repo dx <ref> --yes
```
