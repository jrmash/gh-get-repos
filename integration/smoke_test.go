//go:build smoke

// Package integration exercises a locally installed extension with real gh,
// Git, and the public GitHub API. Run explicitly with go test -tags smoke.
package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/jrmash/gh-get-repos/internal/cli"
)

const fixtureOwner = "octocat"
const fixtureRepository = "octocat/boysenberry-repo-1"

func TestInstalledExtension(t *testing.T) {
	for key, actual := range map[string]string{"GHGR_SMOKE_GOOS": runtime.GOOS, "GHGR_SMOKE_GOARCH": runtime.GOARCH} {
		if expected := os.Getenv(key); expected != "" && expected != actual {
			t.Fatalf("%s requires %s; running natively as %s", key, expected, actual)
		}
	}
	t.Logf("Native smoke test: %s/%s", runtime.GOOS, runtime.GOARCH)
	gh, err := exec.LookPath("gh")
	if err != nil {
		t.Fatal("smoke tests require GitHub CLI on PATH")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Fatal("smoke tests require Git on PATH")
	}
	token := os.Getenv("GH_TOKEN")
	if token == "" {
		token = os.Getenv("GITHUB_TOKEN")
	}
	if token == "" {
		// Local runs can reuse existing authentication without copying config or
		// exposing the token in command arguments or test output.
		cmd := exec.Command(gh, "auth", "token", "--hostname", "github.com")
		output, err := cmd.Output()
		if err != nil {
			t.Fatal("smoke tests require GH_TOKEN, GITHUB_TOKEN, or existing gh authentication")
		}
		token = strings.TrimSpace(string(output))
	}
	if token == "" {
		t.Fatal("smoke tests require a GitHub token")
	}

	root := t.TempDir()
	installation := filepath.Join(root, "gh-get-repos")
	working := filepath.Join(root, "working")
	for _, directory := range []string{installation, working} {
		if err := os.Mkdir(directory, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	binary := filepath.Join(installation, "gh-get-repos")
	if runtime.GOOS == "windows" {
		binary += ".exe"
		// gh dispatches local Windows extensions through sh and requires an
		// extensionless entry point. The actual program remains a native .exe.
		quotedBinary := "'" + strings.ReplaceAll(filepath.ToSlash(binary), "'", "'\\''") + "'"
		launcher := "#!/bin/sh\nexec " + quotedBinary + " \"$@\"\n"
		if err := os.WriteFile(filepath.Join(installation, "gh-get-repos"), []byte(launcher), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	command(t, "..", os.Environ(), "go", "build", "-buildvcs=false", "-ldflags=-X main.version=smoke", "-o", binary, ".")
	env := isolatedEnvironment(root, token)
	command(t, working, env, gh, "config", "set", "git_protocol", "https", "--host", "github.com")
	command(t, installation, env, gh, "extension", "install", ".")
	if version := strings.TrimSpace(command(t, working, env, gh, "get-repos", "--version")); version != "gh get-repos smoke" {
		t.Fatalf("installed extension version = %q; want gh get-repos smoke", version)
	}
	help := command(t, working, env, gh, "get-repos", "--help")
	for _, flag := range []string{"--dry-run", "--max-repos", "--mode", "--format"} {
		if !strings.Contains(help, flag) {
			t.Fatalf("installed extension help omits %s", flag)
		}
	}

	for _, mode := range []string{"standard", "worktree"} {
		t.Run(mode, func(t *testing.T) {
			destination := filepath.Join(working, mode)
			args := []string{"get-repos", fixtureOwner, "--archived", "any", "--forks", "any", "--visibility", "public", "--max-repos", "1", "--format", "json", "--mode", mode, "--path", destination}
			before := snapshot(t, working)
			planned := invoke(t, working, env, gh, append(args, "--dry-run")...)
			assertPlanned(t, planned)
			if after := snapshot(t, working); len(snapshotChanges(before, after)) != 0 {
				diagnoseSnapshotChange(t, working, working, env, before, after)
				t.Fatal("dry-run changed files or created the missing destination")
			}
			checkout := filepath.Join(destination, "boysenberry-repo-1")
			if mode == "worktree" {
				checkout = filepath.Join(checkout, "base")
			}
			if filepath.Clean(planned.Results[0].Path) != checkout {
				t.Fatalf("planned destination = %q; want %q", planned.Results[0].Path, checkout)
			}

			cloned := invoke(t, working, env, gh, args...)
			assertStatus(t, cloned, "cloned")
			if cloned.DryRun || cloned.Summary.Cloned != 1 || cloned.Summary.Processed != 1 {
				t.Fatalf("unexpected clone summary: %+v", cloned.Summary)
			}
			if _, err := os.Stat(filepath.Join(checkout, ".git")); err != nil {
				t.Fatalf("clone did not create the expected checkout: %v", err)
			}
			unchanged := invoke(t, working, env, gh, args...)
			assertStatus(t, unchanged, "unchanged")
			if unchanged.Summary.Unchanged != 1 {
				t.Fatalf("unexpected repeat summary: %+v", unchanged.Summary)
			}
			head := strings.TrimSpace(command(t, checkout, env, "git", "rev-parse", "HEAD"))
			// This disposable clone can be moved behind its real GitHub origin;
			// the next run must fetch and fast-forward without a remote write.
			command(t, checkout, env, "git", "reset", "--hard", "HEAD^")
			before = snapshot(t, destination)
			assertPlanned(t, invoke(t, working, env, gh, append(args, "--dry-run")...))
			if after := snapshot(t, destination); len(snapshotChanges(before, after)) != 0 {
				diagnoseSnapshotChange(t, destination, checkout, env, before, after)
				t.Fatal("dry-run changed an existing checkout, including its Git metadata")
			}
			updated := invoke(t, working, env, gh, args...)
			assertStatus(t, updated, "updated")
			if updated.Summary.Updated != 1 {
				t.Fatalf("unexpected update summary: %+v", updated.Summary)
			}
			if got := strings.TrimSpace(command(t, checkout, env, "git", "rev-parse", "HEAD")); got != head {
				t.Fatalf("updated HEAD = %s; want original upstream HEAD %s", got, head)
			}
			if status := command(t, checkout, env, "git", "status", "--porcelain"); status != "" {
				t.Fatalf("updated checkout is dirty: %s", status)
			}
		})
	}
	command(t, working, env, gh, "extension", "remove", "get-repos")
}

func assertPlanned(t *testing.T, doc cli.Document) {
	t.Helper()
	assertStatus(t, doc, "planned")
	s := doc.Summary
	if !doc.DryRun || s.Planned != 1 || s.Processed != 0 || s.Cloned+s.Updated+s.Unchanged+s.Skipped+s.Failed != 0 {
		t.Fatalf("dry-run reported operations: dry_run=%t summary=%+v", doc.DryRun, s)
	}
}

func assertStatus(t *testing.T, doc cli.Document, status string) {
	t.Helper()
	if doc.Error != "" || doc.Owner != fixtureOwner || doc.Summary.Selected != 1 || len(doc.Results) != 1 {
		t.Fatalf("unexpected smoke result: %+v", doc)
	}
	result := doc.Results[0]
	if result.Repository != fixtureRepository || result.Status != status || result.Error != "" {
		t.Fatalf("result = %+v; want %s %s (the public fixture may have changed)", result, fixtureRepository, status)
	}
}

func invoke(t *testing.T, cwd string, env []string, gh string, args ...string) cli.Document {
	t.Helper()
	output := command(t, cwd, env, gh, args...)
	var doc cli.Document
	if err := json.Unmarshal([]byte(output), &doc); err != nil {
		t.Fatalf("installed command did not produce a clean JSON document: %v\n%s", err, output)
	}
	return doc
}

func command(t *testing.T, cwd string, env []string, executable string, args ...string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, executable, args...)
	cmd.Dir, cmd.Env = cwd, env
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("%s %s failed: %v\nstdout:\n%s\nstderr:\n%s", filepath.Base(executable), strings.Join(args, " "), err, stdout.String(), stderr.String())
	}
	return stdout.String()
}

func isolatedEnvironment(root, token string) []string {
	env := []string{}
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		key = strings.ToUpper(key)
		if strings.HasPrefix(key, "GH_") || strings.HasPrefix(key, "GHGR_") || strings.HasPrefix(key, "GIT_") || key == "GITHUB_TOKEN" || strings.HasPrefix(key, "XDG_") {
			continue
		}
		env = append(env, entry)
	}
	return append(env,
		"GH_TOKEN="+token,
		"GH_HOST=github.com",
		"GH_CONFIG_DIR="+filepath.Join(root, "config"),
		"XDG_CONFIG_HOME="+filepath.Join(root, "xdg-config"),
		"XDG_DATA_HOME="+filepath.Join(root, "data"),
		"XDG_STATE_HOME="+filepath.Join(root, "state"),
		"XDG_CACHE_HOME="+filepath.Join(root, "cache"),
		"GH_PROMPT_DISABLED=1", "GH_NO_UPDATE_NOTIFIER=1", "GH_NO_EXTENSION_UPDATE_NOTIFIER=1",
		"GIT_CONFIG_GLOBAL="+filepath.Join(root, "empty-git-config"), "GIT_CONFIG_NOSYSTEM=1", "GIT_TERMINAL_PROMPT=0", "NO_COLOR=1",
	)
}
