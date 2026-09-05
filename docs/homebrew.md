# Installing with Homebrew

`brew install` from a tap of your own. The formula **builds from source**, so
nothing has to be hosted anywhere and a private repository works with the ssh
key you already have.

```sh
brew tap effetmonstre/tap git@bitbucket.org:effetmonstre/homebrew-tap.git
brew install forge
forge doctor
```

That is the whole install. Lima is fetched and verified the first time a command
actually needs a VM, so there is no second step to remember.

## Setting up the tap, once

A tap is a git repository named `homebrew-<something>` with a `Formula/`
directory. It can live on Bitbucket; only the GitHub shorthand assumes GitHub,
and passing the url explicitly works for any host.

```sh
mkdir homebrew-tap && cd homebrew-tap && git init
mkdir Formula
cp path/to/forge/packaging/homebrew/forge.rb Formula/forge.rb
git add -A && git commit -m "forge"
git remote add origin git@bitbucket.org:effetmonstre/homebrew-tap.git
git push -u origin main
```

Then, on any machine:

```sh
brew tap effetmonstre/tap git@bitbucket.org:effetmonstre/homebrew-tap.git
brew install forge
```

An ssh url means a private tap and a private source repository both work with
the key that is already set up. No token, nothing to host.

## Cutting a release

```sh
cd forge
git tag -a v0.2.0 -m "v0.2.0"
git push origin v0.2.0
git rev-parse v0.2.0^{commit}      # NOT `git rev-parse v0.2.0`
```

Then in the tap, update three lines and push:

```ruby
  url "…", using: :git, tag: "v0.2.0", revision: "<that commit>"
  version "0.2.0"
```

> **`revision:` must be the commit the tag points at, not the tag object.**
> For an annotated tag `git rev-parse v0.2.0` returns the *tag object*, and brew
> refuses the download with `tag should be X but is actually Y`. Append
> `^{commit}`. This is the one thing that will waste your afternoon.

`brew install --HEAD forge` builds the branch tip instead, ignoring tags.

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
brew services start forge
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
brew services stop forge        # if you used brew's supervision
brew uninstall forge
rm -rf ~/.forge                 # state, lima, task history, images metadata
```

`forge uninstall` is for a self-installed copy; with brew, `brew uninstall` is
the one to use.

## Which install to use where

| | how |
| --- | --- |
| your laptop, as a client | `brew install forge` |
| a build machine you can ssh to | `forge peer upgrade <name>` from your laptop |
| a build machine, by hand | `forge install --with-daemon` |
| a CI container | the download shim, see [pipelines.md](pipelines.md) |

A CI container should not use brew: it is a per-run download of a pinned,
checksummed binary, which is what the shim does.
