// Package github discovers owned repositories using GitHub CLI's pagination.
package github

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/jrmash/gh-get-repos/internal/executil"
	"github.com/jrmash/gh-get-repos/internal/settings"
)

const query = `query GetRepositories($owner: String!, $endCursor: String) {
  repositoryOwner(login: $owner) {
    login
    repositories(first: 100, after: $endCursor, ownerAffiliations: [OWNER], orderBy: {field: NAME, direction: ASC}) {
      nodes {
        id name nameWithOwner owner { login } url sshUrl
        isArchived isFork isEmpty visibility
        defaultBranchRef { name }
        primaryLanguage { name }
        repositoryTopics(first: 100) { nodes { topic { name } } }
      }
      pageInfo { hasNextPage endCursor }
    }
  }
}`

var safeName = regexp.MustCompile(`^[A-Za-z0-9_.-]+$`)

type Named struct {
	Name string `json:"name"`
}

type Repository struct {
	ID               string                 `json:"id"`
	Name             string                 `json:"name"`
	NameWithOwner    string                 `json:"nameWithOwner"`
	Owner            struct{ Login string } `json:"owner"`
	URL              string                 `json:"url"`
	SSHURL           string                 `json:"sshUrl"`
	IsArchived       bool                   `json:"isArchived"`
	IsFork           bool                   `json:"isFork"`
	IsEmpty          bool                   `json:"isEmpty"`
	Visibility       string                 `json:"visibility"`
	DefaultBranchRef *Named                 `json:"defaultBranchRef"`
	PrimaryLanguage  *Named                 `json:"primaryLanguage"`
	RepositoryTopics struct {
		Nodes []struct {
			Topic Named `json:"topic"`
		} `json:"nodes"`
	} `json:"repositoryTopics"`
}

type page struct {
	Data struct {
		Owner *struct {
			Login        string `json:"login"`
			Repositories struct {
				Nodes    []Repository `json:"nodes"`
				PageInfo *struct {
					HasNextPage bool   `json:"hasNextPage"`
					EndCursor   string `json:"endCursor"`
				} `json:"pageInfo"`
			} `json:"repositories"`
		} `json:"repositoryOwner"`
	} `json:"data"`
	Errors []struct {
		Message string `json:"message"`
	} `json:"errors"`
}

// Discover returns a canonical owner and its complete repository set. It never
// returns partial results following a command, GraphQL, or pagination error.
func Discover(ctx context.Context, runner executil.Runner, host, owner string) (string, []Repository, error) {
	if owner == "" {
		login, err := runner.Run(ctx, "gh", "api", "user", "--hostname", host, "--jq", ".login")
		if err != nil {
			return "", nil, fmt.Errorf("resolve authenticated owner: %w", err)
		}
		owner = strings.TrimSpace(login)
	}
	if !validName(owner) {
		return "", nil, fmt.Errorf("invalid GitHub owner %q", owner)
	}
	output, err := runner.Run(ctx, "gh", "api", "graphql", "--paginate", "--slurp", "--hostname", host, "--raw-field", "owner="+owner, "--raw-field", "query="+query)
	if err != nil {
		return "", nil, fmt.Errorf("list repositories: %w", err)
	}
	var pages []page
	if err := json.Unmarshal([]byte(output), &pages); err != nil {
		return "", nil, fmt.Errorf("decode repository pages: %w", err)
	}
	if len(pages) == 0 {
		return "", nil, fmt.Errorf("repository listing returned no pages")
	}
	canonical := ""
	repositories := make([]Repository, 0)
	seen, cursors := map[string]bool{}, map[string]bool{}
	for index, p := range pages {
		if len(p.Errors) != 0 {
			return "", nil, fmt.Errorf("repository listing: %s", p.Errors[0].Message)
		}
		account := p.Data.Owner
		if account == nil || !validName(account.Login) || !strings.EqualFold(account.Login, owner) {
			return "", nil, fmt.Errorf("GitHub owner %q was not found or listing owner did not match", owner)
		}
		if canonical == "" {
			canonical = account.Login
		}
		info := account.Repositories.PageInfo
		if info == nil || info.HasNextPage != (index < len(pages)-1) || info.HasNextPage && (info.EndCursor == "" || cursors[info.EndCursor]) {
			return "", nil, fmt.Errorf("incomplete or inconsistent repository pagination")
		}
		cursors[info.EndCursor] = true
		for _, repo := range account.Repositories.Nodes {
			if repo.ID == "" || !validRepositoryName(repo.Name) || !strings.EqualFold(repo.Owner.Login, canonical) || !strings.EqualFold(repo.NameWithOwner, canonical+"/"+repo.Name) {
				return "", nil, fmt.Errorf("invalid repository identity in listing for %s", canonical)
			}
			if !seen[repo.ID] {
				seen[repo.ID] = true
				repositories = append(repositories, repo)
			}
		}
	}
	sort.Slice(repositories, func(i, j int) bool {
		return strings.ToLower(repositories[i].Name) < strings.ToLower(repositories[j].Name)
	})
	return canonical, repositories, nil
}

func validName(name string) bool {
	return validRepositoryName(name) && !strings.HasPrefix(name, "-")
}

func validRepositoryName(name string) bool {
	return name != "." && name != ".." && safeName.MatchString(name)
}

// Filter applies metadata filters together. Topic matching follows gh's AND
// semantics and language means the repository's primary language.
func Filter(repositories []Repository, cfg settings.Config) []Repository {
	result := make([]Repository, 0)
	for _, repo := range repositories {
		if !stateMatches(cfg.Archived, repo.IsArchived) || !stateMatches(cfg.Forks, repo.IsFork) {
			continue
		}
		if cfg.Visibility != "any" && !strings.EqualFold(repo.Visibility, cfg.Visibility) {
			continue
		}
		if cfg.Language != "" && (repo.PrimaryLanguage == nil || !strings.EqualFold(repo.PrimaryLanguage.Name, cfg.Language)) {
			continue
		}
		topics := map[string]bool{}
		for _, node := range repo.RepositoryTopics.Nodes {
			topics[strings.ToLower(node.Topic.Name)] = true
		}
		matches := true
		for _, topic := range cfg.Topics {
			if !topics[strings.ToLower(topic)] {
				matches = false
				break
			}
		}
		if matches {
			result = append(result, repo)
		}
	}
	return result
}

func stateMatches(filter string, value bool) bool {
	return filter == "any" || filter == "only" && value || filter == "exclude" && !value
}
