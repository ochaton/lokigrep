package loki

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	// maxEntriesPerQuery mirrors the server's max_entries_limit_per_query.
	maxEntriesPerQuery = 5000
	// maxQueryWindow stays under the server's max_query_length (30d1h).
	maxQueryWindow = 29 * 24 * time.Hour
)

// Entry is a single log line with the labels of the stream it came from.
type Entry struct {
	NS     int64
	Line   string
	Labels map[string]string

	fp string // cached label fingerprint
}

func (e *Entry) Time() time.Time { return time.Unix(0, e.NS) }

func (e *Entry) fingerprint() string {
	if e.fp == "" {
		keys := make([]string, 0, len(e.Labels))
		for k := range e.Labels {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		var b strings.Builder
		for _, k := range keys {
			b.WriteString(k)
			b.WriteByte('=')
			b.WriteString(e.Labels[k])
			b.WriteByte(',')
		}
		e.fp = b.String()
	}
	return e.fp
}

func (e *Entry) Key() string {
	return strconv.FormatInt(e.NS, 10) + "\x00" + e.fingerprint() + "\x00" + e.Line
}

func sortEntries(entries []Entry) {
	sort.SliceStable(entries, func(i, j int) bool {
		if entries[i].NS != entries[j].NS {
			return entries[i].NS < entries[j].NS
		}
		if fi, fj := entries[i].fingerprint(), entries[j].fingerprint(); fi != fj {
			return fi < fj
		}
		return entries[i].Line < entries[j].Line
	})
}

// ScanStat is one progress report from a scan.
type ScanStat struct {
	Done     int64     // nanoseconds of the time range already scanned
	Total    int64     // nanoseconds the range spans
	At       time.Time // timestamp the scan has reached
	Entries  int       // log lines the server returned so far
	Requests int       // HTTP requests issued so far
}

// Client talks to the Loki HTTP API.
type Client struct {
	URL   string
	HTTP  *http.Client
	Debug func(format string, args ...any)
	// OnScan, when set, is called after every window a scan walks over.
	OnScan func(ScanStat)
	// OnWarn, when set, receives warnings raised mid-scan. It exists so the
	// progress bar can print them above itself instead of being overwritten.
	OnWarn func(string)

	reqs    int // HTTP requests issued by the current scan
	fetched int // log lines the server returned during the current scan
}

// report tells OnScan how much of [from, to) is behind us. at is the timestamp
// the scan has reached.
func (c *Client) report(done, total int64, at int64) {
	if c.OnScan == nil {
		return
	}
	c.OnScan(ScanStat{
		Done:     done,
		Total:    total,
		At:       time.Unix(0, at),
		Entries:  c.fetched,
		Requests: c.reqs,
	})
}

// New returns a client for a Loki base URL.
func New(rawURL string, timeout time.Duration) *Client {
	return &Client{
		URL:  strings.TrimRight(rawURL, "/"),
		HTTP: &http.Client{Timeout: timeout},
	}
}

func (c *Client) warnf(format string, args ...any) {
	if c.OnWarn != nil {
		c.OnWarn(fmt.Sprintf(format, args...))
		return
	}
	_, _ = fmt.Fprintf(os.Stderr, format+"\n", args...)
}

func (c *Client) debugf(format string, args ...any) {
	if c.Debug != nil {
		c.Debug(format, args...)
	}
}

type lokiResponse struct {
	Status string `json:"status"`
	Data   struct {
		ResultType string `json:"resultType"`
		Result     []struct {
			Stream map[string]string   `json:"stream"`
			Values [][]json.RawMessage `json:"values"`
		} `json:"result"`
	} `json:"data"`
}

// QueryRange runs a single /loki/api/v1/query_range request.
func (c *Client) QueryRange(ctx context.Context, query string, start, end int64, limit int, forward bool) ([]Entry, error) {
	if limit <= 0 || limit > maxEntriesPerQuery {
		limit = maxEntriesPerQuery
	}
	direction := "backward"
	if forward {
		direction = "forward"
	}

	params := url.Values{}
	params.Set("query", query)
	params.Set("start", strconv.FormatInt(start, 10))
	params.Set("end", strconv.FormatInt(end, 10))
	params.Set("limit", strconv.Itoa(limit))
	params.Set("direction", direction)

	endpoint := c.URL + "/loki/api/v1/query_range?" + params.Encode()
	c.debugf("GET %s\n", endpoint)
	c.reqs++

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("loki returned %s: %s", resp.Status, strings.TrimSpace(truncate(string(body), 500)))
	}
	if len(strings.TrimSpace(string(body))) == 0 {
		return nil, fmt.Errorf("loki returned an empty body — the query is probably too expensive.\n" +
			"A regex with many alternatives makes Loki give up silently; use a broad -F filter and grep the output instead")
	}

	var parsed lokiResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, fmt.Errorf("parse response: %w", err)
	}
	if parsed.Data.ResultType != "" && parsed.Data.ResultType != "streams" {
		return nil, fmt.Errorf("unexpected result type %q (this tool only handles log queries)", parsed.Data.ResultType)
	}

	var entries []Entry
	for _, stream := range parsed.Data.Result {
		for _, v := range stream.Values {
			if len(v) < 2 {
				continue
			}
			var tsStr, line string
			if err := json.Unmarshal(v[0], &tsStr); err != nil {
				continue
			}
			if err := json.Unmarshal(v[1], &line); err != nil {
				continue
			}
			ns, err := strconv.ParseInt(tsStr, 10, 64)
			if err != nil {
				continue
			}
			entries = append(entries, Entry{NS: ns, Line: line, Labels: stream.Stream})
		}
	}
	c.fetched += len(entries)
	sortEntries(entries)
	return entries, nil
}

// EmitFunc receives entries in ascending timestamp order. Returning an error
// stops the stream.
type EmitFunc func(Entry) error

// errLimitReached unwinds the stream once --limit entries were emitted.
var errLimitReached = fmt.Errorf("limit reached")

// Stream fetches every line matching the queries in [from, to) and emits them
// in ascending timestamp order, paginating past the server's 5000 entry cap.
func (c *Client) Stream(ctx context.Context, queries []string, from, to time.Time, total int, emit EmitFunc) error {
	emitted := 0
	wrapped := func(e Entry) error {
		if err := emit(e); err != nil {
			return err
		}
		emitted++
		if total > 0 && emitted >= total {
			return errLimitReached
		}
		return nil
	}

	err := c.scanForward(ctx, queries, from, to, wrapped)
	if err == errLimitReached {
		return nil
	}
	return err
}

// The server caps every response at max_entries_limit_per_query and that
// truncation is not a fair merge across streams: a full response may drop
// entries that are older than the newest one it returned. Paginating on the
// last timestamp therefore loses lines. Instead the range is walked in time
// windows, and any window that comes back at the cap is retried smaller until
// it fits — a window below the cap provably contains every matching line.
const (
	windowTargetEntries = 4000
	minWindow           = time.Millisecond
)

// scanForward walks [from, to) in adaptive windows, emitting ascending entries.
func (c *Client) scanForward(ctx context.Context, queries []string, from, to time.Time, emit EmitFunc) error {
	start := from.UnixNano()
	cur := start
	end := to.UnixNano()
	total := end - start
	window := initialWindow(total)
	c.report(0, total, cur)

	for cur < end {
		size := window
		if cur+size > end {
			size = end - cur
		}

		entries, capped, err := c.fetchWindow(ctx, queries, cur, cur+size)
		if err != nil {
			return err
		}
		if capped {
			if size <= int64(minWindow) {
				// pathological density: emit what the server gave us and move on
				c.warnf("lokigrep: warning: more than %d entries within %s at %s; some lines may be missing",
					maxEntriesPerQuery, time.Duration(size), time.Unix(0, cur).Format(time.RFC3339Nano))
				if err := emitAll(entries, emit); err != nil {
					return err
				}
				cur += size
				c.report(cur-start, total, cur)
				continue
			}
			window = shrink(size)
			c.report(cur-start, total, cur)
			continue
		}

		if err := emitAll(entries, emit); err != nil {
			return err
		}
		cur += size
		window = nextWindow(size, len(entries))
		c.report(cur-start, total, cur)
	}
	return nil
}

// Tail walks [from, to) newest first and returns at most n entries,
// ascending. Used by --tail.
func (c *Client) Tail(ctx context.Context, queries []string, from, to time.Time, n int) ([]Entry, error) {
	start := from.UnixNano()
	top := to.UnixNano()
	cur := top
	total := top - start
	window := initialWindow(total)
	c.report(0, total, cur)

	var collected []Entry
	for cur > start && len(collected) < n {
		size := window
		if cur-size < start {
			size = cur - start
		}

		entries, capped, err := c.fetchWindow(ctx, queries, cur-size, cur)
		if err != nil {
			return nil, err
		}
		if capped {
			if size <= int64(minWindow) {
				collected = append(entries, collected...)
				cur -= size
				c.report(top-cur, total, cur)
				continue
			}
			window = shrink(size)
			c.report(top-cur, total, cur)
			continue
		}

		collected = append(entries, collected...)
		cur -= size
		window = nextWindow(size, len(entries))
		c.report(top-cur, total, cur)
	}

	sortEntries(collected)
	if len(collected) > n {
		collected = collected[len(collected)-n:]
	}
	return collected, nil
}

// fetchWindow runs every query over one window and merges the results. capped
// is true when any query hit the entry limit, meaning the window must be split.
func (c *Client) fetchWindow(ctx context.Context, queries []string, start, end int64) (entries []Entry, capped bool, err error) {
	for _, q := range queries {
		page, err := c.QueryRange(ctx, q, start, end, maxEntriesPerQuery, true)
		if err != nil {
			return nil, false, err
		}
		if len(page) >= maxEntriesPerQuery {
			return nil, true, nil
		}
		entries = append(entries, page...)
	}
	if len(queries) > 1 {
		sortEntries(entries)
		entries = dedupeEntries(entries)
	}
	return entries, false, nil
}

// dedupeEntries drops entries that several selectors matched.
func dedupeEntries(in []Entry) []Entry {
	seen := make(map[string]bool, len(in))
	out := in[:0]
	for i := range in {
		k := in[i].Key()
		if seen[k] {
			continue
		}
		seen[k] = true
		out = append(out, in[i])
	}
	return out
}

func emitAll(entries []Entry, emit EmitFunc) error {
	for i := range entries {
		if err := emit(entries[i]); err != nil {
			return err
		}
	}
	return nil
}

func initialWindow(span int64) int64 {
	window := int64(5 * time.Minute)
	if span < window {
		window = span
	}
	if window < int64(minWindow) {
		window = int64(minWindow)
	}
	return window
}

// nextWindow aims for windowTargetEntries per request, growing at most 4x at a
// time so a quiet stretch cannot make the next window overshoot wildly.
func nextWindow(size int64, got int) int64 {
	if got == 0 {
		return clampWindow(size * 4)
	}
	scaled := float64(size) * float64(windowTargetEntries) / float64(got)
	if scaled > float64(size)*4 {
		scaled = float64(size) * 4
	}
	return clampWindow(int64(scaled))
}

func shrink(size int64) int64 {
	return clampWindow(size * windowTargetEntries / maxEntriesPerQuery / 2)
}

func clampWindow(size int64) int64 {
	if size < int64(minWindow) {
		return int64(minWindow)
	}
	if size > int64(maxQueryWindow) {
		return int64(maxQueryWindow)
	}
	return size
}

// LabelValues fetches the values of a label, used for suggestions. query is an
// optional stream selector that narrows the answer, e.g. {cluster="eu-prod"}.
func (c *Client) LabelValues(ctx context.Context, label string, from, to time.Time, query string) ([]string, error) {
	params := url.Values{}
	params.Set("start", strconv.FormatInt(from.UnixNano(), 10))
	params.Set("end", strconv.FormatInt(to.UnixNano(), 10))
	if query != "" {
		params.Set("query", query)
	}

	endpoint := fmt.Sprintf("%s/loki/api/v1/label/%s/values?%s", c.URL, url.PathEscape(label), params.Encode())
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("loki returned %s: %s", resp.Status, truncate(string(body), 200))
	}
	var parsed struct {
		Data []string `json:"data"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, err
	}
	sort.Strings(parsed.Data)
	return parsed.Data, nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
