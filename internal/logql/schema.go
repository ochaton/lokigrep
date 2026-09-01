package logql

import (
	"errors"
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strings"
)

// Label is one level of the selector hierarchy, from the widest scope to the
// narrowest: with labels cluster, namespace and pod, "-s prod.web.api-0" reads
// left to right in that order. The yaml tags are the shape the config file
// spells this in.
type Label struct {
	// Name is the Loki stream label this level matches on.
	Name string `yaml:"name"`
	// Aliases are shorthands a user may type instead of a full value, e.g.
	// prod: eu-prod. Lookups are case insensitive.
	Aliases map[string]string `yaml:"aliases,omitempty"`
	// Match recognises a token as belonging to this level even when it is not
	// an alias. It is what tells "eu-prod.web" (cluster, namespace)
	// apart from "web.api-0" (namespace, pod).
	Match string `yaml:"match,omitempty"`
	// Primary marks the level a one token selector addresses and the one -l
	// lists. Defaults to the first label.
	Primary bool `yaml:"primary,omitempty"`
}

// Schema is the compiled label hierarchy every selector is parsed against.
type Schema struct {
	labels  []Label
	match   []*regexp.Regexp
	primary int
}

func NewSchema(labels []Label) (*Schema, error) {
	if len(labels) == 0 {
		return nil, errors.New("labels must not be empty")
	}
	s := &Schema{labels: labels, match: make([]*regexp.Regexp, len(labels)), primary: -1}
	for i, l := range labels {
		if l.Name == "" {
			return nil, fmt.Errorf("labels[%d]: missing name", i)
		}
		for j := range i {
			if labels[j].Name == l.Name {
				return nil, fmt.Errorf("labels[%d]: duplicate label %q", i, l.Name)
			}
		}
		if l.Match != "" {
			re, err := regexp.Compile(l.Match)
			if err != nil {
				return nil, fmt.Errorf("labels[%d] (%s): match: %w", i, l.Name, err)
			}
			s.match[i] = re
		}
		if l.Primary {
			if s.primary >= 0 {
				return nil, fmt.Errorf("labels[%d] (%s): primary is already set on %q", i, l.Name, labels[s.primary].Name)
			}
			s.primary = i
		}
	}
	if s.primary < 0 {
		s.primary = 0
	}
	return s, nil
}

// Len is the number of levels a selector may address.
func (s *Schema) Len() int { return len(s.labels) }

// Name is the Loki label at level i.
func (s *Schema) Name(i int) string { return s.labels[i].Name }

// Primary is the level a one token selector addresses, and the one -l lists.
func (s *Schema) Primary() int { return s.primary }

// Names lists the labels, widest scope first.
func (s *Schema) Names() []string {
	out := make([]string, len(s.labels))
	for i, l := range s.labels {
		out[i] = l.Name
	}
	return out
}

// sample is a token to show in help for level i: a configured alias when there
// is one, otherwise a placeholder naming the label.
func (s *Schema) Sample(i int) string {
	if keys := slices.Sorted(maps.Keys(s.labels[i].Aliases)); len(keys) > 0 {
		return keys[0]
	}
	return "<" + s.labels[i].Name + ">"
}

// Spec renders the hierarchy the way -s spells it, e.g. {cluster}.{namespace}.
func (s *Schema) Spec() string {
	parts := make([]string, len(s.labels))
	for i, l := range s.labels {
		parts[i] = "{" + l.Name + "}"
	}
	return strings.Join(parts, ".")
}

// levelOf reports which level a bare token belongs to, by alias or by the
// level's match pattern. "*" always widens to the outermost level so that
// "*.web" means "any cluster, namespace web".
func (s *Schema) levelOf(tok string) (int, bool) {
	if tok == "*" {
		return 0, true
	}
	low := strings.ToLower(tok)
	for i, l := range s.labels {
		if _, ok := l.Aliases[low]; ok {
			return i, true
		}
		if s.match[i] != nil && s.match[i].MatchString(tok) {
			return i, true
		}
	}
	return 0, false
}

// resolve expands an alias at level i. "*" means "any", spelled as "".
func (s *Schema) resolve(i int, tok string) string {
	if tok == "*" {
		return ""
	}
	if v, ok := s.labels[i].Aliases[strings.ToLower(tok)]; ok {
		return v
	}
	return tok
}
