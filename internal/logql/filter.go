package logql

import (
	"fmt"
	"strconv"
	"strings"
)

// FilterMode decides how a pattern is interpreted.
type FilterMode int

const (
	FilterFixed FilterMode = iota // -F, plain substring
	FilterRegex                   // -P, regular expression
)

// Filter is one line filter appended to the stream selector.
type Filter struct {
	Pattern string
	Mode    FilterMode
	Invert  bool
	Ignore  bool // -i, case insensitive
}

func (f Filter) String() string {
	pattern := f.Pattern
	op := "|="
	switch {
	case f.Mode == FilterRegex && f.Invert:
		op = "!~"
	case f.Mode == FilterRegex:
		op = "|~"
	case f.Invert:
		op = "!="
	}
	if f.Ignore {
		// case insensitivity only exists for the regex operators
		if f.Mode == FilterFixed {
			pattern = regexQuoteMeta(pattern)
			if f.Invert {
				op = "!~"
			} else {
				op = "|~"
			}
		}
		pattern = "(?i)" + pattern
	}
	return op + " " + quoteLogQL(pattern)
}

// BuildQueries appends the line filters to every stream selector.
func BuildQueries(streamSelectors []string, filters []Filter) []string {
	suffix := ""
	for _, f := range filters {
		suffix += " " + f.String()
	}
	out := make([]string, 0, len(streamSelectors))
	for _, s := range streamSelectors {
		out = append(out, s+suffix)
	}
	return out
}

// quoteLogQL renders a Go/LogQL double quoted string.
func quoteLogQL(s string) string { return strconv.Quote(s) }

func regexQuoteMeta(s string) string {
	var b strings.Builder
	for _, r := range s {
		if strings.ContainsRune(`\.+*?()|[]{}^$`, r) {
			b.WriteByte('\\')
		}
		b.WriteRune(r)
	}
	return b.String()
}

// alternationCount is a cheap estimate of regex complexity. Loki answers with an
// empty body instead of an error when a filter regex has too many branches.
func alternationCount(pattern string) int {
	return strings.Count(pattern, "|")
}

func WarnComplexRegex(filters []Filter) string {
	for _, f := range filters {
		if f.Mode != FilterRegex {
			continue
		}
		if n := alternationCount(f.Pattern); n > 50 {
			return fmt.Sprintf("regex has %d alternatives; Loki tends to return an empty response for those.\n"+
				"Prefer a broad -F filter and grep the output client side", n+1)
		}
	}
	return ""
}
