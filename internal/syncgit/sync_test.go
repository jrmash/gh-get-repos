package syncgit

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/jrmash/gh-get-repos/internal/executil"
)

type fixture struct {
	t      *testing.T
	root   string
	remote string
	seed   string
	repo   Repository
	runner *fixtureRunner
}

type fixtureRunner struct {
	remote string
	origin string
	calls  []string
	hook   func(string, []string) (string, error, bool)
}

func (r *fixtureRunner) Run(ctx context.Context, command string, args ...string) (string, error) {
	r.calls = append(r.calls, command+" "+strings.Join(args, " "))
	if r.hook != nil {
		if out, err, handled := r.hook(command, args); handled {
			return out, err
		}
	}
	if command == "gh" {
		selector := strings.TrimSuffix(strings.TrimPrefix(r.origin, "https://"), ".git")
		if len(args) != 5 || args[0] != "repo" || args[1] != "clone" || args[2] != selector || args[4] != "--no-upstream" {
			return "", errors.New("unexpected gh clone arguments: " + strings.Join(args, " "))
		}
		command, args = "git", []string{"clone", "--no-hardlinks", r.remote, args[3]}
	}
	cmd := exec.CommandContext(ctx, command, args...)
	cmd.Env = gitEnv()
	out, err := cmd.CombinedOutput()
	if err != nil {
		return string(out), err
	}
	if len(args) >= 3 && strings.Join(args[len(args)-3:], " ") == "remote get-url origin" && samePath(strings.TrimSpace(string(out)), r.remote) {
		return r.origin + "\n", nil
	}
	return string(out), nil
}

func gitEnv() []string {
	// Local fixtures need no inherited Git context, transport, or configuration.
	// In particular, caller-injected templates and GIT_CONFIG_COUNT must not
	// install hooks or override the fixture's disabled global configuration.
	env := make([]string, 0, len(os.Environ())+6)
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		if !strings.HasPrefix(strings.ToUpper(key), "GIT_") {
			env = append(env, entry)
		}
	}
	return append(env, "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL="+os.DevNull,
		"GIT_AUTHOR_NAME=Fixture", "GIT_AUTHOR_EMAIL=fixture@example.test", "GIT_COMMITTER_NAME=Fixture", "GIT_COMMITTER_EMAIL=fixture@example.test", "GIT_TERMINAL_PROMPT=0")
}

func newFixture(t *testing.T, empty bool) *fixture {
	t.Helper()
	root := t.TempDir()
	f := &fixture{t: t, root: root, remote: filepath.Join(root, "remote.git"), seed: filepath.Join(root, "seed"),
		repo: Repository{Host: "github.com", Owner: "acme", Name: "project", DefaultBranch: "main", Empty: empty}}
	f.runner = &fixtureRunner{remote: f.remote, origin: "https://github.com/acme/project.git"}
	f.git("init", "--bare", "--initial-branch=main", f.remote)
	f.git("clone", f.remote, f.seed)
	if !empty {
		f.write(f.seed, "README.md", "initial\n")
		f.commit(f.seed, "initial")
		f.git("-C", f.seed, "push", "origin", "main")
	}
	return f
}

func (f *fixture) git(args ...string) string {
	f.t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Env = gitEnv()
	out, err := cmd.CombinedOutput()
	if err != nil {
		f.t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func (f *fixture) write(dir, name, data string) {
	f.t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(data), 0o600); err != nil {
		f.t.Fatal(err)
	}
}

func (f *fixture) commit(path, message string) {
	f.t.Helper()
	f.git("-C", path, "add", "--all")
	f.git("-C", path, "commit", "-m", message)
}

func (f *fixture) clone(path string) {
	f.t.Helper()
	f.git("clone", f.remote, path)
}

func (f *fixture) sync(path string, want string) Result {
	f.t.Helper()
	result := (Synchronizer{Runner: f.runner}).Sync(context.Background(), f.repo, path)
	if result.Status != want {
		f.t.Fatalf("status = %q, want %q: %+v", result.Status, want, result)
	}
	if (want == "skipped" || want == "failed") && result.Error == "" {
		f.t.Fatal("missing actionable error")
	}
	return result
}

func TestCloneLayoutsAndEmptyDirectories(t *testing.T) {
	for _, layout := range []string{"project", filepath.Join("project", "base")} {
		t.Run(layout, func(t *testing.T) {
			f := newFixture(t, false)
			path := filepath.Join(f.root, "checkout", layout)
			result := f.sync(path, "cloned")
			if result.Repository != "acme/project" || result.Path != path || result.Error != "" {
				t.Fatalf("unexpected result: %+v", result)
			}
			f.sync(path, "unchanged")
		})
	}
	f := newFixture(t, false)
	path := filepath.Join(f.root, "empty-directory")
	if err := os.Mkdir(path, 0o755); err != nil {
		t.Fatal(err)
	}
	f.sync(path, "cloned")
}

func TestEmptyRepositories(t *testing.T) {
	f := newFixture(t, true)
	f.repo.DefaultBranch = ""
	path := filepath.Join(f.root, "checkout")
	f.sync(path, "cloned")
	f.sync(path, "unchanged")
	f.write(path, "local", "local")
	f.commit(path, "local")
	f.sync(path, "skipped")
	f.git("-C", path, "switch", "--detach")
	f.git("-C", path, "branch", "-D", "main")
	f.sync(path, "skipped")
}

func TestFastForwardAndFeatureSwitch(t *testing.T) {
	for _, feature := range []bool{false, true} {
		t.Run(map[bool]string{false: "main", true: "feature"}[feature], func(t *testing.T) {
			f := newFixture(t, false)
			path := filepath.Join(f.root, "checkout")
			f.clone(path)
			if feature {
				f.git("-C", path, "switch", "-c", "feature")
			}
			f.write(f.seed, "README.md", "next\n")
			f.commit(f.seed, "next")
			f.git("-C", f.seed, "push")
			f.sync(path, "updated")
			if f.git("-C", path, "rev-parse", "HEAD") != f.git("-C", f.seed, "rev-parse", "HEAD") {
				t.Fatal("checkout did not advance")
			}
			if f.git("-C", path, "branch", "--show-current") != "main" {
				t.Fatal("default branch was not selected")
			}
		})
	}
}

func TestFeatureSwitchWithNoNewCommitsAndMissingDefaultBranch(t *testing.T) {
	for _, removeDefault := range []bool{false, true} {
		t.Run(map[bool]string{false: "existing", true: "missing"}[removeDefault], func(t *testing.T) {
			f := newFixture(t, false)
			path := filepath.Join(f.root, "checkout")
			f.clone(path)
			f.git("-C", path, "switch", "-c", "feature")
			if removeDefault {
				f.git("-C", path, "branch", "-D", "main")
			}
			f.sync(path, "updated")
			if f.git("-C", path, "branch", "--show-current") != "main" {
				t.Fatal("default branch was not selected")
			}
		})
	}
}

func TestPreserveAheadAndDivergentBranches(t *testing.T) {
	for _, divergent := range []bool{false, true} {
		t.Run(map[bool]string{false: "ahead", true: "divergent"}[divergent], func(t *testing.T) {
			f := newFixture(t, false)
			path := filepath.Join(f.root, "checkout")
			f.clone(path)
			f.write(path, "local", "local\n")
			f.commit(path, "local")
			before := f.git("-C", path, "rev-parse", "main")
			f.git("-C", path, "switch", "-c", "feature")
			if divergent {
				f.write(f.seed, "remote", "remote\n")
				f.commit(f.seed, "remote")
				f.git("-C", f.seed, "push")
			}
			result := f.sync(path, "skipped")
			if divergent && !strings.Contains(result.Error, "diverged") {
				t.Fatalf("unexpected error: %s", result.Error)
			}
			if before != f.git("-C", path, "rev-parse", "main") || f.git("-C", path, "branch", "--show-current") != "feature" {
				t.Fatal("local branches were changed")
			}
		})
	}
}

func TestUnsafeCheckoutsArePreserved(t *testing.T) {
	for _, kind := range []string{"tracked", "untracked", "detached", "operation", "wrong-origin", "no-origin", "nested", "unrelated", "file", "symlink"} {
		t.Run(kind, func(t *testing.T) {
			f := newFixture(t, false)
			path := filepath.Join(f.root, "checkout")
			f.clone(path)
			want := "skipped"
			switch kind {
			case "tracked":
				f.write(path, "README.md", "changes")
			case "untracked":
				f.write(path, "untracked", "changes")
			case "detached":
				f.git("-C", path, "switch", "--detach")
			case "operation":
				f.write(filepath.Join(path, ".git"), "MERGE_HEAD", f.git("-C", path, "rev-parse", "HEAD"))
			case "wrong-origin":
				f.git("-C", path, "remote", "set-url", "origin", "https://github.com/other/wrong.git")
				want = "failed"
			case "no-origin":
				f.git("-C", path, "remote", "remove", "origin")
				want = "failed"
			case "nested":
				path = filepath.Join(path, "nested")
				if err := os.Mkdir(path, 0o755); err != nil {
					t.Fatal(err)
				}
				f.write(path, "unrelated", "data")
				want = "failed"
			case "unrelated":
				path = filepath.Join(f.root, "other")
				if err := os.Mkdir(path, 0o755); err != nil {
					t.Fatal(err)
				}
				f.write(path, "unrelated", "data")
				want = "failed"
			case "file":
				f.write(f.root, "file", "data")
				path = filepath.Join(f.root, "file")
				want = "failed"
			case "symlink":
				linked := filepath.Join(f.root, "linked")
				if err := os.Symlink(path, linked); err != nil {
					t.Skipf("symbolic links unsupported: %v", err)
				}
				path, want = linked, "failed"
			}
			f.sync(path, want)
		})
	}
}

func TestLinkedAndSiblingWorktrees(t *testing.T) {
	f := newFixture(t, false)
	path := filepath.Join(f.root, "checkout")
	f.clone(path)
	f.git("-C", path, "switch", "-c", "feature")
	linked := filepath.Join(f.root, "linked")
	f.git("-C", path, "worktree", "add", linked, "main")
	f.sync(path, "skipped")
	f.sync(linked, "unchanged")
}

func TestIgnoredFilesAreNotOverwritten(t *testing.T) {
	for _, switchBranch := range []bool{false, true} {
		t.Run(map[bool]string{false: "merge", true: "switch"}[switchBranch], func(t *testing.T) {
			f := newFixture(t, false)
			f.write(f.seed, ".gitignore", "collision\n")
			f.commit(f.seed, "ignore")
			f.git("-C", f.seed, "push")
			path := filepath.Join(f.root, "checkout")
			f.clone(path)
			if switchBranch {
				f.git("-C", path, "switch", "-c", "feature")
			}
			f.write(f.seed, "collision", "remote")
			f.git("-C", f.seed, "add", "--force", "collision")
			f.git("-C", f.seed, "commit", "-m", "add collision")
			f.git("-C", f.seed, "push")
			if switchBranch {
				f.git("-C", path, "fetch")
				f.git("-C", path, "branch", "--force", "main", "origin/main")
			}
			f.write(path, "collision", "precious")
			f.sync(path, "failed")
			data, err := os.ReadFile(filepath.Join(path, "collision"))
			if err != nil || string(data) != "precious" {
				t.Fatal("ignored file was overwritten")
			}
		})
	}
}

func TestDirtySubmoduleIsSkipped(t *testing.T) {
	f := newFixture(t, false)
	sub := filepath.Join(f.root, "submodule")
	f.git("init", "--initial-branch=main", sub)
	f.write(sub, "file", "initial")
	f.commit(sub, "initial")
	f.git("-C", f.seed, "-c", "protocol.file.allow=always", "submodule", "add", sub, "sub")
	f.commit(f.seed, "submodule")
	f.git("-C", f.seed, "push")
	path := filepath.Join(f.root, "checkout")
	f.clone(path)
	f.git("-C", path, "-c", "protocol.file.allow=always", "submodule", "update", "--init")
	// The explicit status option must detect dirt even when local configuration
	// would otherwise hide all modifications within this submodule.
	f.git("-C", path, "config", "submodule.sub.ignore", "all")
	f.write(filepath.Join(path, "sub"), "file", "dirty")
	f.sync(path, "skipped")
	f.git("-C", filepath.Join(path, "sub"), "restore", "--", "file")
	f.write(filepath.Join(path, "sub"), "untracked", "local")
	f.sync(path, "skipped")
}

func TestFailuresAndCancellation(t *testing.T) {
	f := newFixture(t, false)
	path := filepath.Join(f.root, "checkout")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	result := (Synchronizer{Runner: f.runner}).Sync(ctx, f.repo, path)
	if result.Status != "failed" || !strings.Contains(result.Error, "canceled") {
		t.Fatalf("unexpected cancellation: %+v", result)
	}
	f.runner.hook = func(command string, _ []string) (string, error, bool) {
		return "", errors.New("clone interruption"), command == "gh"
	}
	f.sync(path, "failed")
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("extension created clone destination itself")
	}
	f.runner.hook = nil
	f.clone(path)
	f.repo.DefaultBranch = ""
	f.sync(path, "failed")
	f.repo.DefaultBranch = "-unsafe"
	f.sync(path, "failed")
	f.repo.DefaultBranch = "missing"
	f.sync(path, "failed")
}

func TestCommandFailures(t *testing.T) {
	for _, match := range []string{"rev-parse --absolute-git-dir", "status --porcelain", "symbolic-ref --quiet", "worktree list", "show-ref --verify", "rev-list --left-right", "switch --no-overwrite-ignore", "for-each-ref --format"} {
		t.Run(match, func(t *testing.T) {
			f := newFixture(t, false)
			path := filepath.Join(f.root, "checkout")
			f.clone(path)
			f.git("-C", path, "switch", "-c", "feature")
			if strings.HasPrefix(match, "for-each-ref") {
				f.repo.Empty = true
			}
			f.runner.hook = func(command string, args []string) (string, error, bool) {
				if command == "git" && strings.Contains(strings.Join(args, " "), match) {
					return "", errors.New("injected failure"), true
				}
				return "", nil, false
			}
			f.sync(path, "failed")
		})
	}
}

func TestHelpers(t *testing.T) {
	// Exercise SSH URL parsing with the supported missing-SSH fallback, without
	// reading the developer's SSH configuration. Alias resolution belongs to
	// the go-gh translator and is covered by that library's tests.
	t.Setenv("PATH", t.TempDir())
	repo := Repository{Host: "github.com", Owner: "acme", Name: "project"}
	for _, origin := range []string{"https://github.com/acme/project.git", "https://GitHub.com/ACME/PROJECT", "ssh://git@github.com/acme/project.git", "git@github.com:acme/project.git"} {
		if err := verifyOrigin(origin, repo); err != nil {
			t.Errorf("verify %q: %v", origin, err)
		}
	}
	for _, origin := range []string{"not a URL", "github.com/acme/project", "acme/project", "file://github.com/acme/project.git", "https://github.com/wrong/project", "https://example.com/acme/project", "https://github.com/acme/other"} {
		if verifyOrigin(origin, repo) == nil {
			t.Errorf("accepted %q", origin)
		}
	}
	for _, input := range []string{"", "1", "1 2 3", "x 2", "1 x", "-1 2", "1 -2"} {
		if _, _, err := parseCounts(input); err == nil {
			t.Errorf("accepted counts %q", input)
		}
	}
	if a, b, err := parseCounts("1\t2"); a != 1 || b != 2 || err != nil {
		t.Fatalf("counts: %d %d %v", a, b, err)
	}
	for _, input := range []string{"", ".", "..", "../repo", "a\\b", "a b", "-option"} {
		if validComponent(input) {
			t.Errorf("accepted component %q", input)
		}
	}
	if !validComponent("valid.repo-name_1") || exitCode(errors.New("unknown")) != -1 || samePath("/does-not-exist", "/does-not-exist") {
		t.Fatal("helper behavior mismatch")
	}
}

// productionFixtureRunner substitutes only network endpoints. All Git commands
// and their errors pass through the application's actual production runner.
type productionFixtureRunner struct {
	remote string
	origin string
	runner executil.ProcessRunner
}

func (r productionFixtureRunner) Run(ctx context.Context, name string, args ...string) (string, error) {
	if name == "gh" && len(args) == 5 && args[0] == "repo" && args[1] == "clone" {
		return r.runner.Run(ctx, "git", "clone", r.remote, args[3])
	}
	output, err := r.runner.Run(ctx, name, args...)
	if err == nil && name == "git" && len(args) >= 3 && strings.Join(args[len(args)-3:], " ") == "remote get-url origin" && samePath(strings.TrimSpace(output), r.remote) {
		return r.origin, nil
	}
	return output, err
}

func TestProductionRunnerCreatesMissingDefaultBranch(t *testing.T) {
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	for _, unborn := range []bool{false, true} {
		t.Run(map[bool]string{false: "feature-checkout", true: "previously-empty-remote"}[unborn], func(t *testing.T) {
			f := newFixture(t, unborn)
			path := filepath.Join(f.root, "checkout")
			f.clone(path)
			if unborn {
				f.write(f.seed, "README.md", "first commit")
				f.commit(f.seed, "first commit")
				f.git("-C", f.seed, "push", "origin", "main")
				f.repo.Empty = false
			} else {
				f.git("-C", path, "switch", "-c", "feature")
				f.git("-C", path, "branch", "-D", "main")
			}
			runner := productionFixtureRunner{remote: f.remote, origin: f.runner.origin}
			result := (Synchronizer{Runner: runner}).Sync(context.Background(), f.repo, path)
			if result.Status != "updated" {
				t.Fatalf("production synchronization: %+v", result)
			}
			if branch := f.git("-C", path, "branch", "--show-current"); branch != "main" {
				t.Fatalf("current branch = %q", branch)
			}
			if upstream := f.git("-C", path, "rev-parse", "--abbrev-ref", "@{upstream}"); upstream != "origin/main" {
				t.Fatalf("upstream = %q", upstream)
			}
		})
	}
}

func TestLeadingHyphenRepositoryName(t *testing.T) {
	f := newFixture(t, false)
	f.repo.Name = "-project"
	f.runner.origin = "https://github.com/acme/-project.git"
	path := filepath.Join(f.root, "-project")
	f.sync(path, "cloned")
	f.sync(path, "unchanged")
}

func TestProgressBeforeGitOperations(t *testing.T) {
	f := newFixture(t, false)
	path := filepath.Join(f.root, "checkout")
	var phases []string
	synchronizer := Synchronizer{Runner: f.runner, Progress: func(phase string) { phases = append(phases, phase) }}
	f.runner.hook = func(command string, args []string) (string, error, bool) {
		if command == "gh" && (len(phases) == 0 || phases[len(phases)-1] != "Cloning") {
			t.Fatal("clone began before progress notification")
		}
		return "", nil, false
	}
	if result := synchronizer.Sync(context.Background(), f.repo, path); result.Status != "cloned" {
		t.Fatal(result)
	}
	if !slices.Equal(phases, []string{"Checking checkout", "Cloning"}) {
		t.Fatal(phases)
	}
	f.runner.hook = nil
	f.git("-C", path, "switch", "-c", "feature")
	f.write(f.seed, "README.md", "update\n")
	f.commit(f.seed, "update")
	f.git("-C", f.seed, "push")
	phases = nil
	if result := synchronizer.Sync(context.Background(), f.repo, path); result.Status != "updated" {
		t.Fatal(result)
	}
	if !slices.Equal(phases, []string{"Checking checkout", "Fetching default branch", "Switching to default branch", "Fast-forwarding default branch"}) {
		t.Fatal(phases)
	}
}

func TestSSHTimeoutIsConciseAndDoesNotChangeTransport(t *testing.T) {
	f := newFixture(t, false)
	path := filepath.Join(f.root, "checkout")
	f.runner.hook = func(command string, args []string) (string, error, bool) {
		if command != "gh" {
			return "", nil, false
		}
		if args[2] != "github.com/acme/project" {
			t.Fatalf("changed gh transport selector: %v", args)
		}
		return "", errors.New("Cloning into 'checkout'...\nssh: connect to host github.com port 22: Operation timed out\nfatal: Could not read from remote repository.\nfailed to run git: exit status 128"), true
	}
	result := f.sync(path, "failed")
	want := "clone failed: SSH connection to github.com:22 timed out; check your network and SSH settings"
	if result.Error != want {
		t.Fatalf("error %q, want %q", result.Error, want)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("unexpected clone destination")
	}
	if err := os.Mkdir(path, 0755); err != nil {
		t.Fatal(err)
	}
	result = f.sync(path, "failed")
	if !strings.Contains(result.Error, "; inspect "+path+" before retrying") {
		t.Fatal(result.Error)
	}
}

func TestOperationErrorPreservesOtherFailures(t *testing.T) {
	repo := Repository{Host: "github.com", Owner: "acme", Name: "project"}
	for _, message := range []string{"Permission denied (publickey)", "ssh: connect to host unrelated.example port 22: Connection timed out"} {
		failure := errors.New(message)
		err := operationError("fetch", failure, repo)
		if !errors.Is(err, failure) || !strings.Contains(err.Error(), message) {
			t.Fatal(err)
		}
	}
	err := operationError("fetch", errors.New("ssh: connect to host ssh.github.com port 443: Connection timed out"), repo)
	if !strings.Contains(err.Error(), "SSH connection to ssh.github.com:443 timed out") {
		t.Fatal(err)
	}
}

func TestGitHubSSH443OriginIdentity(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	repo := Repository{Host: "github.com", Owner: "acme", Name: "project"}
	if err := verifyOrigin("ssh://git@ssh.github.com:443/acme/project.git", repo); err != nil {
		t.Fatal(err)
	}
	for _, origin := range []string{"https://ssh.github.com/acme/project.git", "ssh://git@other.github.com:443/acme/project.git", "ssh://git@ssh.github.com:443/wrong/project.git"} {
		if verifyOrigin(origin, repo) == nil {
			t.Fatalf("accepted unrelated origin %s", origin)
		}
	}
}
