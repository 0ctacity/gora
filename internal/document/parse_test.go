package document

import (
	"strings"
	"testing"
)

func TestParseAppDocument(t *testing.T) {
	t.Parallel()

	src := []byte(`
gora: 1
kind: app
name: Dashboard
viewport:
  width: 1280
  height: 800
  background: transparent
breakpoints:
  compact:
    max_width: 699
  wide:
    min_width: 700
entry: dashboard
screens:
  dashboard:
    type: stack
    name: dashboard-root
    props:
      direction: vertical
      gap: 16
    children:
      - type: text
        props:
          content: Dashboard
`)

	doc, diagnostics := Parse("dashboard.gora", src)
	if len(diagnostics) != 0 {
		t.Fatalf("Parse returned diagnostics: %+v", diagnostics)
	}
	if doc == nil {
		t.Fatal("Parse returned a nil document")
	}
	if doc.Kind != KindApp {
		t.Fatalf("Kind = %q, want %q", doc.Kind, KindApp)
	}
	if doc.Entry != "dashboard" {
		t.Fatalf("Entry = %q, want dashboard", doc.Entry)
	}
	if got := doc.Screens["dashboard"].Name; got != "dashboard-root" {
		t.Fatalf("screen root name = %q, want dashboard-root", got)
	}
}

func TestParseRejectsUnsupportedYAMLFeatures(t *testing.T) {
	t.Parallel()

	tests := map[string]string{
		"alias": `
gora: 1
kind: app
viewport: &viewport
  width: 800
  height: 600
entry: main
screens:
  main:
    type: surface
    props:
      copy: *viewport
`,
		"custom tag": `
gora: 1
kind: tokens
tokens:
  color:
    danger: !color "#ff0000"
`,
		"multiple documents": `
gora: 1
kind: tokens
tokens: {}
---
gora: 1
kind: tokens
tokens: {}
`,
		"timestamp": `
gora: 1
kind: tokens
tokens:
  text:
    created: 2026-07-30
`,
	}

	for name, src := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			_, diagnostics := Parse(name+".gora", []byte(src))
			if len(diagnostics) == 0 {
				t.Fatal("Parse accepted unsupported YAML")
			}
		})
	}
}

func TestParseRejectsUnknownFieldWithSuggestion(t *testing.T) {
	t.Parallel()

	src := []byte(`
gora: 1
kind: app
viewprt:
  width: 800
  height: 600
entry: main
screens:
  main:
    type: surface
`)

	_, diagnostics := Parse("unknown.gora", src)
	if len(diagnostics) == 0 {
		t.Fatal("Parse accepted unknown field")
	}
	if got := diagnostics[0].Line; got != 4 {
		t.Fatalf("diagnostic line = %d, want 4", got)
	}
	if joined := strings.Join(diagnostics[0].Suggestions, ","); !strings.Contains(joined, "viewport") {
		t.Fatalf("suggestions = %q, want viewport", joined)
	}
}

func TestParseRejectsDuplicateNodeNames(t *testing.T) {
	t.Parallel()

	src := []byte(`
gora: 1
kind: app
viewport:
  width: 800
  height: 600
entry: main
screens:
  main:
    type: stack
    children:
      - type: text
        name: title
        props:
          content: One
      - type: text
        name: title
        props:
          content: Two
`)

	_, diagnostics := Parse("duplicates.gora", src)
	if len(diagnostics) == 0 {
		t.Fatal("Parse accepted duplicate authored names")
	}
}

func TestParseScrollOneAxisBaselineContract(t *testing.T) {
	tests := []struct {
		name          string
		props         string
		wantAxis      string
		wantScrollbar any
	}{
		{name: "default vertical", props: "", wantAxis: ""},
		{name: "vertical", props: "axis: vertical", wantAxis: "vertical"},
		{name: "horizontal", props: "axis: horizontal", wantAxis: "horizontal"},
		{name: "legacy scrollbar true", props: "axis: vertical\nscrollbar: true", wantAxis: "vertical", wantScrollbar: true},
		{name: "legacy scrollbar false", props: "axis: horizontal\nscrollbar: false", wantAxis: "horizontal", wantScrollbar: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			source := "gora: 1\nkind: app\nviewport: { width: 100, height: 80 }\nentry: main\nscreens:\n  main:\n    type: scroll\n"
			if test.props != "" {
				source += "    props:\n      " + strings.ReplaceAll(test.props, "\n", "\n      ") + "\n"
			}
			source += "    children: [{ type: spacer, props: { height: 200 } }]\n"
			doc, diagnostics := Parse(test.name+".gora", []byte(source))
			if len(diagnostics) != 0 {
				t.Fatalf("Parse returned diagnostics: %+v", diagnostics)
			}
			if doc == nil || doc.Screens["main"] == nil {
				t.Fatal("Parse returned no main scroll node")
			}
			scroll := doc.Screens["main"]
			if got := testStringValue(scroll.Props["axis"], ""); got != test.wantAxis {
				t.Fatalf("authored axis = %q, want %q", got, test.wantAxis)
			}
			if got, exists := scroll.Props["scrollbar"]; exists {
				if got != test.wantScrollbar {
					t.Fatalf("legacy scrollbar = %#v, want %#v", got, test.wantScrollbar)
				}
			} else if test.wantScrollbar != nil {
				t.Fatalf("legacy scrollbar missing, want %#v", test.wantScrollbar)
			}
		})
	}
}

func TestParseScrollRejectsMultipleChildren(t *testing.T) {
	_, diagnostics := Parse("scroll-children.gora", []byte(`
gora: 1
kind: app
viewport: { width: 100, height: 80 }
entry: main
screens:
  main:
    type: scroll
    children:
      - { type: spacer, props: { height: 100 } }
      - { type: spacer, props: { height: 100 } }
`))
	for _, diagnostic := range diagnostics {
		if diagnostic.Code == "schema.children" {
			return
		}
	}
	t.Fatalf("Parse accepted multiple scroll children: %+v", diagnostics)
}

func testStringValue(value any, fallback string) string {
	text, ok := value.(string)
	if !ok {
		return fallback
	}
	return text
}
