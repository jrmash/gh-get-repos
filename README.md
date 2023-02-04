A [GitHub CLI](https://cli.github.com) extension to clone and update all repositories owned by an organization or user.

## Installation

```shell
gh extension install jrmash/gh-get-repos
```

## Help and Usage

```shell
USAGE
  gh get-repos <owner> [options]

ARGUMENTS
  <owner>           The owner of the repositories to get/list

FLAGS
  --dry-run         Prints the names of the repositories to be cloned or  [flag]
                    updated, but does not actually clone or update them.
  --help, -h        Prints this help message                              [flag]

OPTIONS
  --language, -l    Limit repositories to those where the primary coding  [string, Env: GHGR_OPT_LANGUAGE]
                    language matches this value
  --max-repos, -m   Limit the number of repositories returned by the API  [integer, Env: GHGR_OPT_LIMIT]
                    to the value specified
  --sleep, -s       Sleep the specified number of seconds before issuing  [integer, Env: GHGR_OPT_SLEEP]
                    the next clone/update command. This can be useful in
                    mitigating anti-DDOS and rate-limiting mechanisms.
  --topic, -t       Limit repositories to those where one of the topics   [string, Env: GHGR_OPT_TOPIC]
                    matches this value

  --path, -p        Location where the repositories will be stored after  [string]
                    cloning or updating

EXAMPLES

  $ gh get-repos jrmash
  $ gh get-repos jrmash --dry-run
  $ gh get-repos jrmash --language hcl
  $ gh get-repos jrmash --topic terraform

```
