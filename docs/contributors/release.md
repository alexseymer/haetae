# Release builds

## Cut a release

```bash
git tag v0.1.0            # optional; without a v* tag the version is 0.8.0-<shortsha>[-dirty]
make release              # npm ci + web build, then bin/dist and bin/release
make release-image        # server image from the release binaries
make test-release         # verifies the artifacts and the image (needs Docker)
make bench-release-rss    # agent RSS on the release binary, 3 minutes (needs Docker)
```

`RELEASE_VERSION=x.y.z make release release-image` overrides the version. The version and
the short commit are stamped with `-ldflags -X internal/version.Version/Commit`, so
`<binary> --version` prints `<name> <version> (<commit>)`.
All binaries are `CGO_ENABLED=0`, `-trimpath`, `-s -w`. `make release` reuses `make dist`
for agent and inventory (the files the server serves at `/download/`, names unchanged, with
their own `bin/dist/SHA256SUMS`) and builds the server with the web UI embedded.
A `-dirty` suffix means the tracked files differ from HEAD; do not publish such a build.

## Artifacts (bin/release)

| File | Notes |
|---|---|
| `haetae-agent-linux-{amd64,arm64}` | about 10 MB each |
| `haetae-inventory-linux-{amd64,arm64}` | about 75-80 MB each (Syft library) |
| `haetae-server-linux-{amd64,arm64}` | about 72-77 MB each, web UI embedded |
| `haetae-<version>-linux-{amd64,arm64}.tar.gz` | the three binaries (unsuffixed names), LICENSE, README.md; about 51-56 MB |
| `SHA256SUMS` | `sha256sum` format for all eight files above |
| `VERSION` | the version string, used by `make test-release` |
| `haetae-server-<version>.oci.tar` | multi-arch OCI image, only when `docker buildx` exists |

## Verify

```bash
cd bin/release && sha256sum -c SHA256SUMS
./haetae-agent-linux-amd64 --version
make test-release
```

`make test-release` checks: checksums; every binary is a static ELF of the right machine (no
PT_INTERP, no NEEDED); `--version` of the amd64 binaries equals the stamped version; tarball
contents equal the standalone binaries; `bin/dist/SHA256SUMS` has the four names and line
format `internal/installer` and `agent-install.sh` verify, and matches the release copies; the
image starts, runs as non-root (65532), contains the release server binary with the embedded
web UI and the installer download directory.

## Image

`make release-image` builds from `scripts/Dockerfile.release` (distroless static, nonroot,
copies the release binary; keep it in sync with the root `Dockerfile`). Tags:
`haetae-server:<version>` and `haetae-server:latest` (amd64, loaded into the local daemon). With
`docker buildx` it also writes the linux/amd64 + linux/arm64 OCI tarball. The old
`make docker-image` (source build, tag `:dev`) is unchanged. Nothing is ever pushed.

## Agent RSS on the release build

Measured 2026-10-26 with `haetae-agent-linux-amd64` version `0.1.0-d6e1877-dirty`, harness
`test/bench` (`HAETAE_BENCH_AGENT_BIN`), 3 min, 180 one-second samples, 4 busybox containers,
scan scheduler and security checks active, 8 GB / 2 core VM. Budget 50 MiB.

| Run | max | mean | peak (VmHWM) |
|---|---|---|---|
| Non-root user (`make bench-release-rss`) | 18.69 MiB | 18.10 MiB | 18.69 MiB |
| Root via `sudo` (same harness, compiled test binary) | 19.27 MiB | 18.37 MiB | 19.29 MiB |

The root run was meant to exercise the live conntrack netlink listener as in the M7 measurement
(17.41 MiB peak); the harness does not report whether the listener actually came up, so that is
not confirmed for this run. Raw output: `bench/m7/results-release/`.
Earlier builds: M7 20.21 MiB (non-root), 17.41 MiB (root).

## Not done

- No registry push of images or upload of artifacts (no GitHub release is created).
- No signing: no cosign, no GPG; SHA256SUMS protects against corruption only.
- arm64 binaries are never executed here (no qemu-user, no arm64 host); only ELF headers are checked.
- The arm64 image is built only when `docker buildx` exists; this host has none, so only the
  amd64 image was built and tested and no OCI tarball was produced.
- Tarballs are not reproducibly verified across machines.
