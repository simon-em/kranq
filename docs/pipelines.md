# Wiring it into a pipeline

There is nothing to install. A pipeline pushes with git and reads the result
with git, so the only requirement is an ssh key that reaches the build machine.

```yaml
- step:
    name: spec
    script:
      - source infrastructure/ci/kranq-setup
      - git push kranq -o task_file=ci/tasks/spec.yaml "${KRANQ_OPTS[@]}"
      - git fetch kranq "$KRANQ_OK"
```

This replaced a downloaded Go client behind a checksum-pinned shim. The client
existed to stream logs and turn a result into an exit code; `git push` streams
the hook's output already, and the exit code is a ref that is either there or
not.

## If the pipeline's key already reaches the machine

Then it can already push. A repository given by its real path needs nothing
intercepting the connection: stock `git-receive-pack` runs, and the hooks inside
it are kranq. So there is no forced command, no `receivepack` override, and no
new credential — the URL is the whole of it.

`kranq setup` leaves one repository that takes every codebase, at a path in the
home directory:

```
KRANQ_URL=ssh://macmini@142.127.69.2:333/~/kranq.git
```

Nothing is created per project. One repository is enough because a layer's name
does not contain one — it is a hash of its parent, its command and the contents
of what it copies — so projects with the same Kranqfile steps already share
layers wherever they arrive.

What the code *is* travels beside it as `-o repo=<name>`, which `kranq-ci` sends
from `CI_REPO` or `BITBUCKET_REPO_SLUG`. That is the name a fence and a spec's
own `repo:` are about; the URL only says where the objects go.

On port 22, `macmini@142.127.69.2:kranq` works as well — git resolves a bare
name by appending `.git`. Not on another port: scp-style syntax has no field
for one.

## Or issue the pipeline its own key

```sh
kranq setup ci-dx
```

`kranq setup` alone makes the machine ready and issues nothing. Naming a key is
what asks for a credential, and then it prints what the pipeline needs:

```
KRANQ_PEER=macmini@142.127.69.2:333
KRANQ_HOST_KEY=[142.127.69.2]:333 ssh-ed25519 AAAAC3Nz…
KRANQ_SSH_KEY<<EOF
-----BEGIN OPENSSH PRIVATE KEY-----
…
EOF
```

`KRANQ_PEER` is the only one that must be set. The other two are for a caller
with no ssh identity of its own; a pipeline that forwards an agent reaching the
machine needs neither. Set `KRANQ_SSH_KEY` secured.

Nothing is pre-created: a repository comes into being on its first push.

**The address is the one thing it cannot work out.** A machine behind a
forwarded port sees only the port sshd is bound to, so `setup` uses the address
you arrived on and says that it guessed. Pass `--host 142.127.69.2:333`, or run
it from a laptop, where the peer registry already holds it:

```sh
kranq setup ci-dx --peer mini-1
```

**Why there is no `receivepack` to configure.** The key is installed as a forced
command, so ssh runs kranq rather than what git asked for and passes the request
in `SSH_ORIGINAL_COMMAND`. kranq resolves the repository from that, which is the
thing the client would otherwise have to be told. It also means the key gets no
shell:

```
$ ssh -i ci-dx macmini@142.127.69.2 -p 333 id
kranq: "id" is not a git command
```

## Getting the results back

A run is a branch: the name pushed to is the name pulled from, and when the job
finishes that ref points at a commit whose parent is the commit that was pushed.

```yaml
- git push kranq -o task_file=ci/tasks/playwright.yaml "${KRANQ_OPTS[@]}"
- git pull --ff-only kranq "$KRANQ_TASK"
- git fetch kranq "$KRANQ_OK"
artifacts:
  paths:
    - playwright/playwright-report/**
```

The pull brings back files the job changed and whatever it wrote into its
`artifacts:` path, at the paths it wrote them, so a report ends up where the
tool that made it already put it and the pipeline's own `artifacts: paths:` can
name it directly. Nothing is archived, copied or renamed.

It goes **before** the verdict because a report is worth having precisely when
the run failed.

## Turning a step red

`git push` exits 0 whenever the push was accepted, whatever the task did:
post-receive runs after the ref has moved and nothing it returns reaches git's
exit status. Measured, with a task exiting 12, the push exited 0.

So the verdict is a ref. `ok/<run>` exists only if the task succeeded, and
`git fetch` of a missing ref exits 128.

It cannot be folded into the pull. Measured: `git pull kranq task/<run> ok/<run>`
with the pass ref missing exits 1 and merges nothing, so a failing run would take
its own report down with it. The pull always succeeds and the fetch is the check,
which is why the fetch is last.

## Retrying a step

Each `kranq-setup` picks its own run name, so a retried step pushes to a ref
that does not exist yet and always runs. A push to a ref already holding that
commit is "Everything up-to-date": no hook, no run, and no output to say so.

## The other way in

An ordinary account that can already ssh to the machine can push too, but git
would ask it for `git-receive-pack '/dx.git'` and that is not a path there. Set
`KRANQ_REMOTE_BIN` to where kranq lives and `kranq-setup` names it as the
remote's receive-pack and upload-pack instead.

Do not set it for a `setup` key. Measured: a fetch with
`remote.kranq.uploadpack` set on such a key fails with *the repository argument
is not quoted as git quotes it*, because `--upload` arrives inside
`SSH_ORIGINAL_COMMAND` and lands in the repository argument.
