# Installing with Homebrew

```sh
brew tap simon-em/kranq https://github.com/simon-em/kranq
brew install simon-em/kranq/kranq
kranq doctor
```

That is the whole install. Lima is fetched and verified the first time a command
actually needs a VM, so there is no second step to remember.

The formula **builds from source**, so there is no binary to host or sign. It
needs Go, which brew installs as a build dependency.

## The repository is its own tap

There is no separate `homebrew-kranq` repository to keep in step. Homebrew reads
formulae from `Formula/`, `HomebrewFormula/` or a repository's root, and this
one keeps its formula in [`Formula/kranq.rb`](../Formula/kranq.rb).

The url has to be given explicitly. `brew tap simon-em/kranq` on its own would
look for `github.com/simon-em/homebrew-kranq`, which is the naming convention
the shorthand assumes; passing the url says where it really is.

## "Untrusted" is expected

Homebrew 6 will not load formulae from a third-party tap until you say so, and
`brew tap-info simon-em/kranq` reports **Untrusted** for the tap itself.
Installing it by its full name is the consent, so nothing extra is needed:

```sh
$ brew install simon-em/kranq/kranq      # works, non-interactively
$ cat ~/.homebrew/trust.json
{ "trustedformulae": ["https://github.com/simon-em/kranq/kranq"] }
```

Verified by deleting that file and installing again with stdin closed. To trust
the whole tap ahead of time instead: `brew trust --tap simon-em/kranq`.

## Why the name is free

`kranq` is absent from homebrew/core's 8591 formulae, from the casks, from PyPI
and from npm. That was a requirement, not luck: the tool used to be called
`forge`, and `homebrew/core/forge` is arrayfire's "High Performance
Visualization" library, so `brew install forge` fetched a graphics library.

**The tap step is not optional, and the url is not optional either.** Naming the
tap in the install alone does not work: brew derives the remote from the name
and goes looking for `simon-em/homebrew-kranq`, which does not exist.

```
$ brew install simon-em/kranq/kranq        # without tapping first
Error: ... git clone https://github.com/simon-em/homebrew-kranq ... exited with 128
```

So it is always two commands, and the first carries the url:

```sh
brew tap simon-em/kranq https://github.com/simon-em/kranq
brew install simon-em/kranq/kranq
```

## Cutting a release

Tag it, push the tag, then point the formula at the tarball and push that:

```sh
git tag -a v0.2.0 -m "kranq 0.2.0"
git push origin v0.2.0

curl -sL -o /tmp/kranq.tar.gz \
    https://github.com/simon-em/kranq/archive/refs/tags/v0.2.0.tar.gz
shasum -a 256 /tmp/kranq.tar.gz
```

Then two lines in `Formula/kranq.rb`:

```ruby
  url "https://github.com/simon-em/kranq/archive/refs/tags/v0.2.0.tar.gz"
  sha256 "<that checksum>"
```

The formula on `main` always points at the **last tag**, never at `main` itself,
so the checksum commit necessarily lands after the tag it describes. That is
normal and not a mistake to fix.

`version` is inferred from the url, and the build injects it into the binary:
`kranq version` prints `0.2.0`, not `dev`. `brew install --HEAD simon-em/kranq/kranq`
builds the branch tip instead, ignoring tags.

## What brew owns and what kranq owns

brew owns **the binary and PATH**. kranq owns everything else, deliberately:

- **Lima is not a brew dependency.** kranq keeps its own under `~/.kranq/deps`
  and calls it by absolute path, so a Homebrew lima appearing, disappearing or
  changing version cannot change what runs. It is verified against a checksum
  compiled into the binary *and* the published `SHA256SUMS`, which must agree.
- It is fetched the first time a command needs a VM, so nothing has to be run
  after `brew install`. `kranq install --deps-only` does it ahead of time
  instead, without copying the binary or editing PATH, which is what keeps
  brew's copy the only copy.

```sh
kranq install --deps-only                  # fetch lima now rather than later
kranq install --deps-only --with-daemon    # and a launchd job
```

Or use brew's own service supervision instead of kranq's launchd job:

```sh
brew services start simon-em/kranq/kranq
```

Pick one, not both: two supervisors starting the same daemon means the second
finds the lock held and gives up.

## Upgrading

```sh
brew upgrade simon-em/kranq/kranq
```

**`kranq upgrade` refuses to touch a binary a package manager owns.** Replacing
a file inside a Cellar leaves brew's record of it wrong, and the next
`brew upgrade` silently undoes whatever was put there:

```
$ kranq upgrade ./kranq
kranq: /opt/homebrew/bin/kranq is managed by homebrew
use `brew upgrade simon-em/kranq/kranq`
replacing it here would be undone by the next brew upgrade
```

`kranq upgrade` and `kranq rollback` remain the right tools on a machine where
kranq installed itself, and `kranq peer upgrade` uses them on a build machine.

## Uninstalling

```sh
brew services stop simon-em/kranq/kranq   # if you used brew's supervision
brew uninstall kranq
rm -rf ~/.kranq                 # state, lima, task history, images metadata
```

`kranq uninstall` is for a self-installed copy; with brew, `brew uninstall` is
the one to use.

## Which install to use where

| | how |
| --- | --- |
| your laptop, as a client | `brew install simon-em/kranq/kranq` |
| a build machine you can ssh to | `kranq peer upgrade <name>` from your laptop |
| a build machine, by hand | `kranq install --with-daemon` |
| a CI container | the download shim, see [pipelines.md](pipelines.md) |

A CI container should not use brew: it is a per-run download of a pinned,
checksummed binary, which is what the shim does.
