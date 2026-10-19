# Contributing

Use the Go version in `go.mod`, Git, and Python 3 for the small CI helper. GitHub CLI is needed to run the extension against GitHub. Automated tests use fake commands and temporary local Git repositories; they do not require GitHub credentials or network access after module installation.

## Local checks

Install [pre-commit](https://pre-commit.com/) and enable the hooks for this clone:

```sh
pre-commit install
pre-commit run --all-files
```

The configuration keeps the general file, YAML, merge conflict, and branch checks. Go hooks use the installed Go toolchain: `gofmt` formats changed Go files, and module metadata checks, vetting, and tests run once for the whole module when Go source or `go.mod`/`go.sum` changes. The module check reports required changes without modifying files. Race detection and coverage checks also run in CI.

Run the complete checks manually with:

```sh
go mod download
go mod verify
go mod tidy -diff
python3 scripts/check.py format
go vet ./...
go build ./...
go test ./...
go test -race -covermode=atomic -coverpkg=./... -coverprofile=coverage.out ./...
python3 scripts/check.py coverage coverage.out 90
```

Use `python` instead of `python3` where appropriate. Race detection requires a supported platform and C compiler. View uncovered statements with `go tool cover -html=coverage.out`. The coverage gate includes every package and deduplicates profile blocks shared by tests in multiple packages.

Format changed Go files with `gofmt -w <files>`. Add behavior tests for configuration precedence, filtering, pagination, output contracts, and synchronization changes. Use local bare repositories for Git behavior and injectable command runners for GitHub CLI behavior. Preserve meaningful exit codes and keep structured stdout parseable.

CI runs formatting, module verification and tidy checks, vetting, builds, and tests on Linux, macOS, and Windows. Linux tests also run with race detection and at least 90% total statement coverage. The coverage profile is retained as a workflow artifact for 14 days, and its total appears in the job summary.

## Installation smoke tests

The Smoke workflow runs natively on Linux, macOS, and Windows, each on x64 and ARM64. It builds the current source, installs it with `gh extension install .` in an isolated directory, and runs the installed `gh get-repos` command against a small public GitHub repository. It verifies help and version output, bounded discovery, dry runs without filesystem changes, cloning, unchanged reruns, and fast-forward updates in both directory layouts. It does not modify remote repositories or your installed extension.

Run the same integration test locally with GitHub CLI authentication configured:

```sh
go test -tags smoke -count=1 -v -timeout=15m ./integration
```

These tests require network access and use `GH_TOKEN`, `GITHUB_TOKEN`, or existing `gh` authentication. CI supplies the job's read-only token. Ordinary unit tests stay offline. On Windows, the smoke fixture uses a small shell launcher for GitHub CLI's local-extension dispatch and runs the native executable behind it. Downloading and installing published release assets should also be verified from a prerelease before publishing a stable release.

## Security checks and dependency updates

The CodeQL workflow analyzes Go through a manual build and also analyzes GitHub Actions. The vulnerability workflow installs pinned `govulncheck` v1.9.0 and scans the current vulnerability database. Both workflows run on pull requests, main, and weekly, and can be dispatched manually.

```sh
go install golang.org/x/vuln/cmd/govulncheck@v1.9.0
govulncheck ./...
```

All third-party Actions are pinned to full commit SHAs with readable version comments. Dependabot checks modules and action pins weekly, groups compatible minor/patch updates, and leaves major updates separate. Update the scanner pin deliberately when changing the Go toolchain; tool versions embedded in shell commands are not automatically updated by the Go module ecosystem.

## Releases

1. Merge the intended changes into `main` after CI and security checks pass.
2. Create and push a tag such as `v1.0.0` on that commit. Tags must use `vMAJOR.MINOR.PATCH` with optional prerelease or build metadata.
3. The release workflow reruns the reusable CI, vulnerability, and installation smoke workflows for the tagged commit. Publishing requires all three to succeed.
4. The official [`cli/gh-extension-precompile`](https://github.com/cli/gh-extension-precompile) action builds its default platform targets, embeds the tag in `main.version`, publishes release assets, and generates provenance attestations. Tags containing a prerelease suffix produce prereleases.

The publish job alone receives release and attestation write permissions. Checks have read access. Failed checks leave release publishing blocked. Validate installation and `gh get-repos --version` from a prerelease before the first stable release.

## Hosted repository setup

These settings require repository administration and are not configured by files in this checkout:

- Confirm `origin` points to the hosted repository and enable Actions.
- Use the CodeQL workflow's advanced setup; avoid also enabling a duplicate default setup. Code scanning requires an eligible repository or GitHub Code Security access.
- Enable Dependabot alerts, security updates, dependency graph, private vulnerability reporting, and secret scanning/push protection where available.
- Protect `main` with required `CI`, `Smoke`, `govulncheck`, and CodeQL checks after their first successful runs; use the exact check names shown by GitHub.
- Restrict release tag creation to maintainers with repository rulesets and review changes to workflow permissions.

Hosted settings and workflow execution must be verified on GitHub after pushing the branch.
