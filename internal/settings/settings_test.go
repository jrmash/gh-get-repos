package settings

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
)

func env(values map[string]string) func(string) (string, bool) {
	return func(key string) (string, bool) { value, ok := values[key]; return value, ok }
}

func write(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
}

func fixture(t *testing.T) (string, string, map[string]string) {
	t.Helper()
	root := t.TempDir()
	cwd, global := filepath.Join(root, "work"), filepath.Join(root, "config")
	if err := os.MkdirAll(cwd, 0o755); err != nil {
		t.Fatal(err)
	}
	return cwd, global, map[string]string{"GH_CONFIG_DIR": global, "HOME": root}
}

func TestDefaults(t *testing.T) {
	cwd, _, values := fixture(t)
	got, err := Load(cwd, env(values), nil)
	if err != nil {
		t.Fatal(err)
	}
	want := Config{Archived: "exclude", Forks: "exclude", Format: "human", Mode: "standard", Visibility: "any", Path: cwd}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}

func TestLayers(t *testing.T) {
	cwd, global, values := fixture(t)
	write(t, filepath.Join(global, "get-repos.yml"), `owner: global-org
archived: only
forks: any
format: yaml
mode: worktree
visibility: private
language: Go
topic: [global, backend]
sleep: 9
path: global-repos
verbose: true
`)
	write(t, filepath.Join(cwd, ".gh-get-repos.yml"), `owner: local-org
forks: only
topic: [local]
path: local-repos
`)
	values["GHGR_OWNER"], values["GHGR_TOPIC"] = "env-org", "env, platform"
	values["GHGR_FORMAT"], values["GHGR_SLEEP"], values["GHGR_VERBOSE"] = "json", "3", "false"
	got, err := Load(cwd, env(values), map[string]string{"owner": "cli-org", "topic": "cli", "sleep": "0", "verbose": "false", "language": "", "path": "cli-repos"})
	if err != nil {
		t.Fatal(err)
	}
	want := Config{Owner: "cli-org", Archived: "only", Forks: "only", Format: "json", Mode: "worktree", Visibility: "private", Topics: []string{"cli"}, Path: filepath.Join(cwd, "cli-repos")}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}

func TestLocalOwnerAndEmptyOverrides(t *testing.T) {
	cwd, global, values := fixture(t)
	write(t, filepath.Join(global, "get-repos.yml"), "owner: global\nverbose: true\nsleep: 8\ntopic: [old]\nlanguage: Go\npath: other\n")
	write(t, filepath.Join(cwd, ".gh-get-repos.yml"), "owner: local\nverbose: false\nsleep: 0\ntopic: []\nlanguage: ''\npath: .\n")
	got, err := Load(cwd, env(values), nil)
	if err != nil {
		t.Fatal(err)
	}
	if got.Owner != "local" || got.Verbose || got.Sleep != 0 || len(got.Topics) != 0 || got.Language != "" || got.Path != cwd {
		t.Fatalf("local layer did not override zero values: %+v", got)
	}
	values["GHGR_OWNER"], values["GHGR_LANGUAGE"], values["GHGR_TOPIC"], values["GHGR_PATH"] = "", "", "", ""
	got, err = Load(cwd, env(values), nil)
	if err != nil {
		t.Fatal(err)
	}
	if got.Owner != "" || got.Language != "" || len(got.Topics) != 0 || got.Path != cwd {
		t.Fatalf("empty environment values did not override: %+v", got)
	}
}

func TestLocalConfigOnlyChecksInvocationDirectory(t *testing.T) {
	cwd, _, values := fixture(t)
	write(t, filepath.Join(filepath.Dir(cwd), ".gh-get-repos.yml"), "owner: parent\n")
	got, err := Load(cwd, env(values), nil)
	if err != nil {
		t.Fatal(err)
	}
	if got.Owner != "" {
		t.Fatalf("unexpected parent owner %q", got.Owner)
	}
}

func TestSemanticValidationUsesHighestLayer(t *testing.T) {
	cwd, global, values := fixture(t)
	write(t, filepath.Join(global, "get-repos.yml"), "mode: invalid\nsleep: -1\n")
	values["GHGR_SLEEP"] = "not-a-number"
	got, err := Load(cwd, env(values), map[string]string{"mode": "worktree", "sleep": "1"})
	if err != nil {
		t.Fatal(err)
	}
	if got.Mode != "worktree" || got.Sleep != 1 {
		t.Fatalf("unexpected result %+v", got)
	}
}

func TestLoadValidation(t *testing.T) {
	cases := []struct{ key, value, message string }{
		{"archived", "none", "any|exclude|only"},
		{"forks", "EXCLUDE", "any|exclude|only"},
		{"format", "", "human|json|yaml"},
		{"mode", "standard|worktree", "standard|worktree"},
		{"visibility", "protected", "any|internal|private|public"},
		{"sleep", "-1", "integer from 0 to 99"},
		{"sleep", "100", "integer from 0 to 99"},
		{"sleep", "1.1", "integer from 0 to 99"},
		{"max-repos", "-1", "nonnegative integer"},
		{"max-repos", "1.1", "nonnegative integer"},
		{"max-repos", "", "nonnegative integer"},
		{"max-repos", "99999999999999999999999999999999", "nonnegative integer"},
		{"verbose", "sometimes", "boolean"},
		{"dry-run", "sometimes", "boolean"},
		{"topic", `"unterminated`, "valid CSV"},
		{"path", "~another/repo", "not ~user"},
		{"path", "bad\x00path", "NUL"},
		{"owner", "../bad", "handle"},
		{"owner", "-R", "handle"},
		{"owner", "org/repo", "handle"},
		{"owner", `org\repo`, "handle"},
		{"owner", "a b", "handle"},
		{"owner", "..", "handle"},
		{"owner", "https://github.com/org", "handle"},
	}
	for _, tt := range cases {
		t.Run(tt.key+"_"+tt.value, func(t *testing.T) {
			cwd, _, values := fixture(t)
			_, err := Load(cwd, env(values), map[string]string{tt.key: tt.value})
			if err == nil || !strings.Contains(err.Error(), tt.message) || !strings.Contains(err.Error(), "--"+tt.key) {
				t.Fatalf("got %v, expected --%s and %q", err, tt.key, tt.message)
			}
		})
	}
}

func TestValidValues(t *testing.T) {
	cwd, _, values := fixture(t)
	for _, owner := range []string{"octocat", "example-org", "user_enterprise", "first.last", "0"} {
		if _, err := Load(cwd, env(values), map[string]string{"owner": owner}); err != nil {
			t.Errorf("owner %q: %v", owner, err)
		}
	}
	for key, accepted := range map[string][]string{
		"archived": {"any", "exclude", "only"}, "forks": {"any", "exclude", "only"},
		"format": {"human", "json", "yaml"}, "mode": {"standard", "worktree"},
		"visibility": {"any", "internal", "private", "public"}, "sleep": {"0", "9", "10", "99"},
		"max-repos": {"0", "1", "1000", "10000"}, "verbose": {"false", "true"}, "dry-run": {"false", "true"},
	} {
		for _, input := range accepted {
			if _, err := Load(cwd, env(values), map[string]string{key: input}); err != nil {
				t.Errorf("%s %q: %v", key, input, err)
			}
		}
	}
}

func TestEnvironmentAliases(t *testing.T) {
	cwd, _, values := fixture(t)
	for key, val := range map[string]string{
		"OWNER": "environment", "ARCHIVED": "any", "FORKS": "only", "FORMAT": "yaml", "MODE": "worktree",
		"VISIBILITY": "internal", "LANGUAGE": "Rust", "TOPIC": "cli,go", "SLEEP": "2", "PATH": "repos", "VERBOSE": "true", "MAX_REPOS": "20", "DRY_RUN": "true",
	} {
		values["GHGR_"+key] = val
	}
	values["GH_GET_REPOS_OWNER"], values["GHGR_CONFIG"] = "ignored", "missing-file"
	got, err := Load(cwd, env(values), nil)
	if err != nil {
		t.Fatal(err)
	}
	want := Config{Owner: "environment", Archived: "any", Forks: "only", Format: "yaml", Mode: "worktree", Visibility: "internal", Language: "Rust", Topics: []string{"cli", "go"}, Sleep: 2, MaxRepos: 20, Path: filepath.Join(cwd, "repos"), Verbose: true, DryRun: true}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v, want %+v", got, want)
	}
	values["GHGR_MODE"] = "INVALID"
	_, err = Load(cwd, env(values), nil)
	if err == nil || !strings.Contains(err.Error(), "GHGR_MODE") {
		t.Fatalf("expected environment provenance, got %v", err)
	}
}

func TestCanonicalEnvironmentNames(t *testing.T) {
	cwd, _, values := fixture(t)
	for key, val := range map[string]string{
		"ARCHIVED": "any", "FORKS": "only", "FORMAT": "yaml", "MODE": "worktree",
		"VISIBILITY": "internal", "LANGUAGE": "Rust", "TOPIC": "cli,go", "SLEEP": "99",
		"PATH": "repos", "VERBOSE": "true", "LIMIT": "42", "DRY_RUN": "true",
	} {
		values["GHGR_OPT_"+key] = val
	}
	values["GHGR_OWNER"] = "environment"
	got, err := Load(cwd, env(values), nil)
	if err != nil {
		t.Fatal(err)
	}
	want := Config{Owner: "environment", Archived: "any", Forks: "only", Format: "yaml", Mode: "worktree", Visibility: "internal", Language: "Rust", Topics: []string{"cli", "go"}, Sleep: 99, MaxRepos: 42, Path: filepath.Join(cwd, "repos"), Verbose: true, DryRun: true}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v, want %+v", got, want)
	}
	values["GHGR_OPT_LIMIT"] = "invalid"
	_, err = Load(cwd, env(values), nil)
	if err == nil || !strings.Contains(err.Error(), "GHGR_OPT_LIMIT") {
		t.Fatalf("expected canonical environment provenance, got %v", err)
	}
}

func TestCanonicalEnvironmentOverridesAliases(t *testing.T) {
	cwd, _, values := fixture(t)
	// Each canonical variable wins over the alias by presence, including zero,
	// false and empty values that clear a setting inherited from another layer.
	for key, aliases := range environmentNames {
		if len(aliases) < 2 {
			continue
		}
		t.Run(key, func(t *testing.T) {
			inputs := make(map[string]string, len(values)+2)
			for name, val := range values {
				inputs[name] = val
			}
			canonical := map[string]string{
				"archived": "any", "forks": "any", "format": "human", "mode": "standard",
				"visibility": "any", "language": "", "topic": "", "sleep": "0",
				"max-repos": "0", "path": "", "verbose": "false", "dry-run": "false",
			}[key]
			inputs[aliases[0]], inputs[aliases[1]] = "invalid", canonical
			got, err := Load(cwd, env(inputs), nil)
			if err != nil {
				t.Fatal(err)
			}
			want, err := Load(cwd, env(values), map[string]string{key: canonical})
			if err != nil || !reflect.DeepEqual(got, want) {
				t.Fatalf("got %+v, want %+v (%v)", got, want, err)
			}
		})
	}
}

func TestLimitAndDryRunLayers(t *testing.T) {
	cwd, global, values := fixture(t)
	write(t, filepath.Join(global, "get-repos.yml"), "max-repos: 100\ndry-run: true\n")
	write(t, filepath.Join(cwd, ".gh-get-repos.yml"), "max-repos: 50\ndry-run: false\n")
	got, err := Load(cwd, env(values), nil)
	if err != nil || got.MaxRepos != 50 || got.DryRun {
		t.Fatalf("local layer: %+v, %v", got, err)
	}
	values["GHGR_OPT_LIMIT"], values["GHGR_OPT_DRY_RUN"] = "25", "true"
	got, err = Load(cwd, env(values), nil)
	if err != nil || got.MaxRepos != 25 || !got.DryRun {
		t.Fatalf("environment layer: %+v, %v", got, err)
	}
	got, err = Load(cwd, env(values), map[string]string{"max-repos": "0", "dry-run": "false"})
	if err != nil || got.MaxRepos != 0 || got.DryRun {
		t.Fatalf("command line clearing: %+v, %v", got, err)
	}
}

func TestPaths(t *testing.T) {
	cwd, global, values := fixture(t)
	home := values["HOME"]
	for _, tt := range []struct{ input, want string }{
		{".", cwd}, {"", cwd}, {"nested/../repos", filepath.Join(cwd, "repos")},
		{global, global}, {"~", home}, {"~/repos", filepath.Join(home, "repos")},
	} {
		got, err := Load(cwd, env(values), map[string]string{"path": tt.input})
		if err != nil {
			t.Fatal(err)
		}
		if got.Path != tt.want {
			t.Errorf("path %q got %q, want %q", tt.input, got.Path, tt.want)
		}
	}
	if runtime.GOOS == "windows" {
		got, err := Load(cwd, env(values), map[string]string{"path": `~\repos`})
		if err != nil || got.Path != filepath.Join(home, "repos") {
			t.Fatalf("Windows home expansion: %+v, %v", got, err)
		}
	}
	values["GH_CONFIG_DIR"] = "relative-config"
	write(t, filepath.Join(cwd, "relative-config", "get-repos.yml"), "owner: relative\n")
	got, err := Load(cwd, env(values), nil)
	if err != nil || got.Owner != "relative" {
		t.Fatalf("relative global config: %+v, %v", got, err)
	}
	values["GH_CONFIG_DIR"] = "~/config"
	write(t, filepath.Join(home, "config", "get-repos.yml"), "owner: home\n")
	got, err = Load(cwd, env(values), nil)
	if err != nil || got.Owner != "home" {
		t.Fatalf("home global config: %+v, %v", got, err)
	}
}

func TestConfigurationDirectory(t *testing.T) {
	home := filepath.Join(t.TempDir(), "home")
	cases := []struct {
		name, goos string
		values     map[string]string
		want       string
	}{
		{"explicit", "linux", map[string]string{"GH_CONFIG_DIR": "explicit", "XDG_CONFIG_HOME": "xdg", "HOME": home}, "explicit"},
		{"xdg", "linux", map[string]string{"XDG_CONFIG_HOME": "xdg", "HOME": home}, filepath.Join("xdg", "gh")},
		{"windows", "windows", map[string]string{"APPDATA": "app", "HOME": home}, filepath.Join("app", "GitHub CLI")},
		{"mixed-case-windows", "windows", map[string]string{"AppData": "mixed"}, filepath.Join("mixed", "GitHub CLI")},
		{"home", "linux", map[string]string{"HOME": home, "APPDATA": "ignored"}, filepath.Join(home, ".config", "gh")},
		{"fallback", "windows", nil, filepath.Join(home, ".config", "gh")},
		{"empty-env", "darwin", map[string]string{"GH_CONFIG_DIR": "", "XDG_CONFIG_HOME": "", "HOME": ""}, filepath.Join(home, ".config", "gh")},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			got, err := configDir(env(tt.values), tt.goos, func() (string, error) { return home, nil })
			if err != nil || got != tt.want {
				t.Fatalf("got %q, %v, want %q", got, err, tt.want)
			}
		})
	}
	_, err := configDir(env(nil), "linux", func() (string, error) { return "", errors.New("missing home") })
	if err == nil || !strings.Contains(err.Error(), "missing home") {
		t.Fatalf("got %v", err)
	}
}

func TestYAML(t *testing.T) {
	for _, text := range []string{"", "# comment\n", "{}\n"} {
		if _, err := decodeYAML(text); err != nil {
			t.Errorf("%q: %v", text, err)
		}
	}
	got, err := decodeYAML("owner: &name octocat\nlanguage: *name\ntopic: [go, ' backend ', '', 'a,b']\nsleep: 0x2\nverbose: TRUE\nmax-repos: 0xA\ndry-run: FALSE\n")
	if err != nil {
		t.Fatal(err)
	}
	if got["language"].text != "octocat" || got["sleep"].text != "2" || got["verbose"].text != "true" || got["max-repos"].text != "10" || got["dry-run"].text != "false" || !reflect.DeepEqual(got["topic"].topics, []string{"go", "backend", "a,b"}) {
		t.Fatalf("unexpected YAML values: %+v", got)
	}
	cases := []struct{ name, text, message string }{
		{"syntax", "owner: [\n", "line"},
		{"unknown", "unknown: value\n", "unknown setting"},
		{"numeric-key", "1: value\n", "unknown setting"},
		{"duplicate", "owner: one\nowner: two\n", "duplicate setting"},
		{"multi-document", "owner: first\n---\nowner: second\n", "one YAML document"},
		{"trailing-empty-document", "owner: first\n---\n", "one YAML document"},
		{"bad-second-document", "owner: first\n---\nowner: [\n", "line"},
		{"sequence", "- owner\n", "mapping"},
		{"null", "null\n", "mapping"},
		{"empty-document", "---\n", "mapping"},
		{"numeric-owner", "owner: 123\n", "YAML string"},
		{"null-owner", "owner:\n", "YAML string"},
		{"scalar-topic", "topic: go\n", "sequence of strings"},
		{"bad-topic-item", "topic: [go, 2]\n", "sequence of strings"},
		{"string-sleep", "sleep: '1'\n", "YAML integer"},
		{"fraction-sleep", "sleep: 0.1\n", "YAML integer"},
		{"overflow-sleep", "sleep: 99999999999999999999999999999999999999999\n", "YAML integer"},
		{"integer-overflow-sleep", "sleep: 18446744073709551615\n", "must be an integer"},
		{"string-verbose", "verbose: 'false'\n", "YAML boolean"},
		{"legacy-verbose", "verbose: yes\n", "YAML boolean"},
		{"string-dry-run", "dry-run: 'false'\n", "YAML boolean"},
		{"integer-dry-run", "dry-run: 0\n", "YAML boolean"},
		{"string-max-repos", "max-repos: '1'\n", "YAML integer"},
		{"fraction-max-repos", "max-repos: 1.5\n", "YAML integer"},
		{"overflow-max-repos", "max-repos: 18446744073709551615\n", "must be an integer"},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			_, err := decodeYAML(tt.text)
			if err == nil || !strings.Contains(err.Error(), tt.message) {
				t.Fatalf("got %v, expected %q", err, tt.message)
			}
		})
	}
}

func TestConfigurationErrors(t *testing.T) {
	cwd, global, values := fixture(t)
	file := filepath.Join(global, "get-repos.yml")
	write(t, file, "unknown: bad\n")
	if _, err := Load(cwd, env(values), nil); err == nil || !strings.Contains(err.Error(), file) {
		t.Fatalf("missing file provenance: %v", err)
	}
	write(t, file, "archived: invalid\n")
	if _, err := Load(cwd, env(values), nil); err == nil || !strings.Contains(err.Error(), file) {
		t.Fatalf("missing validation provenance: %v", err)
	}
	write(t, file, "{}\n")
	if err := os.Mkdir(filepath.Join(cwd, ".gh-get-repos.yml"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(cwd, env(values), nil); err == nil || !strings.Contains(err.Error(), "read configuration") {
		t.Fatalf("expected file read error: %v", err)
	}
	if _, err := Load("", env(values), nil); err == nil || !strings.Contains(err.Error(), "invocation directory") {
		t.Fatalf("empty directory: %v", err)
	}
	values["GH_CONFIG_DIR"] = "~other/config"
	if _, err := Load(cwd, env(values), nil); err == nil || !strings.Contains(err.Error(), "configuration directory") {
		t.Fatalf("global path error: %v", err)
	}
}

func TestUnknownOverride(t *testing.T) {
	cwd, _, values := fixture(t)
	_, err := Load(cwd, env(values), map[string]string{"config": "another-file"})
	if err == nil || !strings.Contains(err.Error(), "unknown command line setting") {
		t.Fatalf("got %v", err)
	}
}

func TestNilLookup(t *testing.T) {
	cwd, global, _ := fixture(t)
	t.Setenv("GH_CONFIG_DIR", global)
	for _, key := range names {
		for _, name := range environmentNames[key] {
			t.Setenv(name, "")
		}
	}
	// Explicit values ensure inherited shell settings do not affect this test.
	got, err := Load(cwd, nil, map[string]string{"owner": "test", "archived": "any", "forks": "any", "format": "json", "mode": "standard", "visibility": "any", "sleep": "0", "verbose": "false", "max-repos": "0", "dry-run": "false"})
	if err != nil || got.Owner != "test" {
		t.Fatalf("got %+v, %v", got, err)
	}
}

func TestHomeFallback(t *testing.T) {
	cwd, _, _ := fixture(t)
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	got, err := resolvePath("~/repos", cwd, env(nil))
	if err != nil || got != filepath.Join(home, "repos") {
		t.Fatalf("got %q, %v", got, err)
	}
	if runtime.GOOS == "windows" {
		t.Setenv("USERPROFILE", "")
	} else {
		t.Setenv("HOME", "")
	}
	if _, err := resolvePath("~", cwd, env(nil)); err == nil || !strings.Contains(err.Error(), "home directory") {
		t.Fatalf("expected missing home error: %v", err)
	}
	if _, err := Load(cwd, env(nil), nil); err == nil || !strings.Contains(err.Error(), "home directory") {
		t.Fatalf("expected global directory home error: %v", err)
	}
}

func TestParseTopics(t *testing.T) {
	cases := []struct {
		input string
		want  []string
	}{
		{"", nil}, {"  ", nil}, {",,", nil},
		{"Go, backend , ,CLI", []string{"Go", "backend", "CLI"}},
		{`"comma,topic",cli`, []string{"comma,topic", "cli"}},
	}
	for _, tt := range cases {
		got, err := ParseTopics(tt.input)
		if err != nil || !reflect.DeepEqual(got, tt.want) {
			t.Errorf("%q: got %v, %v, want %v", tt.input, got, err, tt.want)
		}
	}
	for _, input := range []string{`"bad`, "go\ncli", "go\ncli,backend", "go\n\"bad"} {
		if _, err := ParseTopics(input); err == nil {
			t.Errorf("expected error for %q", input)
		}
	}
}
