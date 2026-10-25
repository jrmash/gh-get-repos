package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jrmash/gh-get-repos/internal/settings"
	"github.com/jrmash/gh-get-repos/internal/syncgit"
	"github.com/rivo/uniseg"
	"go.yaml.in/yaml/v3"
)

type fakeRunner func(context.Context, string, ...string) (string, error)

func (f fakeRunner) Run(ctx context.Context, name string, args ...string) (string, error) {
	return f(ctx, name, args...)
}

func listing(names ...string) string {
	nodes := make([]map[string]any, 0, len(names))
	for _, name := range names {
		nodes = append(nodes, map[string]any{
			"id": name, "name": name, "nameWithOwner": "Octo/" + name,
			"owner":      map[string]string{"login": "Octo"},
			"visibility": "PUBLIC", "defaultBranchRef": map[string]string{"name": "main"},
			"primaryLanguage":  map[string]string{"name": "Go"},
			"repositoryTopics": map[string]any{"nodes": []any{map[string]any{"topic": map[string]string{"name": "cli"}}, map[string]any{"topic": map[string]string{"name": "tools"}}}},
		})
	}
	data, _ := json.Marshal([]any{map[string]any{"data": map[string]any{"repositoryOwner": map[string]any{
		"login": "Octo", "repositories": map[string]any{"nodes": nodes, "pageInfo": map[string]any{"hasNextPage": false, "endCursor": "last"}},
	}}}})
	return string(data)
}

func fixture(t *testing.T, names ...string) (Options, *bytes.Buffer, *bytes.Buffer) {
	t.Helper()
	root := t.TempDir()
	out, stderr := &bytes.Buffer{}, &bytes.Buffer{}
	return Options{
		Out: out, Err: stderr, Version: "v1.2.3",
		Getwd: func() (string, error) { return root, nil },
		LookupEnv: func(key string) (string, bool) {
			if key == "GH_CONFIG_DIR" {
				return filepath.Join(root, "global"), true
			}
			return "", false
		},
		Host: func() string { return "github.com" },
		Runner: fakeRunner(func(_ context.Context, name string, args ...string) (string, error) {
			if name != "gh" {
				t.Fatalf("unexpected command %s %v", name, args)
			}
			if len(args) > 1 && args[1] == "user" {
				return "Octo\n", nil
			}
			return listing(names...), nil
		}),
		Synchronize: func(_ context.Context, repo syncgit.Repository, path string) syncgit.Result {
			return syncgit.Result{Repository: repo.Owner + "/" + repo.Name, Path: path, Status: "cloned"}
		},
	}, out, stderr
}

func TestInformationalFlagsBypassConfiguration(t *testing.T) {
	for _, flag := range []string{"--help", "-h", "--version"} {
		t.Run(flag, func(t *testing.T) {
			o, out, stderr := fixture(t)
			o.Getwd = func() (string, error) { t.Fatal("configuration accessed"); return "", nil }
			if code := Execute(context.Background(), []string{flag}, o); code != 0 {
				t.Fatalf("code %d: %s", code, stderr)
			}
			if flag == "--version" {
				if out.String() != "gh get-repos v1.2.3\n" {
					t.Fatal(out.String())
				}
			} else {
				for _, expected := range []string{"gh get-repos [owner]", "--archived string", "--sleep int", "default: 0", "--verbose", "GHGR_*", "--version"} {
					if !strings.Contains(out.String(), expected) {
						t.Errorf("missing %q in %s", expected, out)
					}
				}
			}
		})
	}
}

func TestBatchResultsContinueAndSleep(t *testing.T) {
	o, out, stderr := fixture(t, "zeta", "alpha", "middle")
	root, _ := o.Getwd()
	var synced []string
	o.Synchronize = func(_ context.Context, repo syncgit.Repository, path string) syncgit.Result {
		synced = append(synced, repo.Name)
		if repo.Host != "github.com" || repo.DefaultBranch != "main" || repo.Owner != "Octo" {
			t.Fatalf("repo %+v", repo)
		}
		if path != filepath.Join(root, repo.Name, "base") {
			t.Fatalf("path %s", path)
		}
		status, reason := "updated", ""
		if repo.Name == "middle" {
			status, reason = "skipped", "untracked files"
		}
		if repo.Name == "zeta" {
			status, reason = "failed", "network failed"
		}
		return syncgit.Result{Repository: "Octo/" + repo.Name, Path: path, Status: status, Error: reason}
	}
	waits := 0
	o.Sleep = func(_ context.Context, duration time.Duration) error {
		waits++
		if duration != 2*time.Second {
			t.Fatalf("sleep %s", duration)
		}
		return nil
	}
	code := Execute(context.Background(), []string{"Octo", "--mode=worktree", "--format=json", "--sleep=2"}, o)
	if code != 1 || waits != 2 || strings.Join(synced, ",") != "alpha,middle,zeta" {
		t.Fatalf("code %d waits%d synced%v stderr%s", code, waits, synced, stderr)
	}
	var doc Document
	if err := json.Unmarshal(out.Bytes(), &doc); err != nil {
		t.Fatal(err, out.String())
	}
	if doc.Summary != (Summary{Selected: 3, Processed: 3, Updated: 1, Skipped: 1, Failed: 1}) || doc.Owner != "Octo" {
		t.Fatalf("%+v", doc)
	}
	if strings.Contains(out.String(), "error:") || !strings.Contains(stderr.String(), "could not be synchronized") {
		t.Fatalf("out%s stderr%s", out, stderr)
	}
}

func TestHumanRowsStreamOnce(t *testing.T) {
	o, out, stderr := fixture(t, "a", "b")
	o.Terminal, o.Color = true, true
	o.Synchronize = func(_ context.Context, repo syncgit.Repository, path string) syncgit.Result {
		if repo.Name == "b" && !strings.Contains(out.String(), "Octo/a") {
			t.Fatal("first repository result was not streamed")
		}
		return syncgit.Result{Repository: "Octo/" + repo.Name, Path: path, Status: "cloned"}
	}
	if code := Execute(context.Background(), []string{"Octo"}, o); code != 0 {
		t.Fatalf("code%d %s", code, stderr)
	}
	if strings.Count(out.String(), "Octo/a") != 1 || strings.Count(out.String(), "\n") != 3 || !strings.Contains(out.String(), "\x1b[32m✓") {
		t.Fatal(out.String())
	}
}

func TestConfigurationAndExplicitClears(t *testing.T) {
	o, out, stderr := fixture(t, "a")
	root, _ := o.Getwd()
	if err := os.WriteFile(filepath.Join(root, ".gh-get-repos.yml"), []byte("owner: Octo\nmode: worktree\ntopic: [missing]\nverbose: true\nsleep: 9\n"), 0600); err != nil {
		t.Fatal(err)
	}
	lookup := o.LookupEnv
	o.LookupEnv = func(key string) (string, bool) {
		switch key {
		case "GHGR_TOPIC":
			return "absent", true
		case "GHGR_MODE":
			return "worktree", true
		}
		return lookup(key)
	}
	o.Sleep = func(context.Context, time.Duration) error { t.Fatal("zero delay should not sleep"); return nil }
	if code := Execute(context.Background(), []string{"--format=yaml", "--topic=", "--mode=standard", "--sleep=0", "--verbose=false"}, o); code != 0 {
		t.Fatalf("code%d %s", code, stderr)
	}
	var doc Document
	if err := yaml.Unmarshal(out.Bytes(), &doc); err != nil {
		t.Fatal(err)
	}
	if doc.Mode != "standard" || len(doc.Results) != 1 || doc.Results[0].Path != filepath.Join(root, "a") {
		t.Fatalf("%+v", doc)
	}
}

func TestRepeatedTopicsAndFilters(t *testing.T) {
	for _, args := range [][]string{
		{"--topic=CLI", "--topic=tools"},
		{"--topic=cli,tools", "--language=go", "--visibility=public", "--archived=any", "--forks=any"},
		{"--topic=missing"},
	} {
		o, out, stderr := fixture(t, "a")
		args = append([]string{"Octo", "--format=json"}, args...)
		if code := Execute(context.Background(), args, o); code != 0 {
			t.Fatalf("code%d %s", code, stderr)
		}
		var doc Document
		if err := json.Unmarshal(out.Bytes(), &doc); err != nil {
			t.Fatal(err)
		}
		want := 1
		if args[len(args)-1] == "--topic=missing" {
			want = 0
		}
		if doc.Summary.Selected != want {
			t.Fatalf("args%v doc%+v", args, doc)
		}
	}
}

func TestInputAndRuntimeFailures(t *testing.T) {
	cases := []struct {
		name      string
		args      []string
		configure func(*testing.T, *Options)
		code      int
		contains  string
	}{
		{name: "unknown flag", args: []string{"--config=file"}, code: 2, contains: "unknown flag"},
		{name: "bad int", args: []string{"--sleep=soon"}, code: 2, contains: "invalid argument"},
		{name: "too many owners", args: []string{"one", "two"}, code: 2, contains: "accepts at most"},
		{name: "bad enum", args: []string{"--mode=bare"}, code: 2, contains: "mode must be"},
		{name: "bad yaml", configure: func(t *testing.T, o *Options) {
			root, _ := o.Getwd()
			if err := os.WriteFile(filepath.Join(root, ".gh-get-repos.yml"), []byte("unknown: setting\n"), 0600); err != nil {
				t.Fatal(err)
			}
		}, code: 2, contains: "unknown setting"},
		{name: "cwd", configure: func(_ *testing.T, o *Options) {
			o.Getwd = func() (string, error) { return "", errors.New("cwd unavailable") }
		}, code: 1, contains: "cwd unavailable"},
		{name: "bad host", configure: func(_ *testing.T, o *Options) { o.Host = func() string { return "https://github.com" } }, code: 1, contains: "invalid GitHub hostname"},
		{name: "discovery", configure: func(_ *testing.T, o *Options) {
			o.Runner = fakeRunner(func(context.Context, string, ...string) (string, error) { return "", errors.New("API unavailable") })
		}, code: 1, contains: "API unavailable"},
		{name: "no matches", code: 0, contains: "0 selected"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			o, out, stderr := fixture(t)
			if tc.configure != nil {
				tc.configure(t, &o)
			}
			if code := Execute(context.Background(), tc.args, o); code != tc.code {
				t.Fatalf("code%d want%d stdout%s stderr%s", code, tc.code, out, stderr)
			}
			if !strings.Contains(out.String()+stderr.String(), tc.contains) {
				t.Fatalf("missing%q stdout%s stderr%s", tc.contains, out, stderr)
			}
		})
	}
}

func TestWorktreeLayoutConflict(t *testing.T) {
	o, out, stderr := fixture(t, "a")
	root, _ := o.Getwd()
	if err := os.MkdirAll(filepath.Join(root, "a", ".git"), 0755); err != nil {
		t.Fatal(err)
	}
	o.Synchronize = func(context.Context, syncgit.Repository, string) syncgit.Result {
		t.Fatal("must not nest clone in existing checkout")
		return syncgit.Result{}
	}
	if code := Execute(context.Background(), []string{"Octo", "--mode=worktree", "--format=json"}, o); code != 1 {
		t.Fatal(code, stderr.String())
	}
	var doc Document
	if err := json.Unmarshal(out.Bytes(), &doc); err != nil {
		t.Fatal(err)
	}
	if doc.Summary.Failed != 1 || !strings.Contains(doc.Results[0].Error, "standard layout") {
		t.Fatal(doc)
	}
}

func TestCancellationAndSleepFailures(t *testing.T) {
	for _, when := range []string{"discovery", "before sync", "during sync", "sleep", "sleep failure"} {
		t.Run(when, func(t *testing.T) {
			o, out, stderr := fixture(t, "a", "b")
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			original := o.Runner
			if when == "discovery" || when == "before sync" {
				o.Runner = fakeRunner(func(ctx context.Context, name string, args ...string) (string, error) {
					cancel()
					if when == "discovery" {
						return "", ctx.Err()
					}
					return original.Run(ctx, name, args...)
				})
			}
			if when == "during sync" {
				o.Synchronize = func(_ context.Context, repo syncgit.Repository, path string) syncgit.Result {
					cancel()
					return syncgit.Result{Repository: repo.Name, Path: path, Status: "failed", Error: "cancelled"}
				}
			}
			o.Sleep = func(ctx context.Context, _ time.Duration) error {
				if when == "sleep failure" {
					return errors.New("clock failure")
				}
				cancel()
				return ctx.Err()
			}
			code := Execute(ctx, []string{"Octo", "--format=json", "--sleep=1"}, o)
			want := 130
			if when == "sleep failure" {
				want = 1
			}
			if code != want {
				t.Fatalf("code%d out%s err%s", code, out, stderr)
			}
			if !json.Valid(out.Bytes()) {
				t.Fatal(out.String())
			}
		})
	}
}

type brokenWriter struct{}

func (brokenWriter) Write([]byte) (int, error) { return 0, errors.New("broken output") }

func TestOutputFailures(t *testing.T) {
	for _, format := range []string{"human", "json", "yaml"} {
		t.Run(format, func(t *testing.T) {
			o, _, stderr := fixture(t, "a")
			o.Out = brokenWriter{}
			if code := Execute(context.Background(), []string{"Octo", "--format=" + format}, o); code != 1 {
				t.Fatal(code, stderr.String())
			}
			if !strings.Contains(stderr.String(), "write results") {
				t.Fatal(stderr.String())
			}
		})
	}
}

func TestRenderStatusesAndOneLine(t *testing.T) {
	doc := Document{Results: []syncgit.Result{}}
	for _, status := range []string{"cloned", "updated", "unchanged", "skipped", "failed"} {
		doc.Results = append(doc.Results, syncgit.Result{Repository: "Octo/" + status, Status: status, Error: "first\nsecond\x1b third"})
		doc.Summary.add(status)
	}
	for _, terminal := range []bool{false, true} {
		var out bytes.Buffer
		if err := render(&out, doc, "human", false, terminal); err != nil {
			t.Fatal(err)
		}
		if strings.Count(out.String(), "\n") != 6 || strings.Contains(out.String(), "\x1b") {
			t.Fatal(out.String())
		}
		if terminal && !strings.Contains(out.String(), "✗") {
			t.Fatal(out.String())
		}
	}
	if err := render(brokenWriter{}, doc, "human", true, true); err == nil {
		t.Fatal("expected row output error")
	}
	doc.Results = nil
	if err := render(brokenWriter{}, doc, "human", false, false); err == nil {
		t.Fatal("expected summary output error")
	}
	if doc.Summary.Processed != 5 || doc.Summary.Unchanged != 1 {
		t.Fatal(doc.Summary)
	}
}

func TestDefaultsAndSleep(t *testing.T) {
	o := defaults(Options{})
	if o.Out == nil || o.Err == nil || o.Getwd == nil || o.LookupEnv == nil || o.Host == nil || o.Sleep == nil || o.Version != "dev" {
		t.Fatalf("%+v", o)
	}
	if _, err := o.Getwd(); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GHGR_TEST_DEFAULTS", "present")
	if value, _ := o.LookupEnv("GHGR_TEST_DEFAULTS"); value != "present" {
		t.Fatal(value)
	}
	if err := sleep(context.Background(), 0); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := sleep(ctx, time.Hour); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

func TestRunHasNoPostBatchDelay(t *testing.T) {
	o, _, _ := fixture(t, "a")
	o = defaults(o)
	o.Sleep = func(context.Context, time.Duration) error {
		t.Fatal("must not sleep after last repository")
		return nil
	}
	doc, code := run(context.Background(), settings.Config{Owner: "Octo", Mode: "standard", Path: t.TempDir(), Archived: "exclude", Forks: "exclude", Visibility: "any", Sleep: 9, Format: "json"}, o)
	if code != 0 || doc.Summary.Cloned != 1 {
		t.Fatal(code, doc)
	}
}

func TestDefaultSynchronizerUsesRunner(t *testing.T) {
	o, out, stderr := fixture(t, "a")
	o.Synchronize = nil
	base := o.Runner
	clones := 0
	o.Runner = fakeRunner(func(ctx context.Context, name string, args ...string) (string, error) {
		if name == "gh" && len(args) > 1 && args[0] == "repo" {
			clones++
			return "clone output", nil
		}
		return base.Run(ctx, name, args...)
	})
	if code := Execute(context.Background(), []string{"Octo", "--format=json"}, o); code != 0 || clones != 1 {
		t.Fatal(code, clones, out.String(), stderr.String())
	}
}

func TestNilRunnerCreatedWithoutNetwork(t *testing.T) {
	o, _, stderr := fixture(t)
	o.Runner = nil
	o.Host = func() string { return "bad/host" }
	if code := Execute(context.Background(), []string{"Octo", "--verbose"}, o); code != 1 {
		t.Fatal(code, stderr.String())
	}
}

func ExampleExecute() {
	var output bytes.Buffer
	Execute(context.Background(), []string{"--version"}, Options{Version: "v1.0.0", Out: &output, Err: io.Discard})
	fmt.Print(output.String())
	// Output: gh get-repos v1.0.0
}

func TestProgressStartsBeforeDiscoveryAndClone(t *testing.T) {
	for _, animated := range []bool{false, true} {
		t.Run(fmt.Sprint(animated), func(t *testing.T) {
			o, out, _ := fixture(t, "a")
			logs := &progressRecorder{}
			o.Err, o.ProgressTerminal, o.ProgressWidth = logs, animated, 120
			o.Synchronize = nil
			base := o.Runner
			o.Runner = fakeRunner(func(ctx context.Context, name string, args ...string) (string, error) {
				if args[0] == "api" {
					if !strings.Contains(logs.String(), "Discovering repositories for Octo") {
						t.Fatal("discovery began without feedback")
					}
					return base.Run(ctx, name, args...)
				}
				if args[2] != "github.com/Octo/a" {
					t.Fatal("gh selector changed", args)
				}
				if !strings.Contains(logs.String(), "Cloning Octo/a (1/1)") {
					t.Fatal("clone began without phase feedback", logs.String())
				}
				return "", nil
			})
			if code := Execute(context.Background(), []string{"Octo"}, o); code != 0 {
				t.Fatal(code, logs.String())
			}
			if strings.Count(out.String(), "Octo/a") != 1 || !strings.Contains(out.String(), "cloned") {
				t.Fatal(out.String())
			}
			if animated && !strings.HasSuffix(logs.String(), "\r\x1b[2K") {
				t.Fatal("active spinner remains after completion", logs.String())
			}
			if !animated && strings.Contains(logs.String(), "\x1b") {
				t.Fatal("ANSI progress in plain output")
			}
		})
	}
}

func TestStructuredOutputHasNoProgress(t *testing.T) {
	for _, format := range []string{"json", "yaml"} {
		o, out, stderr := fixture(t, "a")
		o.ProgressTerminal = true
		if code := Execute(context.Background(), []string{"Octo", "--format=" + format}, o); code != 0 {
			t.Fatal(code, stderr.String())
		}
		if stderr.Len() != 0 || strings.Contains(out.String(), "\x1b") {
			t.Fatalf("format%s stdout%s stderr%s", format, out, stderr)
		}
	}
}

func TestVerboseAndDumbTerminalsUsePlainProgress(t *testing.T) {
	for _, verbose := range []bool{false, true} {
		o, _, stderr := fixture(t, "a", "b")
		o.ProgressTerminal = true
		lookup := o.LookupEnv
		o.LookupEnv = func(key string) (string, bool) {
			if key == "TERM" && !verbose {
				return "dumb", true
			}
			return lookup(key)
		}
		o.Sleep = func(context.Context, time.Duration) error { return nil }
		if code := Execute(context.Background(), []string{"Octo", "--sleep=1", fmt.Sprintf("--verbose=%t", verbose)}, o); code != 0 {
			t.Fatal(code, stderr.String())
		}
		if strings.Contains(stderr.String(), "\x1b") || !strings.Contains(stderr.String(), "Waiting 1s") {
			t.Fatal(stderr.String())
		}
	}
}

func TestNoExtensionProtocolOverride(t *testing.T) {
	o, _, stderr := fixture(t)
	if code := Execute(context.Background(), []string{"--git-protocol=https"}, o); code != 2 || !strings.Contains(stderr.String(), "unknown flag") {
		t.Fatal(code, stderr.String())
	}
}

func TestProgressStopsAfterInterruptedOperation(t *testing.T) {
	o, out, _ := fixture(t, "a")
	logs := &progressRecorder{}
	o.Err, o.ProgressTerminal = logs, true
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	o.Synchronize = func(_ context.Context, repo syncgit.Repository, path string) syncgit.Result {
		cancel()
		return syncgit.Result{Repository: repo.Owner + "/" + repo.Name, Path: path, Status: "failed", Error: "interrupted"}
	}
	if code := Execute(ctx, []string{"Octo"}, o); code != 130 {
		t.Fatal(code, logs.String())
	}
	if !strings.Contains(logs.String(), "\r\x1b[2Kerror:") || !strings.Contains(out.String(), "failed") {
		t.Fatal(out.String(), logs.String())
	}
}

func TestStreamedStatusColumnUsesLongestSelectedRepository(t *testing.T) {
	longName := "perl-Dist-Zilla-PluginBundle-Author-JRMASH"
	for _, tc := range []struct {
		name      string
		terminal  bool
		width     int
		shortened bool
	}{
		{"redirected", false, 20, false},
		{"wide terminal", true, 120, false},
		{"narrow terminal", true, 40, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			o, out, stderr := fixture(t, "a", longName)
			o.Terminal, o.OutputWidth = tc.terminal, tc.width
			if code := Execute(context.Background(), []string{"Octo"}, o); code != 0 {
				t.Fatal(code, stderr.String())
			}
			rows := strings.Split(strings.TrimSpace(out.String()), "\n")
			if len(rows) != 3 {
				t.Fatal(out.String())
			}
			column := 3 + len("Octo/"+longName)
			if tc.shortened {
				column = 30
			}
			for _, row := range rows[:2] {
				index := strings.Index(row, "cloned")
				if index < 0 || uniseg.StringWidth(row[:index]) != column {
					t.Fatalf("misaligned status column in %q; want column %d", row, column)
				}
				if tc.shortened && uniseg.StringWidth(row) >= tc.width {
					t.Fatalf("row wraps terminal: %q", row)
				}
			}
			if strings.Contains(out.String(), "Octo/"+longName) == tc.shortened {
				t.Fatal("incorrect repository truncation", out.String())
			}
		})
	}
}
