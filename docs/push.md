# Receiving source by git push

A job's code can arrive two ways. The old way is a clone: the VM fetches the
repository from the git host, which needs a credential that reaches that host.
The new way is a push: the caller sends the code to forge, and forge already has
it.

```sh
forge push ci/tasks/spec.yaml --repo dx -e BITBUCKET_TOKEN
```

That runs `git push` under the hood, waits for the run, and exits with the
task's own exit code.

## Why it is worth the machinery

**It removes the source credential.** Over https there is no ssh agent to
forward, so without a credential the endpoint could not clone at all and the
whole thing would be decorative. Pushing removes the question: nothing is
fetched, so nothing needs to authenticate to the git host.

**It runs what you have, not what you pushed upstream.** The commit under test
never has to exist anywhere else, which is what makes debugging a pipeline
bearable.

**The commit is pinned.** A task records the exact object, not a branch tip that
can move while it waits in the queue.

## What was probed first, and what it changed

**A shallow clone cannot push.** A CI container starts with one, and git rejects
the push with `shallow update not allowed` unless the receiving repository sets
`receive.shallowUpdate`. This would have failed on the first real pipeline run.
The tree at the pushed commit is complete even though the history behind it is
not, which is all a job needs.

**Push options, not headers, carry the parameters.** They are the only channel
git offers that is neither the URL nor a header: arbitrary strings, delivered to
the hook as `GIT_PUSH_OPTION_<n>`, absent from any proxy's request log. The
server needs `receive.advertisePushOptions`.

**Git quarantines pushed objects during `pre-receive`.** The hook can read them;
no separate process can. So a job started there would find nothing.

| hook | objects readable by a job | can reject the push | streams to the client |
| --- | --- | --- | --- |
| `pre-receive` | no | **yes** | yes |
| `post-receive` | yes | no | yes |

So validation lives in `pre-receive`, where a missing or unparseable spec still
fails the push, and the run lives in `post-receive`.

**A push cannot carry the run's exit code**, because `post-receive` runs after
the ref has already been accepted and nothing it returns reaches git's exit
status. The hook prints a `FORGE-RESULT` line instead and `forge push` exits on
it. A bare `git push` still shows you the whole log; it just always exits 0 when
the push itself was fine.

**A commit under `refs/forge/*` is invisible to a plain clone**, which only
looks at `refs/heads`. The clone then succeeds and produces an empty tree, so
forge names each run's commit under `refs/heads/forge/<task-id>`.

## The shape of it

```
caller                     forge host                        VM
------                     ----------                        --
git push ------------->  repos/<repo>.git
                         pre-receive: validate, or refuse
                         post-receive: submit to the daemon
                         stage one commit into a bare repo -> copied in
                                                              clone from it
                                                              origin -> real remote
```

`origin` in the VM is repointed at the real git host, because only the *source*
came from forge. A task with `effects.push` still pushes its branch and opens
its pull request there, and its fence lives there too.

## What this does not remove

**A task with effects still needs a token for the git host.** Pushing removes
the credential needed to *read* the code, not the one needed to write a branch
and open a pull request. `maintenance.yaml` still forwards `BITBUCKET_TOKEN`.
Read-only tasks, which is most of them, need nothing.

## Limits worth knowing

- A push is one HTTP request, so a tunnel's request body limit caps the first
  push of a large tree. Cloudflare's is 100MB on most plans. Subsequent pushes
  send only what changed.
- The endpoint binds loopback. `cloudflared` dials out to reach it, so no port
  is opened and no router is touched.
- Any valid token can create a repository by pushing to a new name. That is by
  design, since repositories are made on first push.

## Setting it up

```sh
forge token create ci-dx          # printed once, stored only as a hash
FORGE_HTTP_ADDR=127.0.0.1:8420 forge daemon restart
cloudflared tunnel --url http://127.0.0.1:8420
```

Then in the pipeline:

```sh
export FORGE_ENDPOINT=https://ci.example.com
export FORGE_TOKEN=$FORGE_TOKEN
forge push ci/tasks/spec.yaml --repo dx
```
