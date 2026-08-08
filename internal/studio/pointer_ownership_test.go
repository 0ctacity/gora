package studio

import (
	"image"
	"path/filepath"
	"testing"
	"time"

	"gioui.org/app"
	"gioui.org/f32"
	"gioui.org/io/event"
	"gioui.org/io/input"
	"gioui.org/io/pointer"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
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

func TestStudioScrollbarTrackOwnsPointerOverUnderlyingField(t *testing.T) {
	maximum := 100.0
	axis := &semantic.Node{ID: "scrollbar", Handle: "scrollbar", Type: "scrollbar", Role: "scrollbar", Orientation: "vertical", Visible: true, Enabled: true,
		Bounds: &semantic.Rect{X: 90, Y: 2, Width: 8, Height: 96}, Clip: &semantic.Rect{X: 0, Y: 0, Width: 100, Height: 100}, Max: &maximum,
		ViewportSize: &semantic.Rect{Height: 100}, ContentSize: &semantic.Rect{Height: 300}, FocusOrder: 0, PaintOrder: 10}
	axis.Children = []*semantic.Node{
		{ID: "scrollbar/track", Handle: "scrollbar/track", Type: "scrollbar_track", Group: axis.Handle, Visible: true, Enabled: true,
			Bounds: &semantic.Rect{X: 90, Y: 2, Width: 8, Height: 96}, Clip: &semantic.Rect{X: 0, Y: 0, Width: 100, Height: 100}, FocusOrder: -1, PaintOrder: 11},
		{ID: "scrollbar/thumb", Handle: "scrollbar/thumb", Type: "scrollbar_thumb", Group: axis.Handle, Visible: true, Enabled: true,
			Bounds: &semantic.Rect{X: 90, Y: 2, Width: 8, Height: 30}, Clip: &semantic.Rect{X: 0, Y: 0, Width: 100, Height: 100}, FocusOrder: -1, PaintOrder: 12},
	}
	field := &semantic.Node{ID: "field", Handle: "field", Role: "textbox", Visible: true, Enabled: true,
		Bounds: &semantic.Rect{X: 0, Y: 0, Width: 100, Height: 100}, Clip: &semantic.Rect{X: 0, Y: 0, Width: 100, Height: 100}, FocusOrder: 1, PaintOrder: 1}
	root := &semantic.Node{Type: "_viewport", Visible: true, Enabled: true, Bounds: &semantic.Rect{X: 0, Y: 0, Width: 100, Height: 100}, Clip: &semantic.Rect{X: 0, Y: 0, Width: 100, Height: 100}, Children: []*semantic.Node{field, axis}}
	state := &uiState{router: interaction.NewRouter(), runtimeTree: root, zoomValue: 1, zoomInitialized: true, canvasViewport: image.Pt(100, 100), canvasSize: image.Pt(100, 100)}
	state.router.Update(root)
	runtime := &Runtime{}
	var inputOps op.Ops
	inputClip := clip.Rect{Max: image.Pt(100, 100)}.Push(&inputOps)
	event.Op(&inputOps, &state.interactionInput)
	inputClip.Pop()
	var inputRouter input.Router
	_, _ = inputRouter.Event(pointer.Filter{Target: &state.interactionInput, Kinds: pointer.Press | pointer.Release | pointer.Move | pointer.Drag | pointer.Cancel})
	inputRouter.Frame(&inputOps)
	window := new(app.Window)
	frame := func() {
		var frameOps op.Ops
		gtx := layout.Context{Ops: &frameOps, Source: inputRouter.Source(), Metric: unit.Metric{PxPerDp: 1, PxPerSp: 1}, Constraints: layout.Exact(image.Pt(100, 100))}
		handleDocumentInteraction(gtx, runtime, state, window)
		inputRouter.Frame(gtx.Ops)
	}
	inputRouter.Queue(pointer.Event{Source: pointer.Mouse, PointerID: 1, Kind: pointer.Press, Buttons: pointer.ButtonPrimary, Position: f32.Pt(94, 50)})
	frame()
	if !state.router.ScrollbarPointerOwned() || state.fieldSelectionID != "" {
		t.Fatalf("track ownership leaked to field: owned=%v selection=%q", state.router.ScrollbarPointerOwned(), state.fieldSelectionID)
	}
	inputRouter.Queue(pointer.Event{Source: pointer.Mouse, PointerID: 2, Kind: pointer.Press, Buttons: pointer.ButtonPrimary, Position: f32.Pt(94, 50)})
	frame()
	if state.fieldSelectionID != "" {
		t.Fatalf("second pointer began field selection: %q", state.fieldSelectionID)
	}
	inputRouter.Queue(pointer.Event{Source: pointer.Mouse, PointerID: 1, Kind: pointer.Release, Position: f32.Pt(94, 50)})
	frame()
	if state.router.ScrollbarPointerOwned() || state.fieldSelectionID != "" {
		t.Fatalf("track release left ownership/selection: owned=%v selection=%q", state.router.ScrollbarPointerOwned(), state.fieldSelectionID)
	}
	state.inspecting = true
	state.router.SetInspecting(true)
	before := runtime.Snapshot()
	inputRouter.Queue(pointer.Event{Source: pointer.Mouse, PointerID: 4, Kind: pointer.Press, Buttons: pointer.ButtonPrimary, Position: f32.Pt(94, 50)})
	frame()
	inputRouter.Queue(pointer.Event{Source: pointer.Mouse, PointerID: 4, Kind: pointer.Release, Position: f32.Pt(94, 50)})
	frame()
	if got := runtime.Snapshot(); got.RuntimeRevision != before.RuntimeRevision || got.Scroll["scrollbar"] != before.Scroll["scrollbar"] {
		t.Fatalf("Inspect scrollbar click mutated runtime: before=%+v after=%+v", before, got)
	}
}
