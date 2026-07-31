package project

import (
	"image"
	"image/png"
	"os"
	"path/filepath"
	"testing"
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
