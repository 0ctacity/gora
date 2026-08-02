package studio

import (
	"image"
	"path/filepath"
	"testing"
	"time"

	"gioui.org/app"
	"gioui.org/f32"
	"gioui.org/io/input"
	"gioui.org/io/pointer"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/unit"
	"gioui.org/widget/material"

	"gora/internal/interaction"
	"gora/internal/semantic"
)

func TestStudioCanvasDeliversRepeatedDocumentClicksAfterStateRebuild(t *testing.T) {
	repositoryRoot, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := NewRuntime(repositoryRoot, filepath.Join(repositoryRoot, "examples", "interactivity", "app.gora"))
	if err != nil {
		t.Fatal(err)
	}
	var inputRouter input.Router
	var operations op.Ops
	state := &uiState{zoomValue: 0.87, zoomInitialized: true, router: interaction.NewRouter()}
	theme := material.NewTheme()
	window := new(app.Window)
	gtx := layout.Context{
		Ops: &operations, Source: inputRouter.Source(), Metric: unit.Metric{PxPerDp: 1, PxPerSp: 1},
		Constraints: layout.Exact(image.Pt(1151, 642)), Now: time.Now(),
	}
	frame := func() {
		gtx.Reset()
		gtx.Constraints = layout.Exact(image.Pt(1151, 642))
		gtx.Now = time.Now()
		snapshot := runtime.Snapshot()
		handleActions(gtx, runtime, state, snapshot, window)
		layoutStudioCanvas(gtx, theme, runtime, state, runtime.Snapshot(), window)
		inputRouter.Frame(gtx.Ops)
	}
	regionCenter := func(name string) f32.Point {
		for _, node := range semantic.Flatten(state.runtimeTree) {
			if node.Name == name && node.Bounds != nil && node.Clip != nil {
				visible := node.Bounds.ImageRectangle().Intersect(node.Clip.ImageRectangle())
				logical := f32.Pt(float32(visible.Min.X+visible.Max.X)/2, float32(visible.Min.Y+visible.Max.Y)/2)
				position := canvasPosition(state.canvasViewport, state.canvasSize, state.canvasPan)
				scale := state.zoomValue * gtx.Metric.PxPerDp
				return f32.Pt(float32(position.X)+logical.X*scale, float32(position.Y)+logical.Y*scale)
			}
		}
		t.Fatalf("missing region %q", name)
		return f32.Point{}
	}
	click := func(name string, id pointer.ID) {
		point := regionCenter(name)
		inputRouter.Queue(pointer.Event{Source: pointer.Mouse, PointerID: id, Kind: pointer.Press, Buttons: pointer.ButtonPrimary, Position: point})
		frame()
		inputRouter.Queue(pointer.Event{Source: pointer.Mouse, PointerID: id, Kind: pointer.Release, Position: point})
		frame()
	}

	frame()
	click("annual-plan", 1)
	if got := runtime.Snapshot().StateValues["screen:main"]["plan"]; got != "annual" {
		t.Fatalf("annual plan = %#v", got)
	}
	click("monthly-plan", 1)
	if got := runtime.Snapshot().StateValues["screen:main"]["plan"]; got != "monthly" {
		t.Fatalf("monthly plan = %#v", got)
	}
	click("increment", 1)
	if got := runtime.Snapshot().StateValues["screen:main/team-seats"]["count"]; got != float64(4) {
		t.Fatalf("incremented count = %#v", got)
	}
	click("decrement", 1)
	click("toggle-details", 1)
	click("toggle-details", 1)
	if got := runtime.Snapshot().StateValues["screen:main"]["details"]; got != false {
		t.Fatalf("details = %#v", got)
	}

	state.inspecting = true
	state.router.SetInspecting(true)
	frame()
	monthlyPoint := regionCenter("monthly-plan")
	inputRouter.Queue(pointer.Event{Source: pointer.Mouse, PointerID: 1, Kind: pointer.Press, Buttons: pointer.ButtonPrimary, Position: monthlyPoint})
	frame()
	inputRouter.Queue(pointer.Event{Source: pointer.Mouse, PointerID: 1, Kind: pointer.Release, Position: monthlyPoint})
	frame()
	if state.selectedHandle == "" {
		t.Fatal("inspect click did not select a document node")
	}
	if got := runtime.Snapshot().StateValues["screen:main"]["plan"]; got != "monthly" {
		t.Fatalf("inspect click activated plan button: %#v", got)
	}
}
