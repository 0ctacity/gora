package studio

import (
	"image"
	"image/png"
	"os"
	"path/filepath"
	"testing"

	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/unit"
	"gioui.org/widget/material"

	"gora/internal/document"
	"gora/internal/interaction"
	"gora/internal/render"
)

func TestRepeatedButtonActivationAcrossPersistentRebuilds(t *testing.T) {
	repositoryRoot, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := NewRuntime(repositoryRoot, filepath.Join(repositoryRoot, "examples", "interactivity", "app.gora"))
	if err != nil {
		t.Fatal(err)
	}
	var cache render.GioCache
	theme := material.NewTheme()
	router := interaction.NewRouter()
	nextResult := func() (Snapshot, render.GioResult) {
		snapshot := runtime.Snapshot()
		var operations op.Ops
		result := cache.Layout(layout.Context{
			Ops: &operations, Metric: unit.Metric{PxPerDp: 1, PxPerSp: 1}, Constraints: layout.Exact(snapshot.Viewport),
		}, theme, snapshot.Root, snapshot.Viewport, renderState(snapshot))
		router.Update(result.Interactions)
		return snapshot, result
	}
	activate := func(name string, pointerID int) {
		t.Helper()
		_, result := nextResult()
		var region *render.InteractionRegion
		for index := range result.Interactions {
			inspectionName := ""
			for _, inspection := range result.Inspections {
				if inspection.Handle == result.Interactions[index].Handle {
					inspectionName = inspection.Name
					break
				}
			}
			if inspectionName == name {
				region = &result.Interactions[index]
				break
			}
		}
		if region == nil {
			t.Fatalf("missing interaction region %q: %+v", name, result.Interactions)
		}
		visible := region.Bounds.Intersect(region.Clip)
		point := image.Pt((visible.Min.X+visible.Max.X)/2, (visible.Min.Y+visible.Max.Y)/2)
		if !router.Press(pointerID, point) {
			t.Fatalf("press %q was rejected", name)
		}
		activation, ok := router.Release(pointerID, point)
		if !ok {
			t.Fatalf("release %q did not activate", name)
		}
		if err := runtime.Activate(activation); err != nil {
			t.Fatal(err)
		}
	}

	activate("annual-plan", 1)
	if got := runtime.Snapshot().StateValues["screen:main"]["plan"]; got != "annual" {
		t.Fatalf("annual plan = %#v", got)
	}
	activate("monthly-plan", 2)
	if got := runtime.Snapshot().StateValues["screen:main"]["plan"]; got != "monthly" {
		t.Fatalf("monthly plan = %#v", got)
	}
	activate("increment", 3)
	activate("decrement", 4)
	if got := runtime.Snapshot().StateValues["screen:main/team-seats"]["count"]; got != float64(3) {
		t.Fatalf("stepper count = %#v", got)
	}
	activate("toggle-details", 5)
	activate("toggle-details", 6)
	if got := runtime.Snapshot().StateValues["screen:main"]["details"]; got != false {
		t.Fatalf("details = %#v", got)
	}
}

func TestRuntimeOwnsStateAcrossActivationReloadAndReset(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "app.gora")
	source := []byte(`
gora: 1
kind: app
viewport: { width: 200, height: 100 }
state:
  count: { type: number, default: 1 }
entry: main
screens:
  main:
    type: text
    props: { content: { ref: state.count } }
`)
	if err := os.WriteFile(path, source, 0o600); err != nil {
		t.Fatal(err)
	}
	runtime, err := NewRuntime(dir, path)
	if err != nil {
		t.Fatal(err)
	}
	if got := runtime.Snapshot().Root.Props["content"]; got != "1" {
		t.Fatalf("initial content = %#v", got)
	}
	if err := runtime.Activate(interaction.Activation{Scope: "screen:main", Actions: []document.Action{{Action: "increment", State: "count", By: float64(2)}}}); err != nil {
		t.Fatal(err)
	}
	if got := runtime.Snapshot().Root.Props["content"]; got != "3" {
		t.Fatalf("mutated content = %#v", got)
	}
	persistentRoot := runtime.Snapshot().Root
	runtime.SetTransient(interaction.Transient{Focused: "button"})
	if transientRoot := runtime.Snapshot().Root; transientRoot != persistentRoot {
		t.Fatal("transient interaction rebuilt persistent geometry")
	}
	runtime.Reload()
	if got := runtime.Snapshot().Root.Props["content"]; got != "3" {
		t.Fatalf("reloaded content = %#v", got)
	}
	runtime.ResetState()
	snapshot := runtime.Snapshot()
	if got := snapshot.Root.Props["content"]; got != "1" || snapshot.Transient != (interaction.Transient{}) {
		t.Fatalf("reset snapshot = %#v", snapshot)
	}
}

func TestReloadPreservesLastGoodFrame(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "app.gora")
	valid := []byte(`
gora: 1
kind: app
viewport: { width: 320, height: 200 }
entry: main
screens:
  main:
    type: surface
    props: { background: "#112233" }
`)
	if err := os.WriteFile(path, valid, 0o600); err != nil {
		t.Fatal(err)
	}
	runtime, err := NewRuntime(dir, path)
	if err != nil {
		t.Fatal(err)
	}
	before := runtime.Snapshot()
	if before.Root == nil || before.Invalid {
		t.Fatalf("initial snapshot = %#v", before)
	}

	if err := os.WriteFile(path, []byte("gora: 1\nkind: app\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runtime.Reload()
	after := runtime.Snapshot()
	if after.Root != before.Root || !after.Invalid || len(after.Diagnostics) == 0 {
		t.Fatalf("last-good state not preserved: %#v", after)
	}
}

func TestNamedScrollPersistsAndUnnamedScrollResets(t *testing.T) {
	tests := []struct {
		name      string
		nodeName  string
		preserved bool
	}{
		{name: "named", nodeName: "feed", preserved: true},
		{name: "unnamed", preserved: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "app.gora")
			nameLine := ""
			if test.nodeName != "" {
				nameLine = "\n    name: " + test.nodeName
			}
			source := `gora: 1
kind: app
viewport: { width: 200, height: 100 }
entry: main
screens:
  main:
    type: scroll` + nameLine + `
    props: { axis: vertical }
    children:
      - type: spacer
        props: { height: 400 }
`
			if err := os.WriteFile(path, []byte(source), 0o600); err != nil {
				t.Fatal(err)
			}
			runtime, err := NewRuntime(dir, path)
			if err != nil {
				t.Fatal(err)
			}
			runtime.Scroll(30)
			if len(runtime.Snapshot().Scroll) != 1 {
				t.Fatal("scroll offset was not recorded")
			}
			runtime.Reload()
			got := len(runtime.Snapshot().Scroll) == 1
			if got != test.preserved {
				t.Fatalf("preserved=%v, want %v", got, test.preserved)
			}
		})
	}
}

func TestScrollAxisTargetsHorizontalScrollAlongsideVerticalScroll(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "app.gora")
	source := []byte(`
gora: 1
kind: app
viewport: { width: 200, height: 100 }
entry: main
screens:
  main:
    type: overlay
    children:
      - type: scroll
        name: feed
        props: { axis: vertical }
        children:
          - type: spacer
            props: { height: 400 }
      - type: scroll
        name: rail
        props: { axis: horizontal }
        children:
          - type: spacer
            props: { width: 400 }
`)
	if err := os.WriteFile(path, source, 0o600); err != nil {
		t.Fatal(err)
	}
	runtime, err := NewRuntime(dir, path)
	if err != nil {
		t.Fatal(err)
	}

	runtime.ScrollAxis("horizontal", 30)

	scroll := runtime.Snapshot().Scroll
	if got := scroll["rail"].X; got != 30 {
		t.Fatalf("horizontal offset = %d", got)
	}
	if _, ok := scroll["feed"]; ok {
		t.Fatal("horizontal gesture moved the vertical scroll node")
	}
}

func TestSetScrollOffsetSupportsDraggableScrollbar(t *testing.T) {
	runtime := &Runtime{scroll: make(map[string]image.Point)}
	runtime.SetScrollOffset("feed", "vertical", 95)
	runtime.SetScrollOffset("gallery", "horizontal", 42)
	snapshot := runtime.Snapshot()
	if got := snapshot.Scroll["feed"].Y; got != 95 {
		t.Fatalf("vertical offset = %d", got)
	}
	if got := snapshot.Scroll["gallery"].X; got != 42 {
		t.Fatalf("horizontal offset = %d", got)
	}
}

func TestCaptureUsesViewportScaleAndWarnsForLastGood(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "app.gora")
	valid := []byte(`
gora: 1
kind: app
viewport: { width: 20, height: 10 }
entry: main
screens:
  main: { type: surface, props: { background: "#112233" } }
`)
	if err := os.WriteFile(path, valid, 0o600); err != nil {
		t.Fatal(err)
	}
	runtime, err := NewRuntime(dir, path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("gora: 1\nkind: app\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runtime.Reload()
	output := filepath.Join(dir, "capture.png")
	warning, err := runtime.Capture(output, 2)
	if err != nil {
		t.Fatal(err)
	}
	if warning == "" {
		t.Fatal("missing last-good warning")
	}
	file, err := os.Open(output)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	captured, err := png.DecodeConfig(file)
	if err != nil {
		t.Fatal(err)
	}
	if captured.Width != 40 || captured.Height != 20 {
		t.Fatalf("capture size = %dx%d", captured.Width, captured.Height)
	}
}
