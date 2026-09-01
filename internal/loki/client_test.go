package loki

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sort"
	"strconv"
	"testing"
	"time"
)

// fakeLoki serves generated entries and truncates like the real server does:
// when a window holds more than the limit it answers with a full page that is
// biased towards one stream, so entries older than the newest returned one can
// be missing. lokigrep must not rely on that page being a fair merge.
type fakeLoki struct {
	base     time.Time
	perMilli int
	millis   int
	streams  []string
	requests int
}

func (f *fakeLoki) entries(start, end int64) []Entry {
	var out []Entry
	for i := 0; i < f.millis; i++ {
		ts := f.base.Add(time.Duration(i) * time.Millisecond).UnixNano()
		if ts < start || ts >= end {
			continue
		}
		for j := 0; j < f.perMilli; j++ {
			pod := f.streams[j%len(f.streams)]
			out = append(out, Entry{
				NS:     ts,
				Line:   fmt.Sprintf("line %d/%d", i, j),
				Labels: map[string]string{"pod": pod},
			})
		}
	}
	return out
}

func (f *fakeLoki) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.requests++
	start, _ := strconv.ParseInt(r.URL.Query().Get("start"), 10, 64)
	end, _ := strconv.ParseInt(r.URL.Query().Get("end"), 10, 64)
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))

	entries := f.entries(start, end)
	// bias: order by pod first, so truncation drops one pod's older lines
	sort.SliceStable(entries, func(i, j int) bool {
		if entries[i].Labels["pod"] != entries[j].Labels["pod"] {
			return entries[i].Labels["pod"] < entries[j].Labels["pod"]
		}
		return entries[i].NS < entries[j].NS
	})
	if len(entries) > limit {
		entries = entries[:limit]
	}

	byStream := map[string][][]string{}
	for _, e := range entries {
		pod := e.Labels["pod"]
		byStream[pod] = append(byStream[pod], []string{strconv.FormatInt(e.NS, 10), e.Line})
	}

	type stream struct {
		Stream map[string]string `json:"stream"`
		Values [][]string        `json:"values"`
	}
	resp := struct {
		Status string `json:"status"`
		Data   struct {
			ResultType string   `json:"resultType"`
			Result     []stream `json:"result"`
		} `json:"data"`
	}{Status: "success"}
	resp.Data.ResultType = "streams"
	for pod, values := range byStream {
		resp.Data.Result = append(resp.Data.Result, stream{Stream: map[string]string{"pod": pod}, Values: values})
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

func TestScanForwardIsLossless(t *testing.T) {
	fake := &fakeLoki{
		base:     time.Unix(1787148000, 0),
		perMilli: 3,
		millis:   20000, // 60000 entries over 20s, twelve times the page limit
		streams:  []string{"pod-a", "pod-b", "pod-c"},
	}
	srv := httptest.NewServer(fake)
	defer srv.Close()

	client := New(srv.URL, 10*time.Second)
	from := fake.base
	to := fake.base.Add(time.Duration(fake.millis) * time.Millisecond)

	var got []Entry
	err := client.Stream(context.Background(), []string{`{app="x"}`}, from, to, 0, func(e Entry) error {
		got = append(got, e)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	want := fake.millis * fake.perMilli
	if len(got) != want {
		t.Fatalf("got %d entries, want %d (lost %d)", len(got), want, want-len(got))
	}
	for i := 1; i < len(got); i++ {
		if got[i].NS < got[i-1].NS {
			t.Fatalf("entry %d is out of order", i)
		}
	}
	seen := map[string]bool{}
	for i := range got {
		k := got[i].Key()
		if seen[k] {
			t.Fatalf("duplicate entry %q", k)
		}
		seen[k] = true
	}
	t.Logf("%d entries in %d requests", len(got), fake.requests)
}

func TestTail(t *testing.T) {
	fake := &fakeLoki{
		base:     time.Unix(1787148000, 0),
		perMilli: 2,
		millis:   10000,
		streams:  []string{"pod-a", "pod-b"},
	}
	srv := httptest.NewServer(fake)
	defer srv.Close()

	client := New(srv.URL, 10*time.Second)
	from := fake.base
	to := fake.base.Add(time.Duration(fake.millis) * time.Millisecond)

	got, err := client.Tail(context.Background(), []string{`{app="x"}`}, from, to, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 10 {
		t.Fatalf("got %d entries, want 10", len(got))
	}
	lastTS := fake.base.Add(time.Duration(fake.millis-1) * time.Millisecond).UnixNano()
	if got[len(got)-1].NS != lastTS {
		t.Fatalf("tail does not end on the newest entry: %d != %d", got[len(got)-1].NS, lastTS)
	}
	for i := 1; i < len(got); i++ {
		if got[i].NS < got[i-1].NS {
			t.Fatalf("entry %d is out of order", i)
		}
	}
}

func TestStreamRespectsLimit(t *testing.T) {
	fake := &fakeLoki{
		base:     time.Unix(1787148000, 0),
		perMilli: 2,
		millis:   1000,
		streams:  []string{"pod-a"},
	}
	srv := httptest.NewServer(fake)
	defer srv.Close()

	client := New(srv.URL, 10*time.Second)
	count := 0
	err := client.Stream(context.Background(), []string{`{app="x"}`},
		fake.base, fake.base.Add(time.Second), 7, func(Entry) error {
			count++
			return nil
		})
	if err != nil {
		t.Fatal(err)
	}
	if count != 7 {
		t.Fatalf("emitted %d entries, want 7", count)
	}
}
