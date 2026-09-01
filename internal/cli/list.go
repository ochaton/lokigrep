package cli

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/ochaton/lokigrep/internal/config"
	"github.com/ochaton/lokigrep/internal/logql"
	"github.com/ochaton/lokigrep/internal/loki"
	"github.com/ochaton/lokigrep/internal/timerange"
)

func listValues(ctx context.Context, c *loki.Client, o options, cfg *config.Config) (int, error) {
	from, to := listRange(o)
	scope, err := outerScope(o, cfg.Schema)
	if err != nil {
		return 2, err
	}
	values, err := c.LabelValues(ctx, o.values, from, to, scope)
	if err != nil {
		return 2, err
	}
	if len(values) == 0 {
		return 1, nil
	}
	sort.Strings(values)
	for _, v := range values {
		fprintf(os.Stdout, "%s\n", v)
	}
	return 0, nil
}

// listPrimary answers -l: every value of the primary label that has logs in
// the range. On a terminal, and when the outer label is neither scoped nor the
// primary one itself, each value also gets the list of outer values it lives
// in; piped output stays one bare name per line.
func listPrimary(ctx context.Context, c *loki.Client, o options, cfg *config.Config) (int, error) {
	sc := cfg.Schema
	primary := sc.Name(sc.Primary())
	from, to := listRange(o)
	scope, err := outerScope(o, sc)
	if err != nil {
		return 2, err
	}

	if scope != "" || sc.Primary() == 0 || !stdoutIsTTY() {
		values, err := c.LabelValues(ctx, primary, from, to, scope)
		if err != nil {
			return 2, err
		}
		if len(values) == 0 {
			return 1, nil
		}
		sort.Strings(values)
		for _, v := range values {
			fprintf(os.Stdout, "%s\n", v)
		}
		return 0, nil
	}

	outer := sc.Name(0)
	outers, err := c.LabelValues(ctx, outer, from, to, "")
	if err != nil {
		return 2, err
	}
	sort.Strings(outers)

	in := map[string][]string{}
	for _, v := range outers {
		names, err := c.LabelValues(ctx, primary, from, to, sc.ScopeSelector(v))
		if err != nil {
			return 2, err
		}
		for _, n := range names {
			in[n] = append(in[n], v)
		}
	}
	// values whose streams carry no outer label at all
	all, err := c.LabelValues(ctx, primary, from, to, "")
	if err != nil {
		return 2, err
	}
	for _, n := range all {
		if _, ok := in[n]; !ok {
			in[n] = nil
		}
	}
	if len(in) == 0 {
		return 1, nil
	}

	names := make([]string, 0, len(in))
	width := 0
	for n := range in {
		names = append(names, n)
		width = max(width, len(n))
	}
	sort.Strings(names)
	for _, n := range names {
		line := fmt.Sprintf("%-*s  %s", width, n, strings.Join(in[n], " "))
		fprintf(os.Stdout, "%s\n", strings.TrimRight(line, " "))
	}
	return 0, nil
}

// listRange is the window the label endpoints are asked about. Label queries
// are cheap, so the default is wider than a log query's.
func listRange(o options) (from, to time.Time) {
	now := time.Now()
	from, to = now.Add(-24*time.Hour), now
	if o.timeRange != "" {
		if f, t, err := timerange.Parse(o.timeRange, now); err == nil {
			from, to = f, t
		}
	}
	return from, to
}

// outerScope turns -c and the outermost part of -s into a stream selector for
// the label endpoints. An empty string means "do not narrow".
func outerScope(o options, sc *logql.Schema) (string, error) {
	spec := o.selector
	if spec == "" {
		if o.scope == "" {
			return "", nil
		}
		spec = "*"
	}
	sels, err := logql.ParseSelectors(spec, sc, o.scope)
	if err != nil {
		return "", err
	}
	values := logql.LevelValues(sels, 0)
	for _, v := range values {
		if v == "" {
			return "", nil // one unconstrained selector opens up everything
		}
	}
	return sc.ScopeSelector(values...), nil
}
