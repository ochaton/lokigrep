# logrep, the ancestor

lokigrep copies its command line from `logrep`, a C++ tool that grepped log
*files* across a fleet by merging them on their timestamp field. The scopes,
`-t from/to`, the grep-style exit codes and the "just give me the lines, oldest
first" output all come from there. This file records what was inherited, what
was dropped, and why — it is a design reference, not documentation of anything
lokigrep ships.

## What carried over

| logrep | lokigrep | note |
|---|---|---|
| `-s`, scopes from a config file | `-s`, selector over configured labels | scopes were file globs; selectors are Loki stream labels, but both are "a name you configure once and then type" |
| `-t '4d/2 minutes ago'` | same syntax | `internal/timerange` reimplements the durations, the `ago`/`last`/`past` phrases, `+` offsets, `midnight`/`yesterday`, ISO dates and unix timestamps — but not weekday names (`last monday`) or RFC1123 dates (`Mon, 07 Aug 2006 12:34:56`) |
| `-l`, list scopes | `-l`, list the primary label's values | in Loki the answer comes from the label endpoints, so it also reflects what actually has logs in the range |
| `-P`, PCRE | `-P`, regex | pushed down into LogQL as `\|~` rather than run locally |
| `--fgrep` | `-F` (the default) | pushed down as `\|=` |
| `-f`, tail | `-f`, follow | polls `query_range` instead of following a file |
| `--nocolor` | `color: never` | plus `NO_COLOR` and TTY detection |
| merge on the time field | merge on the entry timestamp | the reason both tools can promise oldest-line-first across sources |

## What was dropped

- `-c/--config`: the scope table lived in `/etc/logrep.conf` because log files
  needed a mask, a time field index and a time format per scope. Loki streams
  already carry labels and RFC3339 nanosecond timestamps, so lokigrep's config
  only needs the label hierarchy. Note the flag letter was reused: lokigrep's
  `-c` overrides the outermost label.
- `-H/--with-filename`: replaced by stream labels, which `--labels` selects and
  `--no-labels` suppresses.
- `-y/--test`: self-tests belong in `go test`.
- `--parallel` and `--god-mode`: reading a fleet of files in parallel was a
  local I/O concern with a footgun attached. Concurrency here is the server's
  problem; lokigrep's own pacing is the adaptive window in `internal/loki`.
- `-v/--verbose` became `--verbose`, freeing `-v` for grep's invert-match.

## The original help, verbatim

```
Usage: logrep [options] [string_to_search]
Options:
  -h [ --help ]                           produce help message
  -c [ --config ] arg (=/etc/logrep.conf) Config file. Config file format:
                                          SCOPE_NAME MASK TIME_FIELD TIME_FIELD_TYPE
                                          SCOPE_NAME       - name of scope
                                          MASK             - log files mask. Examples: /path/to/*dir*/*, /root/*/*/*, /path/*dir1*/*dir2*/*dir3*
                                          TIME_FIELD       - number of time field used for merging (0 based)
                                          TIME_FIELD_TYPE  - SEC|MSEC|FULL_TIME, where SEC - UNIX timestamp, MSEC - SEC*1000, FULL_TIME - %y.%m.%d %H:%M:%S

  -s [ --scopes ] arg                     List of scopes to search within (comma separated). Exaples:  --scopes z,l. Empty value means searching everywhere
  -t [ --time ] arg (=5 minutes ago/)     Time period to search. Examples:
                                          '1h'                          - exactly one hour ago
                                          '00:00/12m'                   - from today's 00:00 till 12 minutes ago
                                          '4d/2 minutes ago'            - from 4 days ago till 2 minutes ago
                                          '1h/+1m'                      - from 1 hour ago till 59 minutes ago
                                          '03:30/+2h'                   - from today's 3:30 till today's 5:30
                                          'day ago/12 hours ago'        - from one day ago to 12 hours ago
                                          '1416434697'                  - exactly 2014.11.19 22:04:57 GMT. This is a UNIX timestamp
                                          '14-11-19 22:04:57/'          - from 2014-11-19 22:04:57 till now
                                          '/1416434697000'              - everything till 2014-11-19 22:04:57. UNIX timestamp in millis
                                          '1416434697000/1 hour ago'    - from 2014-11-19 22:04:57 till 1 hour ago
                                          '40 minutes ago/35 min ago'   - from 40 minutes ago till 35 minutes ago
                                          'last monday/yesterday'       - from last monday 00:00:00 till 24 hours ago
                                          'last week/Mon, 07 Aug 2006 12:34:56'       - from week ago till Mon, 07 Aug 2006 12:34:56

  -H [ --with-filename ]                  Also print file name
  -v [ --verbose ]                        Verbose output
  -l [ --list-scopes ]                    List all available scopes
  -y [ --test ]                           Run self tests
  -P [ --pcre ]                           Use pcre regexp
  -f [ --tail ]                           Causes logrep to not stop when end of file is reached, but rather to wait for additional data to be appended to the
                                          input
  --fgrep arg                             Substring to search
  --parallel                              Use parallel logs iterator
  --nocolor                               Do not use colors in output
  --god-mode                              Allow use parallel grep with no limitations
```
