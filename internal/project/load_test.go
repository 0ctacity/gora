package project

import (
	"image"
	"image/png"
	"os"
	"path/filepath"
	"testing"

	"gora/internal/document"
)

func TestLoadResolvesComponentsTokensParametersAndResponsiveProps(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	writeFile(t, filepath.Join(root, "theme.gora"), `
gora: 1
kind: tokens
tokens:
  color:
    text: "#112233"
`)
	writeFile(t, filepath.Join(root, "components", "card.gora"), `
gora: 1
kind: component
name: Card
imports:
  tokens:
    theme: ../theme.gora
viewport:
  width: 320
  height: 180
parameters:
  title:
    type: text
    required: true
slots:
  footer:
    required: false
previews:
  default:
    parameters:
      title: Preview
root:
  type: stack
  name: card-root
  props:
    direction: vertical
  children:
    - type: text
      name: title
      props:
        content: { ref: parameter.title }
        color: { ref: theme.color.text }
    - type: slot
      props:
        name: footer
`)
	writeFile(t, filepath.Join(root, "app.gora"), `
gora: 1
kind: app
name: App
imports:
  components:
    card: ./components/card.gora
viewport:
  width: 900
  height: 600
breakpoints:
  compact:
    max_width: 699
  wide:
    min_width: 700
entry: main
screens:
  main:
    type: instance
    name: revenue-card
    props:
      component: card
      parameters:
        title: Revenue
    responsive:
      compact:
        props:
          opacity: 0.5
    children:
      - type: slot_content
        props:
          slot: footer
        children:
          - type: text
            props:
              content: Updated today
`)

	loaded, diagnostics := Load(root, filepath.Join(root, "app.gora"), 640)
	if len(diagnostics) != 0 {
		t.Fatalf("Load returned diagnostics: %+v", diagnostics)
	}
	screen := loaded.Screens["main"]
	if screen.Type != "stack" {
		t.Fatalf("resolved root type = %q, want stack", screen.Type)
	}
	if got := screen.Props["opacity"]; got != float64(0.5) {
		t.Fatalf("responsive opacity = %#v, want 0.5", got)
	}
	if got := screen.Children[0].Props["content"]; got != "Revenue" {
		t.Fatalf("parameter content = %#v, want Revenue", got)
	}
	if got := screen.Children[0].Props["color"]; got != "#112233" {
		t.Fatalf("token color = %#v, want #112233", got)
	}
	if got := screen.Children[1].Props["content"]; got != "Updated today" {
		t.Fatalf("slot content = %#v, want Updated today", got)
	}
	if len(screen.Breadcrumb) != 1 || screen.Breadcrumb[0] != "revenue-card" {
		t.Fatalf("breadcrumb = %#v, want [revenue-card]", screen.Breadcrumb)
	}
}

func TestLoadRejectsDuplicateResolvedChoiceValues(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "chooser.gora"), `
gora: 1
kind: component
parameters:
  first: { type: text, required: true }
  second: { type: text, required: true }
state:
  choice: { type: text, default: a }
viewport: { width: 300, height: 200 }
previews:
  default: { parameters: { first: a, second: b } }
root:
  type: radio_group
  name: choices
  props: { label: Choices, bind: choice }
  children:
    - type: radio
      name: first-choice
      props: { label: First, value: { ref: parameter.first } }
      children: [{ type: text, props: { content: First } }]
    - type: radio
      name: second-choice
      props: { label: Second, value: { ref: parameter.second } }
      children: [{ type: text, props: { content: Second } }]
`)
	writeFile(t, filepath.Join(root, "app.gora"), `
gora: 1
kind: app
imports: { components: { chooser: chooser.gora } }
viewport: { width: 600, height: 400 }
entry: main
screens:
  main:
    type: instance
    name: chooser-instance
    props: { component: chooser, parameters: { first: same, second: same } }
`)
	_, diagnostics := Load(root, filepath.Join(root, "app.gora"), 600)
	for _, diagnostic := range diagnostics {
		if diagnostic.Code == "control.value_duplicate" {
			return
		}
	}
	t.Fatalf("missing duplicate resolved choice diagnostic: %+v", diagnostics)
}

func TestLoadValidatesResolvedFieldProps(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "field.gora"), `
gora: 1
kind: component
viewport: { width: 320, height: 180 }
parameters:
  pattern: { type: text, required: true }
  minimum: { type: number, required: true }
state:
  value: { type: text, default: Ada }
previews:
  default: { parameters: { pattern: '[', minimum: 2.5 } }
root:
  type: text_field
  name: value-field
  props:
    label: Value
    bind: value
    pattern: { ref: parameter.pattern }
    min_length: { ref: parameter.minimum }
  children:
    - type: field_box
`)

	_, diagnostics := Load(root, filepath.Join(root, "field.gora"), 320)
	for _, code := range []string{"field.pattern", "field.length"} {
		found := false
		for _, diagnostic := range diagnostics {
			if diagnostic.Code == code {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("missing %s diagnostic: %+v", code, diagnostics)
		}
	}
}

func TestLoadResolvesPercentageDimensionsFromTokensAndParameters(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	writeFile(t, filepath.Join(root, "theme.gora"), `
gora: 1
kind: tokens
tokens:
  dimension:
    half: { percent: 50 }
`)
	writeFile(t, filepath.Join(root, "card.gora"), `
gora: 1
kind: component
viewport: { width: 400, height: 240 }
parameters:
  basis:
    type: dimension
    default: { percent: 30 }
previews:
  default: {}
root:
  type: surface
  place:
    basis: { ref: parameter.basis }
`)
	writeFile(t, filepath.Join(root, "app.gora"), `
gora: 1
kind: app
imports:
  components:
    card: ./card.gora
  tokens:
    theme: ./theme.gora
viewport: { width: 800, height: 600 }
entry: main
screens:
  main:
    type: stack
    children:
      - type: instance
        props:
          component: card
          width: { ref: theme.dimension.half }
`)

	loaded, diagnostics := Load(root, filepath.Join(root, "app.gora"), 800)
	if len(diagnostics) != 0 {
		t.Fatalf("Load returned diagnostics: %+v", diagnostics)
	}
	child := loaded.Screens["main"].Children[0]
	if got := child.Props["width"]; !samePercent(got, 50) {
		t.Fatalf("resolved token width = %#v, want 50 percent", got)
	}
	if got := child.Place["basis"]; !samePercent(got, 30) {
		t.Fatalf("resolved parameter basis = %#v, want 30 percent", got)
	}
}

func TestLoadBindsStateScopesAcrossComponentAndSlotBoundaries(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	writeFile(t, filepath.Join(root, "counter.gora"), `
gora: 1
kind: component
viewport: { width: 320, height: 180 }
state:
  count: { type: number, default: 1 }
previews:
  five:
    state: { count: 5 }
root:
  type: stack
  children:
    - type: text
      props: { content: { ref: state.count } }
    - type: slot
      props: { name: default }
`)
	writeFile(t, filepath.Join(root, "app.gora"), `
gora: 1
kind: app
imports:
  components: { counter: counter.gora }
viewport: { width: 800, height: 600 }
state:
  title: { type: text, default: Caller }
entry: main
screens:
  main:
    type: instance
    name: primary-counter
    props: { component: counter }
    children:
      - type: slot_content
        children:
          - type: text
            props: { content: { ref: state.title } }
`)

	loaded, diagnostics := Load(root, filepath.Join(root, "app.gora"), 800)
	if len(diagnostics) != 0 {
		t.Fatalf("Load returned diagnostics: %+v", diagnostics)
	}
	if len(loaded.StateScopes) != 2 {
		t.Fatalf("state scopes = %+v", loaded.StateScopes)
	}
	componentText := loaded.Screens["main"].Children[0].Props["content"]
	if componentText != (StateReference{Scope: "screen:main/primary-counter", Name: "count"}) {
		t.Fatalf("component ref = %#v", componentText)
	}
	slotText := loaded.Screens["main"].Children[1].Props["content"]
	if slotText != (StateReference{Scope: "screen:main", Name: "title"}) {
		t.Fatalf("slot ref = %#v", slotText)
	}
}

func TestLoadKeepsCallerFieldScopeInsideComponentFormSlot(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "profile.gora"), `
gora: 1
kind: component
viewport: { width: 320, height: 180 }
state:
  local_name: { type: text, default: Local }
slots:
  default: { required: true }
previews:
  default:
    children:
      - type: slot_content
        props: { slot: default }
        children: [{ type: spacer }]
root:
  type: form
  name: profile-form
  children:
    - type: stack
      children:
        - type: text_field
          name: local-field
          props: { label: Local, bind: local_name }
          children: [{ type: field_box }]
        - type: slot
          props: { name: default }
`)
	writeFile(t, filepath.Join(root, "app.gora"), `
gora: 1
kind: app
imports: { components: { profile: profile.gora } }
viewport: { width: 800, height: 600 }
state:
  caller_name: { type: text, default: Caller }
entry: main
screens:
  main:
    type: instance
    name: profile
    props: { component: profile }
    children:
      - type: slot_content
        children:
          - type: text_field
            name: caller-field
            props: { label: Caller, bind: caller_name }
            children: [{ type: field_box }]
`)
	loaded, diagnostics := Load(root, filepath.Join(root, "app.gora"), 800)
	if len(diagnostics) != 0 {
		t.Fatalf("Load returned diagnostics: %+v", diagnostics)
	}
	byName := make(map[string]*Node)
	var walk func(*Node)
	walk = func(node *Node) {
		if node == nil {
			return
		}
		if node.Name != "" {
			byName[node.Name] = node
		}
		for _, child := range node.Children {
			walk(child)
		}
	}
	walk(loaded.Screens["main"])
	local, caller := byName["local-field"], byName["caller-field"]
	if local == nil || caller == nil {
		t.Fatalf("expanded fields = %+v", byName)
	}
	if local.Scope != "screen:main/profile" || caller.Scope != "screen:main" {
		t.Fatalf("field scopes local=%q caller=%q", local.Scope, caller.Scope)
	}
	if local.Form == "" || caller.Form != local.Form {
		t.Fatalf("form handles local=%q caller=%q", local.Form, caller.Form)
	}
}

func TestLoadRejectsFormNestedThroughComponentSlotExpansion(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "shell.gora"), `
gora: 1
kind: component
viewport: { width: 320, height: 180 }
slots:
  default: { required: true }
previews:
  default:
    children:
      - type: slot_content
        children: [{ type: spacer }]
root:
  type: form
  name: outer-form
  children:
    - type: slot
      props: { name: default }
`)
	writeFile(t, filepath.Join(root, "app.gora"), `
gora: 1
kind: app
imports: { components: { shell: shell.gora } }
viewport: { width: 800, height: 600 }
entry: main
screens:
  main:
    type: instance
    name: shell
    props: { component: shell }
    children:
      - type: slot_content
        children:
          - type: form
            name: inner-form
            children: [{ type: spacer }]
`)

	_, diagnostics := Load(root, filepath.Join(root, "app.gora"), 800)
	found := false
	for _, diagnostic := range diagnostics {
		found = found || diagnostic.Code == "form.nested"
	}
	if !found {
		t.Fatalf("diagnostics = %+v, want form.nested", diagnostics)
	}
}

func TestLoadRejectsFormCardinalityChangedBySlotExpansion(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "shell.gora"), `
gora: 1
kind: component
viewport: { width: 320, height: 180 }
slots:
  default: { required: true }
previews:
  default:
    children:
      - type: slot_content
        children: [{ type: spacer }]
root:
  type: form
  name: profile-form
  children:
    - type: slot
      props: { name: default }
`)
	writeFile(t, filepath.Join(root, "app.gora"), `
gora: 1
kind: app
imports: { components: { shell: shell.gora } }
viewport: { width: 800, height: 600 }
entry: main
screens:
  main:
    type: instance
    name: shell
    props: { component: shell }
    children:
      - type: slot_content
        children:
          - { type: spacer }
          - { type: spacer }
`)

	_, diagnostics := Load(root, filepath.Join(root, "app.gora"), 800)
	requireProjectDiagnostic(t, diagnostics, "schema.children")
}

func TestLoadRejectsInteractiveComponentInsideFieldSupport(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "action.gora"), `
gora: 1
kind: component
viewport: { width: 160, height: 60 }
previews:
  default: {}
root:
  type: button
  name: nested-button
  props: { label: Nested }
  children:
    - { type: text, props: { text: Nested } }
`)
	writeFile(t, filepath.Join(root, "app.gora"), `
gora: 1
kind: app
imports: { components: { action: action.gora } }
viewport: { width: 800, height: 600 }
state:
  name: { type: text, default: Ada }
entry: main
screens:
  main:
    type: text_field
    name: name-field
    props: { label: Name, bind: name }
    children:
      - { type: field_box }
      - type: field_support
        children:
          - type: instance
            props: { component: action }
`)

	_, diagnostics := Load(root, filepath.Join(root, "app.gora"), 800)
	requireProjectDiagnostic(t, diagnostics, "control.nested")
}

func TestLoadRejectsUnnamedStatefulComponentInstance(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	writeFile(t, filepath.Join(root, "toggle.gora"), `
gora: 1
kind: component
viewport: { width: 100, height: 100 }
state:
  on: { type: boolean, default: false }
previews: { default: {} }
root: { type: spacer }
`)
	writeFile(t, filepath.Join(root, "app.gora"), `
gora: 1
kind: app
imports:
  components: { toggle: toggle.gora }
viewport: { width: 100, height: 100 }
entry: main
screens:
  main:
    type: instance
    props: { component: toggle }
`)

	_, diagnostics := Load(root, filepath.Join(root, "app.gora"), 100)
	for _, diagnostic := range diagnostics {
		if diagnostic.Code == "state.instance_name" {
			return
		}
	}
	t.Fatalf("missing state.instance_name diagnostic: %+v", diagnostics)
}

func TestLoadRejectsResolvedInteractionTypeMismatches(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	writeFile(t, filepath.Join(root, "app.gora"), `
gora: 1
kind: app
viewport: { width: 100, height: 100 }
state:
  on: { type: boolean, default: false }
  label: { type: text, default: bad }
  count: { type: number, default: 1 }
entry: main
screens:
  main:
    type: button
    name: save
    props: { label: Save, disabled: { ref: state.count } }
    on:
      activate:
        - { action: set, state: on, value: { ref: state.label } }
        - { action: increment, state: count, by: { ref: state.label } }
    variants:
      - when: { state: count, equals: { ref: state.label } }
        props: { opacity: 0.5 }
    children:
      - { type: text, props: { content: Save } }
`)

	_, diagnostics := Load(root, filepath.Join(root, "app.gora"), 100)
	want := map[string]bool{"button.disabled_type": false, "action.resolved_type": false, "variant.resolved_type": false}
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

func TestLoadResolvesAndValidatesComponentLinkTargetsAgainstAppScreens(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	writeFile(t, filepath.Join(root, "nav-item.gora"), `
gora: 1
kind: component
viewport: { width: 200, height: 50 }
parameters:
  target: { type: text, required: true }
previews:
  default:
    parameters: { target: reports }
root:
  type: link
  name: nav-link
  props: { label: Open screen, to: { ref: parameter.target } }
  children:
    - { type: text, props: { text: Open screen } }
`)
	writeFile(t, filepath.Join(root, "app.gora"), `
gora: 1
kind: app
viewport: { width: 400, height: 300 }
imports:
  components: { nav: nav-item.gora }
entry: home
screens:
  home:
    type: instance
    props: { component: nav, parameters: { target: reports } }
  reports: { type: spacer }
`)

	loaded, diagnostics := Load(root, filepath.Join(root, "app.gora"), 400)
	if len(diagnostics) != 0 {
		t.Fatalf("Load diagnostics: %+v", diagnostics)
	}
	if got := loaded.Screens["home"]; got == nil || got.Type != "link" || got.Props["to"] != "reports" {
		t.Fatalf("resolved link = %+v", got)
	}

	writeFile(t, filepath.Join(root, "invalid.gora"), `
gora: 1
kind: app
viewport: { width: 400, height: 300 }
imports:
  components: { nav: nav-item.gora }
entry: home
screens:
  home:
    type: instance
    props: { component: nav, parameters: { target: missing } }
`)
	_, diagnostics = Load(root, filepath.Join(root, "invalid.gora"), 400)
	requireProjectDiagnostic(t, diagnostics, "link.target")
}

func TestLoadPreservesResponsiveHiddenNodes(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	writeFile(t, filepath.Join(root, "app.gora"), `
gora: 1
kind: app
viewport: { width: 400, height: 300 }
breakpoints:
  compact: { max_width: 500 }
entry: home
screens:
  home:
    type: stack
    children:
      - type: text
        name: compact-hidden
        props: { text: Hidden }
        responsive:
          compact: { visible: false }
`)

	loaded, diagnostics := Load(root, filepath.Join(root, "app.gora"), 400)
	if len(diagnostics) != 0 {
		t.Fatalf("Load diagnostics: %+v", diagnostics)
	}
	child := loaded.Screens["home"].Children[0]
	if child.Name != "compact-hidden" || !child.Hidden {
		t.Fatalf("hidden child = %+v", child)
	}
}

func samePercent(value any, want float64) bool {
	mapping, ok := value.(map[string]any)
	if !ok {
		return false
	}
	switch got := mapping["percent"].(type) {
	case int64:
		return float64(got) == want
	case float64:
		return got == want
	default:
		return false
	}
}

func requireProjectDiagnostic(t *testing.T, diagnostics []document.Diagnostic, code string) {
	t.Helper()
	for _, diagnostic := range diagnostics {
		if diagnostic.Code == code {
			return
		}
	}
	t.Fatalf("missing %s diagnostic: %+v", code, diagnostics)
}

func TestLoadRejectsImportOutsideRoot(t *testing.T) {
	t.Parallel()

	parent := t.TempDir()
	root := filepath.Join(parent, "project")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(parent, "outside.gora"), `
gora: 1
kind: component
viewport:
  width: 100
  height: 100
previews:
  default: {}
root:
  type: surface
`)
	writeFile(t, filepath.Join(root, "app.gora"), `
gora: 1
kind: app
imports:
  components:
    outside: ../outside.gora
viewport:
  width: 100
  height: 100
entry: main
screens:
  main:
    type: instance
    props:
      component: outside
`)

	_, diagnostics := Load(root, filepath.Join(root, "app.gora"), 100)
	if len(diagnostics) == 0 {
		t.Fatal("Load accepted import outside project root")
	}
}

func TestLoadRejectsComponentImportCycle(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	writeFile(t, filepath.Join(root, "a.gora"), `
gora: 1
kind: component
imports:
  components:
    b: ./b.gora
viewport:
  width: 100
  height: 100
previews:
  default: {}
root:
  type: instance
  props:
    component: b
`)
	writeFile(t, filepath.Join(root, "b.gora"), `
gora: 1
kind: component
imports:
  components:
    a: ./a.gora
viewport:
  width: 100
  height: 100
previews:
  default: {}
root:
  type: instance
  props:
    component: a
`)

	_, diagnostics := Load(root, filepath.Join(root, "a.gora"), 100)
	if len(diagnostics) == 0 {
		t.Fatal("Load accepted component import cycle")
	}
	found := false
	for _, diagnostic := range diagnostics {
		found = found || diagnostic.Code == "import.cycle"
	}
	if !found {
		t.Fatalf("diagnostics = %+v, want import.cycle", diagnostics)
	}
}

func TestLoadTracksAndContainsAssets(t *testing.T) {
	dir := t.TempDir()
	imageFile, err := os.Create(filepath.Join(dir, "image.png"))
	if err != nil {
		t.Fatal(err)
	}
	if err := png.Encode(imageFile, image.NewRGBA(image.Rect(0, 0, 1, 1))); err != nil {
		t.Fatal(err)
	}
	if err := imageFile.Close(); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(dir, "app.gora"), `
gora: 1
kind: app
viewport: { width: 100, height: 100 }
entry: main
screens:
  main:
    type: image
    props: { src: image.png }
`)
	loaded, diagnostics := Load(dir, filepath.Join(dir, "app.gora"), 100)
	if len(diagnostics) != 0 {
		t.Fatalf("Load returned diagnostics: %+v", diagnostics)
	}
	expected, err := filepath.EvalSymlinks(filepath.Join(dir, "image.png"))
	if err != nil {
		t.Fatal(err)
	}
	if got := loaded.Screens["main"].Props["src"]; got != expected {
		t.Fatalf("resolved asset = %v", got)
	}
	if len(loaded.Dependencies) != 2 {
		t.Fatalf("dependencies = %v", loaded.Dependencies)
	}
}

func TestUnknownComponentSuggestsCloseAlias(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "card.gora"), `
gora: 1
kind: component
viewport: { width: 100, height: 100 }
previews:
  default: {}
root: { type: spacer }
`)
	writeFile(t, filepath.Join(root, "app.gora"), `
gora: 1
kind: app
imports:
  components: { card: card.gora }
viewport: { width: 100, height: 100 }
entry: main
screens:
  main: { type: instance, props: { component: crad } }
`)
	_, diagnostics := Load(root, filepath.Join(root, "app.gora"), 100)
	for _, diagnostic := range diagnostics {
		if diagnostic.Code == "component.unknown" && len(diagnostic.Suggestions) == 1 && diagnostic.Suggestions[0] == "card" {
			return
		}
	}
	t.Fatalf("diagnostics = %+v, want card suggestion", diagnostics)
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
