package document

import (
	"strings"
	"testing"
)

func TestRejectsUnknownPrimitiveWithSuggestion(t *testing.T) {
	_, diagnostics := Parse("app.gora", []byte(`
gora: 1
kind: app
viewport: { width: 800, height: 600 }
entry: main
screens:
  main:
    type: suface
`))
	requireDiagnostic(t, diagnostics, "schema.node_type", "surface")
}

func TestRejectsInvalidChildCardinality(t *testing.T) {
	_, diagnostics := Parse("app.gora", []byte(`
gora: 1
kind: app
viewport: { width: 800, height: 600 }
entry: main
screens:
  main:
    type: surface
    children:
      - { type: spacer }
      - { type: spacer }
`))
	requireDiagnostic(t, diagnostics, "schema.children", "")
}

func TestRejectsUnknownResponsiveBreakpoint(t *testing.T) {
	_, diagnostics := Parse("app.gora", []byte(`
gora: 1
kind: app
viewport: { width: 800, height: 600 }
breakpoints:
  compact: { max_width: 600 }
entry: main
screens:
  main:
    type: text
    responsive:
      compat:
        visible: false
`))
	requireDiagnostic(t, diagnostics, "responsive.breakpoint", "compact")
}

func TestRejectsUnsupportedParameterType(t *testing.T) {
	_, diagnostics := Parse("card.gora", []byte(`
gora: 1
kind: component
viewport: { width: 320, height: 200 }
parameters:
  title: { type: strng }
root: { type: text }
`))
	requireDiagnostic(t, diagnostics, "parameter.type", "string")
}

func TestRejectsInvalidSizingAndOpacity(t *testing.T) {
	_, diagnostics := Parse("app.gora", []byte(`
gora: 1
kind: app
viewport: { width: 800, height: 600 }
entry: main
screens:
  main:
    type: stack
    props: { gap: -1, opacity: 2 }
    children:
      - type: spacer
        props: { width: huge }
`))
	requireDiagnostic(t, diagnostics, "schema.number_range", "")
	requireDiagnostic(t, diagnostics, "schema.size", "")
}

func requireDiagnostic(t *testing.T, diagnostics []Diagnostic, code, suggestion string) {
	t.Helper()
	for _, diagnostic := range diagnostics {
		if diagnostic.Code != code {
			continue
		}
		if suggestion == "" {
			return
		}
		for _, got := range diagnostic.Suggestions {
			if got == suggestion {
				return
			}
		}
		t.Fatalf("diagnostic %s has suggestions %v, want %q", code, diagnostic.Suggestions, suggestion)
	}
	var got []string
	for _, diagnostic := range diagnostics {
		got = append(got, diagnostic.Code+":"+diagnostic.Message)
	}
	t.Fatalf("missing diagnostic %s (%s): %s", code, suggestion, strings.Join(got, ", "))
}
