// Package syncgit safely clones and fast-forwards repository checkouts.
package syncgit

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/cli/go-gh/v2/pkg/repository"
	"github.com/cli/go-gh/v2/pkg/ssh"
	"github.com/jrmash/gh-get-repos/internal/executil"
)

// Repository identifies the remote repository and its current default branch.
type Repository struct {
	Host          string
	Owner         string
	Name          string
	DefaultBranch string
	Empty         bool
}

// Result describes the outcome of one repository synchronization.
type Result struct {
	Repository string `json:"repository" yaml:"repository"`
	Path       string `json:"path" yaml:"path"`
	Status     string `json:"status" yaml:"status"`
	Error      string `json:"error,omitempty" yaml:"error,omitempty"`
}

// Synchronizer delegates Git and GitHub CLI commands to Runner.
type Synchronizer struct {
	Runner   executil.Runner
	Progress func(string)
}

// Sync clones an absent or empty path, or safely updates an existing checkout.
// It never removes a destination, discards changes, or rewrites local commits.
func (s Synchronizer) Sync(ctx context.Context, repo Repository, path string) Result {
	result := Result{Repository: repo.Owner + "/" + repo.Name, Path: path}
	finish := func(status, reason string) Result {
		result.Status, result.Error = status, reason
		return result
	}
	fail := func(err error) Result { return finish("failed", err.Error()) }
	if err := ctx.Err(); err != nil {
		return fail(err)
	}
	if !validComponent(repo.Owner) || !validRepositoryComponent(repo.Name) || repo.Host == "" || strings.HasPrefix(repo.Host, "-") || strings.ContainsAny(repo.Host, "/\\\r\n\t ") {
		return fail(errors.New("invalid repository identity"))
	}
	s.progress("Checking checkout")
	absolute, err := filepath.Abs(path)
	if err != nil {
		return fail(fmt.Errorf("resolve destination: %w", err))
	}
	result.Path = absolute
	info, err := os.Lstat(absolute)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fail(fmt.Errorf("inspect destination: %w", err))
	}
	clone := errors.Is(err, os.ErrNotExist)
	if err == nil {
		if !info.IsDir() {
			return fail(errors.New("destination must be a directory, not a file or symbolic link"))
		}
		dir, err := os.Open(absolute)
		if err != nil {
			return fail(fmt.Errorf("inspect destination: %w", err))
		}
		_, readErr := dir.Readdirnames(1)
		closeErr := dir.Close()
		if readErr != nil && !errors.Is(readErr, io.EOF) {
			return fail(fmt.Errorf("inspect destination: %w", readErr))
		}
		if closeErr != nil {
			return fail(fmt.Errorf("inspect destination: %w", closeErr))
		}
		clone = errors.Is(readErr, io.EOF)
	}
	if clone {
		if err := os.MkdirAll(filepath.Dir(absolute), 0o755); err != nil {
			return fail(fmt.Errorf("create parent directory: %w", err))
		}
		s.progress("Cloning")
		_, err := s.Runner.Run(ctx, "gh", "repo", "clone", repo.Host+"/"+repo.Owner+"/"+repo.Name, absolute, "--no-upstream")
		if err != nil {
			message := operationError("clone failed", err, repo).Error()
			if _, remainingErr := os.Lstat(absolute); remainingErr == nil {
				message += fmt.Sprintf("; inspect %s before retrying", absolute)
			}
			return fail(errors.New(message))
		}
		return finish("cloned", "")
	}

	git := func(args ...string) (string, error) {
		out, err := s.Runner.Run(ctx, "git", append([]string{"-C", absolute}, args...)...)
		return strings.TrimSpace(out), err
	}
	root, err := git("rev-parse", "--show-toplevel")
	if err != nil {
		return fail(fmt.Errorf("destination is not a Git checkout: %w", err))
	}
	if !samePath(root, absolute) {
		return fail(errors.New("destination is inside another Git checkout; expected its exact root"))
	}
	origin, err := git("remote", "get-url", "origin")
	if err != nil {
		return fail(fmt.Errorf("read origin remote: %w", err))
	}
	if err := verifyOrigin(origin, repo); err != nil {
		return fail(err)
	}
	gitDir, err := git("rev-parse", "--absolute-git-dir")
	if err != nil {
		return fail(fmt.Errorf("locate Git directory: %w", err))
	}
	for _, operation := range []string{"MERGE_HEAD", "CHERRY_PICK_HEAD", "REVERT_HEAD", "BISECT_LOG", "rebase-merge", "rebase-apply", "sequencer"} {
		if _, err := os.Lstat(filepath.Join(gitDir, operation)); err == nil {
			return finish("skipped", "Git operation in progress ("+operation+"); finish or abort it before retrying")
		} else if !errors.Is(err, os.ErrNotExist) {
			return fail(fmt.Errorf("inspect Git operation: %w", err))
		}
	}
	status, err := git("status", "--porcelain=v1", "--untracked-files=all", "--ignore-submodules=none")
	if err != nil {
		return fail(fmt.Errorf("inspect working tree: %w", err))
	}
	if status != "" {
		return finish("skipped", "working tree has local changes or untracked files; commit, stash, or remove them before retrying")
	}
	if repo.Empty {
		refs, err := git("for-each-ref", "--format=%(refname)")
		if err != nil {
			return fail(fmt.Errorf("inspect local references: %w", err))
		}
		if refs != "" {
			return finish("skipped", "remote repository is empty but local references contain commits")
		}
		// A detached commit has no branch, but must not be treated as an empty checkout.
		if _, err := git("rev-parse", "--verify", "HEAD"); err == nil {
			return finish("skipped", "remote repository is empty but the checkout contains a local commit")
		} else if ctx.Err() != nil {
			return fail(ctx.Err())
		}
		return finish("unchanged", "")
	}
	branch, err := git("symbolic-ref", "--quiet", "--short", "HEAD")
	if err != nil {
		if ctx.Err() != nil {
			return fail(ctx.Err())
		}
		if exitCode(err) != 1 {
			return fail(fmt.Errorf("inspect current branch: %w", err))
		}
		return finish("skipped", "HEAD is detached; check out a branch before retrying")
	}
	if repo.DefaultBranch == "" {
		return fail(errors.New("repository has no default branch"))
	}
	if _, err := git("check-ref-format", "--branch", repo.DefaultBranch); err != nil {
		return fail(fmt.Errorf("invalid default branch: %w", err))
	}
	worktrees, err := git("worktree", "list", "--porcelain", "-z")
	if err != nil {
		return fail(fmt.Errorf("inspect worktrees: %w", err))
	}
	if branchInOtherWorktree(worktrees, repo.DefaultBranch, absolute) {
		return finish("skipped", "default branch is checked out in another worktree")
	}
	local := "refs/heads/" + repo.DefaultBranch
	remote := "refs/remotes/origin/" + repo.DefaultBranch
	s.progress("Fetching default branch")
	if _, err := git("fetch", "--no-tags", "--no-recurse-submodules", "origin", "+refs/heads/"+repo.DefaultBranch+":"+remote); err != nil {
		return fail(operationError("fetch default branch", err, repo))
	}
	_, err = git("show-ref", "--verify", "--quiet", local)
	if err != nil {
		if exitCode(err) != 1 {
			return fail(fmt.Errorf("inspect default branch: %w", err))
		}
		s.progress("Switching to default branch")
		if _, err := git("switch", "--no-overwrite-ignore", "--no-recurse-submodules", "--track", "-c", repo.DefaultBranch, remote); err != nil {
			return fail(fmt.Errorf("create default branch: %w", err))
		}
		return finish("updated", "")
	}
	counts, err := git("rev-list", "--left-right", "--count", local+"..."+remote)
	if err != nil {
		return fail(fmt.Errorf("compare default branch: %w", err))
	}
	ahead, behind, err := parseCounts(counts)
	if err != nil {
		return fail(err)
	}
	if ahead > 0 {
		if behind > 0 {
			return finish("skipped", "local default branch has diverged from origin; reconcile its commits before retrying")
		}
		return finish("skipped", "local default branch has unpushed commits; push or reconcile them before retrying")
	}
	if branch != repo.DefaultBranch {
		s.progress("Switching to default branch")
		if _, err := git("switch", "--no-overwrite-ignore", "--no-recurse-submodules", repo.DefaultBranch); err != nil {
			return fail(fmt.Errorf("switch to default branch: %w", err))
		}
	}
	if behind > 0 {
		s.progress("Fast-forwarding default branch")
		if _, err := git("-c", "submodule.recurse=false", "merge", "--ff-only", "--no-autostash", "--no-overwrite-ignore", "--no-edit", remote); err != nil {
			return fail(fmt.Errorf("fast-forward default branch: %w", err))
		}
		return finish("updated", "")
	}
	if branch != repo.DefaultBranch {
		return finish("updated", "")
	}
	return finish("unchanged", "")
}

func (s Synchronizer) progress(message string) {
	if s.Progress != nil {
		s.Progress(message)
	}
}

var sshTimeout = regexp.MustCompile(`(?im)ssh:\s+connect to host (\S+) port ([0-9]+): (?:operation|connection) timed out`)

func operationError(operation string, err error, repo Repository) error {
	match := sshTimeout.FindStringSubmatch(err.Error())
	if len(match) == 3 && (strings.EqualFold(match[1], repo.Host) || strings.EqualFold(repo.Host, "github.com") && strings.EqualFold(match[1], "ssh.github.com")) {
		return fmt.Errorf("%s: SSH connection to %s:%s timed out; check your network and SSH settings", operation, match[1], match[2])
	}
	return fmt.Errorf("%s: %w", operation, err)
}

func validComponent(s string) bool {
	return validRepositoryComponent(s) && !strings.HasPrefix(s, "-")
}

func validRepositoryComponent(s string) bool {
	return s != "" && s != "." && s != ".." && !strings.ContainsAny(s, "/\\\x00\r\n\t ")
}

func samePath(a, b string) bool {
	left, err := filepath.EvalSymlinks(a)
	if err != nil {
		return false
	}
	right, err := filepath.EvalSymlinks(b)
	return err == nil && filepath.Clean(left) == filepath.Clean(right)
}

func verifyOrigin(origin string, repo Repository) error {
	normalized := origin
	if !strings.Contains(normalized, "://") {
		if colon := strings.IndexByte(normalized, ':'); colon > 0 {
			normalized = "ssh://" + normalized[:colon] + "/" + normalized[colon+1:]
		}
	}
	parsed, parseErr := url.Parse(normalized)
	if parseErr != nil || parsed.Hostname() == "" || (parsed.Scheme != "ssh" && parsed.Scheme != "https" && parsed.Scheme != "http" && parsed.Scheme != "git") {
		return fmt.Errorf("origin must be a GitHub network URL for %s/%s/%s; verify its remote URL", repo.Host, repo.Owner, repo.Name)
	}
	if parsed.Scheme == "ssh" {
		parsed = ssh.NewTranslator().Translate(parsed)
		// GitHub's supported SSH endpoint on port 443 identifies github.com.
		// Normalize only this exact SSH hostname, including translated aliases.
		if strings.EqualFold(repo.Host, "github.com") && strings.EqualFold(parsed.Hostname(), "ssh.github.com") {
			parsed.Host = "github.com"
		}
		normalized = parsed.String()
	}
	actual, err := repository.Parse(normalized)
	if err != nil || !strings.EqualFold(actual.Host, repo.Host) || !strings.EqualFold(actual.Owner, repo.Owner) || !strings.EqualFold(actual.Name, repo.Name) {
		return fmt.Errorf("origin does not identify %s/%s/%s; verify the destination and its origin remote", repo.Host, repo.Owner, repo.Name)
	}
	return nil
}

func branchInOtherWorktree(data, branch, path string) bool {
	worktree := ""
	for _, line := range strings.Split(data, "\x00") {
		if strings.HasPrefix(line, "worktree ") {
			worktree = strings.TrimPrefix(line, "worktree ")
		} else if line == "branch refs/heads/"+branch && !samePath(worktree, path) {
			return true
		}
	}
	return false
}

func exitCode(err error) int {
	var coded interface{ ExitCode() int }
	if errors.As(err, &coded) {
		return coded.ExitCode()
	}
	return -1
}

func parseCounts(s string) (int, int, error) {
	fields := strings.Fields(s)
	if len(fields) == 2 {
		ahead, errA := strconv.Atoi(fields[0])
		behind, errB := strconv.Atoi(fields[1])
		if errA == nil && errB == nil && ahead >= 0 && behind >= 0 {
			return ahead, behind, nil
		}
	}
	return 0, 0, fmt.Errorf("unexpected Git history counts: %q", s)
}
