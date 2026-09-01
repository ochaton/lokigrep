package logql

import (
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
)

// Selector is one value per schema level. An empty value means "any".
type Selector []string

func (s Selector) String() string {
	parts := make([]string, len(s))
	for i, v := range s {
		parts[i] = orStar(v)
	}
	return strings.Join(parts, ".")
}

// key identifies a selector for deduplication; slices are not comparable.
func (s Selector) key() string { return strings.Join(s, "\x00") }

func orStar(s string) string {
	if s == "" {
		return "*"
	}
	return s
}

// ParseSelectors parses a comma separated list of dotted specs. With the
// labels cluster, namespace (primary) and pod:
//
//	web                  -> any cluster, namespace web, any pod
//	prod.web             -> "prod" is a cluster alias, so it shifts left
//	eu-prod.*     -> that cluster, any namespace and pod
//	web.api-0            -> namespace web, single pod
//	prod.web.api-*       -> pod glob
//	~web-(app|ml)        -> raw regex for that level
//
// Fields accept * and ? globs, or a leading ~ for a raw regular expression.
// override, when set, replaces the outermost level on every selector.
func ParseSelectors(spec string, sc *Schema, override string) ([]Selector, error) {
	spec = strings.TrimSpace(spec)
	if spec == "" {
		return nil, errors.New("empty selector")
	}

	var out []Selector
	for _, part := range strings.Split(spec, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		sel, err := sc.parseOne(part)
		if err != nil {
			return nil, err
		}
		if override != "" {
			sel[0] = sc.resolve(0, override)
		}
		out = append(out, sel)
	}
	if len(out) == 0 {
		return nil, errors.New("empty selector")
	}
	return dedupeSelectors(out), nil
}

// parseOne places the dotted tokens into the hierarchy. They start at the
// primary level, shift left when the first token names a wider level or when
// there are more tokens than levels left, and any surplus tokens fold back
// into the innermost level, whose values may legally contain dots.
func (s *Schema) parseOne(part string) (Selector, error) {
	toks := strings.Split(part, ".")
	for i, t := range toks {
		toks[i] = strings.TrimSpace(t)
	}

	start := s.primary
	if len(toks) > 1 {
		if lvl, ok := s.levelOf(toks[0]); ok && lvl < start {
			start = lvl
		}
		if start+len(toks) > len(s.labels) {
			start = max(0, len(s.labels)-len(toks))
		}
	}

	n := min(len(toks), len(s.labels)-start)
	sel := make(Selector, len(s.labels))
	for i := range n - 1 {
		sel[start+i] = s.resolve(start+i, toks[i])
	}
	last := start + n - 1
	sel[last] = s.resolve(last, strings.Join(toks[n-1:], "."))
	return sel, nil
}

func dedupeSelectors(in []Selector) []Selector {
	seen := make(map[string]bool, len(in))
	out := make([]Selector, 0, len(in))
	for _, s := range in {
		k := s.key()
		if seen[k] {
			continue
		}
		seen[k] = true
		out = append(out, s)
	}
	return out
}

// StreamSelectors turns selectors into LogQL stream selectors. When the set of
// selectors forms a full product over every level it collapses into a single
// query (cheap); otherwise one query per selector is returned and the caller
// merges the results.
func StreamSelectors(sc *Schema, sels []Selector) []string {
	if len(sels) == 0 {
		return nil
	}
	if len(sels) > 1 {
		if q, ok := productSelector(sc, sels); ok {
			return []string{q}
		}
	}
	out := make([]string, 0, len(sels))
	for _, s := range sels {
		values := make([][]string, len(s))
		for i, v := range s {
			values[i] = []string{v}
		}
		out = append(out, sc.streamSelector(values))
	}
	return out
}

func productSelector(sc *Schema, sels []Selector) (string, bool) {
	values := make([][]string, sc.Len())
	size := 1
	for i := range values {
		values[i] = LevelValues(sels, i)
		size *= len(values[i])
	}
	if size != len(sels) {
		return "", false
	}
	present := make(map[string]bool, len(sels))
	for _, s := range sels {
		present[s.key()] = true
	}
	// walk the cartesian product by counting in mixed radix
	point := make(Selector, len(values))
	for n := range size {
		rest := n
		for i := len(values) - 1; i >= 0; i-- {
			point[i] = values[i][rest%len(values[i])]
			rest /= len(values[i])
		}
		if !present[point.key()] {
			return "", false
		}
	}
	return sc.streamSelector(values), true
}

func LevelValues(sels []Selector, i int) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range sels {
		if v := s[i]; !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	slices.Sort(out)
	return out
}

// streamSelector builds "{label=…, …}" from the alternatives per level.
func (s *Schema) streamSelector(values [][]string) string {
	matchers := make([]string, 0, len(values))
	for i, pats := range values {
		if m := labelMatcher(s.labels[i].Name, pats); m != "" {
			matchers = append(matchers, m)
		}
	}
	if len(matchers) == 0 {
		// Loki rejects a selector where every matcher can match the empty
		// string, so fall back to "the primary label exists".
		matchers = append(matchers, fmt.Sprintf("%s=~%s", s.labels[s.primary].Name, quoteLogQL(".+")))
	}
	return "{" + strings.Join(matchers, ", ") + "}"
}

// ScopeSelector narrows the label endpoints to one value of the outermost
// level. An empty value means "do not narrow".
func (s *Schema) ScopeSelector(values ...string) string {
	all := make([][]string, len(s.labels))
	for i := range all {
		all[i] = []string{""}
	}
	all[0] = values
	return s.streamSelector(all)
}

func labelMatcher(label string, pats []string) string {
	res := make([]string, 0, len(pats))
	exact := true
	for _, p := range pats {
		if p == "" || p == "*" {
			// one unconstrained alternative makes the whole matcher useless
			return ""
		}
		if isPatternGlob(p) {
			exact = false
		}
		res = append(res, patternToRegex(p))
	}
	if exact && len(pats) == 1 {
		return fmt.Sprintf("%s=%s", label, quoteLogQL(pats[0]))
	}
	if len(res) == 1 {
		return fmt.Sprintf("%s=~%s", label, quoteLogQL(res[0]))
	}
	return fmt.Sprintf("%s=~%s", label, quoteLogQL("("+strings.Join(res, "|")+")"))
}

func isPatternGlob(p string) bool {
	return strings.HasPrefix(p, "~") || strings.ContainsAny(p, "*?")
}

// patternToRegex converts a glob to a regexp. A leading ~ passes the rest
// through verbatim as a regular expression.
func patternToRegex(p string) string {
	if strings.HasPrefix(p, "~") {
		return strings.TrimPrefix(p, "~")
	}
	var b strings.Builder
	for _, r := range p {
		switch r {
		case '*':
			b.WriteString(".*")
		case '?':
			b.WriteString(".")
		default:
			b.WriteString(regexp.QuoteMeta(string(r)))
		}
	}
	return b.String()
}

// Ambiguous reports, per level, whether more than one value can match, which
// decides whether that label is worth printing next to every line.
func Ambiguous(sc *Schema, sels []Selector) []bool {
	out := make([]bool, sc.Len())
	for i := range out {
		vals := LevelValues(sels, i)
		out[i] = len(vals) != 1 || vals[0] == "" || isPatternGlob(vals[0])
	}
	return out
}
