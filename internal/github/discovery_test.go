package github

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/jrmash/gh-get-repos/internal/settings"
)

type fakeRunner struct {
	calls   [][]string
	outputs []string
	errors  []error
}

func (r *fakeRunner) Run(_ context.Context, name string, args ...string) (string, error) {
	index := len(r.calls)
	r.calls = append(r.calls, append([]string{name}, args...))
	var output string
	var err error
	if index < len(r.outputs) {
		output = r.outputs[index]
	}
	if index < len(r.errors) {
		err = r.errors[index]
	}
	return output, err
}

func repository(name string) Repository {
	repo := Repository{ID: "id-" + name, Name: name, NameWithOwner: "Example/" + name, URL: "https://github.example/Example/" + name, SSHURL: "git@github.example:Example/" + name + ".git", Visibility: "PUBLIC", DefaultBranchRef: &Named{Name: "main"}}
	repo.Owner.Login = "Example"
	return repo
}

func envelope(owner string, repos []Repository, next bool, cursor string) map[string]any {
	return map[string]any{"data": map[string]any{"repositoryOwner": map[string]any{
		"login":        owner,
		"repositories": map[string]any{"nodes": repos, "pageInfo": map[string]any{"hasNextPage": next, "endCursor": cursor}},
	}}}
}

func encoded(t *testing.T, pages ...map[string]any) string {
	t.Helper()
	data, err := json.Marshal(pages)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func account(page map[string]any) map[string]any {
	return page["data"].(map[string]any)["repositoryOwner"].(map[string]any)
}

func connection(page map[string]any) map[string]any {
	return account(page)["repositories"].(map[string]any)
}

func TestDiscoverFetchesAllPagesAndSorts(t *testing.T) {
	var pages []map[string]any
	for pageIndex := range 12 {
		var repos []Repository
		for index := range 100 {
			repos = append(repos, repository(fmt.Sprintf("repo-%04d", 1199-pageIndex*100-index)))
		}
		pages = append(pages, envelope("Example", repos, pageIndex != 11, fmt.Sprintf("cursor-%d", pageIndex)))
	}
	// Repeated entries can occur while the repository set changes between pages.
	last := connection(pages[11])
	last["nodes"] = append(last["nodes"].([]Repository), repository("repo-1199"))
	runner := &fakeRunner{outputs: []string{encoded(t, pages...)}}
	owner, repos, err := Discover(context.Background(), runner, "github.example", "example")
	if err != nil {
		t.Fatal(err)
	}
	if owner != "Example" || len(repos) != 1200 {
		t.Fatalf("owner %q, %d repositories; want Example and 1,200", owner, len(repos))
	}
	if repos[0].Name != "repo-0000" || repos[1199].Name != "repo-1199" {
		t.Fatalf("first/last %q/%q", repos[0].Name, repos[len(repos)-1].Name)
	}
	if len(runner.calls) != 1 {
		t.Fatalf("expected one paginated gh call, got %v", runner.calls)
	}
	call := runner.calls[0]
	for _, required := range []string{"gh", "api", "graphql", "--paginate", "--slurp", "github.example", "owner=example"} {
		if !slices.Contains(call, required) {
			t.Errorf("missing %q in %v", required, call)
		}
	}
	queryFound := false
	for _, arg := range call {
		if strings.HasPrefix(arg, "query=") {
			queryFound = true
			for _, required := range []string{"repositoryOwner(login: $owner)", "first: 100", "after: $endCursor", "ownerAffiliations: [OWNER]", "pageInfo { hasNextPage endCursor }"} {
				if !strings.Contains(arg, required) {
					t.Errorf("query missing %q", required)
				}
			}
		}
	}
	if !queryFound {
		t.Fatal("missing GraphQL query")
	}
}

func TestDiscoverAuthenticatedOwnerAndEmptyListing(t *testing.T) {
	runner := &fakeRunner{outputs: []string{"  Example\n", encoded(t, envelope("Example", nil, false, ""))}}
	owner, repos, err := Discover(context.Background(), runner, "github.example", "")
	if err != nil || owner != "Example" || len(repos) != 0 || repos == nil {
		t.Fatalf("got %q, %v, %v", owner, repos, err)
	}
	want := []string{"gh", "api", "user", "--hostname", "github.example", "--jq", ".login"}
	if len(runner.calls) != 2 || !reflect.DeepEqual(runner.calls[0], want) {
		t.Fatalf("unexpected commands: %v", runner.calls)
	}
}

func TestDiscoverMetadataAndCaseInsensitiveIdentity(t *testing.T) {
	repo := repository("Mixed.Name")
	repo.Owner.Login, repo.NameWithOwner = "eXAMPLE", "example/mixed.name"
	repo.IsEmpty, repo.IsArchived, repo.IsFork = true, true, true
	repo.DefaultBranchRef, repo.PrimaryLanguage = nil, nil
	repo.Visibility = "INTERNAL"
	runner := &fakeRunner{outputs: []string{encoded(t, envelope("Example", []Repository{repo}, false, "last"))}}
	_, repos, err := Discover(context.Background(), runner, "github.example", "EXAMPLE")
	if err != nil || !reflect.DeepEqual(repos, []Repository{repo}) {
		t.Fatalf("metadata changed: %v, %v", repos, err)
	}
}

func TestDiscoverAllowsLegalRepositoryNameWithLeadingHyphen(t *testing.T) {
	repo := repository("-legal")
	runner := &fakeRunner{outputs: []string{encoded(t, envelope("Example", []Repository{repo}, false, "last"))}}
	_, repos, err := Discover(context.Background(), runner, "github.example", "Example")
	if err != nil || !reflect.DeepEqual(repos, []Repository{repo}) {
		t.Fatalf("legal repository rejected: %v, %v", repos, err)
	}
}

func TestDiscoverCommandErrorsAreWrappedAndNeverReturnPartialResults(t *testing.T) {
	failure := errors.New("token expired")
	for _, tt := range []struct {
		name, owner string
		runner      fakeRunner
		message     string
	}{
		{"resolve", "", fakeRunner{errors: []error{failure}}, "resolve authenticated owner"},
		{"listing", "Example", fakeRunner{outputs: []string{encoded(t, envelope("Example", []Repository{repository("repo")}, false, ""))}, errors: []error{failure}}, "list repositories"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			owner, repos, err := Discover(context.Background(), &tt.runner, "github.example", tt.owner)
			if !errors.Is(err, failure) || !strings.Contains(err.Error(), tt.message) || owner != "" || repos != nil {
				t.Fatalf("got %q, %v, %v", owner, repos, err)
			}
		})
	}
	runner := &fakeRunner{outputs: []string{"[{"}}
	_, _, err := Discover(context.Background(), runner, "github.example", "Example")
	var syntax *json.SyntaxError
	if !errors.As(err, &syntax) {
		t.Fatalf("JSON syntax error not preserved: %v", err)
	}
}

func TestDiscoverRejectsInvalidOwnersBeforeListing(t *testing.T) {
	for _, owner := range []string{"../escape", "a/b", `a\b`, "-R", ".", "..", "a b", "https://host/owner", "a\x00b"} {
		t.Run(owner, func(t *testing.T) {
			runner := &fakeRunner{}
			_, _, err := Discover(context.Background(), runner, "github.example", owner)
			if err == nil || !strings.Contains(err.Error(), "invalid GitHub owner") || len(runner.calls) != 0 {
				t.Fatalf("got %v, calls %v", err, runner.calls)
			}
		})
	}
	runner := &fakeRunner{outputs: []string{"../escape\n"}}
	if _, _, err := Discover(context.Background(), runner, "github.example", ""); err == nil || len(runner.calls) != 1 {
		t.Fatalf("invalid authenticated owner: %v, calls %v", err, runner.calls)
	}
}

func TestDiscoverRejectsBadResponsesAndPagination(t *testing.T) {
	valid := func() map[string]any { return envelope("Example", []Repository{repository("repo")}, false, "cursor") }
	cases := []struct {
		name, output, message string
	}{
		{"null", "null", "no pages"},
		{"empty", "[]", "no pages"},
		{"not-array", "{}", "decode repository pages"},
		{"graphql-errors", `[{"data":{"repositoryOwner":null},"errors":[{"message":"permission denied","type":"FORBIDDEN"}]}]`, "permission denied"},
		{"owner-null", `[{"data":{"repositoryOwner":null}}]`, "not found"},
		{"owner-mismatch", encoded(t, envelope("Another", nil, false, "")), "did not match"},
		{"owner-unsafe", encoded(t, envelope("../Example", nil, false, "")), "did not match"},
		{"last-still-has-next", encoded(t, envelope("Example", nil, true, "more")), "pagination"},
		{"premature-final", encoded(t, envelope("Example", nil, false, "first"), valid()), "pagination"},
		{"missing-next-cursor", encoded(t, envelope("Example", nil, true, ""), valid()), "pagination"},
		{"repeated-cursor", encoded(t, envelope("Example", nil, true, "same"), envelope("Example", nil, true, "same"), valid()), "pagination"},
	}
	missingInfo := valid()
	delete(connection(missingInfo), "pageInfo")
	cases = append(cases, struct{ name, output, message string }{"missing-page-info", encoded(t, missingInfo), "pagination"})
	lateError := valid()
	lateError["errors"] = []map[string]string{{"message": "later page unavailable"}}
	cases = append(cases, struct{ name, output, message string }{"later-page-error", encoded(t, envelope("Example", []Repository{repository("first")}, true, "first"), lateError), "later page unavailable"})
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			runner := &fakeRunner{outputs: []string{tt.output}}
			owner, repos, err := Discover(context.Background(), runner, "github.example", "Example")
			if err == nil || !strings.Contains(err.Error(), tt.message) || owner != "" || repos != nil {
				t.Fatalf("got owner %q, repos %v, error %v; want %q", owner, repos, err, tt.message)
			}
		})
	}
}

func TestDiscoverRejectsUnsafeOrMismatchedRepositoryIdentities(t *testing.T) {
	changes := map[string]func(*Repository){
		"no-id":           func(repo *Repository) { repo.ID = "" },
		"owner":           func(repo *Repository) { repo.Owner.Login = "Another" },
		"empty-owner":     func(repo *Repository) { repo.Owner.Login = "" },
		"qualified-name":  func(repo *Repository) { repo.NameWithOwner = "Another/repo" },
		"mismatched-name": func(repo *Repository) { repo.NameWithOwner = "Example/other" },
	}
	for _, name := range []string{".", "..", "../escape", "dir/repo", `dir\repo`, "C:drive", "a\x00b", "repo with space"} {
		changes["unsafe-name-"+name] = func(repo *Repository) { repo.Name, repo.NameWithOwner = name, "Example/"+name }
	}
	for name, change := range changes {
		t.Run(name, func(t *testing.T) {
			repo := repository("repo")
			change(&repo)
			runner := &fakeRunner{outputs: []string{encoded(t, envelope("Example", []Repository{repository("valid"), repo}, false, ""))}}
			owner, repos, err := Discover(context.Background(), runner, "github.example", "Example")
			if err == nil || !strings.Contains(err.Error(), "invalid repository identity") || owner != "" || repos != nil {
				t.Fatalf("got %q, %v, %v", owner, repos, err)
			}
		})
	}
}

func TestFilterStateCombinations(t *testing.T) {
	var repos []Repository
	for _, archived := range []bool{false, true} {
		for _, fork := range []bool{false, true} {
			repo := repository(fmt.Sprintf("archived-%t-fork-%t", archived, fork))
			repo.IsArchived, repo.IsFork = archived, fork
			repos = append(repos, repo)
		}
	}
	for _, archived := range []string{"any", "exclude", "only"} {
		for _, forks := range []string{"any", "exclude", "only"} {
			t.Run(archived+"_"+forks, func(t *testing.T) {
				selected := Filter(repos, settings.Config{Archived: archived, Forks: forks, Visibility: "any"})
				want := 1
				if archived == "any" {
					want *= 2
				}
				if forks == "any" {
					want *= 2
				}
				if len(selected) != want {
					t.Fatalf("got %d repos, want %d", len(selected), want)
				}
				for _, repo := range selected {
					if archived == "only" && !repo.IsArchived || archived == "exclude" && repo.IsArchived || forks == "only" && !repo.IsFork || forks == "exclude" && repo.IsFork {
						t.Fatalf("incorrectly selected %+v", repo)
					}
				}
			})
		}
	}
}

func TestFilterLanguageVisibilityAndAllTopics(t *testing.T) {
	repo := repository("match")
	repo.PrimaryLanguage = &Named{Name: "Go"}
	if err := json.Unmarshal([]byte(`{"nodes":[{"topic":{"name":"CLI"}},{"topic":{"name":"backend"}}]}`), &repo.RepositoryTopics); err != nil {
		t.Fatal(err)
	}
	missingLanguage := repository("no-language")
	otherLanguage := repository("rust")
	otherLanguage.PrimaryLanguage = &Named{Name: "Rust"}
	private := repo
	private.ID, private.Name, private.Visibility = "private", "private", "PRIVATE"
	internal := repo
	internal.ID, internal.Name, internal.Visibility = "internal", "internal", "INTERNAL"
	noTopics := repo
	noTopics.ID, noTopics.Name, noTopics.RepositoryTopics.Nodes = "no-topics", "no-topics", nil
	repos := []Repository{repo, missingLanguage, otherLanguage, private, internal, noTopics}
	cases := []struct {
		name       string
		visibility string
		language   string
		topics     []string
		want       []string
	}{
		{"unfiltered-null-metadata", "any", "", nil, []string{"match", "no-language", "rust", "private", "internal", "no-topics"}},
		{"public", "public", "", nil, []string{"match", "no-language", "rust", "no-topics"}},
		{"private", "private", "", nil, []string{"private"}},
		{"internal", "internal", "", nil, []string{"internal"}},
		{"language-case", "any", "gO", nil, []string{"match", "private", "internal", "no-topics"}},
		{"one-topic", "any", "", []string{"cLi"}, []string{"match", "private", "internal"}},
		{"all-topics", "any", "", []string{"CLI", "BACKEND"}, []string{"match", "private", "internal"}},
		{"one-topic-missing", "any", "", []string{"cli", "missing"}, nil},
		{"all-filters", "public", "go", []string{"cli", "backend"}, []string{"match"}},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			selected := Filter(repos, settings.Config{Archived: "any", Forks: "any", Visibility: tt.visibility, Language: tt.language, Topics: tt.topics})
			var got []string
			for _, repo := range selected {
				got = append(got, repo.Name)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("got %v, want %v", got, tt.want)
			}
		})
	}
	if got := Filter(nil, settings.Config{Archived: "any", Forks: "any", Visibility: "any"}); len(got) != 0 {
		t.Fatalf("empty input produced %v", got)
	}
}

func TestValidRepositoryNames(t *testing.T) {
	for _, name := range []string{"repo", "repo-name", "repo.name", "_repo", ".github", "0", "repo_123", "-legal"} {
		if !validRepositoryName(name) {
			t.Errorf("rejected valid repository name %q", name)
		}
	}
}
