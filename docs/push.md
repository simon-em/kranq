# Pushing work to kranq

A job's code can arrive two ways. The old way is a **clone**: the VM fetches the
repository from the git host, which needs a credential that reaches that host.
The new way is a **push**: you send the code to kranq, and kranq already has it.

```sh
git push kranq main -o task=ci/tasks/spec.yaml
```

The build log streams back to your terminal line by line while it runs, the way
Heroku and Dokku do it.

- [Why it is worth the machinery](#why)
- [Over ssh](#ssh) — no tunnel, no token, no size limit, and usually no setup
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

### The simple way: nothing to set up

**If your key already reaches the machine, you can already push.** Git lets the
client say what to run on the far side, so it runs kranq directly:

```sh
kranq push ci/tasks/spec.yaml --repo dx     # KRANQ_ENDPOINT=ssh://macmini@host:333
```

or with plain git, once per clone:

```sh
git remote add kranq ssh://macmini@host:333/dx.git
git config remote.kranq.receivepack '$HOME/.local/bin/kranq git-receive'
git push kranq main:refs/heads/run -o task=ci/tasks/spec.yaml
```

The repository is created on the first push. Nothing is installed on the build
machine beyond kranq itself, and no kranq-specific key exists.

`kranq push` asks for this by default. `--receive-pack PATH` points at a kranq
installed somewhere else; `--receive-pack=""` turns it off for a key whose forced
command already decides what runs.

### The locked-down way: a key that can only push

The simple path gives whoever pushes a full shell, because it uses the key they
already had. For a CI system that should be able to push and nothing else, give
it its own key with a forced command:

```sh
kranq key add ci-laptop ~/path/to/pushkey.pub     # on the build machine
```

That appends one line to `~/.ssh/authorized_keys`:

```
restrict,command="/Users/macmini/.local/bin/kranq git-receive --name ci-laptop" ssh-ed25519 AAAA… kranq-key:ci-laptop
```

`restrict` turns off agent forwarding, port forwarding, pty and X11 in one word;
a git push needs none of them. `command=` replaces whatever the client asked for
and puts the original in `SSH_ORIGINAL_COMMAND`.

**No new user account is needed.** Dokku gives itself a `dokku` user; on macOS
that needs `dscl` and an admin password and buys nothing, because a forced
command already confines the key to one program:

```
$ ssh -i pushkey macmini@buildhost whoami
kranq: "whoami" is not a git command
$ ssh -i pushkey macmini@buildhost 'cat ~/.ssh/id_ed25519'
kranq: "cat" is not allowed; this key may only push and fetch
```

`kranq key add` only appends and `kranq key rm` only removes lines carrying its
own marker, because that file is usually how you administer the machine and
losing a line locks you out.

### Which to use

| | simple | forced-command key |
| --- | --- | --- |
| setup on the build machine | none | `kranq key add` |
| what the pusher can do there | whatever their key already allowed | push and fetch, nothing else |
| good for | you, from a laptop | a pipeline, a shared credential |

### Pinning the identity, when you have both

A kranq push key sits beside your ordinary key for the same host, and ssh offers
keys in its own order. `--ssh-key` sets `IdentitiesOnly`, or in `~/.ssh/config`:

```
Host kranq-mini
    HostName 142.127.69.2
    Port 333
    User macmini
    IdentityFile ~/.ssh/kranq_push
    IdentitiesOnly yes
```

Without `IdentitiesOnly`, ssh presents the ordinary key, gets a shell, and the
forced command never runs.

### Repositories are made on arrival

A push to a name nothing has used yet creates it. On a machine that should only
accept repositories somebody set up deliberately:

```sh
kranq config set KRANQ_AUTO_CREATE_REPOS=false
```

Then an unknown name is refused, and `kranq repo create <name>` is how one
appears. `kranq repo create` is also what makes a **plain path push** work,
where git runs the real `git-receive-pack` and no kranq code is in the loop to
create anything:

```sh
git push ssh://macmini@buildhost:333/Users/macmini/.kranq/repos/dx.git \
    -o task=ci/tasks/spec.yaml HEAD:refs/heads/run
```

<a name="https"></a>

## Over https

For a caller that cannot ssh to the machine. It needs a tunnel, and a tunnel
brings a request body limit, so prefer ssh where you have the choice.

### On the build machine

```sh
kranq config set KRANQ_HTTP_ADDR=127.0.0.1:8420
kranq daemon stop --force && kranq daemon start
kranq token create ci-dx                    # printed once, stored only as a hash
cloudflared tunnel --url http://127.0.0.1:8420
```

The listener binds loopback. `cloudflared` dials **out**, so no port is opened
and no router is touched. What changes is not the network posture but who can
reach the endpoint.

### From the caller

```sh
export KRANQ_ENDPOINT=https://ci.example.com
export KRANQ_TOKEN=kranq_…
kranq push ci/tasks/spec.yaml --repo dx -e BITBUCKET_TOKEN
```

or with plain git:

```sh
git push "https://kranq:$KRANQ_TOKEN@ci.example.com/git/dx.git" \
    -o task=ci/tasks/spec.yaml HEAD:refs/heads/run
```

The token is the http password. `kranq push` puts it in the URL's userinfo
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
KRANQ-RESULT id=20260905T174031-bd4970a48ed775be status=failed exit=12
```

`kranq push` reads it and exits with the task's own code. Use `kranq push` in a
pipeline, and plain `git push` interactively. Or do it by hand:

```sh
git push kranq main -o task=ci/spec.yaml 2>&1 | tee /tmp/out
grep -q 'KRANQ-RESULT.*exit=0' /tmp/out
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

**A commit under `refs/kranq/*` is invisible to a plain clone**, which only looks
at `refs/heads`. The clone then succeeds and produces an empty tree, so kranq
names each run's commit `refs/heads/kranq/<task-id>`.

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
caller                     kranq host                        VM
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
came from kranq.

<a name="troubleshooting"></a>

## Troubleshooting

| Symptom | Cause |
| --- | --- |
| `shallow update not allowed` | the repository predates `receive.shallowUpdate`; `kranq repo rm <name>` and push again |
| `Permission denied (publickey)` | the key is not installed, or ssh offered a different one — set `IdentitiesOnly yes` |
| `"whoami" is not a git command` | expected: that key can only push and fetch |
| `no task: push with -o task=…` | the required push option is missing |
| `<path> is not in the pushed commit` | the spec is not at that path in what you pushed |
| output arrives in clumps | a proxy in front of the endpoint is buffering; ssh has no proxy |
| push accepted but exit 0 on a failing task | expected; use `kranq push`, or grep for `KRANQ-RESULT` |
| a huge first push over https fails | the tunnel's request body cap; use ssh |

## Limits worth knowing

- A push is one HTTP request, so a tunnel's body limit caps the first push of a
  large tree. Cloudflare's is 100MB on most plans. Later pushes send only what
  changed. **ssh has no such limit.**
- Any valid credential can create a repository by pushing to a new name. That is
  by design: repositories are made on first push.
- `kranq repo ls` shows what has accumulated and `kranq repo rm` clears one out.
