# Changelog

## [2.1.0] - 2026-09-17

### Added

- Scalar aliases such as `uses: *checkout` are now detected in offline checks, resolved checks, and fix/diff mode. Fixes materialize a pinned scalar at each alias occurrence without changing a shared anchor defined outside `uses`.
- Nested annotated tags are resolved through a bounded API traversal to the underlying commit.

### Fixed

- Annotated-tag API resolution validates object types instead of accepting a tag, tree, or blob SHA as a commit SHA.
- Tag-object requests are constructed from the configured API base and validated object SHA rather than following response-provided URLs with credentials.
- Alias findings retain the occurrence's source location, and fixes preserve occurrence comments and remain idempotent. Alias edits fall back to YAML encoding and may normalize formatting.

### Compatibility

- No CLI flag, Action input, or Go module path changes. Continue using the `/v2` module path; install this release with `go install github.com/emirhan-karaca/action-pin/v2/cmd/action-pin@v2.1.0`.
- Offline checks, `--resolve`, `--diff`, source builds, and checksum-verified release binaries retain their v2 behavior.

### Validation

- Local tests, `go vet ./...`, `go build ./...`, and `go mod verify` passed. The code commit also passed GitHub CI.
- Local race-detector tests were not run successfully because the Windows environment lacks a C compiler.

## [2.0.0] - 2026-09-14

### Breaking changes

- The Go module is now `github.com/emirhan-karaca/action-pin/v2`. Install the CLI with `go install github.com/emirhan-karaca/action-pin/v2/cmd/action-pin@v2.0.0`.
- The composite Action now defaults to `version: source`, builds the selected Action checkout, and requires Go 1.22 or newer on the runner.
- Release-binary mode accepts only an exact release tag and requires the matching 64-character SHA-256 archive checksum. The former `latest` behavior is no longer supported.

### Added

- Default CLI checks are offline and report unpinned references without resolving them. Use `--resolve` to request SHA suggestions.
- `--diff` and the Action `diff` input produce a reviewable unified patch without changing workflow files.
- Fix and diff modes prepare the selected workflow set before writes, reject unsafe symlink targets, preserve file permissions, and report partial replacement failures.

### Compatibility

- v1.0.x keeps its previous Action launcher behavior: it selects `latest` by default, has no source-mode/checksum contract, resolves CLI checks online, and does not provide `--resolve` or `--diff`.

### Validation and release assets

- The release candidate passed seven CI jobs: Linux, macOS, and Windows on Go 1.23.x and 1.24.x, plus the workflow self-check.
- Publishing `v2.0.0` is expected to produce six archives for Linux, macOS, and Windows on amd64 and arm64, plus `checksums.txt`. Copy platform-specific SHA-256 values from that manifest; no archive digest is recorded here before publication.
