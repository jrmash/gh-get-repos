package executil

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"
)

// The current Go test executable doubles as a subprocess fixture. This avoids a
// shell, credentials, and platform-specific commands in process runner tests.
func TestMain(m *testing.M) {
	if os.Getenv("GHGR_RUNNER_TEST_HELPER") == "1" {
		switch os.Getenv("GHGR_RUNNER_TEST_MODE") {
		case "args":
			_ = json.NewEncoder(os.Stdout).Encode(os.Args[1:])
		case "env":
			values := map[string]string{}
			for _, key := range []string{"GH_PROMPT_DISABLED", "GIT_TERMINAL_PROMPT", "GIT_DIR", "GIT_WORK_TREE", "GIT_INDEX_FILE", "GIT_COMMON_DIR", "GHGR_RUNNER_KEPT"} {
				values[key] = os.Getenv(key)
			}
			_ = json.NewEncoder(os.Stdout).Encode(values)
		case "fail":
			fmt.Fprint(os.Stdout, "partial output\n")
			fmt.Fprint(os.Stderr, "  helpful error  \n")
			os.Exit(7)
		case "fail-quiet":
			os.Exit(8)
		case "block":
			time.Sleep(30 * time.Second)
		default:
			fmt.Fprint(os.Stdout, "captured stdout\n")
			fmt.Fprint(os.Stderr, "captured stderr\n")
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func helper(t *testing.T, mode string) string {
	t.Helper()
	path, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("GHGR_RUNNER_TEST_HELPER", "1")
	t.Setenv("GHGR_RUNNER_TEST_MODE", mode)
	t.Setenv("GH_PATH", path)
	return path
}

func installGitHelper(t *testing.T) {
	t.Helper()
	source := helper(t, "")
	directory := t.TempDir()
	filename := "git"
	if runtime.GOOS == "windows" {
		filename += ".exe"
	}
	in, err := os.Open(source)
	if err != nil {
		t.Fatal(err)
	}
	defer in.Close()
	out, err := os.OpenFile(filepath.Join(directory, filename), os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0o755)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		t.Fatal(err)
	}
	if err := out.Close(); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", directory)
}

func TestRunGHPathAndLiteralArguments(t *testing.T) {
	helper(t, "args")
	want := []string{"api", "graphql", "--raw-field", "owner=example", "query=$not_a_shell; $(ignored)", "two words", "", "--"}
	output, err := (ProcessRunner{}).Run(context.Background(), "gh", want...)
	if err != nil {
		t.Fatal(err)
	}
	var args []string
	if err := json.Unmarshal([]byte(output), &args); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(args, want) {
		t.Fatalf("arguments changed: got %q, want %q", args, want)
	}
}

func TestRunCapturesBothStreamsAndPreservesExitError(t *testing.T) {
	helper(t, "fail")
	var logs bytes.Buffer
	output, err := (ProcessRunner{Stderr: &logs}).Run(context.Background(), "gh", "api", "user")
	if output != "partial output\n" || err == nil || !strings.Contains(err.Error(), "gh: helpful error") || logs.Len() != 0 {
		t.Fatalf("output %q, error %v, logs %q", output, err, logs.String())
	}
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 7 {
		t.Fatalf("exit error/code was lost: %v", err)
	}
	t.Setenv("GHGR_RUNNER_TEST_MODE", "fail-quiet")
	_, err = (ProcessRunner{}).Run(context.Background(), "gh", "api", "user")
	if !errors.As(err, &exit) || exit.ExitCode() != 8 || !strings.Contains(err.Error(), "exit status 8") {
		t.Fatalf("quiet failure did not preserve status: %v", err)
	}
}

func TestRunVerboseStreamsGitAndCloneButCapturesAPI(t *testing.T) {
	installGitHelper(t)
	for _, tt := range []struct {
		name    string
		args    []string
		verbose bool
		log     bool
	}{
		{"git", []string{"fetch", "origin", "main"}, true, true},
		{"git", []string{"-C", "directory with spaces", "-c", "submodule.recurse=false", "merge", "--ff-only"}, true, true},
		{"git", []string{"-C", "directory", "remote", "get-url", "origin"}, true, false},
		{"git", []string{"-C"}, true, false},
		{"git", nil, true, false},
		{"gh", []string{"repo", "clone", "Example/repo", "repo"}, true, true},
		{"gh", []string{"api", "graphql", "--paginate"}, true, false},
		{"gh", nil, true, false},
		{"gh", []string{"repo"}, true, false},
		{"git", []string{"fetch", "origin"}, false, false},
	} {
		t.Run(tt.name+" "+strings.Join(tt.args, " ")+fmt.Sprint(tt.verbose), func(t *testing.T) {
			var logs bytes.Buffer
			output, err := (ProcessRunner{Verbose: tt.verbose, Stderr: &logs}).Run(context.Background(), tt.name, tt.args...)
			if err != nil || output != "captured stdout\n" {
				t.Fatalf("output %q, error %v", output, err)
			}
			if tt.log {
				for _, expected := range []string{"$ " + tt.name, "captured stdout\n", "captured stderr\n"} {
					if !strings.Contains(logs.String(), expected) {
						t.Errorf("missing %q from logs %q", expected, logs.String())
					}
				}
			} else if logs.Len() != 0 {
				t.Errorf("unexpected logs %q", logs.String())
			}
		})
	}
	if output, err := (ProcessRunner{Verbose: true}).Run(context.Background(), "git", "status"); err != nil || output != "captured stdout\n" {
		t.Fatalf("nil verbose writer: output %q, error %v", output, err)
	}
}

func TestRunCancelsSubprocess(t *testing.T) {
	helper(t, "block")
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	started := time.Now()
	_, err := (ProcessRunner{}).Run(ctx, "gh", "api", "graphql")
	if !errors.Is(err, context.DeadlineExceeded) || time.Since(started) > 5*time.Second {
		t.Fatalf("cancellation took %v and returned %v", time.Since(started), err)
	}
	ctx, cancel = context.WithCancel(context.Background())
	cancel()
	t.Setenv("GH_PATH", filepath.Join(t.TempDir(), "not-an-executable"))
	_, err = (ProcessRunner{}).Run(ctx, "gh")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("already cancelled command returned %v", err)
	}
}

func TestRunMissingExecutables(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	t.Setenv("GH_PATH", "")
	for _, name := range []string{"gh", "ghgr-impossible-executable"} {
		_, err := (ProcessRunner{}).Run(context.Background(), name)
		var missing *exec.Error
		if err == nil || !strings.Contains(err.Error(), "find "+name) || !errors.As(err, &missing) {
			t.Errorf("%s: %v", name, err)
		}
	}
	t.Setenv("GH_PATH", filepath.Join(t.TempDir(), "not-an-executable"))
	_, err := (ProcessRunner{}).Run(context.Background(), "gh")
	// Unix reports a missing explicit path through os.PathError; Windows can
	// reject it during executable lookup with exec.ErrNotFound instead.
	if !errors.Is(err, os.ErrNotExist) && !errors.Is(err, exec.ErrNotFound) {
		t.Fatalf("missing GH_PATH did not preserve process error: %v", err)
	}
}

func TestCommandEnvironmentDisablesPromptsAndRemovesGitContext(t *testing.T) {
	input := []string{
		"PATH=/safe/path", "GH_PROMPT_DISABLED=0", "gh_prompt_disabled=0", "GIT_TERMINAL_PROMPT=1",
		"GIT_DIR=/wrong/git", "git_work_tree=/wrong/tree", "GIT_INDEX_FILE=/wrong/index", "GIT_COMMON_DIR=/wrong/common",
		"GIT_CONFIG_COUNT=0", "KEEP=with=equals", "MALFORMED", "GIT_DIRECTORY=keep",
	}
	want := []string{"PATH=/safe/path", "GIT_CONFIG_COUNT=0", "KEEP=with=equals", "MALFORMED", "GIT_DIRECTORY=keep", "GH_PROMPT_DISABLED=1", "GIT_TERMINAL_PROMPT=0"}
	got := commandEnv(input)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %q, want %q", got, want)
	}
	if input[1] != "GH_PROMPT_DISABLED=0" {
		t.Fatal("input environment was changed")
	}
	helper(t, "env")
	for _, key := range []string{"GIT_DIR", "GIT_WORK_TREE", "GIT_INDEX_FILE", "GIT_COMMON_DIR"} {
		t.Setenv(key, "must-not-reach-child")
	}
	t.Setenv("GH_PROMPT_DISABLED", "0")
	t.Setenv("GIT_TERMINAL_PROMPT", "1")
	t.Setenv("GHGR_RUNNER_KEPT", "kept")
	output, err := (ProcessRunner{}).Run(context.Background(), "gh", "api", "user")
	if err != nil {
		t.Fatal(err)
	}
	var actual map[string]string
	if err := json.Unmarshal([]byte(output), &actual); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"GIT_DIR", "GIT_WORK_TREE", "GIT_INDEX_FILE", "GIT_COMMON_DIR"} {
		if actual[key] != "" {
			t.Errorf("%s leaked to subprocess: %q", key, actual[key])
		}
	}
	if actual["GH_PROMPT_DISABLED"] != "1" || actual["GIT_TERMINAL_PROMPT"] != "0" || actual["GHGR_RUNNER_KEPT"] != "kept" {
		t.Fatalf("unexpected subprocess environment: %v", actual)
	}
}
