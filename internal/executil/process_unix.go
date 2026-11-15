//go:build unix

package executil

import (
	"os"
	"os/exec"
	"syscall"
)

type commandTree struct {
	pid int
}

func startCommand(cmd *exec.Cmd) (*commandTree, error) {
	// gh launches git, which can launch SSH, credential helpers, and hooks.
	// Give them a separate group so cancellation cannot signal our own group.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return &commandTree{pid: cmd.Process.Pid}, nil
}

func (tree *commandTree) kill() error {
	if err := syscall.Kill(-tree.pid, syscall.SIGKILL); err != nil {
		if err == syscall.ESRCH {
			return os.ErrProcessDone
		}
		return err
	}
	return nil
}

func (tree *commandTree) close() {
	// Also clean up descendants left behind when their parent exited first.
	_ = tree.kill()
}
