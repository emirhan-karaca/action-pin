# action-pin 📌

[![CI](https://github.com/emirhan-karaca/action-pin/actions/workflows/ci.yml/badge.svg)](https://github.com/emirhan-karaca/action-pin/actions/workflows/ci.yml)
[![Go Reference](https://pkg.go.dev/badge/github.com/emirhan-karaca/action-pin/v2.svg)](https://pkg.go.dev/github.com/emirhan-karaca/action-pin/v2)
[![License: Apache-2.0](https://img.shields.io/badge/License-Apache_2.0-blue.svg)](LICENSE)
[![Go Version](https://img.shields.io/badge/Go-1.22+-00ADD8?logo=go)](https://go.dev)
[![Release](https://img.shields.io/github/v/release/emirhan-karaca/action-pin?logo=github)](https://github.com/emirhan-karaca/action-pin/releases)

> **Secure your GitHub Actions CI/CD workflows by pinning third-party actions to immutable commit SHAs — without breaking your formatting or losing your comments.**

---

## Why Pin Actions?

In GitHub Actions, referencing actions by mutable tags (e.g., `uses: actions/checkout@v4` or `@main`) poses a serious **supply chain attack risk**:

1. **Tag Hijacking**: If a maintainer's account or repo is compromised, an attacker can move the `v4` tag to malicious code.
2. **Untracked Code Drift**: An author may push bug fixes or breaking changes under the same tag, causing unexpected CI failures.
3. **Reviewable Dependencies**: Full commit hashes identify the exact action revision being reviewed and executed. Pinning is one part of securing a build; it does not alone establish compliance with a security framework.

### The Problem With Existing Pinning Tools
Traditional regex-based or naive YAML re-formatters strip comments, reorder dictionary keys, collapse multi-line scripts, or mangle custom indentation.

### How `action-pin` Solves It
- 🧠 **Format-Preserving**: Uses `gopkg.in/yaml.v3` node positions to edit ordinary single-line action references in place, preserving surrounding comments, spacing, quotes, and line endings. Complex scalar syntax (such as anchors, explicit tags, or multiline values) falls back to YAML encoding, which may normalize formatting.
- 💬 **Human-Readable Annotations**: Automatically appends the original tag name as a comment: `# <tag> [pinned by action-pin]`.
- 🔌 **Offline Checks & Reviewable Diffs**: Detects unpinned references without credentials or network access. Opt in to SHA suggestions with `--resolve`, preview a patch with `--diff`, or resolve and write with `--fix`.
- ⚡ **Zero-Config & Resilient**: Resolves refs via GitHub REST API (supporting `GITHUB_TOKEN`), with an automated fallback to `git ls-remote` when rate-limited.
- 🔁 **Fully Idempotent**: Safe to run on every commit or in pre-commit hooks.

---

## Visual Diff: Before & After

```diff
  jobs:
    build:
      runs-on: ubuntu-latest
      steps:
        # Step 1: Check out source code
-       - uses: actions/checkout@v4
+       - uses: actions/checkout@11d5960a326750d5838078e36cf38b85af677262 # v4 [pinned by action-pin]

        # Step 2: Restore cache from subpath
-       - uses: actions/cache/restore@v3
+       - uses: actions/cache/restore@dacf3200ff73ea515d96a79eefffa1cc3a0bfa99 # v3 [pinned by action-pin]

        # Local & Docker actions remain untouched!
        - uses: ./.github/actions/setup-local
        - uses: docker://alpine:3.18
```

---

## 30-Second Quickstart

### Installation

Version 2.1.0 adds support for scalar aliases in `uses` fields and improves annotated-tag resolution, while retaining offline checks, `--resolve`, and `--diff`. Install this release, or build from a checkout with `go run ./cmd/action-pin --check`.

#### Pre-built Binaries (Linux, macOS, Windows)
Download the latest binary for your operating system and architecture from [GitHub Releases](https://github.com/emirhan-karaca/action-pin/releases).

#### Via Go Install
```bash
go install github.com/emirhan-karaca/action-pin/v2/cmd/action-pin@v2.1.0
```

Use the `/v2` module path for Go installs. The v2.1.0 release contains the offline check, `--resolve`, and `--diff` behavior described below.

---

## CLI Usage

### Check Mode (CI Friendly)
Checks whether any workflow contains unpinned actions, without network access or credentials. Returns exit code `1` if unpinned actions exist. This checks the reference format; it does not verify that a repository, tag, or pinned commit exists:

```bash
action-pin --check
```

Output:
```text
[UNPINNED] .github/workflows/ci.yml:14: actions/checkout@v4

Check failed: Found 1 unpinned action(s) across 1 file(s).
Run 'action-pin --fix --dir .github/workflows' to pin them automatically.
```

To also resolve suggested commit SHAs without changing files, use:

```bash
action-pin --check --resolve
```

This opt-in mode requires network access. Set `GITHUB_TOKEN` or `GH_TOKEN` when checking private repositories or to avoid unauthenticated API rate limits. Resolution errors fail the check.

### Diff Mode (Reviewable Preview)
Resolves unpinned references and writes the resulting unified patch to standard output without changing workflow files. A successful preview exits with code `0`, even when the patch contains changes. Summary messages and errors go to standard error, so the patch can be redirected safely:

```bash
action-pin --diff --dir .github/workflows > action-pin.patch
```

Review `action-pin.patch`, then apply it with a patch tool if desired. For example:

```bash
git apply --check action-pin.patch
git apply action-pin.patch
```

`--diff` requires network access because it resolves references. It works with either `--dir` or `--file`, cannot be combined with `--check` or `--fix`, and makes `--resolve` redundant (although `--resolve` is accepted).

### Fix Mode (In-Place Update)
Rewrites workflows in place, resolving tags to immutable 40-character commit SHAs:

```bash
action-pin --fix
```

Output:
```text
[PINNED]   .github/workflows/ci.yml:14: actions/checkout@v4 -> 11d5960a326750d5838078e36cf38b85af677262

Success: Pinned 1 action(s) across 1 file(s) (Checked 1 file(s))
```

For safety, `--fix` and `--diff` require regular workflow files and reject symlink workflow inputs and symlinked `--dir` roots. `--check` remains read-only and retains its existing path behavior.

When a directory is targeted, fix mode first reads, parses, resolves, and stages every workflow change. If a read, YAML, reference-resolution, or staging step fails before replacement begins, all workflow files remain unchanged. Replacements are performed per file, so errors, cancellation, or concurrent source changes after replacement starts may leave earlier files updated. Each successful per-file write preserves that file's mode bits.

### CLI Flags Reference

| Flag | Default | Description |
|------|---------|-------------|
| `--check` | `false` | Check for unpinned actions offline (exits with code 1 if unpinned actions exist) |
| `--fix` | `false` | Fix workflows in place by pinning actions to commit SHAs |
| `--diff` | `false` | Write a unified patch without changing files; resolves references over the network and exits 0 after a successful preview, including when the patch is nonempty |
| `--resolve` | `false` | Resolve suggested SHAs during checks; requires network access and is implied by `--fix` and `--diff` |
| `--dir` | `.github/workflows` | Directory containing workflow files |
| `--file` | `""` | Target a single workflow file (e.g. `--file .github/workflows/deploy.yml`) |
| `--token` | `$GITHUB_TOKEN` | Token for `--fix`, `--diff`, or `--resolve` (checks `--token`, `$GITHUB_TOKEN`, then `$GH_TOKEN`); unused by offline checks |
| `--verbose` | `false` | Enable verbose logging of inspected actions |
| `--version` | `false` | Print version and build information |

> **Note**: If invoked without flags, `action-pin` runs in check mode by default (`--check --dir .github/workflows`).

---

## GitHub Action Integration (1-Line CI)

Integrate `action-pin` directly into your CI pipeline using the composite Action. Since v2.0.0, the default `source` mode builds the selected Action checkout with Go 1.22 or newer. Pinning the Action to a full commit SHA therefore also selects the program source being executed.

The examples use the reviewed source commit that contains the v2.1.0 behavior. Ensure a supported Go toolchain is available on your runner before this step.

```yaml
name: Security & Pinning Check

on:
  push:
    branches: [main]
  pull_request:
    branches: [main]

permissions:
  contents: read

jobs:
  verify-pinned-actions:
    runs-on: ubuntu-latest
    steps:
      - name: Checkout repository
        uses: actions/checkout@11d5960a326750d5838078e36cf38b85af677262 # v4 [pinned by action-pin]

      - name: Verify all actions are pinned
        uses: emirhan-karaca/action-pin@6d0436eaf6982de74b13f4fb507965efd9ea5275 # reviewed v2.1.0 source
```

The build may need network access to download Go modules on the first run; it uses the checkout's `go.mod` and `go.sum` with `-mod=readonly`. The resulting CLI check itself is offline. The Action ignores any `action-pin` binary already on `PATH` and runs in the caller's working directory, so `dir` remains relative to your repository.

To preview the changes that pinning would make without writing workflows, enable the `diff` input:

```yaml
- name: Preview action pins
  uses: emirhan-karaca/action-pin@6d0436eaf6982de74b13f4fb507965efd9ea5275 # reviewed v2.1.0 source
  with:
    diff: 'true'
```

### Using a Verified Release Binary

To avoid building from source, select an exact release tag and provide the SHA-256 of its archive for your runner's operating system and architecture:

```yaml
- name: Verify all actions are pinned
  uses: emirhan-karaca/action-pin@6d0436eaf6982de74b13f4fb507965efd9ea5275 # reviewed v2.1.0 source
  with:
    version: 'v2.1.0'
    checksum: 'REPLACE_WITH_64_CHARACTER_ARCHIVE_SHA256'
```

Review the v2.1.0 release's `checksums.txt` and copy the matching archive digest into your workflow. A Linux amd64 archive is named `action-pin_2.1.0_linux_amd64.tar.gz`; Windows uses `.zip`. Each runner platform needs its own digest. The Action verifies the download before extraction or execution and fails on a missing or mismatched checksum. `latest` and branch names are rejected.

Release mode runs the selected release's CLI behavior. Version 2.1.0 includes scalar-alias support and annotated-tag resolution fixes alongside offline checks, `--resolve`, and `--diff`.

### Migrating from v1.x

Go installs now use the major-version module path: `github.com/emirhan-karaca/action-pin/v2/cmd/action-pin@v2.1.0`. The v2 Action defaults to a source build, so runners need Go 1.22 or newer. To use a release binary instead, set an exact release tag such as `v2.1.0` and provide the matching platform archive checksum.

The v1.0.x Action used its older `latest` binary-selection behavior and has no source-mode/checksum contract. Its CLI checks resolve references online and it does not support `--resolve` or `--diff`. Keep v1.x workflows on their existing configuration, or migrate them to the v2 inputs above.

### Action Inputs

| Input | Default | Description |
|-------|---------|-------------|
| `check` | `'true'` | Fail workflow if unpinned actions are found |
| `fix` | `'false'` | Automatically fix and pin actions in place |
| `diff` | `'false'` | Resolve references and print a unified patch without changing files; requires network access |
| `dir` | `'.github/workflows'` | Directory containing workflow files |
| `token` | `${{ github.token }}` | GitHub token to avoid API rate limits |
| `version` | `'source'` | Build the selected Action checkout, or download an exact release tag such as `v2.1.0` |
| `checksum` | `''` | Required 64-character SHA-256 of the runner's release archive when `version` is a release tag; leave empty in source mode |
| `resolve` | `'false'` | Resolve suggested SHAs in check mode; requires network access |

`fix: 'true'` takes precedence over `check` and always resolves references. Set `diff: 'true'` to run `--diff`; it overrides the default `check: 'true'`, requires network resolution, and leaves the CLI's standard output available for the patch. `fix: 'true'` and `diff: 'true'` cannot be combined. Otherwise the Action checks workflows, including when both `check` and `fix` are `'false'`. `resolve: 'true'` is redundant with diff but accepted. Boolean inputs accept only `'true'` or `'false'`.

---

## Edge Cases Handled Out-of-the-Box

- **Annotated Tags vs Lightweight Tags**: Resolves nested annotated tags with a bounded API traversal, validates object types before accepting a commit SHA, and constructs tag requests from the configured API base instead of response-provided URLs. Git fallback remains available when API resolution fails.
- **Scalar Aliases**: Detects action references in `uses: *alias`. Fix mode replaces the alias occurrence with a pinned scalar without changing a shared anchor defined outside `uses`. Alias edits use YAML encoding, which may normalize formatting.
- **Repository Subpaths**: Fully supports nested paths like `actions/cache/restore@v3` and `aws-actions/amazon-ecr-login/.github/workflows/shared.yml@v1`.
- **Local Actions**: Ignores relative paths like `./.github/actions/my-action`.
- **Docker Actions**: Ignores `docker://` container actions.
- **Reusable Workflows**: Seamlessly pins calls to external reusable workflows.
- **Offline Checks / Rate-Limit Fallback**: Default checks do not use the network. During `--fix`, `--diff`, or `--resolve`, GitHub API errors can fall back to `git ls-remote`, which also requires network access.
- **Performance Caching**: Caches resolved SHAs in-memory during execution to eliminate duplicate network calls.

---

## Contributing

We love community contributions! Please read our [CONTRIBUTING.md](CONTRIBUTING.md) guide for details on development setup, running tests, and submitting PRs.

---

## License

`action-pin` is distributed under the terms of the [Apache License 2.0](LICENSE).
