# gh get-repos

[![Build and test](https://github.com/jrmash/gh-get-repos/actions/workflows/build-test.yml/badge.svg?branch=main)](https://github.com/jrmash/gh-get-repos/actions/workflows/build-test.yml?query=branch%3Amain)
[![Code checks](https://github.com/jrmash/gh-get-repos/actions/workflows/check-code.yml/badge.svg?branch=main)](https://github.com/jrmash/gh-get-repos/actions/workflows/check-code.yml?query=branch%3Amain)
[![Markdown](https://github.com/jrmash/gh-get-repos/actions/workflows/check-md.yml/badge.svg?branch=main)](https://github.com/jrmash/gh-get-repos/actions/workflows/check-md.yml?query=branch%3Amain)

Clone or update every accessible GitHub repository owned by a user or organization, with filters and predictable directory layouts.

Upgrading from the Bash version? Read [Upgrading](UPGRADING.md) for changes to defaults, filtering, and updates.

To stay on Bash, follow the [fork installation instructions](UPGRADING.md#staying-on-the-bash-version). The `release/v1` branch is frozen source for independently maintained forks.

```sh
gh get-repos example-org --path ~/dev/example-org
gh get-repos example-org --mode worktree --language Go --topic cli
```

## Installation

Install [GitHub CLI](https://cli.github.com/) and Git, then authenticate and install the extension:

```sh
gh auth login
gh extension install jrmash/gh-get-repos
gh get-repos --help
```

Use Git 2.36 or newer and a recent GitHub CLI with `gh api --paginate --slurp` and `gh repo clone --no-upstream` support. The default installation downloads a binary from the latest stable release and requires no Go installation. To build unreleased source from `main`, use the Go version in `go.mod` or newer:

```sh
git clone https://github.com/jrmash/gh-get-repos.git
cd gh-get-repos
go build -o gh-get-repos .
gh extension install .
```

On Windows, build as `gh-get-repos.exe`. Upgrade an installed release with `gh extension upgrade get-repos`. Local builds report version `dev`; release binaries report their release tag.

The extension uses GitHub CLI's authentication, selected host, and configured Git protocol. Set `GH_HOST` for an enterprise host and use `gh auth setup-git` when Git needs HTTPS credential integration. Only repositories visible to the authenticated account can be listed or cloned. See the [GitHub CLI environment reference](https://cli.github.com/manual/gh_help_environment) for token and host settings.

## Usage

```text
gh get-repos [owner] [flags]
```

The owner is a GitHub user or organization. Omit it to use the configured owner, or the authenticated user when no owner is configured.

| Flag | Type and accepted values | Default |
| --- | --- | --- |
| `--archived`, `-a` | `any`, `exclude`, `only` | `exclude` |
| `--forks` | `any`, `exclude`, `only` | `exclude` |
| `--format` | `human`, `json`, `yaml` | `human` |
| `--mode` | `standard`, `worktree` | `standard` |
| `--sort` | `pushed`, `updated` (newest first), `name` (ascending) | `pushed` |
| `--safe-mode` | Boolean; `false` uses Git's configured checkout/pull behavior | `true` |
| `--fork-upstream` | Boolean; add a fork's parent as `upstream` when cloning | `true` |
| `--fail-on-skip` | Boolean; return exit code `1` for skipped repositories | `true` |
| `--visibility`, `-v` | `any`, `internal`, `private`, `public` | `any` |
| `--language`, `-l` | Primary language name; case insensitive | No filter |
| `--topic`, `-t` | Topic name; repeated or comma separated | No filter |
| `--sleep`, `-s` | Integer seconds, `0` through `300` | `0` |
| `--max-repos`, `-m` | Nonnegative integer; `0` means unlimited | `0` |
| `--path`, `-p` | Destination directory | Invocation directory |
| `--dry-run` | List selected repositories without cloning or updating | `false` |
| `--verbose` | Boolean; use `--verbose=false` to disable | `false` |
| `--help` | Show help | — |
| `--version` | Show extension version | — |

All requested topics must match, ignoring case. Language matches the repository's primary language. Other filters combine with these filters. Sleep applies between repository attempts, including skipped and failed attempts, with no delay after the final one. Repositories are processed sequentially, newest push first by default. Use `--sort updated` for repository update time or `--sort name` for alphabetical order. GitHub supplies the order; filtering and deduplication preserve it without another sort.

`--max-repos` limits matching repositories after filtering and sorting. Discovery still reads every page. `--dry-run` lists the selected repositories and target paths in JSON/YAML, or names with a `planned` status in human output. It does not create destination directories, inspect or change checkouts, or sleep between repositories. Discovery still requires GitHub access; a preview does not verify whether an update would succeed.

```sh
# Include archived repositories and forks.
gh get-repos example-org --archived any --forks any

# Require both topics and select public Go repositories.
gh get-repos example-org --visibility public --language go --topic cli,tools

# Keep the base checkout beside future feature worktrees.
gh get-repos example-org --mode worktree --path ~/dev/example-org
git -C ~/dev/example-org/widget/base worktree add ../feature -b feature

# Preview a bounded batch before updating it.
gh get-repos example-org --dry-run -m 10

# Use alphabetical order, or let Git manage checkout and pull.
gh get-repos example-org --sort name
gh get-repos example-org --safe-mode=false

# Keep fork clones focused on the fork, or accept intentional skips.
gh get-repos example-org --forks any --fork-upstream=false
gh get-repos example-org --fail-on-skip=false

# Write machine-readable results and full command logs to separate files.
gh get-repos example-org --format json --verbose > results.json 2> commands.log

# Override configured filters, sleep, and verbosity explicitly.
gh get-repos example-org --language= --topic= --sleep=0 --verbose=false
```

## Configuration

Two optional YAML files are loaded automatically:

1. Global: `<gh-config-dir>/get-repos.yml`.
2. Local: `.gh-get-repos.yml` in the directory where the command is invoked.

The GitHub CLI configuration directory is `GH_CONFIG_DIR`, then `$XDG_CONFIG_HOME/gh`, then `$AppData/GitHub CLI` on Windows, then `$HOME/.config/gh`, following the [GitHub CLI rules](https://cli.github.com/manual/gh_help_environment). The local file is read only from the invocation directory, so a parent directory or `--path` destination does not supply local configuration.

Settings are merged individually in this order, from highest priority to lowest:

**Explicit command arguments → `GHGR_*` environment variables → local YAML → global YAML → defaults.**

Configuration support for each setting is shown below. Environment aliases appear in parentheses.

| Setting | Global YAML key | Local YAML key | Environment variable (alias) | CLI |
| --- | --- | --- | --- | --- |
| Owner | `owner` | `owner` | `GHGR_OWNER` | Positional `[owner]` |
| Archived repositories | `archived` | `archived` | `GHGR_OPT_ARCHIVED` (`GHGR_ARCHIVED`) | `--archived`, `-a` |
| Forks | `forks` | `forks` | `GHGR_OPT_FORKS` (`GHGR_FORKS`) | `--forks` |
| Output format | `format` | `format` | `GHGR_OPT_FORMAT` (`GHGR_FORMAT`) | `--format` |
| Directory layout | `mode` | `mode` | `GHGR_OPT_MODE` (`GHGR_MODE`) | `--mode` |
| Sort | `sort` | `sort` | `GHGR_OPT_SORT` (`GHGR_SORT`) | `--sort` |
| Safe mode | `safe-mode` | `safe-mode` | `GHGR_OPT_SAFE_MODE` (`GHGR_SAFE_MODE`) | `--safe-mode` |
| Fork upstream | `fork-upstream` | `fork-upstream` | `GHGR_OPT_FORK_UPSTREAM` (`GHGR_FORK_UPSTREAM`) | `--fork-upstream` |
| Fail on skip | `fail-on-skip` | `fail-on-skip` | `GHGR_OPT_FAIL_ON_SKIP` (`GHGR_FAIL_ON_SKIP`) | `--fail-on-skip` |
| Visibility | `visibility` | `visibility` | `GHGR_OPT_VISIBILITY` (`GHGR_VISIBILITY`) | `--visibility`, `-v` |
| Language | `language` | `language` | `GHGR_OPT_LANGUAGE` (`GHGR_LANGUAGE`) | `--language`, `-l` |
| Topics | `topic` | `topic` | `GHGR_OPT_TOPIC` (`GHGR_TOPIC`) | `--topic`, `-t` |
| Sleep | `sleep` | `sleep` | `GHGR_OPT_SLEEP` (`GHGR_SLEEP`) | `--sleep`, `-s` |
| Repository limit | `max-repos` | `max-repos` | `GHGR_OPT_LIMIT` (`GHGR_MAX_REPOS`) | `--max-repos`, `-m` |
| Destination | `path` | `path` | `GHGR_OPT_PATH` (`GHGR_PATH`) | `--path`, `-p` |
| Verbose output | `verbose` | `verbose` | `GHGR_OPT_VERBOSE` (`GHGR_VERBOSE`) | `--verbose` |
| Dry run | `dry-run` | `dry-run` | `GHGR_OPT_DRY_RUN` (`GHGR_DRY_RUN`) | `--dry-run` |
| Help | — | — | — | `--help`, `-h` |
| Version | — | — | — | `--version` |

The Bash version's environment names remain supported. The primary environment name wins over its alias when both are set, including explicit empty, `false`, and `0` values. Empty values must still be valid for that setting.

YAML keys match the long flags, with `owner` supplied positionally. `topic` is a YAML sequence; `GHGR_OPT_TOPIC` accepts CSV. A higher-priority topic list replaces the entire lower-priority list. Use `topic: []`, an empty `GHGR_OPT_TOPIC`, or `--topic=` to clear topics. Empty language values clear the language filter. Explicit `false` and `0` override lower-priority values, including `--safe-mode=false`, `--fork-upstream=false`, and `--fail-on-skip=false`. Boolean environment values include `true` and `false`.

For example, global `get-repos.yml` can set personal defaults:

```yaml
archived: exclude
forks: exclude
mode: worktree
sort: pushed
safe-mode: true
fork-upstream: true
fail-on-skip: true
sleep: 1
verbose: false
```

Then `~/dev/example-org/.gh-get-repos.yml` can select that directory's owner:

```yaml
owner: example-org
path: .
visibility: private
language: Go
topic:
  - tools
```

Running `gh get-repos` from that directory uses both files. Relative destination paths are resolved against the invocation directory, including paths from global configuration. `~` expands to the current user's home directory.

Malformed YAML, unknown keys, and invalid effective setting values fail before repository operations. Missing configuration files are allowed. `--help` and `--version` work without authentication or valid configuration. Configuration files contain settings only; GitHub credentials stay in GitHub CLI's credential handling.

## Directory layouts and updates

With `--path ~/dev/example-org`, the two layouts are:

```text
standard                  worktree
example-org/              example-org/
├── widget/               ├── widget/
└── service/              │   └── base/
                          └── service/
                              └── base/
```

Worktree mode creates a normal clone inside `base`, ready for sibling worktrees. Valid existing linked worktrees are also accepted at the expected destination. Changing modes does not migrate existing directories.

Repository discovery uses GitHub CLI's [paginated GraphQL API command](https://cli.github.com/manual/gh_api) to fetch every page of repositories owned by the selected account. Filters run after discovery, avoiding the Search API's 1,000-result ceiling. Discovery must finish successfully before any repositories are modified.

New repositories are cloned directly into their final absent or empty destination using `gh repo clone`. For forks, the parent is added as `upstream` and becomes GitHub CLI's default repository, following [normal clone behavior](https://cli.github.com/manual/gh_repo_clone). Use `--fork-upstream=false` to omit that parent remote and keep the fork as GitHub CLI's default. This setting applies only to new clones; existing remotes are preserved. Empty GitHub repositories count as successful clones. A clone failure is reported, including when the destination needs attention before retrying.

For existing checkouts, the extension verifies the checkout root and expected `origin` in both update modes. A missing local default branch is created with tracking from `origin`. Switching branches or changing HEAD counts as updated; a successful operation that changes neither is unchanged.

With `safe-mode: true` (the default), the extension fetches only the GitHub default branch from `origin` and fast-forwards it. Other branches, tags, and submodules are not refreshed. It skips dirty checkouts, including untracked files or dirty submodules; detached HEADs; in-progress Git operations; default branches checked out in another worktree; and local default branches that are ahead or diverged. Local commits are preserved. Branch switches and updates protect ignored files from being overwritten.

With `--safe-mode=false`, the extension checks out the GitHub default branch and runs plain `git pull`. Git decides whether local changes can be retained and follows the branch's configured upstream, merge/rebase strategy, autostash, fetch scope, and submodule settings. Detached HEADs can be reattached and locally ahead branches can succeed without changes. Configured merges can create commits and rebases can rewrite local commits, just as an ordinary pull does. A preliminary fetch is needed only when creating a missing local default branch. A rejected checkout stops that repository's update before pull; a Git command failure is reported as failed.

In both modes, an existing empty remote's checkout is unchanged only when clean and without local commits. GitHub CLI and Git terminal credential prompts remain disabled; configure authentication before running a batch.

Wrong origins and incompatible or unrelated destination directories produce failures. Processing continues after individual problems. An interruption cancels the active command or sleep.

## Output and exit codes

Human output shows one final line as each repository finishes, a status symbol, its action, and a short reason when needed, followed by totals. Reasons use a normal dash with one space on either side, such as `skipped - working tree has local changes`. Terminal color preferences are respected. Statuses are `cloned`, `updated`, `unchanged`, `skipped`, and `failed`; dry runs use `planned`.

The repository column is sized from the selected repositories so status labels stay aligned. Names are shortened with an ellipsis only when required to fit the terminal; redirected output keeps full names.

An animated spinner appears immediately during discovery and each repository operation, showing the current phase and repository count. It clears before the final result row appears. Progress uses stderr; nonterminal output and `--verbose` use plain progress lines, so command logs remain readable. JSON and YAML disable progress animation and messages.

JSON and YAML contain the same result document:

```json
{
  "host": "github.com",
  "owner": "example-org",
  "path": "/home/developer/dev/example-org",
  "mode": "standard",
  "dry_run": false,
  "results": [
    {
      "repository": "example-org/widget",
      "path": "/home/developer/dev/example-org/widget",
      "status": "cloned"
    }
  ],
  "summary": {
    "selected": 1,
    "planned": 0,
    "processed": 1,
    "cloned": 1,
    "updated": 0,
    "unchanged": 0,
    "skipped": 0,
    "failed": 0
  }
}
```

Skipped and failed results include an `error` explanation. Discovery errors and interruption can also add a document-level `error`. `selected` counts matching repositories after applying the repository limit, `planned` counts previewed repositories, and `processed` counts completed synchronization attempts. Dry runs set `dry_run` to `true`, use `planned` results, and keep `processed` and operation counts at zero. Structured stdout contains only the result document; diagnostics and `--verbose` clone, fetch, switch, merge, checkout, and pull output go to stderr.

| Exit code | Meaning |
| --- | --- |
| `0` | Every selected repository succeeded, including zero matches; skips are allowed with `--fail-on-skip=false` |
| `1` | An operational failure, a failed repository, or a skipped repository when `fail-on-skip` is `true` |
| `2` | Invalid arguments or configuration |
| `130` | Interrupted |

`--fail-on-skip=false` changes only the exit policy for intentional skips. Skipped results and counts remain in human, JSON, and YAML output, and failures still return nonzero.

## Development and releases

See [Contributing](CONTRIBUTING.md) for local checks, smoke-test platforms, coverage policy, and release requirements. See [Security](SECURITY.md) for vulnerability reporting and repository security settings.

Workflow badges show the status on `main`.
