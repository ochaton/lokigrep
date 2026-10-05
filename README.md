# lokigrep

grep for [Loki](https://grafana.com/oss/loki/) logs, in the shape of the old
`logrep` ([what that was, and what carried over](logrep.md)):

```
lokigrep -s {cluster}.{namespace}.{pod} -t {from}/{to} [flags] "{pattern}"
```

Output is always oldest line first, and never silently truncated — the tool
paginates past the server's 5000 entry response cap on its own.

The label hierarchy `-s` addresses is configuration, not a hard-coded
assumption: `cluster.namespace.pod` is only the default. Point it at `job` and
`instance`, or at a single `service_name`, and the selectors, the help text and
`-l` follow.

## Install

```sh
go install github.com/ochaton/lokigrep/cmd/lokigrep@latest
```

## Configure

lokigrep ships with no endpoint, so the first run needs
`~/.config/lokigrep/config.yaml` (override the path with `LOKIGREP_CONFIG`, the
URL alone with `LOKIGREP_URL`). Running it without one prints this file,
comments and all, ready to paste:

```yaml
# Where Loki listens. Required; there is no built in default.
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
```

Unknown keys are an error, not a shrug: a misspelled `aliaes` is reported with
the line it is on rather than silently ignored.

`labels` is the selector hierarchy, widest scope first, and each level maps to
one Loki stream label:

| field | meaning |
|---|---|
| `name` | the stream label to match on |
| `aliases` | shorthands you may type instead of a value, case insensitive |
| `match` | regex that recognises a token as belonging to this level |
| `primary` | the level a one-token selector addresses, and the one `-l` lists (default: the first label) |

`aliases` and `match` are what let `prod.web` be read as *cluster* `prod`,
*namespace* `web`, while `web.api-0` is read as *namespace* `web`, *pod*
`api-0`: tokens start at the primary level and shift left when the first one is
recognised as a wider scope. Extra tokens fold into the innermost level, so pod
names may contain dots.

Omitting `labels` gives the Kubernetes default —
`cluster` / `namespace` (primary) / `pod`, with no aliases. A flat setup is
just as valid:

```yaml
url: http://loki.example:3100
labels:
  - name: service_name
```

## Examples

```sh
lokigrep -s web -t '-2h/-1h' "END="              # 2h ago .. 1h ago
lokigrep -s web -t '10m/' END=500                # last 10 minutes
lokigrep -s web -t '5m/' -f END=500              # backfill 5m, then tail
lokigrep -s web,ingress -t '5m/' END=500
lokigrep -s prod.web -t '1h/' -P 'END=(50[03])'
lokigrep -s 'eu-prod.*' -t '5m/' panic    # whole cluster
lokigrep -s prod.web -t '1h/' --tail 20 END=500
lokigrep -s prod.web -t '2 days ago/+10m' reqid= # quote the spaces
lokigrep -s prod.web -t '30m/' -o json END= | jq -r .line
lokigrep -l                                      # every namespace with logs
lokigrep -l -c prod                              # only that cluster
lokigrep --values pod -s prod.web                # what else can I query?
lokigrep -s prod.batch.etl-run-1 -t '1h/'        # that pod, or Job pods of that name
```

## Selector `-s`

Comma separated, one dotted spec per selector. Missing parts mean "any".

| selector | meaning |
|---|---|
| `web` | namespace, any cluster, any pod |
| `prod.web` | cluster alias `prod` |
| `dev.web.api-*` | pod glob |
| `eu-prod.*` | every namespace and pod of that cluster |
| `web,ingress` | two namespaces |
| `prod.~web-(app\|ml)` | `~` passes a raw regex through |
| `prod.batch.etl-1` | pod `etl-1` and pods of Job `etl-1` |

`-c` sets the outermost level for every selector at once.

An exact value on `pod` matches that pod and, if a Kubernetes Job has the
same name, that Job's pods (`{name}-{5 alphanumeric}`, or `{name}-{n}` for
indexed completions). If both exist, both streams are printed. Globs and `~`
regexes are not rewritten. Loki's `job` label is the scrape target, not the
Job object — do not use it for this.

Selectors that form a full product over the levels collapse into a single LogQL
query; anything else runs one query per selector and the results are merged in
timestamp order.

## Time range `-t`

`from/to`; `to` defaults to now. A bare duration means "ago", regardless of sign.

| form | meaning |
|---|---|
| `-2h/-1h`, `2h/1h` | two hours ago .. one hour ago |
| `10m/`, `5m` | last ten / five minutes |
| `1h30m/`, `7d/`, `1w/` | `d` and `w` are supported on top of Go durations |
| `midnight/`, `today/`, `yesterday/midnight` | local day boundaries |
| `2026-07-10T12:00:00/` | absolute, local time unless the string carries a zone |
| `2026-07-10/2026-07-11` | whole day |
| `2026-07-10/-2h` | absolute start, relative end |
| `1h/+15m` | `+` on the right side is relative to the range start |
| `2 days ago/+10m` | ten minutes, starting two days ago |
| `last 30 minutes/`, `past 2 hours/` | same as `30m/`, `2h/` |
| `1 hour 30 min ago/` | phrases may mix units |
| `1 month ago/`, `2 years ago/` | calendar steps, not fixed hours |
| `yesterday 14:00/+1h` | day keyword plus a clock time |
| `08:00/09:00` | today at those times |
| `1783634400/` | unix seconds (also ms/µs/ns) |

## Patterns

Positional arguments are line filters, ANDed together.

- `-F` plain substring (default) → `|= "pat"`
- `-P` regular expression → `|~ "pat"`
- `-i` case insensitive, `-v` invert the match

A `-P` pattern with more than 50 alternatives gets a warning: Loki answers such
queries with an empty body instead of an error. Use a broad `-F` filter and
grep the output instead.

## Flags

| flag | default | meaning |
|---|---|---|
| `-s` | — | selector |
| `-t` | `1h` | time range |
| `-c` | — | override the outermost label |
| `-f` | off | follow (poll for new lines) |
| `-F` / `-P` | `-F` | substring / regex pattern |
| `-i` / `-v` | off | ignore case / invert match |
| `-n` | 0 | stop after N lines |
| `--tail` | 0 | only the last N lines of the range |
| `-o` | `text` | `text`, `raw`, `json` |
| `--labels`, `--no-labels` | auto | which stream labels to print |
| `--no-time` | off | drop the timestamp column |
| `--dry-run` | off | print the LogQL and the resolved range, then exit |
| `-Q` | — | raw LogQL, bypasses `-s` and the patterns |
| `-l` | off | list the primary label's values and exit, scoped by `-c` / `-s` |
| `--values` | — | list values of a label and exit, scoped by `-c` / `-s` |
| `--verbose` | off | log every HTTP request |

Every flag describes one search. Everything that describes the installation —
the URL, `timeout`, `follow_interval`, `follow_delay`, `color`, `progress`,
`utc` — is a config key instead, so there is no second place to look and no
flag to retype. `LOKIGREP_URL` still overrides the URL for one command, and
`LOKIGREP_CONFIG` swaps the whole file:

```sh
LOKIGREP_URL=http://other-loki:3100 lokigrep -s web -t 5m ERROR
```

Exit codes follow grep: `0` lines found, `1` none, `2` error.

## Progress

A wide range over a quiet namespace can take many requests before the first
match shows up. While that scan runs, and only while stderr is a terminal,
lokigrep draws a progress bar there:

```
⠹ scanning 14:26:56 ███████████░░░░░░░░░  29%  12.4k lines · 37 req · 4.1s · ~9s left
```

The percentage is how much of the time range is behind the scan, not how many
lines matched. It disappears the moment the first matching line reaches stdout,
so it never mixes into the output — piping or redirecting stdout keeps working
unchanged, and `2>/dev/null` (or `progress: never`) turns it off. With `-f` and
no match yet, it turns into a `following since …` spinner. `--verbose` also
disables it, since the request log wants stderr for itself.

## Discovery

`-l` lists the values of the primary label that have logs in the range (`-t`,
default the last 24h). On a terminal it also shows which outer-label values
each one lives in; piped, it prints bare names so it feeds back into `-s`:

```
$ lokigrep -l
alpha               eu-dev eu-prod
beta                eu-prod
web                 eu-dev eu-prod

$ lokigrep -s "$(lokigrep -l -c prod | fzf)" -t '10m/' ERROR
```

`--values <label>` does the same for any label — including ones outside the
selector hierarchy, such as `container`, `app` or `level` — and honours the
scope in `-c` / `-s` too.

## How it fetches

The server caps every response at `max_entries_limit_per_query` (5000 by
default) and that truncation is **not** a fair merge across streams: a full
page can omit lines that are older than the newest line it returned.
Paginating on the last timestamp therefore loses data — measured at 85 missing
lines out of ~18k on a 30 minute window.

lokigrep instead walks the range in time windows and shrinks any window that
comes back at the cap, so every window it accepts is provably complete. Window
size adapts to the line rate, targeting ~4000 entries per request (60k entries
took 19 requests in the test suite). Ranges longer than 30d are split to stay
under `max_query_length`.

`-f` polls `query_range` on an interval with a 5s overlap instead of using the
websocket tail endpoint: no extra dependency, the same lossless fetch path, and
lines that reach Loki late are still picked up. Those late lines are the reason
for `follow_delay`: entries are held back briefly so the printed stream stays
in timestamp order.
