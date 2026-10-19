package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"unicode"

	"github.com/rivo/uniseg"
	"go.yaml.in/yaml/v3"

	"github.com/jrmash/gh-get-repos/internal/syncgit"
)

type Document struct {
	Host    string           `json:"host" yaml:"host"`
	Owner   string           `json:"owner" yaml:"owner"`
	Path    string           `json:"path" yaml:"path"`
	Mode    string           `json:"mode" yaml:"mode"`
	DryRun  bool             `json:"dry_run" yaml:"dry_run"`
	Results []syncgit.Result `json:"results" yaml:"results"`
	Summary Summary          `json:"summary" yaml:"summary"`
	Error   string           `json:"error,omitempty" yaml:"error,omitempty"`
}

type Summary struct {
	Selected  int `json:"selected" yaml:"selected"`
	Planned   int `json:"planned" yaml:"planned"`
	Processed int `json:"processed" yaml:"processed"`
	Cloned    int `json:"cloned" yaml:"cloned"`
	Updated   int `json:"updated" yaml:"updated"`
	Unchanged int `json:"unchanged" yaml:"unchanged"`
	Skipped   int `json:"skipped" yaml:"skipped"`
	Failed    int `json:"failed" yaml:"failed"`
}

func (s *Summary) add(status string) {
	if status == "planned" {
		s.Planned++
		return
	}
	s.Processed++
	switch status {
	case "cloned":
		s.Cloned++
	case "updated":
		s.Updated++
	case "unchanged":
		s.Unchanged++
	case "skipped":
		s.Skipped++
	case "failed":
		s.Failed++
	}
}

func render(out io.Writer, doc Document, format string, color, terminal bool) error {
	switch format {
	case "json":
		encoder := json.NewEncoder(out)
		encoder.SetIndent("", "  ")
		encoder.SetEscapeHTML(false)
		return encoder.Encode(doc)
	case "yaml":
		encoder := yaml.NewEncoder(out)
		encoder.SetIndent(2)
		defer encoder.Close()
		return encoder.Encode(doc)
	default:
		names := make([]string, len(doc.Results))
		for index, result := range doc.Results {
			names[index] = result.Repository
		}
		nameWidth := humanRepositoryWidth(names, 0)
		for _, result := range doc.Results {
			if err := renderHumanResult(out, result, color, terminal, nameWidth); err != nil {
				return err
			}
		}
		s := doc.Summary
		if doc.DryRun {
			_, err := fmt.Fprintf(out, "%d selected · %d planned · dry run: no repositories changed\n", s.Selected, s.Planned)
			return err
		}
		_, err := fmt.Fprintf(out, "%d selected · %d cloned · %d updated · %d unchanged · %d skipped · %d failed\n", s.Selected, s.Cloned, s.Updated, s.Unchanged, s.Skipped, s.Failed)
		return err
	}
}

// humanRepositoryWidth is fixed for a batch before its first row is streamed.
// A terminal reserves space for the icon, separator, longest status, and one
// spare column so a long name cannot wrap the status onto the next line.
func humanRepositoryWidth(names []string, terminalWidth int) int {
	width := 1
	for _, name := range names {
		width = max(width, uniseg.StringWidth(oneLine(name)))
	}
	if terminalWidth > 0 {
		width = min(width, max(1, terminalWidth-13))
	}
	return width
}

func renderHumanResult(out io.Writer, result syncgit.Result, color, terminal bool, nameWidth int) error {
	symbol, tone := "+", "32"
	if terminal {
		symbol = "✓"
	}
	if result.Status == "planned" {
		symbol, tone = "-", "36"
		if terminal {
			symbol = "→"
		}
	}
	if result.Status == "failed" || result.Status == "skipped" {
		symbol, tone = "x", "31"
		if terminal {
			symbol = "✗"
		}
	}
	if color {
		symbol = "\x1b[" + tone + "m" + symbol + "\x1b[0m"
	}
	detail := ""
	if result.Error != "" {
		detail = " — " + oneLine(result.Error)
	}
	nameWidth = max(1, nameWidth)
	name := truncateProgress(oneLine(result.Repository), nameWidth)
	name += strings.Repeat(" ", nameWidth-uniseg.StringWidth(name))
	_, err := fmt.Fprintf(out, "%s %s %-9s%s\n", symbol, name, result.Status, detail)
	return err
}

func oneLine(value string) string {
	return strings.Join(strings.FieldsFunc(value, func(r rune) bool { return unicode.IsControl(r) || unicode.IsSpace(r) }), " ")
}
