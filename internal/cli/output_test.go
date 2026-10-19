package cli

import (
	"bytes"
	"encoding/json"
	"regexp"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/jrmash/gh-get-repos/internal/syncgit"
	"github.com/rivo/uniseg"
	"go.yaml.in/yaml/v3"
)

var humanColorEscape = regexp.MustCompile(`\x1b\[[0-9;]*m`)

func humanStatusColumn(t *testing.T, line, status string) int {
	t.Helper()
	plain := humanColorEscape.ReplaceAllString(line, "")
	index := strings.LastIndex(plain, status)
	if index < 0 {
		t.Fatalf("status %q missing from %q", status, line)
	}
	return uniseg.StringWidth(plain[:index])
}

func TestHumanRepositoryColumnsFitUserRepositoryNames(t *testing.T) {
	names := []string{
		"jrmash/bfl",
		"jrmash/perl-Dist-Zilla-Plugin-PkgHeader",
		"jrmash/perl-Dist-Zilla-PluginBundle-Author-JRMASH",
		"jrmash/terramate-component-library",
	}
	for _, terminal := range []bool{false, true} {
		for _, color := range []bool{false, true} {
			terminalWidth := 0
			if terminal {
				terminalWidth = 120
			}
			width := humanRepositoryWidth(names, terminalWidth)
			var out bytes.Buffer
			for index, name := range names {
				status := []string{"cloned", "updated", "unchanged", "skipped"}[index]
				if err := renderHumanResult(&out, syncgit.Result{Repository: name, Status: status}, color, terminal, width); err != nil {
					t.Fatal(err)
				}
			}
			lines := strings.Split(strings.TrimSuffix(out.String(), "\n"), "\n")
			for index, line := range lines {
				status := []string{"cloned", "updated", "unchanged", "skipped"}[index]
				if column := humanStatusColumn(t, line, status); column != 52 {
					t.Errorf("terminal=%t color=%t: status column %d, want 52 in %q", terminal, color, column, line)
				}
				if !strings.Contains(line, names[index]) {
					t.Errorf("full repository name missing: %q", line)
				}
			}
		}
	}
}

func TestHumanOutputAlignedTableSnapshot(t *testing.T) {
	doc := Document{Results: []syncgit.Result{
		{Repository: "jrmash/bfl", Status: "cloned"},
		{Repository: "jrmash/perl-Dist-Zilla-PluginBundle-Author-JRMASH", Status: "updated"},
		{Repository: "jrmash/terramate-component-library", Status: "unchanged"},
	}, Summary: Summary{Selected: 3, Processed: 3, Cloned: 1, Updated: 1, Unchanged: 1}}
	var out bytes.Buffer
	if err := render(&out, doc, "human", false, false); err != nil {
		t.Fatal(err)
	}
	want := "+ jrmash/bfl" + strings.Repeat(" ", 40) + "cloned   \n" +
		"+ jrmash/perl-Dist-Zilla-PluginBundle-Author-JRMASH updated  \n" +
		"+ jrmash/terramate-component-library" + strings.Repeat(" ", 16) + "unchanged\n" +
		"3 selected · 1 cloned · 1 updated · 1 unchanged · 0 skipped · 0 failed\n"
	if out.String() != want {
		t.Fatalf("human table:\n%q\nwant:\n%q", out.String(), want)
	}
}

func TestHumanRepositoryWidthUsesSanitizedDisplayCells(t *testing.T) {
	for _, tc := range []struct {
		names []string
		limit int
		want  int
	}{
		{nil, 0, 1},
		{[]string{"", " \t\n"}, 0, 1},
		{[]string{"octo/a"}, 0, 6},
		{[]string{" \tocto/a\n"}, 0, 6},
		{[]string{"octo/日"}, 0, 7},
		{[]string{"octo/e\u0301"}, 0, 6},
		{[]string{"octo/👩‍💻"}, 0, 7},
		{[]string{"jrmash/perl-Dist-Zilla-PluginBundle-Author-JRMASH"}, 40, 27},
		{[]string{"jrmash/perl-Dist-Zilla-PluginBundle-Author-JRMASH"}, 20, 7},
		{[]string{"jrmash/perl-Dist-Zilla-PluginBundle-Author-JRMASH"}, 1, 1},
		{[]string{"octo/a"}, -1, 6},
	} {
		if got := humanRepositoryWidth(tc.names, tc.limit); got != tc.want {
			t.Errorf("width(%q, %d) = %d, want %d", tc.names, tc.limit, got, tc.want)
		}
	}
}

func TestHumanRowsFitNarrowTerminals(t *testing.T) {
	names := []string{"jrmash/bfl", "jrmash/perl-Dist-Zilla-PluginBundle-Author-JRMASH"}
	for _, terminalWidth := range []int{20, 32, 60} {
		nameWidth := humanRepositoryWidth(names, terminalWidth)
		for index, name := range names {
			var out bytes.Buffer
			status := []string{"unchanged", "updated"}[index]
			if err := renderHumanResult(&out, syncgit.Result{Repository: name, Status: status}, true, true, nameWidth); err != nil {
				t.Fatal(err)
			}
			line := strings.TrimSuffix(out.String(), "\n")
			plain := humanColorEscape.ReplaceAllString(line, "")
			if cells := uniseg.StringWidth(plain); cells > terminalWidth-1 {
				t.Errorf("width %d produced %d display cells: %q", terminalWidth, cells, line)
			}
			if column := humanStatusColumn(t, line, status); column != 3+nameWidth {
				t.Errorf("status column = %d, want %d in %q", column, 3+nameWidth, line)
			}
			if uniseg.StringWidth(name) > nameWidth && !strings.Contains(plain, "…") {
				t.Errorf("truncated repository has no ellipsis: %q", line)
			}
		}
	}
}

func TestHumanUnicodeNamesAlignWithoutSplittingGraphemes(t *testing.T) {
	names := []string{"octo/a", "octo/日日本", "octo/e\u0301clair", "octo/👩‍💻-tools"}
	width := humanRepositoryWidth(names, 0)
	for _, name := range names {
		var out bytes.Buffer
		if err := renderHumanResult(&out, syncgit.Result{Repository: name, Status: "cloned"}, false, false, width); err != nil {
			t.Fatal(err)
		}
		if column := humanStatusColumn(t, out.String(), "cloned"); column != width+3 || !strings.Contains(out.String(), name) {
			t.Errorf("Unicode column/full name mismatch: %q", out.String())
		}
	}
	for _, tc := range []struct {
		width int
		want  string
	}{
		{8, "octo/e\u0301…"},
		{9, "octo/e\u0301👩‍💻…"},
	} {
		var out bytes.Buffer
		if err := renderHumanResult(&out, syncgit.Result{Repository: "octo/e\u0301👩‍💻-日本", Status: "cloned"}, false, false, tc.width); err != nil {
			t.Fatal(err)
		}
		line := strings.TrimSuffix(out.String(), "\n")
		end := strings.LastIndex(line, " cloned")
		if end < 2 {
			t.Fatalf("missing padded name: %q", line)
		}
		if got := strings.TrimRight(line[2:end], " "); got != tc.want || !utf8.ValidString(got) {
			t.Errorf("Unicode truncation = %q, want %q", got, tc.want)
		}
		if column := humanStatusColumn(t, line, "cloned"); column != tc.width+3 {
			t.Errorf("Unicode status column = %d, want %d", column, tc.width+3)
		}
	}
}

func TestStructuredOutputPreservesFullRepositoryNames(t *testing.T) {
	const repository = "jrmash/perl-Dist-Zilla-PluginBundle-Author-JRMASH"
	doc := Document{Results: []syncgit.Result{{Repository: repository, Status: "cloned"}}}
	for _, format := range []string{"json", "yaml"} {
		var out bytes.Buffer
		if err := render(&out, doc, format, true, true); err != nil {
			t.Fatal(err)
		}
		var decoded Document
		var err error
		if format == "json" {
			err = json.Unmarshal(out.Bytes(), &decoded)
		} else {
			err = yaml.Unmarshal(out.Bytes(), &decoded)
		}
		if err != nil || len(decoded.Results) != 1 || decoded.Results[0].Repository != repository {
			t.Errorf("%s repository name changed: %v %s", format, err, out.String())
		}
	}
}
