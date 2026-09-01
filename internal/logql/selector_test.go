package logql

import (
	"reflect"
	"testing"
)

// testSchema is the three level Kubernetes shape with a couple of cluster
// shorthands, the setup the README documents.
func testSchema(t *testing.T) *Schema {
	t.Helper()
	sc, err := NewSchema([]Label{
		{Name: "cluster", Aliases: map[string]string{"prod": "eu-prod", "dev": "eu-dev"}, Match: "^eu-"},
		{Name: "namespace", Primary: true},
		{Name: "pod"},
	})
	if err != nil {
		t.Fatal(err)
	}
	return sc
}

func TestParseSelectors(t *testing.T) {
	sc := testSchema(t)

	cases := []struct {
		spec string
		want []Selector
	}{
		{"web", []Selector{{"", "web", ""}}},
		{"prod.web", []Selector{{"eu-prod", "web", ""}}},
		{"dev.web", []Selector{{"eu-dev", "web", ""}}},
		{"eu-prod.*", []Selector{{"eu-prod", "", ""}}},
		{"*.web", []Selector{{"", "web", ""}}},
		{"web.api-0", []Selector{{"", "web", "api-0"}}},
		{"prod.web.api-*", []Selector{{"eu-prod", "web", "api-*"}}},
		{"prod.web.pod.with.dots", []Selector{{"eu-prod", "web", "pod.with.dots"}}},
		{"web,ingress", []Selector{{"", "web", ""}, {"", "ingress", ""}}},
		{"web, web", []Selector{{"", "web", ""}}},
	}

	for _, c := range cases {
		t.Run(c.spec, func(t *testing.T) {
			got, err := ParseSelectors(c.spec, sc, "")
			if err != nil {
				t.Fatalf("ParseSelectors(%q): %v", c.spec, err)
			}
			if !reflect.DeepEqual(got, c.want) {
				t.Fatalf("ParseSelectors(%q) = %+v, want %+v", c.spec, got, c.want)
			}
		})
	}
}

func TestParseSelectorsOuterOverride(t *testing.T) {
	got, err := ParseSelectors("web", testSchema(t), "prod")
	if err != nil {
		t.Fatal(err)
	}
	if got[0][0] != "eu-prod" {
		t.Fatalf("override ignored: %+v", got)
	}
}

// A schema with a single label swallows the dots: there is no level to shift
// tokens into, and Loki label values may contain dots.
func TestParseSelectorsSingleLabel(t *testing.T) {
	sc, err := NewSchema([]Label{{Name: "job"}})
	if err != nil {
		t.Fatal(err)
	}
	got, err := ParseSelectors("apps/api.v2", sc, "")
	if err != nil {
		t.Fatal(err)
	}
	want := []Selector{{"apps/api.v2"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}

// Without an explicit primary a bare token addresses the first label.
func TestParseSelectorsDefaultPrimary(t *testing.T) {
	sc, err := NewSchema([]Label{{Name: "job"}, {Name: "instance"}})
	if err != nil {
		t.Fatal(err)
	}
	if sc.Primary() != 0 {
		t.Fatalf("primary = %d, want 0", sc.Primary())
	}
	got, err := ParseSelectors("api", sc, "")
	if err != nil {
		t.Fatal(err)
	}
	want := []Selector{{"api", ""}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}

func TestNewSchemaRejects(t *testing.T) {
	cases := []struct {
		name   string
		labels []Label
	}{
		{"empty", nil},
		{"missing name", []Label{{Name: ""}}},
		{"duplicate", []Label{{Name: "job"}, {Name: "job"}}},
		{"two primaries", []Label{{Name: "job", Primary: true}, {Name: "pod", Primary: true}}},
		{"bad match", []Label{{Name: "job", Match: "("}}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, err := NewSchema(c.labels); err == nil {
				t.Fatalf("NewSchema(%+v) accepted", c.labels)
			}
		})
	}
}

func TestStreamSelectors(t *testing.T) {
	sc := testSchema(t)

	cases := []struct {
		spec string
		want []string
	}{
		{"web", []string{`{namespace="web"}`}},
		{"prod.web", []string{`{cluster="eu-prod", namespace="web"}`}},
		{"eu-prod.*", []string{`{cluster="eu-prod"}`}},
		// nothing is constrained: fall back to "the primary label exists"
		{"*", []string{`{namespace=~".+"}`}},
		{"prod.web.api-*", []string{`{cluster="eu-prod", namespace="web", pod=~"api-.*"}`}},
		{"prod.~web-(app|ml)", []string{`{cluster="eu-prod", namespace=~"web-(app|ml)"}`}},
		// same cluster, two namespaces: collapses into one query
		{"prod.web,prod.ingress", []string{`{cluster="eu-prod", namespace=~"(ingress|web)"}`}},
		// not a product: one query per selector
		{"prod.web,dev.ingress", []string{
			`{cluster="eu-prod", namespace="web"}`,
			`{cluster="eu-dev", namespace="ingress"}`,
		}},
		// a full product over two levels collapses as well
		{"prod.web,prod.ingress,dev.web,dev.ingress", []string{
			`{cluster=~"(eu-dev|eu-prod)", namespace=~"(ingress|web)"}`,
		}},
	}

	for _, c := range cases {
		t.Run(c.spec, func(t *testing.T) {
			sels, err := ParseSelectors(c.spec, sc, "")
			if err != nil {
				t.Fatalf("ParseSelectors(%q): %v", c.spec, err)
			}
			got := StreamSelectors(sc, sels)
			if !reflect.DeepEqual(got, c.want) {
				t.Fatalf("StreamSelectors(%q) = %q, want %q", c.spec, got, c.want)
			}
		})
	}
}

func TestScopeSelector(t *testing.T) {
	sc := testSchema(t)
	if got, want := sc.ScopeSelector("eu-prod"), `{cluster="eu-prod"}`; got != want {
		t.Fatalf("ScopeSelector = %q, want %q", got, want)
	}
}

func TestBuildQueries(t *testing.T) {
	sc := testSchema(t)
	sels, err := ParseSelectors("prod.web", sc, "")
	if err != nil {
		t.Fatal(err)
	}
	const sel = `{cluster="eu-prod", namespace="web"}`
	cases := []struct {
		filters []Filter
		want    string
	}{
		{nil, sel},
		{[]Filter{{Pattern: "END=500"}}, sel + ` |= "END=500"`},
		{[]Filter{{Pattern: "END=500", Invert: true}}, sel + ` != "END=500"`},
		{[]Filter{{Pattern: "END=(500|503)", Mode: FilterRegex}}, sel + ` |~ "END=(500|503)"`},
		{[]Filter{{Pattern: "err", Ignore: true}}, sel + ` |~ "(?i)err"`},
		{[]Filter{{Pattern: "a"}, {Pattern: "b"}}, sel + ` |= "a" |= "b"`},
		{[]Filter{{Pattern: `say "hi"`}}, sel + ` |= "say \"hi\""`},
	}

	for _, c := range cases {
		got := BuildQueries(StreamSelectors(sc, sels), c.filters)
		if len(got) != 1 || got[0] != c.want {
			t.Errorf("BuildQueries(%+v) = %q, want %q", c.filters, got, c.want)
		}
	}
}

func TestAmbiguous(t *testing.T) {
	sc := testSchema(t)

	sels, err := ParseSelectors("prod.web", sc, "")
	if err != nil {
		t.Fatal(err)
	}
	if got, want := Ambiguous(sc, sels), []bool{false, false, true}; !reflect.DeepEqual(got, want) {
		t.Fatalf("prod.web: got %v, want %v", got, want)
	}

	sels, err = ParseSelectors("web,ingress", sc, "")
	if err != nil {
		t.Fatal(err)
	}
	if got, want := Ambiguous(sc, sels), []bool{true, true, true}; !reflect.DeepEqual(got, want) {
		t.Fatalf("two namespaces: got %v, want %v", got, want)
	}
}
