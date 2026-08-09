package document

import "testing"

func TestAcceptsPhase1ScrollAndPositionSchema(t *testing.T) {
	_, diagnostics := Parse("phase1.gora", []byte(`
gora: 1
kind: app
viewport: { width: 800, height: 600 }
breakpoints:
  narrow: { max_width: 500 }
entry: main
screens:
  main:
    type: scroll
    props:
      axis: both
      scrollbar_x: always
      scrollbar_y: auto
      scroll_chain: contain
    place:
      position: sticky
      inset: { top: -10, right: null, bottom: { percent: 25 }, left: { percent: -5 } }
      z_index: -2
    responsive:
      narrow:
        props:
          axis: horizontal
          scrollbar_x: auto
          scrollbar_y: hidden
          scroll_chain: auto
        place:
          position: fixed
          inset: { top: 0, right: null, bottom: null, left: 0 }
          z_index: 3
    children:
      - type: surface
`))
	if len(diagnostics) != 0 {
		t.Fatalf("Parse returned diagnostics: %+v", diagnostics)
	}
}

func TestRejectsInvalidPhase1ScrollAndPositionSchema(t *testing.T) {
	tests := []struct {
		name string
		src  string
		code string
	}{
		{
			name: "mixed scrollbar policies",
			src: `
gora: 1
kind: app
viewport: { width: 800, height: 600 }
entry: main
screens:
  main:
    type: scroll
    props: { axis: both, scrollbar: true, scrollbar_x: auto }
    children: [{ type: surface }]
`,
			code: "schema.scrollbar_policy",
		},
		{
			name: "disabled axis policy",
			src: `
gora: 1
kind: app
viewport: { width: 800, height: 600 }
entry: main
screens:
  main:
    type: scroll
    props: { axis: vertical, scrollbar_x: auto, scroll_chain: never }
    children: [{ type: surface }]
`,
			code: "schema.scrollbar_policy",
		},
		{
			name: "unknown policy suggestion",
			src: `
gora: 1
kind: app
viewport: { width: 800, height: 600 }
entry: main
screens:
  main:
    type: scroll
    props: { axis: both, scrollbarx: auto }
    children: [{ type: surface }]
`,
			code: "schema.prop",
		},
		{
			name: "fractional z index",
			src: `
gora: 1
kind: app
viewport: { width: 800, height: 600 }
entry: main
screens:
  main:
    type: surface
    place: { position: sticky, z_index: 1.5, inset: { top: null, right: null, bottom: null, left: null } }
`,
			code: "schema.z_index",
		},
		{
			name: "flow z index",
			src: `
gora: 1
kind: app
viewport: { width: 800, height: 600 }
entry: main
screens:
  main:
    type: surface
    place: { position: flow, z_index: 1 }
`,
			code: "schema.z_index",
		},
		{
			name: "inset exact keys and values",
			src: `
gora: 1
kind: app
viewport: { width: 800, height: 600 }
entry: main
screens:
  main:
    type: surface
    place: { position: fixed, inset: { top: null, right: { percent: -4, extra: 1 }, bottom: 2 } }
`,
			code: "schema.position_inset",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, diagnostics := Parse("invalid-phase1.gora", []byte(test.src))
			requireDiagnostic(t, diagnostics, test.code, "")
		})
	}
}

func TestAllowsDynamicPhase1ValuesForResolvedValidation(t *testing.T) {
	_, diagnostics := Parse("dynamic-phase1.gora", []byte(`
gora: 1
kind: component
viewport: { width: 800, height: 600 }
parameters:
  axis: { type: text, default: both }
  policy: { type: text, default: auto }
  z: { type: number, default: 1 }
previews:
  default: {}
root:
  type: scroll
  props:
    axis: { ref: parameter.axis }
    scrollbar_x: { ref: parameter.policy }
    scrollbar_y: hidden
  place:
    position: sticky
    inset: { top: { percent: { ref: parameter.z } }, right: null, bottom: null, left: null }
    z_index: { ref: parameter.z }
  children: [{ type: surface }]
`))
	if len(diagnostics) != 0 {
		t.Fatalf("Parse returned diagnostics: %+v", diagnostics)
	}
}

func TestAllowsPositionReferenceWithPositionedZIndex(t *testing.T) {
	_, diagnostics := Parse("dynamic-position.gora", []byte(`
gora: 1
kind: component
viewport: { width: 800, height: 600 }
parameters:
  position: { type: text, default: sticky }
previews:
  default: {}
root:
  type: surface
  place:
    position: { ref: parameter.position }
    z_index: 3
`))
	if len(diagnostics) != 0 {
		t.Fatalf("Parse returned diagnostics: %+v", diagnostics)
	}
}

func TestResponsiveScrollPolicyUsesEffectiveBaseAxis(t *testing.T) {
	_, diagnostics := Parse("responsive-scroll-policy.gora", []byte(`
gora: 1
kind: app
viewport: { width: 800, height: 600 }
breakpoints: { narrow: { max_width: 500 } }
entry: main
screens:
  main:
    type: scroll
    props:
      axis: both
      scrollbar_x: auto
      scrollbar_y: auto
    responsive:
      narrow:
        props:
          scrollbar_x: always
    children: [{ type: surface }]
`))
	if len(diagnostics) != 0 {
		t.Fatalf("Parse returned diagnostics: %+v", diagnostics)
	}
}

func TestResponsivePlacementValidatesAgainstEffectiveBase(t *testing.T) {
	tests := []struct {
		name       string
		responsive string
		wantError  bool
	}{
		{
			name: "z index inherits sticky position",
			responsive: `
        place: { z_index: 3 }
`,
		},
		{
			name: "flow override inherits base z index",
			responsive: `
        place: { position: flow }
`,
			wantError: true,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			source := `
gora: 1
kind: app
viewport: { width: 800, height: 600 }
breakpoints: { narrow: { max_width: 500 } }
entry: main
screens:
  main:
    type: surface
    place:
      position: sticky
      z_index: 2
      inset: { top: null, right: null, bottom: null, left: null }
    responsive:
      narrow:
` + test.responsive
			_, diagnostics := Parse("responsive-position.gora", []byte(source))
			if test.wantError {
				requireDiagnostic(t, diagnostics, "schema.z_index", "")
			} else if len(diagnostics) != 0 {
				t.Fatalf("Parse returned diagnostics: %+v", diagnostics)
			}
		})
	}
}

func TestRejectsOutOfRangeFloatZIndex(t *testing.T) {
	_, diagnostics := Parse("large-z-index.gora", []byte(`
gora: 1
kind: app
viewport: { width: 800, height: 600 }
entry: main
screens:
  main:
    type: surface
    place: { position: sticky, z_index: 1e100 }
`))
	requireDiagnostic(t, diagnostics, "schema.z_index", "")
}

func TestScrollUnknownFieldSuggestsScrollbarX(t *testing.T) {
	_, diagnostics := Parse("scroll-suggestion.gora", []byte(`
gora: 1
kind: app
viewport: { width: 800, height: 600 }
entry: main
screens:
  main:
    type: scroll
    props: { scrollbarx: auto }
    children: [{ type: surface }]
`))
	for _, diagnostic := range diagnostics {
		if diagnostic.Code != "schema.prop" {
			continue
		}
		for _, suggestion := range diagnostic.Suggestions {
			if suggestion == "scrollbar_x" {
				return
			}
		}
	}
	t.Fatalf("missing scrollbar_x suggestion: %+v", diagnostics)
}
