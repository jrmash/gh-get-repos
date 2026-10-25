// Package cli implements the extension's command and batch workflow.
package cli

import (
	"context"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/cli/go-gh/v2/pkg/auth"
	"github.com/cli/go-gh/v2/pkg/term"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/jrmash/gh-get-repos/internal/executil"
	"github.com/jrmash/gh-get-repos/internal/github"
	"github.com/jrmash/gh-get-repos/internal/settings"
	"github.com/jrmash/gh-get-repos/internal/syncgit"
)

// Options allows commands, time, and process state to be substituted in tests.
type Options struct {
	Version          string
	Out, Err         io.Writer
	Color, Terminal  bool
	OutputWidth      int
	ProgressTerminal bool
	ProgressWidth    int
	Runner           executil.Runner
	Getwd            func() (string, error)
	LookupEnv        func(string) (string, bool)
	Host             func() string
	Sleep            func(context.Context, time.Duration) error
	Synchronize      func(context.Context, syncgit.Repository, string) syncgit.Result
}

type commandError struct {
	code int
	err  error
}

func (e *commandError) Error() string { return e.err.Error() }

// Execute returns the documented extension exit code. It never exits the process.
func Execute(ctx context.Context, args []string, options Options) int {
	options = defaults(options)
	cmd := newCommand(options)
	cmd.SetArgs(args)
	if err := cmd.ExecuteContext(ctx); err != nil {
		var failure *commandError
		code := 2
		if errors.As(err, &failure) {
			code = failure.code
		}
		if ctx.Err() != nil {
			code = 130
		}
		fmt.Fprintf(options.Err, "error: %s\n", oneLine(err.Error()))
		return code
	}
	return 0
}

func defaults(o Options) Options {
	terminal := term.FromEnv()
	if o.Out == nil {
		o.Out = terminal.Out()
		o.Color = terminal.IsColorEnabled()
		o.Terminal = terminal.IsTerminalOutput()
		if o.Terminal {
			o.OutputWidth, _, _ = terminal.Size()
		}
	}
	if o.Err == nil {
		o.Err = terminal.ErrOut()
		o.ProgressTerminal = term.IsTerminal(os.Stderr)
		if o.ProgressTerminal {
			o.ProgressWidth, _, _ = terminal.Size()
		}
	}
	if o.Version == "" {
		o.Version = "dev"
	}
	if o.Getwd == nil {
		o.Getwd = os.Getwd
	}
	if o.LookupEnv == nil {
		o.LookupEnv = os.LookupEnv
	}
	if o.Host == nil {
		o.Host = func() string { host, _ := auth.DefaultHost(); return host }
	}
	if o.Sleep == nil {
		o.Sleep = sleep
	}
	return o
}

func newCommand(o Options) *cobra.Command {
	cmd := &cobra.Command{
		Use:         "get-repos [owner]",
		Annotations: map[string]string{cobra.CommandDisplayNameAnnotation: "gh get-repos"},
		Short:       "Clone or update repositories owned by a GitHub user or organization",
		Long: "Clone or update all matching repositories owned by a GitHub user or organization.\n\n" +
			"Configuration: flags > GHGR_* > ./.gh-get-repos.yml > <gh-config-dir>/get-repos.yml > defaults.\n" +
			"Owner defaults to GHGR_OWNER, configuration, or the authenticated user.\n" +
			"Updates switch clean checkouts to the default branch and fast-forward only.",
		Example:           "  gh get-repos octo-org --path ~/src --mode worktree\n  gh get-repos --language Go --topic cli,tools\n  gh get-repos octocat --format json",
		Args:              cobra.MaximumNArgs(1),
		Version:           o.Version,
		SilenceUsage:      true,
		SilenceErrors:     true,
		CompletionOptions: cobra.CompletionOptions{DisableDefaultCmd: true},
	}
	cmd.SetOut(o.Out)
	cmd.SetErr(o.Err)
	cmd.SetVersionTemplate("gh get-repos {{.Version}}\n")
	flags := cmd.Flags()
	flags.StringP("archived", "a", "exclude", "Archived repositories: any|exclude|only")
	flags.String("forks", "exclude", "Forked repositories: any|exclude|only")
	flags.String("format", "human", "Output format: human|json|yaml")
	flags.String("mode", "standard", "Layout: standard (<repo>)|worktree (<repo>/base)")
	flags.StringP("visibility", "v", "any", "Repository visibility: any|internal|private|public")
	flags.StringP("language", "l", "", "Primary language name (case insensitive; default: no filter)")
	flags.StringSliceP("topic", "t", nil, "Required topics (CSV or repeat; all must match; default: no filter)")
	flags.IntP("sleep", "s", 0, "Seconds between repository attempts: 0–99 (default: 0)")
	flags.IntP("max-repos", "m", 0, "Maximum matching repositories: nonnegative integer (default: 0, unlimited)")
	flags.StringP("path", "p", ".", "Destination directory")
	flags.Bool("dry-run", false, "List selected repositories without cloning or updating (default: false)")
	flags.Bool("verbose", false, "Write full Git operation output to stderr (default: false)")
	flags.SortFlags = false
	flags.BoolP("help", "h", false, "Show help")
	flags.Bool("version", false, "Show extension version")
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		cwd, err := o.Getwd()
		if err != nil {
			return &commandError{1, fmt.Errorf("get current directory: %w", err)}
		}
		overrides := map[string]string{}
		flags.Visit(func(flag *pflag.Flag) {
			if flag.Name != "help" && flag.Name != "version" && flag.Name != "topic" {
				overrides[flag.Name] = flag.Value.String()
			}
		})
		if flags.Changed("topic") {
			topics, _ := flags.GetStringSlice("topic")
			var buffer strings.Builder
			writer := csv.NewWriter(&buffer)
			_ = writer.Write(topics)
			writer.Flush()
			overrides["topic"] = strings.TrimSuffix(buffer.String(), "\n")
		}
		if len(args) == 1 {
			overrides["owner"] = args[0]
		}
		cfg, err := settings.Load(cwd, o.LookupEnv, overrides)
		if err != nil {
			return err
		}
		if o.Runner == nil {
			o.Runner = executil.ProcessRunner{Verbose: cfg.Verbose, Stderr: o.Err}
		}
		doc, code := run(cmd.Context(), cfg, o)
		display := doc
		if cfg.Format == "human" {
			// Repository rows were emitted as operations completed.
			display.Results = nil
		}
		if err := render(o.Out, display, cfg.Format, o.Color, o.Terminal); err != nil {
			return &commandError{1, fmt.Errorf("write results: %w", err)}
		}
		if code != 0 {
			message := doc.Error
			if message == "" {
				message = "one or more repositories could not be synchronized"
			}
			return &commandError{code, errors.New(message)}
		}
		return nil
	}
	return cmd
}

func run(ctx context.Context, cfg settings.Config, o Options) (Document, int) {
	doc := Document{Owner: cfg.Owner, Host: o.Host(), Path: cfg.Path, Mode: cfg.Mode, DryRun: cfg.DryRun, Results: []syncgit.Result{}}
	if doc.Host == "" || strings.HasPrefix(doc.Host, "-") || strings.ContainsAny(doc.Host, "/\\ \t\r\n") {
		doc.Error = "invalid GitHub hostname"
		return doc, 1
	}
	terminalType, _ := o.LookupEnv("TERM")
	activity := newProgress(o.Err, cfg.Format == "human", o.ProgressTerminal && !cfg.Verbose && terminalType != "dumb", o.Color, o.ProgressWidth)
	defer activity.Stop()
	start := func(message string) bool {
		if err := activity.Start(ctx, message); err != nil {
			doc.Error = "write progress: " + err.Error()
			return false
		}
		return true
	}
	stop := func() bool {
		if err := activity.Stop(); err != nil {
			doc.Error = "write progress: " + err.Error()
			return false
		}
		return true
	}
	account := cfg.Owner
	if account == "" {
		account = "the authenticated user"
	}
	if !start("Discovering repositories for " + account) {
		return doc, 1
	}
	owner, repos, err := github.Discover(ctx, o.Runner, doc.Host, cfg.Owner)
	progressOK := stop()
	if err != nil {
		doc.Error = err.Error()
		if ctx.Err() != nil {
			return doc, 130
		}
		return doc, 1
	}
	if !progressOK {
		return doc, 1
	}
	doc.Owner = owner
	repos = github.Filter(repos, cfg)
	if cfg.MaxRepos > 0 && len(repos) > cfg.MaxRepos {
		repos = repos[:cfg.MaxRepos]
	}
	doc.Summary.Selected = len(repos)
	names := make([]string, len(repos))
	for index, repo := range repos {
		names[index] = repo.NameWithOwner
	}
	outputWidth := 0
	if o.Terminal {
		outputWidth = o.OutputWidth
		if outputWidth <= 0 {
			outputWidth = 80
		}
	}
	nameWidth := humanRepositoryWidth(names, outputWidth)
	for index, repo := range repos {
		if ctx.Err() != nil {
			doc.Error = "interrupted"
			return doc, 130
		}
		root := filepath.Join(cfg.Path, repo.Name)
		path := root
		if cfg.Mode == "worktree" {
			path = filepath.Join(root, "base")
		}
		if cfg.DryRun {
			result := syncgit.Result{Repository: repo.NameWithOwner, Path: path, Status: "planned"}
			doc.Results = append(doc.Results, result)
			doc.Summary.add(result.Status)
			if cfg.Format == "human" {
				if err := renderHumanResult(o.Out, result, o.Color, o.Terminal, nameWidth); err != nil {
					doc.Error = "write repository result: " + err.Error()
					return doc, 1
				}
			}
			continue
		}
		r := syncgit.Repository{Host: doc.Host, Owner: owner, Name: repo.Name, Empty: repo.IsEmpty}
		if repo.DefaultBranchRef != nil {
			r.DefaultBranch = repo.DefaultBranchRef.Name
		}
		label := func(stage string) string {
			return fmt.Sprintf("%s %s (%d/%d)", stage, repo.NameWithOwner, index+1, len(repos))
		}
		if !start(label("Checking checkout")) {
			return doc, 1
		}
		var result syncgit.Result
		if cfg.Mode == "worktree" && hasGitEntry(root) {
			result = syncgit.Result{Repository: repo.NameWithOwner, Path: path, Status: "failed", Error: "repository already uses the standard layout; move it explicitly before using worktree mode"}
		} else if o.Synchronize != nil {
			result = o.Synchronize(ctx, r, path)
		} else {
			synchronizer := syncgit.Synchronizer{Runner: o.Runner, Progress: func(stage string) { activity.Update(label(stage)) }}
			result = synchronizer.Sync(ctx, r, path)
		}
		progressOK = stop()
		doc.Results = append(doc.Results, result)
		doc.Summary.add(result.Status)
		if cfg.Format == "human" {
			if err := renderHumanResult(o.Out, result, o.Color, o.Terminal, nameWidth); err != nil {
				doc.Error = "write repository result: " + err.Error()
				return doc, 1
			}
		}
		if !progressOK {
			return doc, 1
		}
		if ctx.Err() != nil {
			doc.Error = "interrupted"
			return doc, 130
		}
		if index < len(repos)-1 && cfg.Sleep > 0 {
			if !start(fmt.Sprintf("Waiting %ds before the next repository", cfg.Sleep)) {
				return doc, 1
			}
			err := o.Sleep(ctx, time.Duration(cfg.Sleep)*time.Second)
			progressOK = stop()
			if err != nil {
				doc.Error = err.Error()
				if ctx.Err() != nil {
					return doc, 130
				}
				return doc, 1
			}
			if !progressOK {
				return doc, 1
			}
		}
	}
	if ctx.Err() != nil {
		doc.Error = "interrupted"
		return doc, 130
	}
	if doc.Summary.Skipped+doc.Summary.Failed > 0 {
		return doc, 1
	}
	return doc, 0
}

func hasGitEntry(path string) bool {
	_, err := os.Lstat(filepath.Join(path, ".git"))
	return err == nil
}

func sleep(ctx context.Context, duration time.Duration) error {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
