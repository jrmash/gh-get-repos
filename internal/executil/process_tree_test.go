package executil

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// Every generation inherits stdout/stderr and keeps a control connection open.
// The connections let tests observe actual process death without mistaking an
// unreaped Unix zombie for a running process, and also clean up on failure.
func runnerProcessTreeHelper(role string) {
	if role != "tree-grandchild" {
		path, err := os.Executable()
		if err != nil {
			panic(err)
		}
		next := "tree-child"
		if role == next {
			next = "tree-grandchild"
		}
		child := exec.Command(path)
		child.Env = append(os.Environ(), "GHGR_RUNNER_TEST_MODE="+next)
		child.Stdout, child.Stderr = os.Stdout, os.Stderr
		if err := child.Start(); err != nil {
			panic(err)
		}
	}
	connection, err := net.DialTimeout("tcp", os.Getenv("GHGR_RUNNER_TEST_ADDRESS"), 5*time.Second)
	if err != nil {
		panic(err)
	}
	// Leave the connection open until os.Exit so closure proves process death.
	// A deadline bounds the lifetime of helpers never accepted by a failed test.
	if err := connection.SetDeadline(time.Now().Add(15 * time.Second)); err != nil {
		panic(err)
	}
	if role == "tree-parent" {
		fmt.Fprint(os.Stdout, "tree output\n")
		fmt.Fprint(os.Stderr, "tree diagnostics\n")
	}
	fmt.Fprintln(connection, role)
	// Closing the test's side releases any survivors if cancellation is broken.
	_, _ = connection.Read(make([]byte, 1))
}

func TestRunCancelsDescendants(t *testing.T) {
	for _, tt := range []struct {
		label         string
		name          string
		args          []string
		parentExits   bool
		verbose       bool
		withoutCancel bool
		exitCode      int
	}{
		{"clone-captured", "gh", []string{"repo", "clone", "example/repo"}, false, false, false, 0},
		{"clone-verbose-exited-parent", "gh", []string{"repo", "clone", "example/repo"}, true, true, false, 0},
		{"fetch-verbose", "git", []string{"fetch", "origin"}, false, true, false, 0},
		{"fetch-captured-exited-parent", "git", []string{"fetch", "origin"}, true, false, false, 0},
		{"parent-exit-without-cancellation", "gh", []string{"repo", "clone", "example/repo"}, true, false, true, 0},
		{"failed-parent-without-cancellation", "gh", []string{"repo", "clone", "example/repo"}, true, false, true, 7},
	} {
		t.Run(tt.label, func(t *testing.T) {
			installGitHelper(t)
			t.Setenv("GHGR_RUNNER_TEST_MODE", "tree-parent")
			t.Setenv("GHGR_RUNNER_TEST_PARENT_EXIT", fmt.Sprint(tt.exitCode))
			t.Setenv("GORACE", "atexit_sleep_ms=0")
			listener, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			defer listener.Close()
			if err := listener.(*net.TCPListener).SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
				t.Fatal(err)
			}
			t.Setenv("GHGR_RUNNER_TEST_ADDRESS", listener.Addr().String())
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			var logs bytes.Buffer
			type result struct {
				output string
				err    error
			}
			done := make(chan result, 1)
			go func() {
				output, err := (ProcessRunner{Verbose: tt.verbose, Stderr: &logs}).Run(ctx, tt.name, tt.args...)
				done <- result{output, err}
			}()
			connections := map[string]net.Conn{}
			returned := false
			defer func() {
				for _, connection := range connections {
					connection.Close()
				}
				cancel()
				if !returned {
					select {
					case <-done:
					case <-time.After(5 * time.Second):
						t.Error("runner did not exit after fixture cleanup")
					}
				}
			}()
			for range 3 {
				connection, err := listener.Accept()
				if err != nil {
					t.Fatal(err)
				}
				if err := connection.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
					connection.Close()
					t.Fatal(err)
				}
				role, err := bufio.NewReader(connection).ReadString('\n')
				if err != nil {
					connection.Close()
					t.Fatal(err)
				}
				connections[strings.TrimSpace(role)] = connection
			}
			if tt.parentExits {
				parent := connections["tree-parent"]
				if _, err := parent.Write([]byte("x")); err != nil {
					t.Fatal(err)
				}
				if _, err := parent.Read(make([]byte, 1)); !processConnectionClosed(err) {
					t.Fatalf("parent did not exit: %v", err)
				}
				// Let os/exec observe the parent's exit before canceling. Its
				// normal context watcher can already have stopped at this point.
				time.Sleep(50 * time.Millisecond)
			}
			wantErr := error(context.Canceled)
			if tt.withoutCancel {
				wantErr = exec.ErrWaitDelay
			} else {
				cancel()
			}
			select {
			case result := <-done:
				returned = true
				if result.output != "tree output\n" {
					t.Fatalf("output %q", result.output)
				}
				if tt.exitCode != 0 {
					var exit *exec.ExitError
					if !errors.As(result.err, &exit) || exit.ExitCode() != tt.exitCode {
						t.Fatalf("exit status was lost: %v", result.err)
					}
				} else if !errors.Is(result.err, wantErr) {
					t.Fatalf("output %q, error %v; want %v", result.output, result.err, wantErr)
				}
				if tt.verbose && !strings.Contains(logs.String(), "tree diagnostics\n") {
					t.Fatalf("missing verbose output: %q", logs.String())
				}
			case <-time.After(3 * time.Second):
				t.Fatal("cancellation left Run waiting on inherited output pipes")
			}
			for _, role := range []string{"tree-child", "tree-grandchild"} {
				connection := connections[role]
				if err := connection.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
					t.Fatal(err)
				}
				if _, err := connection.Read(make([]byte, 1)); !processConnectionClosed(err) {
					t.Errorf("%s survived cancellation: %v", role, err)
				}
			}
		})
	}
}
