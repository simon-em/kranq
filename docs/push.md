# Pushing work to forge

A job's code can arrive two ways. The old way is a **clone**: the VM fetches the
repository from the git host, which needs a credential that reaches that host.
The new way is a **push**: you send the code to forge, and forge already has it.

```sh
git push forge main -o task=ci/tasks/spec.yaml
```

The build log streams back to your terminal line by line while it runs, the way
Heroku and Dokku do it.

- [Why it is worth the machinery](#why)
- [Setting it up over ssh](#ssh) — no tunnel, no token, no size limit
- [Setting it up over https](#https) — for callers that cannot ssh
- [The push options](#options)
- [Exit codes, and the one thing a push cannot carry](#exit-codes)
- [What was probed, and what it changed](#probed)
- [Troubleshooting](#troubleshooting)

<a name="why"></a>

## Why it is worth the machinery

**It removes the source credential.** Over https there is no ssh agent to
forward, so without a credential the endpoint could not clone at all and the
whole thing would be decorative. Pushing removes the question: nothing is
fetched, so nothing needs to authenticate to the git host.

**It runs what you have, not what you pushed upstream.** The commit under test
never has to exist anywhere else, which is what makes debugging a pipeline
bearable.

**The commit is pinned.** A task records the exact object, not a branch tip that
can move while the task waits in the queue.

**It does not remove the credential for *writing*.** A task with
`effects.push` still pushes a branch and opens a pull request at the real git
host, and its fence lives there too. `maintenance.yaml` still forwards
`BITBUCKET_TOKEN`. Read-only tasks, which is most of them, need nothing.

<a name="ssh"></a>

## Over ssh

If you can already ssh to the build machine, this is the path to use. No tunnel,
no token, and no request body limit to run into on a first push.

### On the build machine, once

```sh
forge key add ci-laptop ~/path/to/pushkey.pub
```

That appends one line to `~/.ssh/authorized_keys`:

```
restrict,command="/Users/macmini/.local/bin/forge git-receive --name ci-laptop" ssh-ed25519 AAAA… forge-key:ci-laptop
```

`restrict` turns off agent forwarding, port forwarding, pty and X11 in one word;
a git push needs none of them. `command=` replaces whatever the client asked for
and puts the original in `SSH_ORIGINAL_COMMAND`.

**No new user account is needed.** Dokku gives itself a `dokku` user; on macOS
that needs `dscl` and an admin password and buys nothing, because a forced
command already confines the key to one program:

```
$ ssh -i pushkey macmini@buildhost whoami
forge: "whoami" is not a git command
$ ssh -i pushkey macmini@buildhost 'cat ~/.ssh/id_ed25519'
forge: "cat" is not allowed; this key may only push and fetch
```

`forge key add` only appends and `forge key rm` only removes lines carrying its
own marker, because that file is usually how you administer the machine and
losing a line locks you out.

### On the machine that pushes

```
# ~/.ssh/config
Host forge-mini
    HostName 142.127.69.2
    Port 333
    User macmini
    IdentityFile ~/.ssh/forge_push
    IdentitiesOnly yes
```

`IdentitiesOnly` matters: the push key sits beside an ordinary key for the same
host, and ssh offers keys in its own order. Without it, ssh presents the ordinary
key, gets a shell, and the forced command never runs.

```sh
git remote add forge ssh://forge-mini/dx.git
git push forge main -o task=ci/tasks/spec.yaml
```

The repository is created on the first push.

### Without a forge key at all

Anyone who already has shell access can push straight to the repository's real
path, because the hooks live in the repository:

```sh
forge repo create dx                       # on the build machine, once
git push ssh://macmini@buildhost:333/Users/macmini/.forge/repos/dx.git \
    -o task=ci/tasks/spec.yaml HEAD:refs/heads/run
```

`forge repo create` is needed because only the forced command creates a
repository on arrival; a plain push to a path cannot.

<a name="https"></a>

## Over https

For a caller that cannot ssh to the machine. It needs a tunnel, and a tunnel
brings a request body limit, so prefer ssh where you have the choice.

### On the build machine

```sh
forge config set FORGE_HTTP_ADDR=127.0.0.1:8420
forge daemon stop --force && forge daemon start
forge token create ci-dx                    # printed once, stored only as a hash
cloudflared tunnel --url http://127.0.0.1:8420
```

The listener binds loopback. `cloudflared` dials **out**, so no port is opened
and no router is touched. What changes is not the network posture but who can
reach the endpoint.

### From the caller

```sh
export FORGE_ENDPOINT=https://ci.example.com
export FORGE_TOKEN=forge_…
forge push ci/tasks/spec.yaml --repo dx -e BITBUCKET_TOKEN
```

or with plain git:

```sh
git push "https://forge:$FORGE_TOKEN@ci.example.com/git/dx.git" \
    -o task=ci/tasks/spec.yaml HEAD:refs/heads/run
```

The token is the http password. `forge push` puts it in the URL's userinfo
rather than an argument, so it does not appear in the process list, and it is
never defaulted into a flag, because usage output prints flag defaults.

<a name="options"></a>

## The push options

Push options are the channel because they are the only one git offers that is
neither the URL nor a header: arbitrary strings, delivered to the hook as
`GIT_PUSH_OPTION_<n>`, absent from any proxy's request log.

| Option | Meaning |
| --- | --- |
| `-o task=PATH` | **required**; the spec, as a path inside the pushed commit |
| `-o label=NAME` | names the VM and the artifact directory |
| `-o branch=NAME` | branch name to report for the run |
| `-o keep-vm=on-failure` | `never`, `on-failure`, `always` |
| `-o env.NAME=VALUE` | repeatable; forwarded into the task |
| `-o detach` | queue it and return without following |

An unknown option is an error rather than being ignored, because a typo would
otherwise silently give you a default you did not ask for.

<a name="exit-codes"></a>

## Exit codes, and the one thing a push cannot carry

**A plain `git push` always exits 0 if the push itself was accepted, whatever the
task did.** This is not a bug to fix. `post-receive` runs after the ref has
already been accepted, and nothing it returns reaches git's exit status.

So the hook prints a machine-readable line instead:

```
FORGE-RESULT id=20260905T174031-bd4970a48ed775be status=failed exit=12
```

`forge push` reads it and exits with the task's own code. Use `forge push` in a
pipeline, and plain `git push` interactively. Or do it by hand:

```sh
git push forge main -o task=ci/spec.yaml 2>&1 | tee /tmp/out
grep -q 'FORGE-RESULT.*exit=0' /tmp/out
```

A push **is** refused, with a nonzero exit, when the request itself is wrong: no
`task=`, a task file that is not in the pushed commit, a spec that does not
parse, an unknown option, more than one ref at a time, or a bad credential.
Those are caught in `pre-receive`, which can still reject.

<a name="probed"></a>

## What was probed, and what it changed

Every one of these was measured before the design depended on it.

**A shallow clone cannot push.** A CI container starts with one, and git rejects
the push with `shallow update not allowed` unless the receiving repository sets
`receive.shallowUpdate`. Without finding this, the first real pipeline run would
have failed. The tree at the pushed commit is complete even though the history
behind it is not, which is all a job needs.

**Git quarantines pushed objects during `pre-receive`.** The hook can read them;
no separate process can. So a job started there would find nothing.

| hook | objects readable by a job | can reject the push | streams to the client |
| --- | --- | --- | --- |
| `pre-receive` | no | **yes** | yes |
| `post-receive` | yes | no | yes |

Validation lives in the first, the run in the second.

**A commit under `refs/forge/*` is invisible to a plain clone**, which only looks
at `refs/heads`. The clone then succeeds and produces an empty tree, so forge
names each run's commit `refs/heads/forge/<task-id>`.

**Over http, output was not reaching the client as it was written.** Measured
with a task printing a line every three seconds: over ssh the lines arrived three
seconds apart, over http six lines came back as two clumps, because `net/http`
holds a couple of kilobytes back and `cgi.Handler` copies into it without
flushing. Every write is flushed now and both transports behave the same.

**An ssh path is the repository and nothing else**, unlike an http path where the
repository is only the first segment. Taking the first segment there would have
let `/etc/../dx.git` quietly mean a repository called `etc`.

## The shape of it

```
caller                     forge host                        VM
------                     ----------                        --
git push ------------->  repos/<repo>.git
                         pre-receive: validate, or refuse
                         post-receive: submit to the daemon
                           and stream the log back  <-------- (live)
                         stage one commit into a bare repo -> copied in
                                                              clone from it
                                                              origin -> real remote
```

`origin` in the VM is repointed at the real git host, because only the *source*
came from forge.

<a name="troubleshooting"></a>

## Troubleshooting

| Symptom | Cause |
| --- | --- |
| `shallow update not allowed` | the repository predates `receive.shallowUpdate`; `forge repo rm <name>` and push again |
| `Permission denied (publickey)` | the key is not installed, or ssh offered a different one — set `IdentitiesOnly yes` |
| `"whoami" is not a git command` | expected: that key can only push and fetch |
| `no task: push with -o task=…` | the required push option is missing |
| `<path> is not in the pushed commit` | the spec is not at that path in what you pushed |
| output arrives in clumps | a proxy in front of the endpoint is buffering; ssh has no proxy |
| push accepted but exit 0 on a failing task | expected; use `forge push`, or grep for `FORGE-RESULT` |
| a huge first push over https fails | the tunnel's request body cap; use ssh |

## Limits worth knowing

- A push is one HTTP request, so a tunnel's body limit caps the first push of a
  large tree. Cloudflare's is 100MB on most plans. Later pushes send only what
  changed. **ssh has no such limit.**
- Any valid credential can create a repository by pushing to a new name. That is
  by design: repositories are made on first push.
- `forge repo ls` shows what has accumulated and `forge repo rm` clears one out.
