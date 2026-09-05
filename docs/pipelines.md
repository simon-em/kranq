# Wiring it into a pipeline

The pipeline container is Linux and forge cross-compiles to a static
`linux/amd64` binary, which is why the client is Go and not bash: the previous
bash clients broke twice on a `ruby:` image having no `python3` and no `pgrep`.

## The shim

Two files, and neither changes when forge does:

```sh
# infrastructure/ci/forge
#!/bin/sh
set -eu
dir="$(cd "$(dirname "$0")" && pwd)"
. "$dir/forge.lock"                       # version= and sha256 per platform
bin="$HOME/.forge/bin/forge-$version"
if [ ! -x "$bin" ]; then
    mkdir -p "$(dirname "$bin")"
    curl -fsSL "$url" -o "$bin.tmp"
    echo "$sha256_linux_amd64  $bin.tmp" | sha256sum -c -
    chmod +x "$bin.tmp" && mv "$bin.tmp" "$bin"
fi
exec "$bin" "$@"
```

Bumping forge is then a one-line diff to `forge.lock` that reverts cleanly. Add
`$HOME/.forge` to the pipeline's `caches:` so the download happens once.

## Over ssh, which needs no tunnel

```yaml
- step:
    name: spec
    script:
      - export FORGE_ENDPOINT="ssh://$FORGE_HOST"
      - infrastructure/ci/forge push ci/tasks/spec.yaml --repo dx
```

If the pipeline's key already reaches the build machine, that is all of it:
forge asks for itself as the receive-pack, so nothing is set up on the far side
and the repository is created on the first push.

For a pipeline that should be able to push and nothing else, give it its own key
and add it with `forge key add bitbucket-dx <key>.pub`. The forced command then
confines it, which is worth doing for a shared credential even though it is not
required.

If the pipeline has no ambient ssh setup, give it a key in a secured variable and
point `FORGE_SSH_KEY` at a file you write from it.

## Over https

```yaml
- step:
    name: spec
    script:
      - infrastructure/ci/forge push ci/tasks/spec.yaml --repo dx
    # FORGE_ENDPOINT and FORGE_TOKEN as repository variables, FORGE_TOKEN secured
```

## Forwarding what the task needs

```sh
forge push infrastructure/ci/tasks/maintenance.yaml --repo dx \
    -e BITBUCKET_TOKEN \
    -e MAINTENANCE_SCAN_URL \
    -e MAINTENANCE_TEST_CMD \
    --artifacts ./maintenance-output
```

Bare `-e NAME` forwards the value from the pipeline's environment. A task that
writes a branch and opens a pull request needs `BITBUCKET_TOKEN` regardless of
how its source arrived: pushing removes the credential needed to *read* the
code, not the one needed to write to the git host.

## Retrying a step

The likeliest cause of a duplicated effect is not a network partition, it is
someone clicking retry on a failed step. For a task with `effects.push` the fence
handles it: the second attempt cannot claim a fence the first still holds, and it
stops before building anything.

## Variables

| Variable | Secured | Required | What for |
| --- | --- | --- | --- |
| `FORGE_ENDPOINT` | no | yes | `ssh://user@host:port` or `https://host` |
| `FORGE_TOKEN` | **yes** | https only | a `forge token create` secret |
| `FORGE_SSH_KEY` | **yes** | ssh, if no agent | path to a private key |
| `FORGE_REPO` | no | no | defaults from `$BITBUCKET_REPO_SLUG` |

`$CI_BRANCH` / `$BITBUCKET_BRANCH` is picked up automatically for the branch
name a run reports.

## Sharing the pipeline definition

Bitbucket can import a pipeline definition from another repository, so a shared
step can live in `infrastructure` rather than being copy-pasted. Two constraints
shape how far that goes:

- **`import` replaces the whole pipeline definition.** An imported pipeline
  cannot be combined with locally defined steps in the same trigger block, so it
  fits a standalone `custom:` pipeline and not a mixed `pull-requests:` one.
- **It is a Premium-only feature**, and workspace-scoped. Confirm the plan covers
  it before depending on it; the fallback is YAML anchors, which work.

A repository can execute an exported configuration even if the caller has no
access to the exporting repo, so an exported pipeline is effectively readable
workspace-wide. Never put a secret in one. Variables resolve from the *importing*
repository, which is what makes one definition work across projects.
