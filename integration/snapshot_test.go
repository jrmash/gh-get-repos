//go:build smoke

package integration

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"
)

type fileSnapshot struct {
	Mode               fs.FileMode
	ModTime            int64
	SHA256             [sha256.Size]byte
	Size               int64
	LFNormalizedSHA256 [sha256.Size]byte
	CRLFCount          int
	BareLFCount        int
}

type filesystemSnapshot map[string]fileSnapshot

type snapshotChange struct {
	Path                      string
	Before, After             fileSnapshot
	ExistsBefore, ExistsAfter bool
}

func snapshot(t *testing.T, root string) filesystemSnapshot {
	t.Helper()
	result, err := collectSnapshot(root)
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func collectSnapshot(root string) (filesystemSnapshot, error) {
	result := filesystemSnapshot{}
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		var contents []byte
		if !entry.IsDir() {
			contents, err = os.ReadFile(path)
			if err != nil {
				return err
			}
		}
		// Keep the original metadata read before the original content read.
		// Additional diagnostic fields use the bytes already read, so the
		// before/dry-run/after observation sequence remains unchanged.
		result[path] = recordSnapshot(info, contents)
		return nil
	})
	return result, err
}

func recordSnapshot(info fs.FileInfo, contents []byte) fileSnapshot {
	crlf := bytes.Count(contents, []byte("\r\n"))
	return fileSnapshot{
		Mode: info.Mode(), ModTime: info.ModTime().UnixNano(),
		SHA256: sha256.Sum256(contents), Size: info.Size(),
		LFNormalizedSHA256: sha256.Sum256(bytes.ReplaceAll(contents, []byte("\r\n"), []byte("\n"))),
		CRLFCount:          crlf, BareLFCount: bytes.Count(contents, []byte("\n")) - crlf,
	}
}

func snapshotChanges(before, after filesystemSnapshot) []snapshotChange {
	paths := make(map[string]struct{}, len(before)+len(after))
	for path := range before {
		paths[path] = struct{}{}
	}
	for path := range after {
		paths[path] = struct{}{}
	}
	var changed []string
	for path := range paths {
		old, existsBefore := before[path]
		current, existsAfter := after[path]
		// Preserve the original assertion: paths, raw content, modes, and
		// modification times must match. Diagnostic-only fields cannot fail it.
		if !existsBefore || !existsAfter || old.Mode != current.Mode || old.ModTime != current.ModTime || old.SHA256 != current.SHA256 {
			changed = append(changed, path)
		}
	}
	sort.Strings(changed)
	result := make([]snapshotChange, 0, len(changed))
	for _, path := range changed {
		old, existsBefore := before[path]
		current, existsAfter := after[path]
		result = append(result, snapshotChange{path, old, current, existsBefore, existsAfter})
	}
	return result
}

func (change snapshotChange) description() string {
	if !change.ExistsBefore {
		return "added"
	}
	if !change.ExistsAfter {
		return "removed"
	}
	var fields []string
	if change.Before.Mode != change.After.Mode {
		fields = append(fields, "permissions/type")
	}
	if change.Before.ModTime != change.After.ModTime {
		fields = append(fields, "modification time")
	}
	if change.Before.SHA256 != change.After.SHA256 {
		if change.Before.LFNormalizedSHA256 == change.After.LFNormalizedSHA256 {
			fields = append(fields, "contents (CRLF/LF conversion only; still a failure)")
		} else {
			fields = append(fields, "contents")
		}
	} else {
		fields = append(fields, "metadata only; contents unchanged")
	}
	return strings.Join(fields, "; ")
}

func (record fileSnapshot) description() string {
	return fmt.Sprintf("mode=%s (%#o) mtime_unix_ns=%d size=%d sha256=%x CRLF=%d bare_LF=%d LF_normalized_sha256=%x",
		record.Mode, uint32(record.Mode), record.ModTime, record.Size, record.SHA256, record.CRLFCount, record.BareLFCount, record.LFNormalizedSHA256)
}

func relativeSnapshotPath(root, path string) string {
	relative, err := filepath.Rel(root, path)
	if err != nil {
		return path
	}
	return filepath.ToSlash(relative)
}

func logSnapshotChanges(t *testing.T, root string, changes []snapshotChange) {
	t.Helper()
	for _, change := range changes {
		t.Logf("snapshot path %q: %s", relativeSnapshotPath(root, change.Path), change.description())
		if change.ExistsBefore {
			t.Logf("  before: %s", change.Before.description())
		}
		if change.ExistsAfter {
			t.Logf("  after:  %s", change.After.description())
		}
	}
}

// Run only after a mismatch. Diagnostics must not affect the snapshots being
// compared or replace the original dry-run failure with a diagnostic error.
func diagnoseSnapshotChange(t *testing.T, root, gitDirectory string, env []string, before, after filesystemSnapshot) {
	t.Helper()
	changes := snapshotChanges(before, after)
	t.Logf("dry-run snapshot mismatch: %d changed path(s), snapshot root %q", len(changes), root)
	logSnapshotChanges(t, root, changes)
	for _, change := range changes {
		logFreshSnapshotMetadata(t, root, change.Path)
	}
	if later, err := collectSnapshot(root); err != nil {
		t.Logf("post-failure snapshot unavailable: %v", err)
	} else {
		laterChanges := snapshotChanges(after, later)
		t.Logf("post-failure snapshot versus original after snapshot: %d changed path(s)", len(laterChanges))
		logSnapshotChanges(t, root, laterChanges)
	}
	// Use the same isolated environment as the extension, without printing
	// environment variables, unrestricted config, credentials, or file contents.
	logDiagnosticGitCommand(t, gitDirectory, env, "config", "--show-origin", "--get-regexp", `^core\.(autocrlf|eol|safecrlf|attributesfile|filemode|ignorecase|symlinks)$`)
	logDiagnosticGitCommand(t, gitDirectory, env, "ls-files", "--eol")
}

func logFreshSnapshotMetadata(t *testing.T, root, path string) {
	t.Helper()
	file, err := os.Open(path)
	if err != nil {
		t.Logf("fresh handle metadata %q unavailable: %v", relativeSnapshotPath(root, path), err)
		return
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		t.Logf("fresh handle metadata %q unavailable: %v", relativeSnapshotPath(root, path), err)
		return
	}
	t.Logf("fresh handle metadata %q: mode=%s (%#o) mtime_unix_ns=%d size=%d",
		relativeSnapshotPath(root, path), info.Mode(), uint32(info.Mode()), info.ModTime().UnixNano(), info.Size())
}

func logDiagnosticGitCommand(t *testing.T, cwd string, env []string, args ...string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir, cmd.Env = cwd, env
	output, err := cmd.CombinedOutput()
	t.Logf("diagnostic git %s (directory %q):\n%s", strings.Join(args, " "), cwd, strings.TrimSpace(string(output)))
	if err != nil {
		// git config exits 1 when none of the requested settings are set.
		t.Logf("diagnostic command result: %v (best effort; unset Git settings may return exit status 1)", err)
	}
}
