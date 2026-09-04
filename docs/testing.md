# Testing

## The suite

```sh
go test ./...
```

Everything runs against `vm.Fake` and finishes in a couple of seconds. No VM is created, so
this is safe to run anywhere.

Two exceptions worth knowing:

- `internal/vm/lima_test.go` writes a stub `limactl` into a temp dir and puts it on PATH, and
  parses real `limactl list --format json` output checked in at `internal/vm/testdata/list.json`.
- `internal/sshagent/agent_test.go` skips rather than fails when the machine has no ssh
  identity, since it cannot manufacture one.

## Running a real job

Needs Lima and an ssh key or a forwarded token. On a machine with neither, forge says so
rather than hanging.

```sh
go build -o /tmp/forge .
/tmp/forge image build                                   # base only, ~2.5 min
/tmp/forge image build --repo dx --ref ci/lima           # + project layer, ~5 min
/tmp/forge run smoke.yaml --repo dx --branch ci/lima --artifacts ./out
```

A useful smoke spec, which proves the checkout is real, Docker works inside the VM, exports
survive across steps, and artifacts come back:

```yaml
name: smoke
resources:
  memory: 2GiB
steps:
  - name: prove the checkout is real
    run: |
      echo "branch: $(git rev-parse --abbrev-ref HEAD)"
      test -f ci/setup.yaml && echo "ci/setup.yaml is present"
  - name: prove the toolchain came from the image
    run: |
      docker run --rm hello-world 2>&1 | grep -q "Hello from Docker" && echo "docker runs containers"
  - name: prove exported vars survive across steps
    run: export CARRIED=yes
  - name: read it back in a later step
    run: |
      test "$CARRIED" = yes && echo "exports persist"
      mkdir -p ci-artifacts && echo proof > ci-artifacts/proof.txt
```

Lima's own progress output is noisy. To read only forge's:

```sh
/tmp/forge run smoke.yaml --repo dx --branch ci/lima 2>&1 \
  | grep -viE 'hostagent|Time sync|Forwarding UDP|^\|'
```

## Cleaning up

```sh
/tmp/forge image ls
/tmp/forge image prune          # keeps the newest 3 per layer, never touches Running
```

Images are large: a base is ~2.6 GB and a dx project layer ~5.5 GB. They are the cache, so
deleting them costs the build time above, not correctness.

## Known cosmetic issue

Step headers can appear out of order relative to step output, because `ci_step` writes to
stderr and step bodies write to stdout, and both go through one pipe with different buffering.
The system forge replaces does this too. The `-o ndjson` event stream planned for phase 2
fixes it properly; anything sooner would be papering over it.

## Debugging a failed run

By default a job VM is destroyed whether the job passed or failed, which means a failure that
only reproduces on the build machine cannot be investigated. Keep it:

```sh
forge run spec.yaml --repo dx --branch main --keep-vm on-failure
forge vm ls                       # which VMs are alive, and whose task they were
forge vm shell <task-id>          # a shell inside it
forge vm shell <task-id> -- cat /tmp/whatever
forge vm rm --all                 # they are not cleaned up on their own
```

`--keep-vm on-failure` also keeps the VM when the run failed for an infrastructure reason with
no exit code at all, which is the case most worth looking at.

A kept VM holds several GB and a concurrency slot until removed, so `forge vm ls` is worth
checking after a debugging session.
