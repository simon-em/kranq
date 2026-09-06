# Installing with Homebrew

```sh
brew tap simontlbt/forge https://github.com/simontlbt/forge
brew install simontlbt/forge/forge
forge doctor
```

That is the whole install. Lima is fetched and verified the first time a command
actually needs a VM, so there is no second step to remember.

The formula **builds from source**, so there is no binary to host or sign. It
needs Go, which brew installs as a build dependency.

## The repository is its own tap

There is no separate `homebrew-forge` repository to keep in step. Homebrew reads
formulae from `Formula/`, `HomebrewFormula/` or a repository's root, and this
one keeps its formula in [`Formula/forge.rb`](../Formula/forge.rb).

The url has to be given explicitly. `brew tap simontlbt/forge` on its own would
look for `github.com/simontlbt/homebrew-forge`, which is the naming convention
the shorthand assumes; passing the url says where it really is.

## Cutting a release

Tag it, push the tag, then point the formula at the tarball and push that:

```sh
git tag -a v0.2.0 -m "forge 0.2.0"
git push origin v0.2.0

curl -sL -o /tmp/forge.tar.gz \
    https://github.com/simontlbt/forge/archive/refs/tags/v0.2.0.tar.gz
shasum -a 256 /tmp/forge.tar.gz
```

Then two lines in `Formula/forge.rb`:

```ruby
  url "https://github.com/simontlbt/forge/archive/refs/tags/v0.2.0.tar.gz"
  sha256 "<that checksum>"
```

The formula on `main` always points at the **last tag**, never at `main` itself,
so the checksum commit necessarily lands after the tag it describes. That is
normal and not a mistake to fix.

`version` is inferred from the url, and the build injects it into the binary:
`forge version` prints `0.2.0`, not `dev`. `brew install --HEAD simontlbt/forge/forge`
builds the branch tip instead, ignoring tags.

## What brew owns and what forge owns

brew owns **the binary and PATH**. forge owns everything else, deliberately:

- **Lima is not a brew dependency.** forge keeps its own under `~/.forge/deps`
  and calls it by absolute path, so a Homebrew lima appearing, disappearing or
  changing version cannot change what runs. It is verified against a checksum
  compiled into the binary *and* the published `SHA256SUMS`, which must agree.
- It is fetched the first time a command needs a VM, so nothing has to be run
  after `brew install`. `forge install --deps-only` does it ahead of time
  instead, without copying the binary or editing PATH, which is what keeps
  brew's copy the only copy.

```sh
forge install --deps-only                  # fetch lima now rather than later
forge install --deps-only --with-daemon    # and a launchd job
```

Or use brew's own service supervision instead of forge's launchd job:

```sh
brew services start simontlbt/forge/forge
```

Pick one, not both: two supervisors starting the same daemon means the second
finds the lock held and gives up.

## Upgrading

```sh
brew upgrade forge
```

**`forge upgrade` refuses to touch a binary a package manager owns.** Replacing
a file inside a Cellar leaves brew's record of it wrong, and the next
`brew upgrade` silently undoes whatever was put there:

```
$ forge upgrade ./forge
forge: /opt/homebrew/bin/forge is managed by homebrew
use `brew upgrade forge`
replacing it here would be undone by the next brew upgrade
```

`forge upgrade` and `forge rollback` remain the right tools on a machine where
forge installed itself, and `forge peer upgrade` uses them on a build machine.

## Uninstalling

```sh
brew services stop simontlbt/forge/forge   # if you used brew's supervision
brew uninstall forge
rm -rf ~/.forge                 # state, lima, task history, images metadata
```

`forge uninstall` is for a self-installed copy; with brew, `brew uninstall` is
the one to use.

## Which install to use where

| | how |
| --- | --- |
| your laptop, as a client | `brew install simontlbt/forge/forge` |
| a build machine you can ssh to | `forge peer upgrade <name>` from your laptop |
| a build machine, by hand | `forge install --with-daemon` |
| a CI container | the download shim, see [pipelines.md](pipelines.md) |

A CI container should not use brew: it is a per-run download of a pinned,
checksummed binary, which is what the shim does.
