//go:build !unix && !windows

package executil

import "os/exec"

type commandTree struct {
	cmd *exec.Cmd
}

func startCommand(cmd *exec.Cmd) (*commandTree, error) {
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return &commandTree{cmd: cmd}, nil
}

func (tree *commandTree) kill() error { return tree.cmd.Process.Kill() }

func (tree *commandTree) close() {}
