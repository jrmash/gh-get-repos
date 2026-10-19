# Upgrading from the Bash version

The Go version preserves the standard `<path>/<repository>` layout and existing long and short options. Authentication and clone protocol continue to follow your GitHub CLI configuration. Existing clones do not need to be recreated.

## Preview before upgrading

Archived repositories and forks, previously included by default, are now excluded. To preview a batch with the Bash version's broader selection and repository limit:

```sh
gh get-repos OWNER --archived any --forks any --max-repos 10000 --dry-run
```

Use `./gh-get-repos` when running a local build. A dry run still requires GitHub access to list repositories. It does not create directories, inspect or change checkouts, or check whether an update would succeed.

## Changes to options and defaults

| Area | What changes |
| --- | --- |
| Repository limit | `--max-repos` / `-m` defaults to `0` (unlimited), replacing the Bash default of 10,000. A positive limit applies after filtering. Set `GHGR_OPT_LIMIT=10000` to keep the previous ceiling. |
| Sleep | `--sleep` / `-s` accepts whole seconds from **0 to 99**. Higher values now fail. Sleep occurs between repository attempts, with no delay after the final repository or during dry runs. |
| Topics | Repeated or comma-separated topics must **all** match, ignoring case. Previously, repeating `--topic` kept only its final value. To retain that result, pass only the final topic. |
| Parsing | Unknown options, invalid values, missing option arguments, and multiple owners now produce an error. Empty archived, fork, or visibility values are invalid; use `any` to disable those filters. Both `--option value` and `--option=value` are supported. |

Repositories are now processed in case-insensitive name order. With a limit, the selected batch may therefore differ from the Bash version's listing order.

Discovery uses `gh api` pagination instead of `gh repo list`, then applies filters locally. Some `gh repo list` filters use GitHub Search, which can return at most 1,000 results. The paginated listing provides complete results beyond that ceiling. Discovery may take longer because every page must be read before processing, even with a small `--max-repos`.

## Output and automation

Human output now uses progress indicators, one result row per repository, and totals. Full Git operation output is available with `--verbose` and goes to stderr. `-v` continues to mean visibility. Scripts that parse the Bash version's headings or Git output should switch to `--format json` or `--format yaml`; structured stdout contains a single result document. Dry-run results use `planned` status and leave synchronization counters at zero.

Failed or skipped repositories now make the command return `1`, even when the rest of the batch succeeds. Invalid arguments or configuration return `2`, and interruption returns `130`. The Bash version could return success after Git failures. Adjust automation that previously assumed a successful exit meant every repository was updated.

## Changes to updates

Updates switch clean checkouts to the GitHub default branch and fast-forward from `origin`. They preserve local commits and skip checkouts with:

- Modified or untracked files, including dirty submodules.
- A detached HEAD or an active Git operation.
- A default branch that is ahead of or diverged from `origin`, or checked out in another worktree.

Resolve these conditions and rerun the command. Automatic merge/rebase behavior configured for `git pull` is no longer used. A failed branch switch stops work on that repository; processing continues with the rest of the batch.

Destinations must be actual checkout roots with an `origin` identifying the selected GitHub repository. Symlink destinations, local mirror URLs, missing or unrelated origins, and incompatible layouts are rejected. Credential prompts are disabled, so configure authentication before running a batch.

Only the default branch is fetched and updated. Other branches, new tags, and submodules are not refreshed; update them separately when needed. When forks are included, new clones do not automatically receive a parent `upstream` remote. Existing remotes are preserved.

Empty GitHub repositories can now be cloned and recognized on later runs before their first commit.

## Configuration and layouts

Settings can now come from global `<gh-config-dir>/get-repos.yml` and local `.gh-get-repos.yml` files. Explicit arguments override environment variables, local configuration, global configuration, and defaults, in that order. A configured owner replaces the authenticated-user fallback when no owner argument is supplied.

The local file is read only from the invocation directory, not a parent directory or the `--path` destination. Relative destination paths, including those in global configuration, are resolved against the invocation directory. Review these settings when running the same command from different directories.

The Bash environment names remain supported. The shorter names introduced during development, such as `GHGR_LANGUAGE`, remain aliases; `GHGR_OPT_*` takes precedence when both are present. See [Configuration](README.md#configuration) for the full mapping, value types, and examples.

`--mode worktree` adds normal clones at `<path>/<repository>/base` for sibling worktrees. Changing modes does not move existing checkouts; arrange the directories explicitly before switching an existing collection. Additional options include `--forks` for fork filtering and `--version` for version reporting.

## Installation and verification

Platform-specific release binaries replace the Bash script and require no Go or Bash installation. GitHub CLI and Git remain required; source builds also need the Go version specified in `go.mod`. See [Installation](README.md#installation) for supported tool versions and installation or upgrade commands.

The smoke-test workflow is configured to install and exercise the extension on Linux, macOS, and Windows, each on x64 and ARM64. It covers live discovery, dry runs, cloning, and updating. Release publishing requires this workflow, the automated test suite, and vulnerability checks to pass.
