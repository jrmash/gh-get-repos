package executil

import (
	"errors"
	"fmt"
	"os/exec"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

// A job retains ownership of descendants even after the original process exits.
type commandTree struct {
	job windows.Handle
}

func startCommand(cmd *exec.Cmd) (*commandTree, error) {
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return nil, fmt.Errorf("create process job: %w", err)
	}
	tree := &commandTree{job: job}
	fail := func(err error) (*commandTree, error) {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}
		tree.close()
		return nil, err
	}
	// Assign before the primary thread executes, so even a short-lived launcher
	// cannot create children outside the job or exit before it can be assigned.
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.CreationFlags |= windows.CREATE_SUSPENDED
	if err := cmd.Start(); err != nil {
		return fail(err)
	}
	process, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE, false, uint32(cmd.Process.Pid))
	if err != nil {
		return fail(fmt.Errorf("open process for job: %w", err))
	}
	defer windows.CloseHandle(process)
	if err := windows.AssignProcessToJobObject(job, process); err != nil {
		return fail(fmt.Errorf("assign process job: %w", err))
	}
	if err := resumeCommand(uint32(cmd.Process.Pid)); err != nil {
		return fail(fmt.Errorf("resume process: %w", err))
	}
	return tree, nil
}

// os/exec closes the primary thread handle returned by CreateProcess. A
// suspended process has not run yet, so its only thread can be recovered from
// the documented thread snapshot API before it is resumed.
func resumeCommand(pid uint32) error {
	snapshot, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPTHREAD, 0)
	if err != nil {
		return err
	}
	defer windows.CloseHandle(snapshot)
	entry := windows.ThreadEntry32{Size: uint32(unsafe.Sizeof(windows.ThreadEntry32{}))}
	for err := windows.Thread32First(snapshot, &entry); ; err = windows.Thread32Next(snapshot, &entry) {
		if err != nil {
			if errors.Is(err, windows.ERROR_NO_MORE_FILES) {
				return fmt.Errorf("primary thread for process %d not found", pid)
			}
			return err
		}
		if entry.OwnerProcessID != pid {
			continue
		}
		thread, err := windows.OpenThread(windows.THREAD_SUSPEND_RESUME, false, entry.ThreadID)
		if err != nil {
			return err
		}
		defer windows.CloseHandle(thread)
		_, err = windows.ResumeThread(thread)
		return err
	}
}

func (tree *commandTree) kill() error {
	return windows.TerminateJobObject(tree.job, 1)
}

func (tree *commandTree) close() {
	// Only kill explicitly on cancellation or a pipe timeout. Closing the job
	// otherwise preserves background helpers started by successful commands.
	_ = windows.CloseHandle(tree.job)
}
