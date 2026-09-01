package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/ochaton/lokigrep/internal/config"
	"github.com/ochaton/lokigrep/internal/logql"
	"github.com/ochaton/lokigrep/internal/loki"
	"github.com/ochaton/lokigrep/internal/output"
	"github.com/ochaton/lokigrep/internal/progress"
	"github.com/ochaton/lokigrep/internal/timerange"
)

// options is one search: what to look for, over what range, shaped how for
// this pipe. Anything that is a property of the installation instead lives in
// the config file, so the flag set stays short enough to read.
type options struct {
	selector  string
	scope     string
	timeRange string
	follow    bool
	fixed     bool
	regex     bool
	ignore    bool
	invert    bool
	limit     int
	tail      int
	format    string
	labels    string
	noLabels  bool
	noTime    bool
	dryRun    bool
	rawQuery  string
	values    string
	list      bool
	verbose   bool
}

// Run is the whole command line. It returns the process exit code: 0 for at
// least one matching line, 1 for none, 2 for an error.
func Run(args []string) (int, error) {
	cfg, err := config.Load()
	if err != nil {
		return 2, err
	}

	var o options
	fs := flag.NewFlagSet("lokigrep", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	sc := cfg.Schema
	outer := sc.Name(0)
	primary := sc.Name(sc.Primary())

	fs.Usage = func() {
		fprintf(os.Stderr, "%s", usage(cfg))
		fs.PrintDefaults()
	}

	fs.StringVar(&o.selector, "s", "", "selector: "+sc.Spec()+", comma separated")
	fs.StringVar(&o.scope, "c", "", "override the "+outer+" of every selector")
	fs.StringVar(&o.timeRange, "t", cfg.Range, "time range: from/to")
	fs.BoolVar(&o.follow, "f", false, "follow: keep printing new lines")
	fs.BoolVar(&o.fixed, "F", false, "pattern is a plain substring (default)")
	fs.BoolVar(&o.regex, "P", false, "pattern is a regular expression")
	fs.BoolVar(&o.ignore, "i", false, "case insensitive pattern")
	fs.BoolVar(&o.invert, "v", false, "invert: keep lines that do not match")
	fs.IntVar(&o.limit, "n", cfg.Limit, "stop after N lines (0 = unlimited)")
	fs.IntVar(&o.tail, "tail", 0, "print only the last N lines of the range")
	fs.StringVar(&o.format, "o", "text", "output format: text, raw, json")
	fs.StringVar(&o.labels, "labels", "", "stream labels to print, comma separated (default: whatever the selector leaves open)")
	fs.BoolVar(&o.noLabels, "no-labels", false, "never print stream labels")
	fs.BoolVar(&o.noTime, "no-time", false, "do not print timestamps")
	fs.BoolVar(&o.dryRun, "dry-run", false, "print the LogQL query and resolved range, then exit")
	fs.StringVar(&o.rawQuery, "Q", "", "raw LogQL query, bypasses -s and the patterns")
	fs.BoolVar(&o.list, "l", false, "list the "+primary+" values that have logs, then exit (scoped by -c and -s)")
	fs.StringVar(&o.values, "values", "", "list the values of a label ("+strings.Join(sc.Names(), ", ")+", …) and exit")
	fs.BoolVar(&o.verbose, "verbose", false, "log every HTTP request to stderr")

	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0, nil
		}
		return 2, nil
	}

	if cfg.URL == "" {
		return 2, cfg.ErrNoURL()
	}

	client := loki.New(cfg.URL, cfg.Timeout)
	if o.verbose {
		client.Debug = func(format string, a ...any) { fprintf(os.Stderr, format, a...) }
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if o.list {
		return listPrimary(ctx, client, o, cfg)
	}
	if o.values != "" {
		return listValues(ctx, client, o, cfg)
	}

	now := time.Now()
	from, to, err := timerange.Parse(o.timeRange, now)
	if err != nil {
		return 2, err
	}
	if o.follow {
		// the follower owns everything newer than this
		to = now.Add(-cfg.Delay)
		if !to.After(from) {
			from = to.Add(-time.Second)
		}
	}

	queries, selectors, err := buildQueries(&o, cfg, fs.Args())
	if err != nil {
		return 2, err
	}

	if o.dryRun {
		fprintf(os.Stdout, "from:  %s\nto:    %s\n", from.Format(time.RFC3339Nano), to.Format(time.RFC3339Nano))
		fprintf(os.Stdout, "start: %d\nend:   %d\n", from.UnixNano(), to.UnixNano())
		for _, q := range queries {
			fprintf(os.Stdout, "query: %s\n", q)
		}
		return 0, nil
	}

	printer := output.New(os.Stdout, o.format, useColor(cfg.Color), cfg.UTC, !o.noTime, labelsToPrint(&o, sc, selectors))
	defer printer.Flush()

	// The progress bar owns stderr until the first matching line shows up, so
	// it can never interleave with the output on stdout.
	bar := startScanProgress(cfg, &o, from, to)
	defer bar.Stop()
	client.OnScan = bar.Update
	client.OnWarn = bar.Println

	found := 0
	seen := map[string]int64{}
	overlap := 5 * time.Second
	emit := func(e loki.Entry) error {
		if found == 0 {
			bar.Stop()
		}
		found++
		if o.follow && e.NS >= to.Add(-overlap).UnixNano() {
			seen[e.Key()] = e.NS
		}
		return printer.Print(e)
	}

	if o.tail > 0 {
		entries, err := client.Tail(ctx, queries, from, to, o.tail)
		if err != nil {
			bar.Stop()
			return 2, err
		}
		for i := range entries {
			if err := emit(entries[i]); err != nil {
				return 2, err
			}
		}
	} else if err := client.Stream(ctx, queries, from, to, o.limit, emit); err != nil {
		bar.Stop()
		if ctx.Err() != nil {
			return interruptedCode(found), nil
		}
		return 2, hintFor(err, o)
	}
	printer.Flush()

	if o.follow {
		if found == 0 {
			bar.Following(to)
		}
		f := &loki.Follower{
			Client:   client,
			Queries:  queries,
			Interval: cfg.Interval,
			Overlap:  overlap,
			Delay:    cfg.Delay,
			Emit:     emit,
			OnCycle:  func() { _ = printer.Flush() },
		}
		if err := f.Run(ctx, to, seen); err != nil {
			bar.Stop()
			return 2, hintFor(err, o)
		}
		printer.Flush()
		return interruptedCode(found), nil
	}

	bar.Stop()
	if found == 0 {
		return 1, nil
	}
	return 0, nil
}

// startScanProgress returns nil (a no-op indicator) whenever the bar would get
// in the way: progress never, --verbose, or a machine on the other end.
func startScanProgress(cfg *config.Config, o *options, from, to time.Time) *progress.Progress {
	if cfg.Progress == "never" || o.verbose {
		return nil
	}
	label := "scanning"
	if o.tail > 0 {
		label = "scanning back from"
	}
	return progress.Start(from, to, label)
}

func interruptedCode(found int) int {
	if found == 0 {
		return 1
	}
	return 0
}

func buildQueries(o *options, cfg *config.Config, patterns []string) ([]string, []logql.Selector, error) {
	if o.rawQuery != "" {
		return []string{o.rawQuery}, nil, nil
	}
	if o.selector == "" {
		return nil, nil, fmt.Errorf("missing -s selector (try -h)")
	}

	selectors, err := logql.ParseSelectors(o.selector, cfg.Schema, o.scope)
	if err != nil {
		return nil, nil, err
	}

	mode := logql.FilterFixed
	if o.regex {
		mode = logql.FilterRegex
	}
	if o.regex && o.fixed {
		return nil, nil, fmt.Errorf("-F and -P are mutually exclusive")
	}

	filters := make([]logql.Filter, 0, len(patterns))
	for _, p := range patterns {
		if p == "" {
			continue
		}
		filters = append(filters, logql.Filter{Pattern: p, Mode: mode, Invert: o.invert, Ignore: o.ignore})
	}
	if w := logql.WarnComplexRegex(filters); w != "" {
		fprintf(os.Stderr, "lokigrep: warning: %s\n", w)
	}

	return logql.BuildQueries(logql.StreamSelectors(cfg.Schema, selectors), filters), selectors, nil
}

func labelsToPrint(o *options, sc *logql.Schema, selectors []logql.Selector) []string {
	if o.noLabels {
		return nil
	}
	if o.labels != "" {
		return strings.Split(o.labels, ",")
	}
	names := sc.Names()
	if len(selectors) == 0 {
		// a raw -Q query constrains nothing we can reason about
		return names[sc.Primary():]
	}
	var out []string
	for i, ambiguous := range logql.Ambiguous(sc, selectors) {
		if ambiguous {
			out = append(out, names[i])
		}
	}
	return out
}

func useColor(mode string) bool {
	switch mode {
	case "always":
		return true
	case "never":
		return false
	}
	if os.Getenv("NO_COLOR") != "" {
		return false
	}
	return stdoutIsTTY()
}

func stdoutIsTTY() bool {
	st, err := os.Stdout.Stat()
	if err != nil {
		return false
	}
	return st.Mode()&os.ModeCharDevice != 0
}

func hintFor(err error, o options) error {
	msg := err.Error()
	if strings.Contains(msg, "empty body") && !o.regex {
		return err
	}
	if strings.Contains(msg, "max entries limit") {
		return fmt.Errorf("%w\n(lokigrep paginates for you — this usually means the server limit changed)", err)
	}
	return err
}

func fprintf(w io.Writer, format string, args ...any) {
	_, _ = fmt.Fprintf(w, format, args...)
}
