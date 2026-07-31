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

func TestAcceptsPercentageDimensionsAspectRatioAndWrapping(t *testing.T) {
	_, diagnostics := Parse("layout.gora", []byte(`
gora: 1
kind: component
viewport: { width: 900, height: 600 }
parameters:
  card_width:
    type: dimension
    default: { percent: 30 }
previews:
  default: {}
root:
  type: stack
  props:
    direction: horizontal
    wrap: true
    row_gap: 20
    column_gap: 12
  children:
    - type: surface
      props:
        width: { percent: 50 }
        min_width: { percent: 25 }
        aspect_ratio: { width: 16, height: 9 }
      place:
        basis: { ref: parameter.card_width }
        grow: 1
        shrink: 1
        alignment: center
`))
	if len(diagnostics) != 0 {
		t.Fatalf("Parse returned diagnostics: %+v", diagnostics)
	}
}

func TestAcceptsPercentageDimensionToken(t *testing.T) {
	_, diagnostics := Parse("theme.gora", []byte(`
gora: 1
kind: tokens
tokens:
  dimension:
    half: { percent: 50 }
`))
	if len(diagnostics) != 0 {
		t.Fatalf("Parse returned diagnostics: %+v", diagnostics)
	}
}

func TestRejectsMalformedPercentageAndAspectRatio(t *testing.T) {
	_, diagnostics := Parse("layout.gora", []byte(`
gora: 1
kind: app
viewport: { width: 800, height: 600 }
entry: main
screens:
  main:
    type: stack
    props:
      wrap: yes
    children:
      - type: surface
        props:
          width: { percent: -1, extra: 2 }
          aspect_ratio: { width: 16, height: 0 }
        place:
          basis: fill
          shrink: -1
          alignment: top_left
`))
	requireDiagnostic(t, diagnostics, "schema.size", "")
	requireDiagnostic(t, diagnostics, "schema.aspect_ratio", "")
	requireDiagnostic(t, diagnostics, "schema.prop_value", "")
	requireDiagnostic(t, diagnostics, "schema.number_range", "")
}

func TestRejectsMalformedResponsiveLayoutValues(t *testing.T) {
	_, diagnostics := Parse("responsive.gora", []byte(`
gora: 1
kind: app
viewport: { width: 800, height: 600 }
breakpoints:
  narrow: { max_width: 600 }
entry: main
screens:
  main:
    type: stack
    responsive:
      narrow:
        props:
          wrap: sometimes
          row_gap: -1
        place:
          basis: fill
`))
	for _, code := range []string{"schema.prop_value", "schema.number_range"} {
		requireDiagnostic(t, diagnostics, code, "")
	}
}

func TestAcceptsStateButtonActionsAndVariants(t *testing.T) {
	doc, diagnostics := Parse("interaction.gora", []byte(`
gora: 1
kind: app
viewport: { width: 400, height: 300 }
state:
  expanded: { type: boolean, default: false }
  seats: { type: number, default: 3 }
  plan: { type: enum, values: [monthly, annual], default: monthly }
entry: main
screens:
  main:
    type: button
    props: { label: Add seat, background: "#172033", disabled: false }
    on:
      activate:
        - { action: increment, state: seats, by: 1 }
        - { action: set, state: plan, value: annual }
    variants:
      - { when: { state: seats, greater_than_or_equal: 10 }, props: { opacity: 0.6 } }
      - { when: { interaction: hovered }, props: { background: "#252F46" } }
    children:
      - { type: text, props: { text: { ref: state.seats } } }
`))
	if len(diagnostics) != 0 {
		t.Fatalf("Parse diagnostics: %+v", diagnostics)
	}
	if len(doc.State) != 3 || len(doc.Screens["main"].On.Activate) != 2 || len(doc.Screens["main"].Variants) != 2 {
		t.Fatalf("parsed interaction document = %+v", doc)
	}
}

func TestRejectsInvalidStateAndButtonContracts(t *testing.T) {
	_, diagnostics := Parse("invalid-interaction.gora", []byte(`
gora: 1
kind: app
viewport: { width: 400, height: 300 }
state:
  count: { type: number, default: nope }
entry: main
screens:
  main:
    type: button
    props: { label: "" }
    on:
      activate:
        - { action: toggle, state: count }
    variants:
      - { when: { interaction: hovered }, props: { width: 20 } }
    children: []
`))
	for _, code := range []string{"state.default", "action.type", "variant.transient", "button.label", "schema.children"} {
		requireDiagnostic(t, diagnostics, code, "")
	}
}

func TestRejectsAmbiguousInteractionShapes(t *testing.T) {
	_, diagnostics := Parse("ambiguous.gora", []byte(`
gora: 1
kind: app
viewport: { width: 100, height: 100 }
state:
  mode: { type: enum, values: [a, a], default: a }
entry: main
screens:
  main:
    type: button
    props: { label: Save, disabled: nope }
    on:
      activate:
        - { action: toggle, state: mode, value: a }
    variants:
      - when: { state: mode, interaction: hovered, equals: a, not_equals: b }
        props: { background: "#000000" }
    children:
      - { type: text, props: { content: Save } }
`))
	want := map[string]bool{
		"state.enum_duplicate": false,
		"button.disabled":      false,
		"action.fields":        false,
		"variant.condition":    false,
	}
	for _, diagnostic := range diagnostics {
		if _, ok := want[diagnostic.Code]; ok {
			want[diagnostic.Code] = true
		}
	}
	for code, found := range want {
		if !found {
			t.Fatalf("missing %s diagnostic: %+v", code, diagnostics)
		}
	}
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
