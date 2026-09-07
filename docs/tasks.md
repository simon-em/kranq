# Writing a task

A task is one YAML file. It is compiled to a single bash script and run inside a
disposable VM cloned from the project's image. `kranq render <task.yaml>` prints
that script, which is the fastest way to understand what a spec does.

```yaml
name: spec
label: rspec                  # names the VM and artifact dir; defaults to name
repo: dx                      # usually supplied by --repo instead
branch: main                  # usually supplied by --branch instead
artifacts: ci-artifacts       # directory inside the checkout to collect

resources:
  memory: 6GiB
  cpus: 4
  disk: 40GiB

env:
  RAILS_ENV: test

steps:
  - name: install
    run: bundle install --jobs 4

  - name: rspec
    run: bundle exec rspec
```

## Steps are one script, not many

Every step is concatenated into **one** bash script, so an exported variable set
in step 1 is still there in step 4. Real tasks depend on this. A test asserts it,
so nobody refactors the property away.

Each step has either `run` or `claude`, never both.

| Field | Meaning |
| --- | --- |
| `name` | shown in the log as `===== name =====` |
| `run` | bash |
| `claude` | a prompt, run through the Claude CLI |
| `continue_on_error` | keep going if this step fails |

## Claude steps

```yaml
  - name: review the diff
    claude: |
      Review the changes on this branch and write your findings to
      ci-artifacts/review.md.
    allowed_tools: [Read, Grep, Bash]
    disallowed_tools: [WebFetch]
    max_turns: 40
    model: claude-opus-5
    effort: high
    permission_mode: acceptEdits
    mcp_servers:
      bitbucket:
        command: /opt/ci/mcp/bitbucket.py
        env:
          BITBUCKET_TOKEN: ${BITBUCKET_TOKEN}
```

`permission_mode` is one of `acceptEdits`, `auto`, `bypassPermissions`,
`manual`, `dontAsk`, `plan`.

Claude's output is streamed to the log as it happens, with elapsed timestamps,
rather than appearing all at once at the end.

**A task's own status outranks its exit code.** `claude -p` exits 0 even when it
stops to ask a question, so a task that must be judged on its result should write
one to a file and have a later step read it. Both real tasks do this.

**Usage exhaustion is not a failure.** When the subscription runs out, the task
exits 75, the daemon holds it as `blocked` on `claude`, and it resumes when usage
returns. A known reset time is used if the stream reported one; otherwise the
gate is re-checked on a flat interval. Rotating the token opens the gate at once,
because the gate is keyed by a hash of the token.

## Effects, for a task that changes something

A task that pushes a branch and opens a pull request must do it at most once,
even if kranq loses track of the machine running it.

```yaml
effects:
  push: true          # take a fence for the duration
  key: maintenance    # what it contends on; defaults to the task name
  scope: branch       # branch (default), or repo for a repo-wide effect
```

With this set, two things change inside the VM:

- `kranq_push <refspec>...` becomes available. It ties the push to the run's
  fence in one atomic push, so it lands only if the fence is still ours.
- a plain `git push` **fails**, with an error naming `kranq_push`. Without that
  the helper would be advisory and one stray push would bypass the whole thing.

See [fence.md](fence.md) for what that protects against and what it does not.

## Resources

`memory` accepts `GiB`, `GB`, `G`, `MiB`, `MB`, `M`. A task asking for more than
the machine has is refused at submit time rather than queued forever.

`cpus` is enforced: the scheduler will not admit a task whose CPUs do not fit
alongside what is already running.

## Environment

Three sources, later winning:

1. `env:` in the spec
2. `-e NAME=VALUE`, or bare `-e NAME` to forward it from the caller
3. `CLAUDE_CODE_OAUTH_TOKEN`, injected when the task has a Claude step

A forwarded `BITBUCKET_TOKEN` (or `KRANQ_GIT_TOKEN`, `BITBUCKET_API_TOKEN`,
`BITBUCKET_STEP_OAUTH_TOKEN`) also becomes the checkout credential: the VM clones
over https with it instead of needing a forwarded ssh agent. That is what lets a
task run with no ambient ssh setup at all.

Secrets are stored in a separate `secrets.json` in the task directory, so
`kranq ps`, `kranq status` and any diagnostic dump structurally cannot include them.

## The project's own configuration

Each repository supplies a `Kranqfile` at its root describing the environment its
jobs run in. It reads like a Dockerfile, and each `RUN` is a layer:

```
MEMORY 6GiB
CPUS 4

COPY .ruby-version .
RUN ruby-build "$(cat .ruby-version)" /opt/ci/ruby

COPY Gemfile Gemfile.lock .
RUN bundle install
```

Editing `Gemfile.lock` rebuilds `bundle install` and nothing before it. A layer's
identity is its parent plus its command plus the contents of the files it copies,
and carries no repository name, so two projects doing identical work share the
layer. Full detail in [build.md](build.md).

A task can name a different Kranqfile, and `--kranqfile` overrides that:

```yaml
name: perf
kranqfile: Kranqfile.perf
```

## Artifacts

`artifacts:` names a path inside the checkout, defaulting to `ci-artifacts`. Set
it to wherever the tool that writes the output already puts it:

```yaml
artifacts: playwright/playwright-report
```

That path is committed even though it is almost always gitignored, which is what
lets a run hand it back. Two ways to collect it:

- **Pull the run.** `git pull --ff-only kranq task/<run>` brings back the job's
  own commit: files it changed *and* the artifacts path, at the paths the job
  wrote them. Nothing is unpacked or renamed, so a pipeline can point its own
  artifact paths straight at them.
- **`--artifacts DIR`**, which copies the directory out of the VM. Older, still
  works, and does not need the repository the push landed in.

Either way they come back **even when the job fails**, which is when a test
report is worth having.
