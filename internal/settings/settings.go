// Package settings loads extension settings from configuration files, the
// environment, and explicitly supplied command line arguments.
package settings

import (
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"sort"
	"strconv"
	"strings"

	"go.yaml.in/yaml/v3"
)

// Config is the fully resolved and validated configuration for one invocation.
type Config struct {
	Owner      string
	Archived   string
	Forks      string
	Format     string
	Mode       string
	Visibility string
	Language   string
	Topics     []string
	Sleep      int
	MaxRepos   int
	Path       string
	Verbose    bool
	DryRun     bool
}

var names = []string{"owner", "archived", "forks", "format", "mode", "visibility", "language", "topic", "sleep", "max-repos", "path", "verbose", "dry-run"}

// Environment names are ordered from fallback aliases to the canonical name.
// Presence matters: an explicitly empty canonical value overrides an alias.
var environmentNames = map[string][]string{
	"owner":      {"GHGR_OWNER"},
	"archived":   {"GHGR_ARCHIVED", "GHGR_OPT_ARCHIVED"},
	"forks":      {"GHGR_FORKS", "GHGR_OPT_FORKS"},
	"format":     {"GHGR_FORMAT", "GHGR_OPT_FORMAT"},
	"mode":       {"GHGR_MODE", "GHGR_OPT_MODE"},
	"visibility": {"GHGR_VISIBILITY", "GHGR_OPT_VISIBILITY"},
	"language":   {"GHGR_LANGUAGE", "GHGR_OPT_LANGUAGE"},
	"topic":      {"GHGR_TOPIC", "GHGR_OPT_TOPIC"},
	"sleep":      {"GHGR_SLEEP", "GHGR_OPT_SLEEP"},
	"max-repos":  {"GHGR_MAX_REPOS", "GHGR_OPT_LIMIT"},
	"path":       {"GHGR_PATH", "GHGR_OPT_PATH"},
	"verbose":    {"GHGR_VERBOSE", "GHGR_OPT_VERBOSE"},
	"dry-run":    {"GHGR_DRY_RUN", "GHGR_OPT_DRY_RUN"},
}

var ownerPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

type value struct {
	text   string
	topics []string
	list   bool
	source string
}

// Load resolves defaults < global YAML < local YAML < GHGR_* < overrides.
// Overrides contain only explicitly supplied flags; their keys match flag names.
// Relative destination paths are always relative to cwd, including paths from
// the global configuration file. A nil lookup uses os.LookupEnv.
func Load(cwd string, lookup func(string) (string, bool), overrides map[string]string) (Config, error) {
	if lookup == nil {
		lookup = os.LookupEnv
	}
	if cwd == "" {
		return Config{}, errors.New("invocation directory must not be empty")
	}
	cwd, err := filepath.Abs(cwd)
	if err != nil {
		return Config{}, fmt.Errorf("resolve invocation directory: %w", err)
	}
	values := map[string]value{
		"owner": {}, "archived": {text: "exclude"}, "forks": {text: "exclude"},
		"format": {text: "human"}, "mode": {text: "standard"}, "visibility": {text: "any"},
		"language": {}, "topic": {list: true}, "sleep": {text: "0"},
		"max-repos": {text: "0"}, "path": {text: cwd}, "verbose": {text: "false"},
		"dry-run": {text: "false"},
	}
	globalDir, err := configDir(lookup, runtime.GOOS, os.UserHomeDir)
	if err != nil {
		return Config{}, err
	}
	globalDir, err = resolvePath(globalDir, cwd, lookup)
	if err != nil {
		return Config{}, fmt.Errorf("resolve global configuration directory: %w", err)
	}
	for _, filename := range []string{filepath.Join(globalDir, "get-repos.yml"), filepath.Join(cwd, ".gh-get-repos.yml")} {
		layer, err := readFile(filename)
		if err != nil {
			return Config{}, err
		}
		for key, entry := range layer {
			values[key] = entry
		}
	}
	for _, key := range names {
		for _, env := range environmentNames[key] {
			if text, ok := lookup(env); ok {
				values[key] = value{text: text, source: env}
			}
		}
	}
	keys := make([]string, 0, len(overrides))
	for key := range overrides {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		if _, ok := values[key]; !ok {
			return Config{}, fmt.Errorf("unknown command line setting %q", key)
		}
		values[key] = value{text: overrides[key], source: "--" + key}
	}
	return resolve(values, cwd, lookup)
}

func configDir(lookup func(string) (string, bool), goos string, home func() (string, error)) (string, error) {
	if dir, _ := lookup("GH_CONFIG_DIR"); dir != "" {
		return dir, nil
	}
	if dir, _ := lookup("XDG_CONFIG_HOME"); dir != "" {
		return filepath.Join(dir, "gh"), nil
	}
	if goos == "windows" {
		for _, key := range []string{"APPDATA", "AppData"} {
			if dir, _ := lookup(key); dir != "" {
				return filepath.Join(dir, "GitHub CLI"), nil
			}
		}
	}
	dir, _ := lookup("HOME")
	if dir == "" {
		var err error
		dir, err = home()
		if err != nil {
			return "", fmt.Errorf("find home directory for global configuration: %w", err)
		}
	}
	return filepath.Join(dir, ".config", "gh"), nil
}

func readFile(filename string) (map[string]value, error) {
	data, err := os.ReadFile(filename)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read configuration %q: %w", filename, err)
	}
	layer, err := decodeYAML(string(data))
	if err != nil {
		return nil, fmt.Errorf("configuration %q: %w", filename, err)
	}
	for key, entry := range layer {
		entry.source = filename
		layer[key] = entry
	}
	return layer, nil
}

func decodeYAML(data string) (map[string]value, error) {
	decoder := yaml.NewDecoder(strings.NewReader(data))
	var document yaml.Node
	if err := decoder.Decode(&document); err != nil {
		if errors.Is(err, io.EOF) {
			return nil, nil
		}
		return nil, err
	}
	var extra yaml.Node
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err != nil {
			return nil, err
		}
		return nil, errors.New("expected one YAML document")
	}
	root := document.Content[0]
	if root.Kind != yaml.MappingNode {
		return nil, errors.New("expected a YAML mapping of setting names to values")
	}
	allowed := make(map[string]bool, len(names))
	for _, name := range names {
		allowed[name] = true
	}
	result := make(map[string]value, len(root.Content)/2)
	for i := 0; i < len(root.Content); i += 2 {
		key, node := root.Content[i], root.Content[i+1]
		if key.Kind != yaml.ScalarNode || key.Tag != "!!str" || !allowed[key.Value] {
			return nil, fmt.Errorf("unknown setting %q", key.Value)
		}
		if _, exists := result[key.Value]; exists {
			return nil, fmt.Errorf("duplicate setting %q", key.Value)
		}
		entry, err := decodeValue(key.Value, node)
		if err != nil {
			return nil, err
		}
		result[key.Value] = entry
	}
	return result, nil
}

func decodeValue(key string, node *yaml.Node) (value, error) {
	if node.Kind == yaml.AliasNode {
		node = node.Alias
	}
	switch key {
	case "topic":
		if node.Kind != yaml.SequenceNode {
			return value{}, errors.New("topic must be a YAML sequence of strings")
		}
		topics := make([]string, 0, len(node.Content))
		for _, topic := range node.Content {
			if topic.Kind != yaml.ScalarNode || topic.Tag != "!!str" {
				return value{}, errors.New("topic must be a YAML sequence of strings")
			}
			topics = append(topics, topic.Value)
		}
		return value{topics: normalizeTopics(topics), list: true}, nil
	case "sleep", "max-repos":
		if node.Kind != yaml.ScalarNode || node.Tag != "!!int" {
			return value{}, fmt.Errorf("%s must be a YAML integer", key)
		}
		var number int
		if err := node.Decode(&number); err != nil {
			return value{}, fmt.Errorf("%s must be an integer: %w", key, err)
		}
		return value{text: strconv.Itoa(number)}, nil
	case "verbose", "dry-run":
		if node.Kind != yaml.ScalarNode || node.Tag != "!!bool" {
			return value{}, fmt.Errorf("%s must be a YAML boolean", key)
		}
		return value{text: strings.ToLower(node.Value)}, nil
	default:
		if node.Kind != yaml.ScalarNode || node.Tag != "!!str" {
			return value{}, fmt.Errorf("%s must be a YAML string", key)
		}
		return value{text: node.Value}, nil
	}
}

func resolve(values map[string]value, cwd string, lookup func(string) (string, bool)) (Config, error) {
	cfg := Config{
		Owner: values["owner"].text, Archived: values["archived"].text,
		Forks: values["forks"].text, Format: values["format"].text, Mode: values["mode"].text,
		Visibility: values["visibility"].text, Language: values["language"].text,
	}
	invalid := func(key, reason string) error {
		if source := values[key].source; source != "" {
			return fmt.Errorf("%s: %s %s", source, key, reason)
		}
		return fmt.Errorf("%s %s", key, reason)
	}
	if cfg.Owner != "" && !ownerPattern.MatchString(cfg.Owner) {
		return Config{}, invalid("owner", "must be a GitHub user or organization handle without paths, whitespace, or option prefixes")
	}
	for _, rule := range []struct {
		key     string
		allowed string
	}{
		{"archived", "any|exclude|only"}, {"forks", "any|exclude|only"},
		{"format", "human|json|yaml"}, {"mode", "standard|worktree"},
		{"visibility", "any|internal|private|public"},
	} {
		if !slices.Contains(strings.Split(rule.allowed, "|"), values[rule.key].text) {
			return Config{}, invalid(rule.key, "must be one of "+rule.allowed)
		}
	}
	var err error
	cfg.Sleep, err = strconv.Atoi(values["sleep"].text)
	if err != nil || cfg.Sleep < 0 || cfg.Sleep > 99 {
		return Config{}, invalid("sleep", "must be an integer from 0 to 99")
	}
	cfg.MaxRepos, err = strconv.Atoi(values["max-repos"].text)
	if err != nil || cfg.MaxRepos < 0 {
		return Config{}, invalid("max-repos", "must be a nonnegative integer (0 means unlimited)")
	}
	cfg.Verbose, err = strconv.ParseBool(values["verbose"].text)
	if err != nil {
		return Config{}, invalid("verbose", "must be a boolean")
	}
	cfg.DryRun, err = strconv.ParseBool(values["dry-run"].text)
	if err != nil {
		return Config{}, invalid("dry-run", "must be a boolean")
	}
	if topic := values["topic"]; topic.list {
		cfg.Topics = topic.topics
	} else {
		cfg.Topics, err = ParseTopics(topic.text)
		if err != nil {
			return Config{}, invalid("topic", "must be valid CSV: "+err.Error())
		}
	}
	cfg.Path, err = resolvePath(values["path"].text, cwd, lookup)
	if err != nil {
		return Config{}, invalid("path", err.Error())
	}
	return cfg, nil
}

// ParseTopics reads the CSV syntax used by --topic and GHGR_OPT_TOPIC. Empty input
// clears the filter. Whitespace and empty entries are removed; matching and
// case folding are left to repository filtering.
func ParseTopics(text string) ([]string, error) {
	if strings.TrimSpace(text) == "" {
		return nil, nil
	}
	reader := csv.NewReader(strings.NewReader(text))
	reader.TrimLeadingSpace = true
	topics, err := reader.Read()
	if err != nil {
		return nil, err
	}
	if _, err := reader.Read(); !errors.Is(err, io.EOF) {
		if err != nil {
			return nil, err
		}
		return nil, errors.New("expected one CSV record")
	}
	return normalizeTopics(topics), nil
}

func normalizeTopics(topics []string) []string {
	var normalized []string
	for _, topic := range topics {
		if topic = strings.TrimSpace(topic); topic != "" {
			normalized = append(normalized, topic)
		}
	}
	return normalized
}

func resolvePath(path, cwd string, lookup func(string) (string, bool)) (string, error) {
	if strings.IndexByte(path, 0) >= 0 {
		return "", errors.New("must not contain NUL characters")
	}
	if strings.HasPrefix(path, "~") {
		if path != "~" && !strings.HasPrefix(path, "~/") && !(runtime.GOOS == "windows" && strings.HasPrefix(path, `~\`)) {
			return "", errors.New("supports ~ for the current user's home directory, not ~user")
		}
		home, _ := lookup("HOME")
		if home == "" {
			var err error
			home, err = os.UserHomeDir()
			if err != nil {
				return "", fmt.Errorf("cannot find home directory: %w", err)
			}
		}
		if path == "~" {
			path = home
		} else {
			path = filepath.Join(home, path[2:])
		}
	}
	if !filepath.IsAbs(path) {
		path = filepath.Join(cwd, path)
	}
	return filepath.Clean(path), nil
}
