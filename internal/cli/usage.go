package cli

import (
	"strings"

	"github.com/ochaton/lokigrep/internal/config"
)

const usageTail = `time range (-t), "from/to", "to" defaults to now:
  -2h/-1h               two hours ago .. one hour ago
  10m/                  last ten minutes
  5m                    same as 5m/
  midnight/             since local midnight
  yesterday/midnight
  2026-07-10T12:00:00/
  2026-07-10/2026-07-11
  2026-07-10/-2h
  1h/+15m               one hour ago .. 45 minutes ago
  '2 days ago/+10m'     ten minutes, starting two days ago
  'last 30 minutes/'    same as 30m/
  '1 hour 30 min ago/'  phrases may mix units
  'yesterday 14:00/+1h' a day keyword plus a clock time

patterns are plain substrings (-F, the default) or regexes (-P), and several
patterns are ANDed together. Output is always oldest line first.

while the scan runs, a progress bar is drawn on stderr; it goes away as soon as
the first matching line is printed ("progress: never" in the config turns it
off).
`

// usage renders the help against the configured label hierarchy, so the
// selector examples always name the labels this installation actually has.
func usage(cfg *config.Config) string {
	sc := cfg.Schema
	var b strings.Builder
	b.WriteString("lokigrep — grep Loki logs from the command line\n\nusage:\n  lokigrep -s ")
	b.WriteString(sc.Spec())
	b.WriteString(" -t {from}/{to} [flags] [pattern ...]\n\nselector (-s), levels are ")
	b.WriteString(sc.Spec())
	b.WriteString(", widest first:\n")

	primary := sc.Sample(sc.Primary())
	example := func(spec, meaning string) { fprintf(&b, "  %-28s %s\n", spec, meaning) }
	example(primary, "a bare token is a "+sc.Name(sc.Primary()))
	for n := 2; n <= sc.Len(); n++ {
		parts := make([]string, n)
		for i := range n {
			parts[i] = sc.Sample(i)
		}
		meaning := "one token per level"
		if n == 2 && sc.Primary() > 0 {
			meaning = "an alias or a match pattern makes the tokens shift left"
			if !strings.HasPrefix(parts[0], "<") {
				meaning = parts[0] + " names a " + sc.Name(0) + ", so the tokens shift left"
			}
		}
		example(strings.Join(parts, "."), meaning)
	}
	if sc.Len() > 1 {
		example(sc.Sample(0)+".*", "every "+strings.Join(sc.Names()[1:], " and ")+" in that "+sc.Name(0))
	}
	b.WriteString("comma separated for several at once; * and ? are globs, ~ is a raw regex.\n\n")

	b.WriteString(usageTail)

	scoped := primary
	if sc.Len() > 1 && sc.Primary() > 0 {
		scoped = sc.Sample(0) + "." + primary
	}
	fprintf(&b, `
examples:
  lokigrep -s %[1]s -t '-2h/-1h' "END="
  lokigrep -s %[1]s -t '5m/' -f END=500
  lokigrep -s %[2]s -t '1h/' -P 'END=(500|503)'
  lokigrep -s %[2]s -t '2 days ago/+10m' reqid=
  lokigrep -l                     every %[3]s that has logs
  lokigrep --values %[4]s -s %[2]s
`, primary, scoped, sc.Name(sc.Primary()), sc.Name(sc.Len()-1))
	fprintf(&b, "\nthe Loki URL and the other settings that are not per search — timeouts,\ncolor, the progress bar, UTC timestamps — live in %s\n", cfg.FilePath())
	b.WriteString("\nflags:\n")
	return b.String()
}
