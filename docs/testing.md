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
