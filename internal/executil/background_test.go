package executil

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

// Background tasks redirect their output, so they do not hold the command's
// pipes open. A successful command must leave them able to finish their work.
func runnerBackgroundHelper(role string) {
	directory := os.Getenv("GHGR_RUNNER_TEST_BACKGROUND")
	if role == "background-parent" {
		path, err := os.Executable()
		if err != nil {
			panic(err)
		}
		null, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
		if err != nil {
			panic(err)
		}
		defer null.Close()
		child := exec.Command(path)
		child.Env = append(os.Environ(), "GHGR_RUNNER_TEST_MODE=background-child")
		child.Stdout, child.Stderr = null, null
		if err := child.Start(); err != nil {
			panic(err)
		}
		if err := waitForBackgroundFile(filepath.Join(directory, "ready"), 5*time.Second); err != nil {
			panic(err)
		}
		return
	}
	if err := writeBackgroundMarker(filepath.Join(directory, "ready"), []byte(strconv.Itoa(os.Getpid()))); err != nil {
		panic(err)
	}
	if err := waitForBackgroundFile(filepath.Join(directory, "release"), 10*time.Second); err != nil {
		panic(err)
	}
	if err := writeBackgroundMarker(filepath.Join(directory, "completed"), nil); err != nil {
		panic(err)
	}
}

func writeBackgroundMarker(path string, data []byte) error {
	// Publish only after closing the file, so Windows cleanup never races a
	// helper still writing its completion marker.
	if err := os.WriteFile(path+".tmp", data, 0o600); err != nil {
		return err
	}
	return os.Rename(path+".tmp", path)
}

func waitForBackgroundFile(path string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return nil
		} else if !os.IsNotExist(err) {
			return err
		}
		time.Sleep(10 * time.Millisecond)
	}
	return fmt.Errorf("timed out waiting for %s", filepath.Base(path))
}

func assertBackgroundCompletes(t *testing.T, run func(context.Context) error) {
	t.Helper()
	directory := t.TempDir()
	t.Setenv("GHGR_RUNNER_TEST_BACKGROUND", directory)
	t.Setenv("GORACE", "atexit_sleep_ms=0")
	defer func() {
		// Release survivors on failure, then clean up a helper that did not finish.
		_ = os.WriteFile(filepath.Join(directory, "release"), nil, 0o600)
		if _, err := os.Stat(filepath.Join(directory, "completed")); err != nil {
			pid, err := os.ReadFile(filepath.Join(directory, "ready"))
			if err == nil {
				if number, err := strconv.Atoi(string(pid)); err == nil {
					if process, err := os.FindProcess(number); err == nil {
						_ = process.Kill()
						_ = process.Release()
					}
				}
			}
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := run(ctx); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "release"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := waitForBackgroundFile(filepath.Join(directory, "completed"), 3*time.Second); err != nil {
		t.Fatalf("background task could not finish after successful command: %v", err)
	}
}

func TestRunPreservesBackgroundDescendants(t *testing.T) {
	for _, code := range []int{0, 7} {
		t.Run(fmt.Sprintf("exit-%d", code), func(t *testing.T) {
			helper(t, "background-parent")
			t.Setenv("GHGR_RUNNER_TEST_PARENT_EXIT", strconv.Itoa(code))
			assertBackgroundCompletes(t, func(ctx context.Context) error {
				_, err := (ProcessRunner{}).Run(ctx, "gh", "repo", "clone", "example/repo")
				if code != 0 {
					var exit *exec.ExitError
					if !errors.As(err, &exit) || exit.ExitCode() != code {
						return fmt.Errorf("exit status was lost: %v", err)
					}
					return nil
				}
				return err
			})
		})
	}
}
