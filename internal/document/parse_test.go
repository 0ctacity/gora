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
