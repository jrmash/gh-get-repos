# Contributing

Use the Go version in `go.mod`, Git, and Python 3 for the small CI helper. GitHub CLI is needed to run the extension against GitHub. Unit tests use fake commands and temporary local Git repositories; they do not require GitHub credentials or network access after module installation.

## Local checks

Install [pre-commit](https://pre-commit.com/) and enable the hooks for this clone:

```sh
pre-commit install
pre-commit run --all-files
```

The configuration keeps the general file, YAML, merge conflict, and branch checks. Go hooks from [pre-commit-golang](https://github.com/TekWizely/pre-commit-golang) use the installed Go toolchain and Bash: `gofmt` formats changed Go files, while `go mod tidy -diff` and `go vet` check modules affected by Go source or `go.mod`/`go.sum` changes. The tidy check reports required changes without modifying module files. Builds, tests, race detection, and coverage checks run in `build-test.yml`.

Hook repositories are pinned to commit SHAs. Update them while preserving hash pins with:

```sh
pre-commit autoupdate --freeze
```

Validation workflows support pull requests, pushes to `main`, and manual dispatch. Native path filters limit build/test runs to Go inputs, test fixtures, the CI helper, Git attributes, and the build workflow. Code checks run for Go inputs and workflow or action changes. Markdown checks run for documentation, lint/link configuration, ignore rules, and linked workflow or release configuration files. Manual runs bypass path filters, and scheduled security scans still run weekly.

The [Execute pre-commit workflow](.github/workflows/execute-pre-commit.yml) runs the same hooks against all tracked files. It sets up Python and Go and uses the SHA-pinned pre-commit Action to run the hooks and report formatting changes as a failing check with a diff.

Run the complete checks manually with:

```sh
go mod download
go mod verify
go mod tidy -diff
python3 scripts/check.py format
go vet ./...
go build ./...
go test ./...
go test -race -covermode=atomic '-coverpkg=./...' '-coverprofile=coverage.out' ./...
python3 scripts/check.py coverage coverage.out 90
```

Use `python` instead of `python3` where appropriate. Quote the complete coverage flags as shown so PowerShell preserves dotted values. Race detection requires a supported platform and C compiler; omit `-race` on Windows/ARM64. View uncovered statements with `go tool cover '-html=coverage.out'`. The coverage gate includes every package and deduplicates profile blocks shared by tests in multiple packages.

Format changed Go files with `gofmt -w <files>`. Add behavior tests for configuration precedence, filtering, pagination, output contracts, and synchronization changes. Use local bare repositories for Git behavior and injectable command runners for GitHub CLI behavior. Preserve meaningful exit codes and keep structured stdout parseable.

The [Build and test workflow](.github/workflows/build-test.yml) runs natively on Linux, macOS, and Windows, each on x64 and ARM64. Every platform runs unit tests with coverage and must reach at least 90% statement coverage. Race detection is enabled on every supported target; Windows/ARM64 runs coverage without race detection because Go does not support it there. The hosted runner images include the required C compilers, and CGO is enabled for race builds. Each platform's coverage profile is retained as a separate workflow artifact for 14 days, and its total appears in the job summary.

## Installation smoke tests

Each platform job in `build-test.yml` also builds the current source, installs it with `gh extension install .` in an isolated directory, and runs the installed `gh get-repos` command against a small public GitHub repository. These smoke tests verify help and version output, bounded discovery, dry runs without filesystem changes, cloning, unchanged reruns, and fast-forward updates in both directory layouts. They do not modify remote repositories or your installed extension. Unit-test coverage is collected separately from the installed-extension smoke test.

Run the same integration test locally with GitHub CLI authentication configured:

```sh
go test -tags smoke -count=1 -v -timeout=15m ./integration
```

These tests require network access and use `GH_TOKEN`, `GITHUB_TOKEN`, or existing `gh` authentication. CI supplies the job's read-only token. Ordinary unit tests stay offline. On Windows, the smoke fixture uses a small shell launcher for GitHub CLI's local-extension dispatch and runs the native executable behind it. Published release assets should also be downloaded and checked for installation and version output.

## Security checks and dependency updates

The [Check code workflow](.github/workflows/check-code.yml) runs module verification and tidy checks, vetting, and pinned `govulncheck` v1.9.0 on Linux, macOS, and Windows. It also runs CodeQL analysis for Go and GitHub Actions. These checks also run on the default branch every Saturday at 08:00 UTC.

```sh
go install golang.org/x/vuln/cmd/govulncheck@v1.9.0
govulncheck ./...
```

All third-party Actions are pinned to full commit SHAs with readable version comments. Dependabot checks modules and action pins weekly, groups compatible minor/patch updates, and leaves major updates separate. Update the scanner pin deliberately when changing the Go toolchain; tool versions embedded in shell commands are not automatically updated by the Go module ecosystem.

## Markdown checks

The [Check Markdown workflow](.github/workflows/check-md.yml) runs markdownlint for Markdown structure and style, and Lychee for local and external links. Long prose lines and table rows are allowed. GitHub workflow status URLs are excluded from link checking because new workflows are registered after they reach the default branch. This repository's version-to-version comparison URLs are also excluded because Release Please generates changelog links before the new release tag exists. Branch comparisons, issue links, and commit links remain checked.

For the generated changelog, markdownlint allows up to two consecutive blank lines and repeated headings under different release versions. Duplicate headings within the same release section still fail.

With `markdownlint-cli2` and `lychee` installed, run the same checks locally:

```sh
markdownlint-cli2 '**/*.{md,markdown}'
lychee --no-progress '**/*.md'
```

## Releases

[Release Please](https://github.com/googleapis/release-please-action) manages stable versions, changelogs, and release PRs through a single [Release PR workflow](.github/workflows/release-pr.yml) and [.release-please-config.json](.release-please-config.json). The workflow runs on pushes to `main`; manual release runs are limited to the default branch.

Develop on short-lived feature branches and open PRs into `main`. Use Conventional Commit titles: `fix: ...` for a patch release, `feat: ...` for a minor release, and `feat!: ...` or a `BREAKING CHANGE:` footer for a major release. Squash merging keeps each feature PR's release intent in one commit. Documentation and maintenance commits do not normally trigger a release.

1. Open a feature PR into `main`, review it, and wait for all required checks to pass before merging.
2. Squash merge with the Conventional Commit title. Release Please opens or updates a stable release PR containing the proposed version, changelog, and version manifest.
3. Review the release PR's version and notes, approve its workflows, and wait for all required checks to pass. Squash merge the generated release PR when ready to publish.
4. The **Prepare Release** job creates the tag and draft release. The **Publish Release** job checks out the release commit and uses [`cli/gh-extension-precompile`](https://github.com/cli/gh-extension-precompile) to build and upload binaries, embed the release version, and generate attestations. It then publishes the completed release and marks it Latest. Publishing does not rerun the validation workflows.

Merging feature PRs into `main` does not publish binaries. Users install or upgrade to the latest published stable release; unreleased changes are available through a [source build](README.md#installation).

Failed PR checks block merging when branch protection is configured. Failed release builds or attestations leave the release draft. Use GitHub Actions' **Re-run failed jobs** on the original run to retry publishing.

The draft, upload, and publish sequence supports [immutable releases](https://docs.github.com/en/code-security/concepts/supply-chain-security/immutable-releases). If enabled in repository settings, published assets and tags cannot be replaced. Publish a new version to correct a binary; draft releases remain retryable.

The Go rewrite starts the V2 release line. [.release-please-manifest.json](.release-please-manifest.json) records the current stable version. Release Please uses its standard versioning strategy to derive the next version from Conventional Commits since the last release and updates the manifest in release PRs.

Release Please maintains `CHANGELOG.md` automatically. The `extra-files` setting updates the annotated installation example below. Release binaries receive the tag through build flags; local source builds continue to report `dev`.

Enable **Allow GitHub Actions to create and approve pull requests** in repository settings. The workflow uses the built-in `GITHUB_TOKEN`. Publishing runs in the same workflow and does not rely on bot-created tags triggering another run.

Install the latest stable release without a pin:

```sh
gh extension install jrmash/gh-get-repos
```

Upgrade an existing installation and verify its version:

```sh
gh extension upgrade get-repos && gh get-repos --version
```

To install a specific version:

<!-- x-release-please-start-version -->

```sh
gh extension install jrmash/gh-get-repos --pin v2.0.0
gh get-repos --version
```

<!-- x-release-please-end -->

Pinned installations stay on that version. To change a pin, reinstall with the new `--pin` value and `--force`. Verify installation and version output after each release.

## Hosted repository setup

These settings require repository administration and are not configured by files in this checkout:

- Confirm `origin` points to the hosted repository and enable Actions.
- Use the CodeQL jobs in `check-code.yml` for advanced setup; avoid also enabling a duplicate default setup. Code scanning requires an eligible repository or GitHub Code Security access.
- Enable Dependabot alerts, security updates, dependency graph, private vulnerability reporting, and secret scanning/push protection where available.
- Protect `main` with all build/test and code checks listed below, plus `Markdown` and `Pre-commit`. Ordinary branch protection requires individual job checks; selecting a workflow name does not require all its jobs. Add the exact names after their first successful runs, and require PR branches to be up to date before merging. Replace existing `CI`, `Smoke`, and aggregate `Code checks` requirements when adopting these workflows.
- Restrict release tag creation to maintainers with repository rulesets and review changes to workflow permissions.

Hosted settings and workflow execution must be verified on GitHub after pushing the branch.

The required build/test checks are:

- `Build and Test (linux/amd64)`
- `Build and Test (linux/arm64)`
- `Build and Test (darwin/amd64)`
- `Build and Test (darwin/arm64)`
- `Build and Test (windows/amd64)`
- `Build and Test (windows/arm64)`

The required code checks are:

- `Static checks (ubuntu-latest)`
- `Static checks (macos-latest)`
- `Static checks (windows-latest)`
- `Analyze (go)`
- `Analyze (actions)`
