package config

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/goccy/go-yaml"

	"github.com/ochaton/lokigrep/internal/logql"
)

// Config holds everything that is a property of the installation rather than
// of a single search: the Loki endpoint, the label hierarchy that selectors
// address, and the settings that have no business being retyped on every
// invocation. The binary ships with no endpoint of its own, so a URL has to
// come from the config file or LOKIGREP_URL.
type Config struct {
	URL    string        `yaml:"url"`
	Labels []logql.Label `yaml:"labels"`
	Range  string        `yaml:"range"`
	Limit  int           `yaml:"limit"`
	// Durations are written the way a human writes them: 30s, 1m, 2h30m.
	// A bare number is rejected rather than guessed at.
	Timeout  time.Duration `yaml:"timeout"`
	Interval time.Duration `yaml:"follow_interval"`
	Delay    time.Duration `yaml:"follow_delay"`
	Color    string        `yaml:"color"`
	Progress string        `yaml:"progress"`
	UTC      bool          `yaml:"utc"`

	Schema *logql.Schema `yaml:"-"`
	Path   string        `yaml:"-"`
}

// enums are the keys whose value is one of a fixed set. A typo in a hand
// written file has to be reported, not rounded down to the default.
var enums = map[string][]string{
	"color":    {"auto", "always", "never"},
	"progress": {"auto", "never"},
}

// defaultLabels is the Kubernetes shape that promtail and grafana-agent
// produce. The values are site specific, so no aliases are built in.
func defaultLabels() []logql.Label {
	return []logql.Label{
		{Name: "cluster"},
		{Name: "namespace", Primary: true},
		{Name: "pod"},
	}
}

// configPath follows the XDG basedir spec rather than os.UserConfigDir, which
// on macOS points at ~/Library/Application Support — not where anyone keeps
// the dotfiles of a command line tool.
func configPath() string {
	if p := os.Getenv("LOKIGREP_CONFIG"); p != "" {
		return p
	}
	dir := os.Getenv("XDG_CONFIG_HOME")
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return ""
		}
		dir = filepath.Join(home, ".config")
	}
	return filepath.Join(dir, "lokigrep", "config.yaml")
}

// Load merges defaults, the config file and environment overrides.
func Load() (*Config, error) {
	cfg := &Config{
		Labels:   defaultLabels(),
		Range:    "1h",
		Timeout:  60 * time.Second,
		Interval: time.Second,
		Delay:    2 * time.Second,
		Color:    "auto",
		Progress: "auto",
		Path:     configPath(),
	}

	if cfg.Path != "" {
		data, err := os.ReadFile(cfg.Path)
		switch {
		case err == nil:
			var fromFile Config
			// Strict: a misspelled key in a hand written file is a mistake to
			// report, not a line to ignore. goccy points at the offending line.
			if err := yaml.UnmarshalWithOptions(data, &fromFile, yaml.Strict()); err != nil {
				return nil, fmt.Errorf("%s:\n%s", cfg.Path, indent(yaml.FormatError(err, false, true)))
			}
			if fromFile.URL != "" {
				cfg.URL = fromFile.URL
			}
			if len(fromFile.Labels) > 0 {
				cfg.Labels = fromFile.Labels
			}
			if fromFile.Range != "" {
				cfg.Range = fromFile.Range
			}
			if fromFile.Limit != 0 {
				cfg.Limit = fromFile.Limit
			}
			if fromFile.Timeout != 0 {
				cfg.Timeout = fromFile.Timeout
			}
			if fromFile.Interval != 0 {
				cfg.Interval = fromFile.Interval
			}
			if fromFile.Delay != 0 {
				cfg.Delay = fromFile.Delay
			}
			if fromFile.Color != "" {
				cfg.Color = fromFile.Color
			}
			if fromFile.Progress != "" {
				cfg.Progress = fromFile.Progress
			}
			cfg.UTC = fromFile.UTC

			for key, value := range map[string]string{"color": cfg.Color, "progress": cfg.Progress} {
				if !slices.Contains(enums[key], value) {
					return nil, fmt.Errorf("%s: %s: %q is not one of %s", cfg.Path, key, value, strings.Join(enums[key], ", "))
				}
			}
		case errors.Is(err, fs.ErrNotExist):
			// no config file is the normal case
		default:
			return nil, fmt.Errorf("read %s: %w", cfg.Path, err)
		}
	}

	if u := os.Getenv("LOKIGREP_URL"); u != "" {
		cfg.URL = u
	}

	schema, err := logql.NewSchema(cfg.Labels)
	if err != nil {
		if cfg.Path != "" {
			return nil, fmt.Errorf("%s: %w", cfg.Path, err)
		}
		return nil, err
	}
	cfg.Schema = schema
	return cfg, nil
}

// indent shifts a multi line YAML error under its "path:" header.
func indent(s string) string {
	s = strings.TrimRight(s, "\n")
	return "  " + strings.ReplaceAll(s, "\n", "\n  ")
}

// sampleConfig is what a first run tells the user to write. It doubles as the
// documentation of every key, which is the whole reason this file is YAML.
const sampleConfig = `# Where Loki listens. Required; there is no built in default.
url: http://loki.example:3100

# The selector hierarchy, widest scope first. "-s prod.web.api-0" reads left to
# right along this list, and each entry names one Loki stream label.
labels:
  - name: cluster
    # Shorthands you may type instead of the full value, case insensitive.
    aliases:
      prod: eu-prod
      dev: eu-dev
    # Recognises a token as a cluster even when it is not an alias. This is
    # what tells "eu-prod.web" (cluster, namespace) from "web.api-0"
    # (namespace, pod).
    match: ^eu-

  - name: namespace
    # A selector with one token means a namespace, and -l lists namespaces.
    primary: true

  - name: pod

# Settings. All optional; these live here because they are properties of the
# installation, not of a single search, so there are no flags for them.
range: 1h             # default -t
limit: 0              # default -n, 0 is unlimited
timeout: 60s          # per HTTP request
follow_interval: 1s   # -f poll interval
follow_delay: 2s      # -f holds lines back this long so late arrivals stay ordered
color: auto           # auto, always, never (NO_COLOR is honoured either way)
progress: auto        # scan progress bar on stderr: auto, never
utc: false            # print timestamps in UTC instead of local time
`

// FilePath is where the config file is, or should be written: the same string
// the help text and the first-run error point at.
func (c *Config) FilePath() string {
	if c.Path == "" {
		return "~/.config/lokigrep/config.yaml"
	}
	return c.Path
}

// ErrNoURL spells out the whole first-run setup, since without an endpoint the
// tool cannot do anything at all.
func (c *Config) ErrNoURL() error {
	return fmt.Errorf("no Loki URL configured. Write %s:\n\n%s\nor set LOKIGREP_URL", c.FilePath(), sampleConfig)
}
