package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ochaton/lokigrep/internal/logql"
)

func loadFrom(t *testing.T, body string) (*Config, error) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("LOKIGREP_CONFIG", path)
	t.Setenv("LOKIGREP_URL", "")
	return Load()
}

// The sample is what a first run tells the user to write, and it is a const
// string that nothing else compiles: this is the only thing keeping it honest.
func TestSampleConfigIsValid(t *testing.T) {
	cfg, err := loadFrom(t, sampleConfig)
	if err != nil {
		t.Fatalf("sample config does not load: %v", err)
	}
	if cfg.URL == "" {
		t.Error("sample config sets no url")
	}
	if got, want := cfg.Schema.Names(), []string{"cluster", "namespace", "pod"}; len(got) != len(want) {
		t.Fatalf("labels = %v, want %v", got, want)
	}
	if got := cfg.Schema.Name(cfg.Schema.Primary()); got != "namespace" {
		t.Errorf("primary = %q, want namespace", got)
	}
	// the aliases and the match pattern the comments promise
	sels, err := logql.ParseSelectors("prod.web", cfg.Schema, "")
	if err != nil {
		t.Fatal(err)
	}
	if got, want := sels[0][0], "eu-prod"; got != want {
		t.Errorf("alias resolved to %q, want %q", got, want)
	}
	if got, want := logql.StreamSelectors(cfg.Schema, sels)[0], `{cluster="eu-prod", namespace="web"}`; got != want {
		t.Errorf("selector = %q, want %q", got, want)
	}
}

func TestLoadDefaults(t *testing.T) {
	cfg, err := loadFrom(t, "url: http://loki.example:3100\n")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Range != "1h" {
		t.Errorf("range = %q, want 1h", cfg.Range)
	}
	if cfg.Timeout != 60*time.Second {
		t.Errorf("timeout = %s, want 1m0s", cfg.Timeout)
	}
	if cfg.Interval != time.Second {
		t.Errorf("follow_interval = %s, want 1s", cfg.Interval)
	}
	if cfg.Delay != 2*time.Second {
		t.Errorf("follow_delay = %s, want 2s", cfg.Delay)
	}
	if cfg.Color != "auto" || cfg.Progress != "auto" || cfg.UTC {
		t.Errorf("color/progress/utc = %q/%q/%v, want auto/auto/false", cfg.Color, cfg.Progress, cfg.UTC)
	}
	if got, want := cfg.Schema.Names(), 3; len(got) != want {
		t.Errorf("labels = %v, want the %d default levels", got, want)
	}
}

func TestLoadDurations(t *testing.T) {
	cfg, err := loadFrom(t, "url: http://x\ntimeout: 2m30s\nfollow_interval: 500ms\n")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Timeout != 150*time.Second {
		t.Errorf("timeout = %s, want 2m30s", cfg.Timeout)
	}
	if cfg.Interval != 500*time.Millisecond {
		t.Errorf("follow_interval = %s, want 500ms", cfg.Interval)
	}
}

// The settings that lost their flags are only reachable through the file, so
// the file is the only thing that can get them wrong.
func TestLoadSettings(t *testing.T) {
	cfg, err := loadFrom(t, "url: http://x\nfollow_delay: 5s\ncolor: never\nprogress: never\nutc: true\n")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Delay != 5*time.Second {
		t.Errorf("follow_delay = %s, want 5s", cfg.Delay)
	}
	if cfg.Color != "never" || cfg.Progress != "never" || !cfg.UTC {
		t.Errorf("color/progress/utc = %q/%q/%v, want never/never/true", cfg.Color, cfg.Progress, cfg.UTC)
	}
}

// A hand written file deserves a complaint about a misspelled key, not a
// silently ignored line.
func TestLoadRejects(t *testing.T) {
	cases := []struct{ name, body string }{
		{"unknown key", "url: http://x\nulr: http://y\n"},
		{"unknown label key", "url: http://x\nlabels:\n  - name: job\n    aliaes: {a: b}\n"},
		{"bare number duration", "url: http://x\ntimeout: 60\n"},
		{"nonsense duration", "url: http://x\ntimeout: soon\n"},
		{"bad match regex", "url: http://x\nlabels:\n  - name: job\n    match: \"(\"\n"},
		{"duplicate label", "url: http://x\nlabels:\n  - name: job\n  - name: job\n"},
		{"bad color", "url: http://x\ncolor: yes\n"},
		{"bad progress", "url: http://x\nprogress: always\n"},
		{"not a mapping", "just a string\n"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, err := loadFrom(t, c.body); err == nil {
				t.Fatalf("accepted %q", c.body)
			}
		})
	}
}

func TestLoadMissingFileIsFine(t *testing.T) {
	t.Setenv("LOKIGREP_CONFIG", filepath.Join(t.TempDir(), "absent.yaml"))
	t.Setenv("LOKIGREP_URL", "")
	cfg, err := Load()
	if err != nil {
		t.Fatalf("missing config is an error: %v", err)
	}
	if cfg.URL != "" {
		t.Errorf("url = %q, want empty", cfg.URL)
	}
	if cfg.ErrNoURL() == nil {
		t.Error("ErrNoURL returned nil")
	}
}

func TestLoadEnvURLWins(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("url: http://from-file\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("LOKIGREP_CONFIG", path)
	t.Setenv("LOKIGREP_URL", "http://from-env")
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.URL != "http://from-env" {
		t.Errorf("url = %q, want the environment override", cfg.URL)
	}
}
