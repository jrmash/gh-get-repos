// Package executil runs gh and git without a shell.
package executil

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"

	gh "github.com/cli/go-gh/v2"
	"github.com/cli/safeexec"
)

// Runner is shared by discovery and checkout synchronization.
type Runner interface {
	Run(context.Context, string, ...string) (string, error)
}

// ProcessRunner captures command output and optionally streams Git operation logs.
type ProcessRunner struct {
	Verbose bool
	Stderr  io.Writer
}

type commandError struct {
	name    string
	message string
	cause   error
}

func (e *commandError) Error() string { return e.name + ": " + e.message }
func (e *commandError) Unwrap() error { return e.cause }

func (r ProcessRunner) Run(ctx context.Context, name string, args ...string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	var path string
	var err error
	if name == "gh" {
		path, err = gh.Path()
	} else {
		path, err = safeexec.LookPath(name)
	}
	if err != nil {
		return "", fmt.Errorf("find %s: %w", name, err)
	}
	cmd := exec.CommandContext(ctx, path, args...)
	cmd.Env = commandEnv(os.Environ())
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	// API metadata is deliberately captured, even in verbose mode. Git and clone
	// logs go to stderr so machine-readable stdout remains a single document.
	logOutput := r.Verbose && r.Stderr != nil && (name == "git" && gitOperation(args) || name == "gh" && len(args) > 1 && args[0] == "repo" && args[1] == "clone")
	if logOutput {
		logs := &lockedWriter{target: r.Stderr}
		fmt.Fprintf(logs, "$ %s %s\n", name, strings.Join(args, " "))
		cmd.Stdout = io.MultiWriter(&stdout, logs)
		cmd.Stderr = io.MultiWriter(&stderr, logs)
	}
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return stdout.String(), ctx.Err()
		}
		message := strings.TrimSpace(stderr.String())
		if message == "" {
			message = err.Error()
		}
		return stdout.String(), &commandError{name: name, message: message, cause: err}
	}
	return stdout.String(), nil
}

func gitOperation(args []string) bool {
	for index := 0; index < len(args); index++ {
		switch args[index] {
		case "-C", "-c":
			index++
		case "fetch", "switch", "merge":
			return true
		default:
			return false
		}
	}
	return false
}

// os/exec copies stdout and stderr concurrently. An arbitrary log writer may
// not support concurrent writes, even though the batch itself is sequential.
type lockedWriter struct {
	mu     sync.Mutex
	target io.Writer
}

func (w *lockedWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.target.Write(p)
}

func commandEnv(env []string) []string {
	result := make([]string, 0, len(env)+2)
	for _, entry := range env {
		key, _, _ := strings.Cut(entry, "=")
		switch strings.ToUpper(key) {
		case "GH_PROMPT_DISABLED", "GIT_TERMINAL_PROMPT", "GIT_DIR", "GIT_WORK_TREE", "GIT_INDEX_FILE", "GIT_COMMON_DIR":
			continue
		}
		result = append(result, entry)
	}
	return append(result, "GH_PROMPT_DISABLED=1", "GIT_TERMINAL_PROMPT=0")
}
