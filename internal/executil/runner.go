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
	"time"

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
	cmd := exec.Command(path, args...)
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
	if err := runCommand(ctx, cmd); err != nil {
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

func runCommand(ctx context.Context, cmd *exec.Cmd) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	// Own the output pipes so the drain timeout remains observable even when
	// the process fails: Cmd.Wait masks ErrWaitDelay behind an ExitError.
	var outputs []commandOutput
	defer func() {
		for _, output := range outputs {
			_ = output.reader.Close()
			_ = output.writer.Close()
		}
	}()
	for _, target := range []io.Writer{cmd.Stdout, cmd.Stderr} {
		reader, writer, err := os.Pipe()
		if err != nil {
			return err
		}
		outputs = append(outputs, commandOutput{reader, writer, target})
	}
	cmd.Stdout, cmd.Stderr = outputs[0].writer, outputs[1].writer
	tree, err := startCommand(cmd)
	if err != nil {
		return err
	}
	defer tree.close()
	copied := make(chan error, len(outputs))
	for _, output := range outputs {
		_ = output.writer.Close()
		go func() {
			_, err := io.Copy(output.target, output.reader)
			_ = output.reader.Close()
			copied <- err
		}()
	}

	// CommandContext stops watching once the direct process exits, even if
	// descendants still hold its output pipes. Watch until output draining ends.
	done := make(chan struct{})
	canceled := make(chan bool, 1)
	go func() {
		select {
		case <-ctx.Done():
			_ = tree.kill()
			canceled <- true
		case <-done:
			canceled <- false
		}
	}()
	err = cmd.Wait()
	if outputErr := drainCommandOutput(tree, outputs, copied); err == nil {
		err = outputErr
	}
	close(done)
	if <-canceled {
		return ctx.Err()
	}
	return err
}

type commandOutput struct {
	reader, writer *os.File
	target         io.Writer
}

func drainCommandOutput(tree *commandTree, outputs []commandOutput, copied <-chan error) error {
	// The process has exited. A descendant holding either output pipe must not
	// block completion indefinitely, regardless of the process's exit status.
	timer := time.NewTimer(time.Second)
	defer timer.Stop()
	var err error
	for remaining := len(outputs); remaining > 0; remaining-- {
		select {
		case outputErr := <-copied:
			if err == nil {
				err = outputErr
			}
		case <-timer.C:
			_ = tree.kill()
			for _, output := range outputs {
				_ = output.reader.Close()
			}
			for range remaining {
				<-copied
			}
			return exec.ErrWaitDelay
		}
	}
	return err
}

func gitOperation(args []string) bool {
	for index := 0; index < len(args); index++ {
		switch args[index] {
		case "-C", "-c":
			index++
		case "fetch", "switch", "merge", "checkout", "pull":
			return true
		default:
			return false
		}
	}
	return false
}

// stdout and stderr are copied concurrently. An arbitrary log writer may
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
