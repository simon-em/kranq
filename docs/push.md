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

## Over ssh, which is what a build machine already has

An https endpoint needs a tunnel, and a tunnel puts a request body limit in the
way of the first push of a large repository. If you can already ssh to the
machine, none of that is necessary.

```sh
forge key add ci-laptop ~/.ssh/forge_push.pub          # on the build machine
FORGE_ENDPOINT=ssh://macmini@host:333 \
FORGE_SSH_KEY=~/.ssh/forge_push \
  forge push ci/tasks/spec.yaml --repo dx
```

No token, no tunnel, no body limit. The key is the credential.

### No new user is needed

Dokku gives itself a `dokku` account. On macOS creating one needs `dscl` and an
admin password, and it buys nothing here: a **forced command** already confines
a key to one program.

```
restrict,command="/Users/macmini/.local/bin/forge git-receive --name ci-laptop" ssh-ed25519 AAAA... forge-key:ci-laptop
```

`restrict` turns off agent forwarding, port forwarding, pty and X11 in one word;
a git push needs none of them. `command=` replaces whatever the client asked for
and puts the original in `SSH_ORIGINAL_COMMAND`.

Verified on the real build machine, with that key:

```
$ ssh -i forge_push macmini@host whoami
forge: "whoami" is not a git command
$ ssh -i forge_push macmini@host 'cat ~/.ssh/id_ed25519'
forge: "cat" is not allowed; this key may only push and fetch
```

The ordinary key on the same account still gets a shell. `forge key add` only
ever appends and `forge key rm` only removes lines carrying its own marker,
because that file is usually how someone administers the machine and losing a
line locks them out.

### Parsing SSH_ORIGINAL_COMMAND is the whole boundary

Everything that gets past it runs as the forge user. It accepts
`git-receive-pack` and `git-upload-pack` with a single quoted path, unquoted the
way git quotes it, and refuses everything else rather than sanitising anything.

One case worth naming: an ssh path is the repository and nothing else, unlike an
http path where the repository is only the first segment. Taking the first
segment there would have let `/etc/../dx.git` quietly mean a repository called
`etc`. A path with an interior slash is refused.

### Pinning the identity matters

The push key sits beside an ordinary key for the same host, and ssh offers keys
in its own order, so without `IdentitiesOnly=yes` it presents the ordinary one
and the forced command never runs. `--ssh-key` sets that up.
