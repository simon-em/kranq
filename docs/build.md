# The Kranqfile

Every repository has a `Kranqfile` at its root describing the environment its
jobs run in. It reads like a Dockerfile, because it is doing the same job:

```
MEMORY 6GiB
CPUS 4

RUN sudo apt-get update
RUN sudo apt-get install -y --no-install-recommends default-jdk libvips

COPY .ruby-version .
RUN ruby-build "$(cat .ruby-version)" /opt/ci/ruby

COPY Gemfile Gemfile.lock .
RUN bundle install
```

There is no `FROM`: every Kranqfile starts from kranq's own base image, and
there is nothing to choose. There is no `CMD` or `ENTRYPOINT` either — a layer
is an environment, not a service, and what runs in it is the task.


## Values the build takes from the caller

A build sees nothing it has not asked for. Forwarding the caller's whole
environment would put `BITBUCKET_COMMIT` in every layer's hash and rebuild the
image on every push, so a Kranqfile names the few values it actually needs.

```
ARG RUBY_VERSION=3.4.1     # part of the layer's identity
SECRET BITBUCKET_TOKEN     # available to RUN, and hashed by name only
```

Both are read from what the caller forwards — the task environment, so
`-o env.NAME=` or `KRANQ_FORWARD_ENV` — and both are exported to every `RUN` at
or after the declaration.

| | in the hash | absent is | for |
| --- | --- | --- | --- |
| `ARG` | name **and value** | an error, unless it has a default | something that changes what gets built |
| `SECRET` | name only | fine; the `RUN` decides | a credential the build needs to fetch something |

**Why the split.** An arg's value makes the layer: two builds that differ by one
are two layers rather than a collision, which is what makes `ARG` safe to have
at all. A credential is the opposite — `bundle install` produces the same gems
whoever fetched them, so hashing the token would rebuild every image the day it
is rotated. Measured against the build machine:

```
BUILD_TOKEN=secret-one     -> building kranq-layer-01-78ff2ec32c65
BUILD_TOKEN=rotated        -> cached as kranq-layer-01-78ff2ec32c65
TOOL_VERSION=9.9           -> building kranq-layer-01-0ddce511ba3a
TOOL_VERSION back to 2.0   -> cached as kranq-layer-01-78ff2ec32c65
```

A secret's *name* is hashed, because a `RUN` that can suddenly see a token may
do something else, and that is a different layer.

**What a secret does not do.** It keeps the credential out of the layer's name
and out of its stored environment; it cannot keep it out of whatever the `RUN`
does with it. A `RUN` that echoes one puts it in the build log, and a layer
built with one still contains what it fetched — and that layer is shared with
any project whose steps hash the same. On a machine serving one team that is
the intent; it is not an access boundary.


## RUN is the layer boundary

Each `RUN` produces a real, stopped VM image. `COPY`, `ENV` and `WORKDIR` stage
into the next `RUN` rather than making images of their own, because an image has
to be **stopped to be cloned**, so a layer here costs a VM boot rather than a
filesystem commit. Making one for a two-line `COPY` would buy nothing and cost
thirty seconds.

The cache behaves the way you expect regardless: a layer's identity covers the
files it copies, so editing `Gemfile.lock` invalidates from `bundle install`
onward and leaves everything before it alone.

```
$ kranq validate Kranqfile
LINE  INSTRUCTION                        FILES  IMAGE
4     RUN sudo apt-get update                0  kranq-layer-01-97ab2ab29342
5     RUN sudo apt-get install -y --no-...   0  kranq-layer-02-8297b861bb31
8     RUN ruby-build "$(cat .ruby-versi...   1  kranq-layer-03-0cf0d6d9e047
11    RUN bundle install                     2  kranq-layer-04-2b3a3377b10b
```

Group commands into one `RUN` where you would not want them cached separately,
exactly as you would with `RUN a && b` in a Dockerfile.

## Layers are diffs, not copies

Measured, not assumed. Cloning the 2.9 GB base:

```
$ df -k /   # free before
299.134 GiB
$ limactl clone kranq-base-8517c8a5d2-1478 probe
real 0.10
$ df -k /   # free after
299.134 GiB
```

Zero bytes and a tenth of a second, because `limactl clone` is an APFS
copy-on-write clone: the child shares every block with its parent until it
writes. A layer is then charged only for what it changed — writing 400 MB inside
the clone cost 0.45 GiB of real disk, and nothing else did.

**`du` will lie to you.** On a base plus two layers it reports 2.9 GB three
times over:

```
$ du -shc ~/.lima/kranq-*
2.9G  kranq-base-8517c8a5d2-1478
2.9G  kranq-layer-01-ee1199af5078
2.9G  kranq-layer-02-275d7293032b
8.7G  total

$ df   # before and after deleting both layers
the two layers actually cost 251 MiB
```

It counts blocks per file, and a shared block belongs to both files. The honest
number is the change in free space, never the sum of the directories.

## State survives in a layer

A layer is a whole disk, not a filesystem overlay, so seeding a database is a
legitimate thing to do:

```
RUN sudo apt-get install -y postgresql && sudo systemctl enable postgresql

COPY db/structure.sql db/seed.sql .
RUN sudo -u postgres createdb app && \
    sudo -u postgres psql app -f structure.sql -f seed.sql
```

Every job then starts with the data already loaded and pays nothing for it, and
editing `seed.sql` reloads it without reinstalling postgres. `systemctl enable`
is the part people kranqt: the layer captures the data either way, but the
service has to be set to come back when the job's VM boots.

## Layers are shared between repositories

A layer's name is a hash of its parent, its command, its environment, and the
**contents** of every file it copies. It contains no repository name, no branch
and no label.

So two projects that install the same packages against the same base share that
layer, and whichever one builds it first builds it for both. Pinning the same
Ruby version with the same `.ruby-version` shares the Ruby layer too. They
diverge at the first instruction that actually differs.

This is why `ci/basekey.txt` is gone. It listed files whose checksums invalidated
the image; now you `COPY` the files you depend on and their contents *are* the
key. A file that matters to the build is in the build.

## Instructions

| | |
| --- | --- |
| `RUN <command>` | bash, in a login shell, in `WORKDIR`. Ends a layer. |
| `RUN <<EOF` … `EOF` | the same, over many lines, with no trailing backslashes |
| `COPY <src>… <dest>` | from the repository root into the image |
| `WORKDIR <abs path>` | for every instruction after it; defaults to `/kranq/build` |
| `ENV NAME=VALUE …` | exported for every `RUN` after it, in order |
| `ARG NAME[=default]` | a value from the caller, part of the layer's identity |
| `SECRET NAME` | a credential from the caller, hashed by name only |
| `MEMORY`, `CPUS`, `DISK` | how big the VM is. Dockerfiles have no equivalent; VMs need one. |

Comments are `#`, and a trailing `\` continues a line.

`ENV` keeps its order and its values expand, so `ENV PATH=/opt/ci/ruby/bin:$PATH`
means what it looks like. It applies to the build, not to the job: to put
something on a job's `PATH`, write it to `/etc/profile.d/` from a `RUN`.

`RUN <<EOF` is not a nicety. A real setup script is dozens of lines, and without
a heredoc every one of them needs a trailing backslash:

```
RUN <<EOF
export DEBIAN_FRONTEND=noninteractive
sudo apt-get update
sudo apt-get install -y --no-install-recommends default-jdk libvips
EOF
```

Instructions a Dockerfile has and a Kranqfile refuses — `FROM`, `CMD`,
`ENTRYPOINT`, `ADD`, `EXPOSE`, `USER`, `VOLUME`, `LABEL`, `HEALTHCHECK`,
`ONBUILD`, `SHELL`, `STOPSIGNAL` — each fail by name with what kranq does
instead, because what someone pastes a Dockerfile in for is usually one of them.

`ADD` is refused rather than aliased to `COPY`: it also unpacks archives and
fetches URLs, which hides what a layer contains and makes it depend on a server.

## COPY

Destinations follow Docker's rules. `COPY a b dest/` and `COPY a .` treat the
destination as a directory; `COPY db/seed.sql /tmp/seed.sql` renames. A directory
source spills its contents into the destination. Relative destinations are
relative to `WORKDIR`; absolute ones go where they say, including places the
build user cannot write — files are staged and installed with sudo, then handed
to the build user, since that is who the `RUN` reading them is.

`COPY` refuses a pattern that matches nothing, refuses to reach outside the
repository, and skips any `.git` directory it walks into — two clones of one
commit have different packfiles, so copying one in would give every machine a
different layer for identical source.

Patterns are shell globs, not `**`: name a directory and it is copied
recursively. A symlink anywhere in what you copy is an error rather than being
followed, which is also what stops one pointing out of the repository.

File modes are normalised to 0755 or 0644 before hashing, because the executable
bit is the only permission git records and anything else would make a layer
depend on the builder's umask.

## Resources

`MEMORY` and `CPUS` size both the layer builds and the job's VM — a 1 GiB base
cannot run a `bundle install` — but they are **not** part of a layer's identity.
The job's own clone asks for them again, so a layer shared with a project that
wanted less memory still runs your job at your size.

`DISK` is different, and it *is* folded into the chain. Lima can grow a disk when
cloning but never shrink one, so two projects asking for different sizes must not
share a chain. Setting `DISK` therefore costs you sharing with projects that
don't; leaving it out is usually right.

## Naming a different file

Both of these read `Kranqfile.staging` from the repository root:

```sh
kranq run ci/tasks/spec.yaml --kranqfile Kranqfile.staging
```

```yaml
name: spec
kranqfile: Kranqfile.staging
steps:
  - run: bundle exec rspec
```

The flag wins over the field. `COPY` paths stay relative to the repository root
wherever the file itself lives.

## Building ahead of time

```sh
kranq image build --repo dx --ref main
kranq image build --repo dx --ref main --kranqfile Kranqfile.staging
kranq image ls
```

`kranq image ls` shows each image with the instruction it came from and when it
was last used.

## Pruning

`kranq image prune` keeps the `KRANQ_KEEP_IMAGES` (default 3) most recently used
chain *heads* — the layer a job actually clones — together with everything they
are built on. Deleting an ancestor of a chain you still want costs a full rebuild
of everything above it, so ancestry is protected rather than aged out on its own.

A layer is only ever considered usable once kranq has recorded that its build
finished. `limactl` creates an instance directory the moment a clone starts, so
existence alone would hand a job a layer whose build is still running, or one
left behind by a machine that died mid-build.

Concurrent builds of the same layer are serialised by a `flock` in
`$KRANQ_HOME/layers`, not an in-process mutex: jobs run in separate `kranq exec`
processes, so a mutex would not see them.
