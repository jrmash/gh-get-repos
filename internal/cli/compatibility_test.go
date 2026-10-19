package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jrmash/gh-get-repos/internal/syncgit"
	"github.com/rivo/uniseg"
	"go.yaml.in/yaml/v3"
)

func TestLegacyShortOptions(t *testing.T) {
	o, out, stderr := fixture(t, "charlie", "alpha", "bravo")
	root, _ := o.Getwd()
	var synced []string
	o.Synchronize = func(_ context.Context, repo syncgit.Repository, path string) syncgit.Result {
		synced = append(synced, repo.Name)
		if path != filepath.Join(root, "destination", repo.Name) {
			t.Fatalf("unexpected path %q", path)
		}
		return syncgit.Result{Repository: "Octo/" + repo.Name, Path: path, Status: "cloned"}
	}
	waits := 0
	o.Sleep = func(_ context.Context, duration time.Duration) error {
		waits++
		if duration != 99*time.Second {
			t.Fatalf("sleep %s, want 99s", duration)
		}
		return nil
	}
	args := []string{"Octo", "-a", "any", "-l", "go", "-t", "CLI", "-t", "tools", "-s", "99", "-v", "public", "-p", "destination", "-m", "2", "--format=json"}
	if code := Execute(context.Background(), args, o); code != 0 {
		t.Fatalf("code %d: %s", code, stderr)
	}
	if !reflect.DeepEqual(synced, []string{"alpha", "bravo"}) || waits != 1 {
		t.Fatalf("synced %v, waits %d", synced, waits)
	}
	var doc Document
	if err := json.Unmarshal(out.Bytes(), &doc); err != nil {
		t.Fatal(err)
	}
	if doc.Summary.Selected != 2 || doc.Summary.Processed != 2 {
		t.Fatalf("unexpected summary %+v", doc.Summary)
	}
}

func TestDryRunAndLimitConfigurationPrecedence(t *testing.T) {
	for _, tc := range []struct {
		name        string
		config      string
		environment map[string]string
		args        []string
		dryRun      bool
		selected    int
	}{
		{"configuration", "dry-run: true\nmax-repos: 1\n", nil, nil, true, 1},
		{"canonical environment", "dry-run: false\nmax-repos: 2\n", map[string]string{"GHGR_OPT_DRY_RUN": "true", "GHGR_OPT_LIMIT": "1"}, nil, true, 1},
		{"environment clears configuration", "dry-run: true\nmax-repos: 1\n", map[string]string{"GHGR_OPT_DRY_RUN": "false", "GHGR_OPT_LIMIT": "0"}, nil, false, 2},
		{"flags clear environment", "dry-run: true\nmax-repos: 1\n", map[string]string{"GHGR_OPT_DRY_RUN": "true", "GHGR_OPT_LIMIT": "1"}, []string{"--dry-run=false", "--max-repos=0"}, false, 2},
		{"flags enable preview", "", map[string]string{"GHGR_OPT_DRY_RUN": "false", "GHGR_OPT_LIMIT": "0"}, []string{"--dry-run", "--max-repos=1"}, true, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			o, out, stderr := fixture(t, "a", "b")
			root, _ := o.Getwd()
			if tc.config != "" {
				if err := os.WriteFile(filepath.Join(root, ".gh-get-repos.yml"), []byte(tc.config), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			lookup := o.LookupEnv
			o.LookupEnv = func(key string) (string, bool) {
				if val, ok := tc.environment[key]; ok {
					return val, true
				}
				return lookup(key)
			}
			calls := 0
			o.Synchronize = func(_ context.Context, repo syncgit.Repository, path string) syncgit.Result {
				calls++
				return syncgit.Result{Repository: "Octo/" + repo.Name, Path: path, Status: "cloned"}
			}
			args := append([]string{"Octo", "--format=json"}, tc.args...)
			if code := Execute(context.Background(), args, o); code != 0 {
				t.Fatalf("code %d: %s", code, stderr)
			}
			var doc Document
			if err := json.Unmarshal(out.Bytes(), &doc); err != nil {
				t.Fatal(err)
			}
			if doc.DryRun != tc.dryRun || doc.Summary.Selected != tc.selected || len(doc.Results) != tc.selected {
				t.Fatalf("unexpected document %+v", doc)
			}
			wantCalls := tc.selected
			if tc.dryRun {
				wantCalls = 0
				if doc.Summary.Planned != tc.selected || doc.Summary.Processed != 0 {
					t.Fatalf("preview summary %+v", doc.Summary)
				}
			}
			if calls != wantCalls {
				t.Fatalf("synchronized %d repositories, want %d", calls, wantCalls)
			}
		})
	}
}

func TestDryRunDoesNotChangeCheckoutOrCreateDestination(t *testing.T) {
	for _, format := range []string{"human", "json", "yaml"} {
		for _, conflict := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/conflict=%t", format, conflict), func(t *testing.T) {
				o, out, stderr := fixture(t, "a", "b")
				root, _ := o.Getwd()
				destination := filepath.Join(root, "destination")
				marker := filepath.Join(destination, "a", ".git")
				if conflict {
					if err := os.MkdirAll(filepath.Dir(marker), 0o755); err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(marker, []byte("existing checkout\n"), 0o600); err != nil {
						t.Fatal(err)
					}
				}
				o.Synchronize = func(context.Context, syncgit.Repository, string) syncgit.Result {
					t.Fatal("dry-run attempted synchronization")
					return syncgit.Result{}
				}
				o.Sleep = func(context.Context, time.Duration) error {
					t.Fatal("dry-run slept between repositories")
					return nil
				}
				listingRunner := o.Runner
				o.Runner = fakeRunner(func(ctx context.Context, name string, args ...string) (string, error) {
					if name != "gh" || len(args) < 1 || args[0] != "api" {
						t.Fatalf("dry-run executed %s %v", name, args)
					}
					return listingRunner.Run(ctx, name, args...)
				})
				args := []string{"Octo", "--dry-run", "--mode=worktree", "--sleep=99", "--path=" + destination, "--format=" + format}
				if code := Execute(context.Background(), args, o); code != 0 {
					t.Fatalf("code %d: %s", code, stderr)
				}
				if conflict {
					data, err := os.ReadFile(marker)
					if err != nil || string(data) != "existing checkout\n" {
						t.Fatalf("checkout marker changed: %q, %v", data, err)
					}
					entries, err := os.ReadDir(filepath.Dir(marker))
					if err != nil || len(entries) != 1 || entries[0].Name() != ".git" {
						t.Fatalf("checkout entries changed: %v, %v", entries, err)
					}
					if _, err := os.Stat(filepath.Join(destination, "b")); !os.IsNotExist(err) {
						t.Fatalf("dry-run created second checkout: %v", err)
					}
				} else if _, err := os.Stat(destination); !os.IsNotExist(err) {
					t.Fatalf("dry-run created destination: %v", err)
				}
				if format == "human" {
					if strings.Count(out.String(), " planned") != 3 || !strings.Contains(out.String(), "2 planned") {
						t.Fatalf("unclear dry-run output: %s", out)
					}
					return
				}
				var doc Document
				var err error
				if format == "json" {
					err = json.Unmarshal(out.Bytes(), &doc)
				} else {
					err = yaml.Unmarshal(out.Bytes(), &doc)
				}
				if err != nil {
					t.Fatal(err, out.String())
				}
				if !doc.DryRun || doc.Path != destination || doc.Mode != "worktree" || len(doc.Results) != 2 || doc.Summary != (Summary{Selected: 2, Planned: 2}) {
					t.Fatalf("unexpected dry-run document %+v", doc)
				}
				for index, name := range []string{"a", "b"} {
					result := doc.Results[index]
					if result.Repository != "Octo/"+name || result.Path != filepath.Join(destination, name, "base") || result.Status != "planned" || result.Error != "" {
						t.Fatalf("unexpected dry-run result %+v", result)
					}
				}
				if stderr.Len() != 0 {
					t.Fatalf("unexpected structured-output diagnostics: %s", stderr)
				}
			})
		}
	}
}

// compatibilityPages builds a paginated listing containing repositories that
// must be filtered before the processing limit is applied.
func compatibilityPages(t *testing.T) string {
	t.Helper()
	var pages []map[string]any
	for index, names := range [][]string{{"zeta", "aa-excluded"}, {"bravo", "alpha"}} {
		var one []map[string]any
		if err := json.Unmarshal([]byte(listing(names...)), &one); err != nil {
			t.Fatal(err)
		}
		repositories := one[0]["data"].(map[string]any)["repositoryOwner"].(map[string]any)["repositories"].(map[string]any)
		for _, node := range repositories["nodes"].([]any) {
			repo := node.(map[string]any)
			if repo["name"] == "aa-excluded" {
				repo["primaryLanguage"] = map[string]string{"name": "Rust"}
			}
		}
		repositories["pageInfo"] = map[string]any{"hasNextPage": index == 0, "endCursor": fmt.Sprintf("page-%d", index)}
		pages = append(pages, one[0])
	}
	data, err := json.Marshal(pages)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestMaxReposSelectsAfterCompleteDiscoveryAndFiltering(t *testing.T) {
	for _, tc := range []struct {
		limit string
		want  []string
	}{
		{"1", []string{"alpha"}},
		{"2", []string{"alpha", "bravo"}},
		{"0", []string{"alpha", "bravo", "zeta"}},
		{"20", []string{"alpha", "bravo", "zeta"}},
	} {
		t.Run(tc.limit, func(t *testing.T) {
			o, out, stderr := fixture(t)
			discovered := false
			o.Runner = fakeRunner(func(_ context.Context, name string, args ...string) (string, error) {
				if name != "gh" || len(args) < 2 || args[0] != "api" || args[1] != "graphql" {
					t.Fatalf("unexpected discovery command %s %v", name, args)
				}
				discovered = true
				return compatibilityPages(t), nil
			})
			var synced []string
			o.Synchronize = func(_ context.Context, repo syncgit.Repository, path string) syncgit.Result {
				if !discovered {
					t.Fatal("synchronization began before discovery completed")
				}
				synced = append(synced, repo.Name)
				return syncgit.Result{Repository: "Octo/" + repo.Name, Path: path, Status: "unchanged"}
			}
			args := []string{"Octo", "--language=go", "--max-repos=" + tc.limit, "--format=json"}
			if code := Execute(context.Background(), args, o); code != 0 {
				t.Fatalf("code %d: %s", code, stderr)
			}
			if !reflect.DeepEqual(synced, tc.want) {
				t.Fatalf("synced %v, want %v", synced, tc.want)
			}
			var doc Document
			if err := json.Unmarshal(out.Bytes(), &doc); err != nil {
				t.Fatal(err)
			}
			if doc.Summary.Selected != len(tc.want) || doc.Summary.Processed != len(tc.want) {
				t.Fatalf("limit summary %+v", doc.Summary)
			}
		})
	}
}

func TestMaxReposColumnWidthUsesOnlySelectedRepositories(t *testing.T) {
	o, out, stderr := fixture(t, "zz-repository-name-that-is-not-selected", "b", "aa")
	if code := Execute(context.Background(), []string{"Octo", "--max-repos=2"}, o); code != 0 {
		t.Fatalf("code %d: %s", code, stderr)
	}
	rows := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(rows) != 3 || strings.Contains(out.String(), "zz-repository") {
		t.Fatalf("unselected repositories in output: %s", out)
	}
	for _, row := range rows[:2] {
		index := strings.Index(row, "cloned")
		if index < 0 || uniseg.StringWidth(row[:index]) != 3+len("Octo/aa") {
			t.Fatalf("status column includes unselected name width: %q", row)
		}
	}
}

type cancelOnPlannedRowWriter struct {
	io.Writer
	cancel context.CancelFunc
}

func (w cancelOnPlannedRowWriter) Write(data []byte) (int, error) {
	n, err := w.Writer.Write(data)
	if strings.Contains(string(data), "Octo/a") && strings.Contains(string(data), "planned") {
		w.cancel()
	}
	return n, err
}

func TestDryRunCancellationDuringFinalRow(t *testing.T) {
	o, out, stderr := fixture(t, "a")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	o.Out = cancelOnPlannedRowWriter{Writer: out, cancel: cancel}
	o.Synchronize = func(context.Context, syncgit.Repository, string) syncgit.Result {
		t.Fatal("dry-run attempted synchronization")
		return syncgit.Result{}
	}
	if code := Execute(ctx, []string{"Octo", "--dry-run"}, o); code != 130 {
		t.Fatalf("code %d, stdout %s, stderr %s", code, out, stderr)
	}
	if !strings.Contains(out.String(), "Octo/a") || !strings.Contains(stderr.String(), "interrupted") {
		t.Fatalf("stdout %s, stderr %s", out, stderr)
	}
}

func TestDryRunCancellationAfterEmptyDiscovery(t *testing.T) {
	o, out, stderr := fixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	listingRunner := o.Runner
	o.Runner = fakeRunner(func(ctx context.Context, name string, args ...string) (string, error) {
		result, err := listingRunner.Run(ctx, name, args...)
		cancel()
		return result, err
	})
	if code := Execute(ctx, []string{"Octo", "--dry-run", "--format=json"}, o); code != 130 {
		t.Fatalf("code %d, stdout %s, stderr %s", code, out, stderr)
	}
	var doc Document
	if err := json.Unmarshal(out.Bytes(), &doc); err != nil {
		t.Fatal(err, out.String())
	}
	if !doc.DryRun || doc.Error != "interrupted" || len(doc.Results) != 0 || doc.Summary != (Summary{}) {
		t.Fatalf("unexpected interrupted preview %+v", doc)
	}
}

func TestMaxReposHelpShowsDefault(t *testing.T) {
	o, out, stderr := fixture(t)
	if code := Execute(context.Background(), []string{"--help"}, o); code != 0 {
		t.Fatalf("code %d: %s", code, stderr)
	}
	for _, line := range strings.Split(out.String(), "\n") {
		if strings.Contains(line, "--max-repos") {
			if !strings.Contains(line, "default: 0") || !strings.Contains(line, "unlimited") {
				t.Fatalf("missing limit default or meaning: %s", line)
			}
			return
		}
	}
	t.Fatal("missing --max-repos help")
}
