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
      --dry-run         Flag
          Prints the names of the repositories to be cloned or updated but does not perform
          any actions.
      --help, -h        Flag
          Prints this help message.
    
    OPTIONS
      --archived, -a    String, Env: GHGR_OPT_ARCHIVED, Values: exclude, only
          Limit repositories based on their archived status in relation to the value of this
          option.
    
      --language, -l    String, Env: GHGR_OPT_LANGUAGE, Values: Any string
          Limit repositories to those where the primary language matches the value specified.
    
      --max-repos, -m   Integer, Env: GHGR_OPT_LIMIT, Values: Any number
          Limit the number of repositories returned by the API to the value specified.
    
      --topic, -t       String, Env: GHGR_OPT_TOPIC, Values: Any string
          Limit repositories to those where one of the topics matches the value specified.
    
      --sleep, -s       Integer, Env: GHGR_OPT_SLEEP, Values: Any number
          Sleep the specified number of seconds before issuing the next command. This can be
          useful in mitigating anti-DDOPS or rate-limiting mechanisms.
    
      --visibility, -v  String, Env: GHGR_OPT_VISIBILITY, Values: internal, private, public
          Limit repositories based on their visibility status in relation to the value of this
          option
    
      --path, -p        String
          Location where the repositories will be cloned/updated."   
    
    EXAMPLES
    
      $ gh get-repos jrmash
      $ gh get-repos jrmash --dry-run
      $ gh get-repos jrmash --language hcl
      $ gh get-repos jrmash --topic terraform
```
