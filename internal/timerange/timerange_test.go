package timerange

import (
	"testing"
	"time"
)

func mustTime(t *testing.T, layout, value string) time.Time {
	t.Helper()
	got, err := time.ParseInLocation(layout, value, time.Local)
	if err != nil {
		t.Fatalf("bad test fixture %q: %v", value, err)
	}
	return got
}

func TestParse(t *testing.T) {
	now := mustTime(t, "2006-01-02T15:04:05", "2026-07-15T12:00:00")

	cases := []struct {
		spec string
		from time.Time
		to   time.Time
	}{
		{"-2h/-1h", now.Add(-2 * time.Hour), now.Add(-time.Hour)},
		{"2h/1h", now.Add(-2 * time.Hour), now.Add(-time.Hour)},
		{"10m/", now.Add(-10 * time.Minute), now},
		{"5m", now.Add(-5 * time.Minute), now},
		{"1h30m/", now.Add(-90 * time.Minute), now},
		{"7d/", now.AddDate(0, 0, -7), now},
		{"1w/", now.AddDate(0, 0, -7), now},
		{"midnight/", mustTime(t, "2006-01-02T15:04:05", "2026-07-15T00:00:00"), now},
		{"yesterday/midnight",
			mustTime(t, "2006-01-02T15:04:05", "2026-07-14T00:00:00"),
			mustTime(t, "2006-01-02T15:04:05", "2026-07-15T00:00:00")},
		{"2026-07-10T12:00:00/", mustTime(t, "2006-01-02T15:04:05", "2026-07-10T12:00:00"), now},
		{"2026-07-10/2026-07-11",
			mustTime(t, "2006-01-02T15:04:05", "2026-07-10T00:00:00"),
			mustTime(t, "2006-01-02T15:04:05", "2026-07-11T00:00:00")},
		{"2026-07-10/-2h", mustTime(t, "2006-01-02T15:04:05", "2026-07-10T00:00:00"), now.Add(-2 * time.Hour)},
		{"1h/+15m", now.Add(-time.Hour), now.Add(-45 * time.Minute)},
		{"08:00/09:00",
			mustTime(t, "2006-01-02T15:04:05", "2026-07-15T08:00:00"),
			mustTime(t, "2006-01-02T15:04:05", "2026-07-15T09:00:00")},

		// English phrases
		{"2 days ago/+10m", now.AddDate(0, 0, -2), now.AddDate(0, 0, -2).Add(10 * time.Minute)},
		{"2 days ago/", now.AddDate(0, 0, -2), now},
		{"2days/", now.AddDate(0, 0, -2), now},
		{"30 minutes ago/", now.Add(-30 * time.Minute), now},
		{"last 30 minutes/", now.Add(-30 * time.Minute), now},
		{"past 2 hours/", now.Add(-2 * time.Hour), now},
		{"1 hour 30 min ago/", now.Add(-90 * time.Minute), now},
		{"3 days ago/2 days ago", now.AddDate(0, 0, -3), now.AddDate(0, 0, -2)},
		{"2 days ago/+10 minutes", now.AddDate(0, 0, -2), now.AddDate(0, 0, -2).Add(10 * time.Minute)},
		{"1 month ago/", now.AddDate(0, -1, 0), now},
		{"1 week ago/+1 day", now.AddDate(0, 0, -7), now.AddDate(0, 0, -6)},

		// day keyword plus a clock time
		{"yesterday 14:00/+1h",
			mustTime(t, "2006-01-02T15:04:05", "2026-07-14T14:00:00"),
			mustTime(t, "2006-01-02T15:04:05", "2026-07-14T15:00:00")},
		{"today 08:30/today 09:00",
			mustTime(t, "2006-01-02T15:04:05", "2026-07-15T08:30:00"),
			mustTime(t, "2006-01-02T15:04:05", "2026-07-15T09:00:00")},
	}

	for _, c := range cases {
		from, to, err := Parse(c.spec, now)
		if err != nil {
			t.Errorf("Parse(%q): %v", c.spec, err)
			continue
		}
		if !from.Equal(c.from) {
			t.Errorf("Parse(%q) from = %s, want %s", c.spec, from, c.from)
		}
		if !to.Equal(c.to) {
			t.Errorf("Parse(%q) to = %s, want %s", c.spec, to, c.to)
		}
	}
}

func TestParseRejects(t *testing.T) {
	now := time.Now()
	for _, spec := range []string{"", "/", "/1h", "-1h/-2h", "1h/2h", "nonsense/", "1h/2h/3h", "0m/",
		"2 days and a bit ago/", "2 potatoes ago/", "days ago/", "yesterday 25:00/", "12h30/"} {
		if _, _, err := Parse(spec, now); err == nil {
			t.Errorf("Parse(%q) should have failed", spec)
		}
	}
}

func TestParseUnix(t *testing.T) {
	now := time.Now()
	from, to, err := Parse("1783634400/1783720800", now)
	if err != nil {
		t.Fatal(err)
	}
	if from.Unix() != 1783634400 || to.Unix() != 1783720800 {
		t.Fatalf("unix range parsed as %s .. %s", from, to)
	}
}
