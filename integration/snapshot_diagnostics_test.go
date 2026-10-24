//go:build smoke

package integration

import (
	"crypto/sha256"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSnapshotUsesFreshHandleMetadata(t *testing.T) {
	root := t.TempDir()
	for _, directory := range []bool{false, true} {
		name := "file"
		if directory {
			name = "directory"
		}
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(root, name)
			if directory {
				if err := os.Mkdir(path, 0o700); err != nil {
					t.Fatal(err)
				}
			} else if err := os.WriteFile(path, []byte("unchanged\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			cached, err := os.Stat(path)
			if err != nil {
				t.Fatal(err)
			}
			mtime := cached.ModTime().Add(-time.Hour).Truncate(time.Second)
			if err := os.Chtimes(path, mtime, mtime); err != nil {
				t.Fatal(err)
			}
			// Simulate Windows returning the earlier cached directory entry.
			record, err := readSnapshotEntry(path, fs.FileInfoToDirEntry(cached))
			if err != nil {
				t.Fatal(err)
			}
			if record.ModTime != mtime.UnixNano() {
				t.Fatalf("snapshot mtime = %d; want fresh time %d, not cached time %d", record.ModTime, mtime.UnixNano(), cached.ModTime().UnixNano())
			}
		})
	}
}

func TestSnapshotLineEndingDiagnostics(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "mixed.txt")
	contents := []byte("one\r\ntwo\nthree\r")
	if err := os.WriteFile(path, contents, 0o600); err != nil {
		t.Fatal(err)
	}
	record := snapshot(t, root)[path]
	if record.Size != int64(len(contents)) || record.CRLFCount != 1 || record.BareLFCount != 1 {
		t.Fatalf("incorrect size or line-ending counts: %+v", record)
	}
	if record.SHA256 != sha256.Sum256(contents) || record.LFNormalizedSHA256 != sha256.Sum256([]byte("one\ntwo\nthree\r")) {
		t.Fatal("snapshot hashes must preserve raw bytes and normalize only CRLF")
	}
}

func TestSnapshotComparisonAndDescriptions(t *testing.T) {
	file, err := os.CreateTemp(t.TempDir(), "snapshot")
	if err != nil {
		t.Fatal(err)
	}
	info, err := file.Stat()
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	if err != nil {
		t.Fatal(err)
	}
	before := recordSnapshot(info, []byte("one\ntwo\n"))
	tests := []struct {
		name        string
		after       fileSnapshot
		mutate      func(*fileSnapshot)
		description string
	}{
		{name: "unchanged", after: before},
		{name: "diagnostic fields do not fail assertion", after: before, mutate: func(record *fileSnapshot) {
			record.Size++
			record.CRLFCount++
			record.BareLFCount++
			record.LFNormalizedSHA256 = sha256.Sum256([]byte("diagnostic only"))
		}},
		{name: "CRLF conversion still fails assertion", after: recordSnapshot(info, []byte("one\r\ntwo\r\n")), description: "contents (CRLF/LF conversion only; still a failure)"},
		{name: "content change", after: recordSnapshot(info, []byte("one\nchanged\n")), description: "contents"},
		{name: "permissions only", after: before, mutate: func(record *fileSnapshot) { record.Mode ^= 0o200 }, description: "permissions/type; metadata only; contents unchanged"},
		{name: "timestamp only", after: before, mutate: func(record *fileSnapshot) { record.ModTime++ }, description: "modification time; metadata only; contents unchanged"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			after := test.after
			if test.mutate != nil {
				test.mutate(&after)
			}
			changes := snapshotChanges(filesystemSnapshot{"file": before}, filesystemSnapshot{"file": after})
			if test.description == "" {
				if len(changes) != 0 {
					t.Fatalf("diagnostic fields changed the assertion: %+v", changes)
				}
				return
			}
			if len(changes) != 1 || changes[0].description() != test.description {
				t.Fatalf("changes = %+v; want one change described as %q", changes, test.description)
			}
		})
	}
}

func TestSnapshotAddedRemovedPathsAreSorted(t *testing.T) {
	changes := snapshotChanges(filesystemSnapshot{"z-removed": {}}, filesystemSnapshot{"a-added": {}})
	if len(changes) != 2 || changes[0].Path != "a-added" || changes[0].description() != "added" || changes[1].Path != "z-removed" || changes[1].description() != "removed" {
		t.Fatalf("added and removed paths must fail comparison and be sorted: %+v", changes)
	}
	root := t.TempDir()
	if got := relativeSnapshotPath(root, filepath.Join(root, "child", "file")); got != "child/file" {
		t.Fatalf("diagnostic path = %q; want child/file", got)
	}
}

func TestSnapshotDiagnosticsRemainBestEffort(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "missing")
	// Missing files and an unusable command environment must produce diagnostic
	// logs without failing the test or concealing the original caller's failure.
	diagnoseSnapshotChange(t, missing, missing, []string{}, filesystemSnapshot{missing: {}}, filesystemSnapshot{})
	if description := (fileSnapshot{}).description(); !strings.Contains(description, "LF_normalized_sha256=") {
		t.Fatalf("snapshot description lacks normalized hash: %s", description)
	}
}
