//go:build unix

package executil

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestRunPreservesPostCheckoutBackgroundTask(t *testing.T) {
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	directory := t.TempDir()
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", directory}, args...)...)
		cmd.Env = commandEnv(os.Environ())
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, output)
		}
	}
	git("init", "--initial-branch=main")
	git("-c", "user.name=Runner Test", "-c", "user.email=runner@example.invalid", "-c", "commit.gpgsign=false", "commit", "--allow-empty", "-m", "initial")
	path := helper(t, "background-parent")
	t.Setenv("GHGR_RUNNER_TEST_EXECUTABLE", path)
	// The foreground hook launches a background task with redirected output.
	hook := "#!/bin/sh\nexec \"$GHGR_RUNNER_TEST_EXECUTABLE\"\n"
	if err := os.WriteFile(filepath.Join(directory, ".git", "hooks", "post-checkout"), []byte(hook), 0o755); err != nil {
		t.Fatal(err)
	}
	assertBackgroundCompletes(t, func(ctx context.Context) error {
		_, err := (ProcessRunner{}).Run(ctx, "git", "-C", directory, "checkout", "-b", "feature")
		return err
	})
}
