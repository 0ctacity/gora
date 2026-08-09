package project

import (
	"path/filepath"
	"testing"

	"gora/internal/document"
)

func TestLoadNormalizesScrollPoliciesAndPreservesAuthoredProps(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "app.gora"), `
gora: 1
kind: app
viewport: { width: 800, height: 600 }
entry: main
screens:
  main:
    type: scroll
    props:
      scrollbar: true
    children: [{ type: surface }]
`)
	loaded, diagnostics := Load(root, filepath.Join(root, "app.gora"), 800)
	if len(diagnostics) != 0 {
		t.Fatalf("Load returned diagnostics: %+v", diagnostics)
	}
	scroll := loaded.Screens["main"]
	if got := scroll.Props["scrollbar"]; got != true {
		t.Fatalf("authored legacy scrollbar = %#v, want true", got)
	}
	if got := scroll.Props["axis"]; got != "vertical" {
		t.Fatalf("effective axis = %#v, want vertical", got)
	}
	if got := scroll.Props["scrollbar_x"]; got != "hidden" {
		t.Fatalf("effective scrollbar_x = %#v, want hidden", got)
	}
	if got := scroll.Props["scrollbar_y"]; got != "auto" {
		t.Fatalf("effective scrollbar_y = %#v, want auto", got)
	}
	if got := scroll.Props["scroll_chain"]; got != "auto" {
		t.Fatalf("effective scroll_chain = %#v, want auto", got)
	}
}

func TestLoadNormalizesModernAndLegacyPoliciesAcrossResponsiveOverrides(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "app.gora"), `
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
      axis: vertical
      scrollbar_x: hidden
      scrollbar_y: auto
    responsive:
      narrow:
        props:
          axis: both
          scrollbar_x: always
    children: [{ type: surface }]
`)
	wide, diagnostics := Load(root, filepath.Join(root, "app.gora"), 800)
	if len(diagnostics) != 0 {
		t.Fatalf("wide Load returned diagnostics: %+v", diagnostics)
	}
	narrow, diagnostics := Load(root, filepath.Join(root, "app.gora"), 400)
	if len(diagnostics) != 0 {
		t.Fatalf("narrow Load returned diagnostics: %+v", diagnostics)
	}
	wideProps := wide.Screens["main"].Props
	if wideProps["scrollbar_x"] != "hidden" || wideProps["scrollbar_y"] != "auto" {
		t.Fatalf("wide policies = %#v, want hidden/auto", wideProps)
	}
	narrowProps := narrow.Screens["main"].Props
	if narrowProps["axis"] != "both" || narrowProps["scrollbar_x"] != "always" || narrowProps["scrollbar_y"] != "auto" {
		t.Fatalf("narrow effective policies = %#v, want both/always/auto", narrowProps)
	}
}

func TestLoadNormalizesPoliciesAfterComponentExpansion(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "card.gora"), `
gora: 1
kind: component
viewport: { width: 320, height: 200 }
previews:
  default: {}
root:
  type: scroll
  props: { axis: both, scrollbar: false }
  place:
    position: sticky
    z_index: 4
    inset: { top: null, right: null, bottom: null, left: null }
  children: [{ type: surface }]
`)
	writeFile(t, filepath.Join(root, "app.gora"), `
gora: 1
kind: app
imports: { components: { card: card.gora } }
viewport: { width: 800, height: 600 }
entry: main
screens:
  main:
    type: instance
    props: { component: card }
`)
	loaded, diagnostics := Load(root, filepath.Join(root, "app.gora"), 800)
	if len(diagnostics) != 0 {
		t.Fatalf("Load returned diagnostics: %+v", diagnostics)
	}
	scroll := loaded.Screens["main"]
	if scroll.Type != "scroll" {
		t.Fatalf("resolved component type = %q, want scroll", scroll.Type)
	}
	if scroll.Props["scrollbar"] != false || scroll.Props["scrollbar_x"] != "hidden" || scroll.Props["scrollbar_y"] != "hidden" {
		t.Fatalf("expanded effective props = %#v, want authored false and hidden/hidden", scroll.Props)
	}
	if scroll.Place["position"] != "sticky" || scroll.Place["z_index"] != int64(4) {
		t.Fatalf("expanded positioned place = %#v, want sticky/z_index 4", scroll.Place)
	}
}

func TestLoadRejectsResolvedParameterAndTokenPhase1Types(t *testing.T) {
	t.Run("parameter enum and z index", func(t *testing.T) {
		root := t.TempDir()
		writeFile(t, filepath.Join(root, "card.gora"), `
gora: 1
kind: component
viewport: { width: 320, height: 200 }
parameters:
  axis: { type: text, required: true }
  z: { type: number, required: true }
previews:
  default: { parameters: { axis: diagonal, z: 1.5 } }
root:
  type: scroll
  props: { axis: { ref: parameter.axis } }
  place:
    position: sticky
    z_index: { ref: parameter.z }
  children: [{ type: surface }]
`)
		_, diagnostics := Load(root, filepath.Join(root, "card.gora"), 800)
		if !hasDiagnosticCode(diagnostics, "reference.type") {
			t.Fatalf("missing resolved parameter type diagnostic: %+v", diagnostics)
		}
	})

	t.Run("token axis", func(t *testing.T) {
		root := t.TempDir()
		writeFile(t, filepath.Join(root, "theme.gora"), `
gora: 1
kind: tokens
tokens:
  color:
    axis: "#123456"
`)
		writeFile(t, filepath.Join(root, "card.gora"), `
gora: 1
kind: component
imports: { tokens: { theme: theme.gora } }
viewport: { width: 320, height: 200 }
previews:
  default: {}
root:
  type: scroll
  props: { axis: { ref: theme.color.axis } }
  children: [{ type: surface }]
`)
		_, diagnostics := Load(root, filepath.Join(root, "card.gora"), 800)
		if !hasDiagnosticCode(diagnostics, "reference.type") {
			t.Fatalf("missing resolved token type diagnostic: %+v", diagnostics)
		}
	})
}

func TestLoadValidatesEffectiveResponsivePosition(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "app.gora"), `
gora: 1
kind: app
viewport: { width: 800, height: 600 }
breakpoints:
  narrow: { max_width: 500 }
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
        place:
          position: flow
`)
	for _, width := range []int{800, 400} {
		_, diagnostics := Load(root, filepath.Join(root, "app.gora"), width)
		if !hasDiagnosticCode(diagnostics, "schema.z_index") && !hasDiagnosticCode(diagnostics, "reference.type") {
			t.Fatalf("missing effective flow z_index diagnostic at width %d: %+v", width, diagnostics)
		}
	}
}

func TestLoadRejectsEffectiveResponsiveScrollbarMixing(t *testing.T) {
	tests := []struct {
		name            string
		baseProps       string
		responsiveProps string
	}{
		{
			name:            "legacy base with per axis override",
			baseProps:       "scrollbar: true",
			responsiveProps: "scrollbar_x: auto",
		},
		{
			name:            "per axis base with legacy override",
			baseProps:       "scrollbar_x: hidden\n      scrollbar_y: auto",
			responsiveProps: "scrollbar: true",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			source := `
gora: 1
kind: app
viewport: { width: 800, height: 600 }
breakpoints: { narrow: { max_width: 500 } }
entry: main
screens:
  main:
    type: scroll
    props:
      ` + test.baseProps + `
    responsive:
      narrow:
        props:
          ` + test.responsiveProps + `
    children: [{ type: surface }]
`
			writeFile(t, filepath.Join(root, "app.gora"), source)
			_, diagnostics := Load(root, filepath.Join(root, "app.gora"), 400)
			if len(diagnostics) == 0 {
				t.Fatalf("Load accepted effective legacy/per-axis mix")
			}
		})
	}
}

func TestLoadRejectsStateReferencesForStructuralScrollPolicies(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "app.gora"), `
gora: 1
kind: app
viewport: { width: 800, height: 600 }
state:
  policy: { type: number, default: 1 }
  chain: { type: boolean, default: false }
entry: main
screens:
  main:
    type: scroll
    props:
      scrollbar_x: { ref: state.policy }
      scroll_chain: { ref: state.chain }
    children: [{ type: surface }]
`)
	_, diagnostics := Load(root, filepath.Join(root, "app.gora"), 800)
	if !hasDiagnosticCode(diagnostics, "reference.type") {
		t.Fatalf("missing structural state-reference diagnostics: %+v", diagnostics)
	}
}

func TestLoadRejectsOutOfRangeFloatZIndex(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "card.gora"), `
gora: 1
kind: component
viewport: { width: 320, height: 200 }
parameters:
  z: { type: number, default: 1e100 }
previews: { default: {} }
root:
  type: surface
  place:
    position: sticky
    z_index: { ref: parameter.z }
`)
	_, diagnostics := Load(root, filepath.Join(root, "card.gora"), 800)
	if !hasDiagnosticCode(diagnostics, "reference.type") {
		t.Fatalf("missing out-of-range z_index diagnostic: %+v", diagnostics)
	}
}

func TestLoadAcceptsResponsiveZIndexOverrideWithInheritedPosition(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "app.gora"), `
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
    responsive:
      narrow:
        place: { z_index: 3 }
`)
	loaded, diagnostics := Load(root, filepath.Join(root, "app.gora"), 400)
	if len(diagnostics) != 0 {
		t.Fatalf("Load rejected responsive z_index with inherited position: %+v", diagnostics)
	}
	if got := loaded.Screens["main"].Place["z_index"]; got != int64(3) {
		t.Fatalf("responsive z_index = %#v, want 3", got)
	}
	if _, diagnostics := Load(root, filepath.Join(root, "app.gora"), 800); len(diagnostics) != 0 {
		t.Fatalf("inactive responsive z_index override changed the base node: %+v", diagnostics)
	}
}

func hasDiagnosticCode(diagnostics []document.Diagnostic, code string) bool {
	for _, diagnostic := range diagnostics {
		if diagnostic.Code == code {
			return true
		}
	}
	return false
}
