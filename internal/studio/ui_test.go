package studio

import (
	"image"
	"os"
	"path/filepath"
	"testing"
	"time"

	"gioui.org/app"
	"gioui.org/f32"
	"gioui.org/io/event"
	"gioui.org/io/input"
	"gioui.org/io/key"
	"gioui.org/io/pointer"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/unit"
	"gioui.org/widget/material"

	"gora/internal/interaction"
	"gora/internal/project"
	"gora/internal/render"
	"gora/internal/semantic"
)

func TestLayoutPreviewKeepsResolvedNodesAsNativeFrameEntries(t *testing.T) {
	snapshot := Snapshot{
		Viewport: image.Pt(120, 80),
		Root: &project.Node{
			Handle: "root", Type: "surface", Props: map[string]any{"background": "#FFFFFF"},
			Children: []*project.Node{{Handle: "label", Type: "text", Props: map[string]any{"text": "Live"}}},
		},
	}
	var operations op.Ops
	gtx := layout.Context{
		Ops:         &operations,
		Metric:      unit.Metric{PxPerDp: 1, PxPerSp: 1},
		Constraints: layout.Exact(snapshot.Viewport),
	}

	result := layoutPreview(gtx, material.NewTheme(), snapshot)

	if _, ok := result.Bounds["root"]; !ok {
		t.Fatal("native preview did not retain the root node")
	}
	if _, ok := result.Bounds["label"]; !ok {
		t.Fatal("native preview collapsed the child into a single image")
	}
}

func TestCheckerboardCacheReusesRecordedOperationsUntilItsGeometryChanges(t *testing.T) {
	var cache checkerboardCache
	paintFrame := func(size image.Point, tile int) {
		var operations op.Ops
		cache.paint(&operations, size, tile)
	}

	paintFrame(image.Pt(120, 80), 8)
	paintFrame(image.Pt(120, 80), 8)
	if cache.builds != 1 {
		t.Fatalf("checkerboard builds = %d, want 1", cache.builds)
	}
	paintFrame(image.Pt(160, 80), 8)
	paintFrame(image.Pt(160, 80), 10)
	if cache.builds != 3 {
		t.Fatalf("checkerboard builds = %d, want 3", cache.builds)
	}
}

func TestUnmodifiedTwoFingerVerticalGestureSelectsVerticalScroll(t *testing.T) {
	axis, delta := canvasScrollDelta(pointer.Event{Scroll: f32.Pt(5, 70)})
	if axis != "vertical" || delta != 70 {
		t.Fatalf("axis=%q delta=%v", axis, delta)
	}
}

func TestUnmodifiedTwoFingerHorizontalGestureSelectsHorizontalScroll(t *testing.T) {
	axis, delta := canvasScrollDelta(pointer.Event{Scroll: f32.Pt(70, 5)})
	if axis != "horizontal" || delta != 70 {
		t.Fatalf("axis=%q delta=%v", axis, delta)
	}
}

func TestHorizontalGesturePansOverflowingStudioCanvas(t *testing.T) {
	state := &uiState{
		canvasViewport: image.Pt(800, 600),
		canvasSize:     image.Pt(1200, 600),
		canvasPan:      image.Pt(200, 0),
	}

	if !state.panCanvas("horizontal", 80) {
		t.Fatal("overflowing Studio canvas did not claim horizontal scrolling")
	}
	if state.canvasPan.X != 280 {
		t.Fatalf("canvas pan = %d", state.canvasPan.X)
	}
	if state.panCanvas("vertical", 80) {
		t.Fatal("vertical document scrolling was incorrectly claimed as canvas panning")
	}
}

func TestCanvasPositionUsesPanForOverflowAndCentersSmallerPreview(t *testing.T) {
	if got := canvasPosition(image.Pt(800, 600), image.Pt(1200, 600), image.Pt(280, 0)); got != image.Pt(-280, 0) {
		t.Fatalf("overflowing canvas position = %v", got)
	}
	if got := canvasPosition(image.Pt(800, 600), image.Pt(400, 300), image.Point{}); got != image.Pt(200, 150) {
		t.Fatalf("smaller canvas position = %v", got)
	}
}

func TestShortcutTrackpadScrollChangesZoomSmoothly(t *testing.T) {
	if !isTrackpadZoom(key.ModShortcut) {
		t.Fatal("shortcut-modified trackpad scroll was not recognized as zoom")
	}
	if isTrackpadZoom(0) {
		t.Fatal("ordinary trackpad scroll was recognized as zoom")
	}

	zoomedIn := zoomAfterTrackpadScroll(1, -100)
	if zoomedIn <= 1 {
		t.Fatalf("scroll up zoom = %v, want greater than 1", zoomedIn)
	}
	zoomedOut := zoomAfterTrackpadScroll(1, 100)
	if zoomedOut >= 1 {
		t.Fatalf("scroll down zoom = %v, want less than 1", zoomedOut)
	}
	if got := zoomAfterTrackpadScroll(.25, 1000); got != .25 {
		t.Fatalf("minimum zoom = %v", got)
	}
	if got := zoomAfterTrackpadScroll(4, -1000); got != 4 {
		t.Fatalf("maximum zoom = %v", got)
	}
}

func TestZoomStepsMoveInBothDirectionsAndClamp(t *testing.T) {
	if got := zoomByStep(1, -1); got != .9 {
		t.Fatalf("zoom out = %v", got)
	}
	if got := zoomByStep(.9, 1); got != 1 {
		t.Fatalf("zoom in = %v", got)
	}
	if got := zoomByStep(.25, -1); got != .25 {
		t.Fatalf("minimum zoom = %v", got)
	}
	if got := zoomByStep(4, 1); got != 4 {
		t.Fatalf("maximum zoom = %v", got)
	}
}

func TestTrackpadZoomReadsShortcutScrollFromCanvas(t *testing.T) {
	state := &uiState{}
	var inputOps op.Ops
	stack := clip.Rect{Max: image.Pt(200, 200)}.Push(&inputOps)
	event.Op(&inputOps, &state.zoomInput)
	stack.Pop()

	var router input.Router
	_, _ = router.Event(trackpadZoomFilter(state))
	router.Frame(&inputOps)
	router.Queue(pointer.Event{
		Kind:      pointer.Scroll,
		Position:  f32.Pt(50, 50),
		Scroll:    f32.Pt(0, -80),
		Modifiers: key.ModShortcut,
	})

	var frameOps op.Ops
	gtx := layout.Context{
		Ops:         &frameOps,
		Source:      router.Source(),
		Metric:      unit.Metric{PxPerDp: 1, PxPerSp: 1},
		Constraints: layout.Exact(image.Pt(200, 200)),
	}
	if got := trackpadZoomScroll(gtx, state); got != -80 {
		t.Fatalf("zoom scroll delta = %v", got)
	}
}

func TestCanvasReadsUnmodifiedHorizontalScrollEvent(t *testing.T) {
	state := &uiState{}
	var inputOps op.Ops
	stack := clip.Rect{Max: image.Pt(200, 200)}.Push(&inputOps)
	event.Op(&inputOps, &state.zoomInput)
	stack.Pop()

	var router input.Router
	_, _ = router.Event(trackpadZoomFilter(state))
	router.Frame(&inputOps)
	router.Queue(pointer.Event{
		Kind:     pointer.Scroll,
		Position: f32.Pt(50, 50),
		Scroll:   f32.Pt(80, 4),
	})

	var frameOps op.Ops
	gtx := layout.Context{
		Ops:         &frameOps,
		Source:      router.Source(),
		Metric:      unit.Metric{PxPerDp: 1, PxPerSp: 1},
		Constraints: layout.Exact(image.Pt(200, 200)),
	}
	zoom, horizontal, vertical := canvasScrollEvents(gtx, state)
	if zoom != 0 || horizontal != 80 || vertical != 0 {
		t.Fatalf("zoom=%v horizontal=%v vertical=%v", zoom, horizontal, vertical)
	}
}

func TestZoomIgnoresOpposingHorizontalTrackpadNoise(t *testing.T) {
	scrollEvent := pointer.Event{Scroll: f32.Pt(120, -20)}
	if got := trackpadZoomDelta(scrollEvent); got != -20 {
		t.Fatalf("zoom delta = %v, want vertical delta -20", got)
	}
}

func TestZoomGestureExclusivelyOwnsTrackpadMomentum(t *testing.T) {
	now := time.Unix(10, 0)
	state := &uiState{}
	if state.blockPageScroll(now, 0, 12) {
		t.Fatal("ordinary page scroll was blocked before zooming")
	}
	if !state.blockPageScroll(now, -20, 12) {
		t.Fatal("page scroll was not blocked when zoom began")
	}
	if !state.blockPageScroll(now.Add(100*time.Millisecond), 0, 8) {
		t.Fatal("trackpad momentum escaped into page scrolling")
	}
	if !state.blockPageScroll(now.Add(200*time.Millisecond), 0, 0) {
		t.Fatal("page scrolling resumed before the zoom gesture settled")
	}
	if state.blockPageScroll(now.Add(400*time.Millisecond), 0, 0) {
		t.Fatal("page scrolling stayed blocked after the zoom gesture settled")
	}
}

func TestFreshScrollAfterZoomIdleDoesNotRequireClickFrame(t *testing.T) {
	now := time.Unix(10, 0)
	state := &uiState{}
	if !state.blockPageScroll(now, -20, 0) {
		t.Fatal("zoom did not claim its gesture")
	}
	if state.blockPageScroll(now.Add(400*time.Millisecond), 0, 12) {
		t.Fatal("fresh scroll was blocked because no idle click frame cleared zoom state")
	}
}

func TestInitialZoomKeepsDocumentScrollbarInsideStudio(t *testing.T) {
	zoom := fitZoom(image.Pt(1280, 800), image.Pt(1154, 650))
	if zoom <= 0 || zoom > 1 {
		t.Fatalf("fit zoom = %v", zoom)
	}
	if int(float32(1280)*zoom) > 1154 || int(float32(800)*zoom) > 650 {
		t.Fatalf("zoom %v leaves canvas edges outside available area", zoom)
	}
}

func TestPreviewScrollbarMapsDragDistanceToDocumentOffset(t *testing.T) {
	scrollNode := &project.Node{
		Handle: "scroll-handle", Name: "feed", Type: "scroll",
		Props: map[string]any{"axis": "vertical", "scrollbar": true},
		Children: []*project.Node{{
			Handle: "content", Type: "stack", Props: map[string]any{"height": int64(300)},
		}},
	}
	snapshot := Snapshot{
		Root:     scrollNode,
		Viewport: image.Pt(100, 100),
		Scroll:   map[string]image.Point{"feed": image.Pt(0, 20)},
	}
	result := render.GioResult{Bounds: map[string]image.Rectangle{
		"scroll-handle": image.Rect(0, 0, 100, 100),
	}}

	model, ok := previewScrollbar(snapshot, result)
	if !ok {
		t.Fatal("visible document scrollbar was not exposed to Studio")
	}
	if model.start != float32(20)/300 || model.end != float32(120)/300 {
		t.Fatalf("viewport range = %v..%v", model.start, model.end)
	}
	if got := model.offsetAfter(.25); got != 95 {
		t.Fatalf("offset after quarter-track drag = %d", got)
	}
}

func TestDocumentScrollAtBottomDoesNotAccumulateHiddenOffset(t *testing.T) {
	root := &project.Node{Handle: "root", Type: "scroll"}
	runtime := &Runtime{scroll: map[string]image.Point{"feed": image.Pt(0, 200)}}
	state := &uiState{
		previewScroll: previewScrollbarModel{
			key: "feed", axis: "vertical", contentSize: 300, viewport: 100, offset: 200,
		},
		previewScrollRoot: root,
	}
	snapshot := Snapshot{Root: root}

	scrollDocument(runtime, state, snapshot, "vertical", 80)
	if got := runtime.Snapshot().Scroll["feed"].Y; got != 200 {
		t.Fatalf("offset after scrolling past bottom = %d, want 200", got)
	}
	scrollDocument(runtime, state, snapshot, "vertical", -10)
	if got := runtime.Snapshot().Scroll["feed"].Y; got != 190 {
		t.Fatalf("first reverse scroll = %d, want 190 without hidden debt", got)
	}
}

func TestStudioDiagonalScrollUpdatesBothAxesOnNextFrameWithoutClick(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "app.gora")
	if err := os.WriteFile(path, []byte(`gora: 1
kind: app
viewport: { width: 100, height: 80 }
entry: main
screens:
  main:
    type: scroll
    name: workspace
    props: { axis: both }
    children: [{ type: surface, props: { width: 200, height: 160 } }]
`), 0o600); err != nil {
		t.Fatal(err)
	}
	runtime, err := NewRuntime(dir, path)
	if err != nil {
		t.Fatal(err)
	}
	tree, err := runtime.RuntimeTree()
	if err != nil {
		t.Fatal(err)
	}
	state := &uiState{zoomValue: 1, zoomInitialized: true, router: interaction.NewRouter(), runtimeTree: tree, canvasViewport: image.Pt(100, 80), canvasSize: image.Pt(100, 80)}
	var inputOps op.Ops
	canvasClip := clip.Rect{Max: image.Pt(100, 80)}.Push(&inputOps)
	event.Op(&inputOps, &state.zoomInput)
	event.Op(&inputOps, &state.interactionInput)
	canvasClip.Pop()
	var inputRouter input.Router
	_, _ = inputRouter.Event(trackpadZoomFilter(state))
	inputRouter.Frame(&inputOps)
	inputRouter.Queue(pointer.Event{Kind: pointer.Scroll, Position: f32.Pt(40, 40), Scroll: f32.Pt(30, 20)})
	var frameOps op.Ops
	gtx := layout.Context{Ops: &frameOps, Source: inputRouter.Source(), Metric: unit.Metric{PxPerDp: 1, PxPerSp: 1}, Constraints: layout.Exact(image.Pt(100, 80))}
	before := runtime.Snapshot()
	handleActions(gtx, runtime, state, before, new(app.Window))
	after := runtime.Snapshot()
	if got := after.Scroll["workspace"]; got != image.Pt(30, 20) {
		t.Fatalf("diagonal Studio scroll = %v, want (30,20)", got)
	}
	if after.RuntimeRevision != before.RuntimeRevision+1 {
		t.Fatalf("diagonal Studio scroll revision = %d, want one atomic commit from %d", after.RuntimeRevision, before.RuntimeRevision)
	}
	next, err := runtime.RuntimeTree()
	if err != nil {
		t.Fatal(err)
	}
	content := namedSemanticNode(next, "")
	for _, node := range semantic.Flatten(next) {
		if node.Handle == tree.Children[0].Handle {
			content = node
			break
		}
	}
	if content == nil || content.Bounds == nil || content.Bounds.X != -30 || content.Bounds.Y != -20 {
		t.Fatalf("next-frame content bounds = %+v, want x=-30 y=-20", content)
	}
}

func TestStudioScrollbarThumbDragUpdatesNextFrameGeometry(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "app.gora")
	if err := os.WriteFile(path, []byte(`gora: 1
kind: app
viewport: { width: 100, height: 80 }
entry: main
screens:
  main:
    type: scroll
    name: workspace
    props: { axis: both, scrollbar_x: always, scrollbar_y: always }
    children: [{ type: surface, props: { width: 240, height: 200 } }]
`), 0o600); err != nil {
		t.Fatal(err)
	}
	runtime, err := NewRuntime(dir, path)
	if err != nil {
		t.Fatal(err)
	}
	state := &uiState{zoomValue: 1, zoomInitialized: true, router: interaction.NewRouter()}
	theme := material.NewTheme()
	window := new(app.Window)
	var inputOps op.Ops
	event.Op(&inputOps, &state.zoomInput)
	event.Op(&inputOps, &state.interactionInput)
	var inputRouter input.Router
	_, _ = inputRouter.Event(trackpadZoomFilter(state))
	_, _ = inputRouter.Event(pointer.Filter{Target: &state.interactionInput, Kinds: pointer.Enter | pointer.Leave | pointer.Move | pointer.Drag | pointer.Press | pointer.Release | pointer.Cancel})
	inputRouter.Frame(&inputOps)
	frame := func() {
		var frameOps op.Ops
		gtx := layout.Context{Ops: &frameOps, Source: inputRouter.Source(), Metric: unit.Metric{PxPerDp: 1, PxPerSp: 1}, Constraints: layout.Exact(image.Pt(100, 80))}
		snapshot := runtime.Snapshot()
		handleActions(gtx, runtime, state, snapshot, window)
		layoutStudioCanvas(gtx, theme, runtime, state, runtime.Snapshot(), window)
		inputRouter.Frame(gtx.Ops)
	}
	frame()
	findScrollbar := func(orientation string) *semantic.Node {
		for _, node := range semantic.Flatten(state.runtimeTree) {
			if node.Role == "scrollbar" && node.Orientation == orientation {
				return node
			}
		}
		return nil
	}
	vertical := findScrollbar("vertical")
	if vertical == nil || len(vertical.Children) < 2 || vertical.Children[1].Bounds == nil {
		t.Fatalf("initial vertical scrollbar = %+v", vertical)
	}
	thumb := vertical.Children[1].Bounds.ImageRectangle()
	thumbCenter := f32.Pt(float32((thumb.Min.X+thumb.Max.X)/2), float32((thumb.Min.Y+thumb.Max.Y)/2))
	inputRouter.Queue(pointer.Event{Source: pointer.Mouse, PointerID: 1, Kind: pointer.Press, Buttons: pointer.ButtonPrimary, Position: thumbCenter})
	frame()
	inputRouter.Queue(pointer.Event{Source: pointer.Mouse, PointerID: 1, Kind: pointer.Move, Buttons: pointer.ButtonPrimary, Position: f32.Pt(thumbCenter.X, float32(vertical.Bounds.ImageRectangle().Max.Y))})
	frame()
	if got := runtime.Snapshot().Scroll["workspace"]; got.Y <= 0 {
		t.Fatalf("thumb drag did not update runtime offset: %v", got)
	}
	beforeThumb := thumb
	frame()
	updated := findScrollbar("vertical")
	if updated == nil || len(updated.Children) < 2 || updated.Children[1].Bounds == nil {
		t.Fatalf("next-frame vertical scrollbar = %+v", updated)
	}
	if got := updated.Children[1].Bounds.ImageRectangle(); got == beforeThumb {
		t.Fatalf("next-frame thumb remained at %v after drag", got)
	}
}

func TestStudioScrollbarPointerScaleConversionPreservesLogicalPosition(t *testing.T) {
	logical := image.Pt(42, 60)
	cases := []struct {
		zoom float32
		pxdp float32
	}{
		{zoom: .75, pxdp: 2},
		{zoom: 1.5, pxdp: 1},
	}
	for _, testCase := range cases {
		state := &uiState{zoomValue: testCase.zoom, canvasViewport: image.Pt(100, 100), canvasSize: image.Pt(100, 100)}
		scale := testCase.zoom * testCase.pxdp
		physical := f32.Pt(float32(logical.X)*scale, float32(logical.Y)*scale)
		if got := documentPoint(state, unit.Metric{PxPerDp: testCase.pxdp}, physical); got != logical {
			t.Fatalf("zoom=%v px/dp=%v converted pointer=%v, want %v", testCase.zoom, testCase.pxdp, got, logical)
		}
	}
}

func TestToolbarModelPresentsStateInsteadOfEditingMechanics(t *testing.T) {
	model := makeToolbarModel(Snapshot{
		Screen:   "overview",
		Viewport: image.Pt(1280, 800),
	}, &uiState{zoomValue: .82, inspecting: true})

	if model.screen != "Overview" {
		t.Fatalf("screen label = %q", model.screen)
	}
	if model.viewport != "1280 × 800" {
		t.Fatalf("viewport label = %q", model.viewport)
	}
	if model.zoom != "82%" {
		t.Fatalf("zoom label = %q", model.zoom)
	}
	if model.status != "Valid" || !model.inspecting {
		t.Fatalf("status=%q inspecting=%v", model.status, model.inspecting)
	}
}

func TestToolbarDoesNotConsumeTheStudioCanvas(t *testing.T) {
	var operations op.Ops
	gtx := layout.Context{
		Ops:    &operations,
		Metric: unit.Metric{PxPerDp: 1, PxPerSp: 1},
		Constraints: layout.Constraints{
			Min: image.Pt(1200, 0), Max: image.Pt(1200, 820),
		},
	}
	state := &uiState{zoomValue: .82}
	state.widthEditor.SingleLine = true
	state.heightEditor.SingleLine = true
	state.output.SingleLine = true

	dimensions := layoutToolbar(gtx, material.NewTheme(), state, Snapshot{
		Screen: "overview", Viewport: image.Pt(1280, 800),
	})
	if dimensions.Size.Y > 120 {
		t.Fatalf("toolbar height = %d, canvas was pushed away", dimensions.Size.Y)
	}
}

func TestCombinedViewportEditorsProduceOneViewport(t *testing.T) {
	state := &uiState{}
	state.widthEditor.SetText("1440")
	state.heightEditor.SetText("900")
	viewport, err := viewportFromEditors(state)
	if err != nil {
		t.Fatal(err)
	}
	if viewport != image.Pt(1440, 900) {
		t.Fatalf("viewport = %v", viewport)
	}
}

func TestLiveCaretPhaseDoesNotChangeDeterministicCaptureState(t *testing.T) {
	snapshot := Snapshot{Transient: interaction.Transient{Focused: "name-field"}}
	visible := liveRenderState(snapshot, time.UnixMilli(0), time.Time{})
	hidden := liveRenderState(snapshot, time.UnixMilli(500), time.Time{})
	if visible.CaretHidden || !hidden.CaretHidden {
		t.Fatalf("live caret phases = visible:%v hidden:%v", visible.CaretHidden, hidden.CaretHidden)
	}
	if renderState(snapshot).CaretHidden {
		t.Fatal("deterministic capture state must keep the focused caret visible")
	}
}

func TestLiveCaretMovementRestartsVisibleBlinkPhase(t *testing.T) {
	snapshot := Snapshot{Transient: interaction.Transient{Focused: "name-field"}}
	restartedAt := time.UnixMilli(750)

	if liveRenderState(snapshot, restartedAt, restartedAt).CaretHidden {
		t.Fatal("caret must be visible immediately after movement")
	}
	if liveRenderState(snapshot, restartedAt.Add(499*time.Millisecond), restartedAt).CaretHidden {
		t.Fatal("caret must remain visible for the first blink interval after movement")
	}
	if !liveRenderState(snapshot, restartedAt.Add(500*time.Millisecond), restartedAt).CaretHidden {
		t.Fatal("caret must resume blinking after the movement interval")
	}
}

func TestFieldPointerAndTripleClickUseSharedVisualLineGeometry(t *testing.T) {
	field := &semantic.Node{Type: "text_area", Value: "abcd\nefgh", Children: []*semantic.Node{{
		Type: "field_box", Bounds: &semantic.Rect{X: 0, Y: 0, Width: 18, Height: 48},
		Props: map[string]any{"text": "abcd\nefgh", "field_multiline": true, "size": float64(10), "line_height": float64(12)},
	}}}
	if got := fieldRuneAtPoint(field, image.Pt(13, 25)); got != 7 {
		t.Fatalf("pointer rune = %d, want 7", got)
	}
	start, end := fieldVisualLineRange(field, 7)
	if start != 5 || end != 8 {
		t.Fatalf("visual line range = %d..%d, want 5..8", start, end)
	}
}

func TestReadOnlyFieldsAllowPointerSelectionButDisabledFieldsDoNot(t *testing.T) {
	if !fieldAllowsPointerSelection(&semantic.Node{Role: "textbox", Enabled: true, ReadOnly: true}) {
		t.Fatal("read-only field rejected pointer selection")
	}
	if fieldAllowsPointerSelection(&semantic.Node{Role: "textbox", Enabled: false}) {
		t.Fatal("disabled field accepted pointer selection")
	}
}

func TestInspectModeClearsFieldPointerSelectionOwnership(t *testing.T) {
	state := &uiState{fieldPointerID: 7, fieldSelectionID: "screen/main/node/name", fieldAnchor: 3}
	clearFieldSelectionOwnership(state)
	if state.fieldPointerID != 0 || state.fieldSelectionID != "" || state.fieldAnchor != 0 {
		t.Fatalf("field pointer ownership = %+v", state)
	}
}

func TestFieldRedoUsesPlatformConventionalShortcuts(t *testing.T) {
	if !fieldRedoShortcut(key.Name("Z"), key.ModShortcut|key.ModShift) {
		t.Fatal("Shortcut+Shift+Z was not recognized as redo")
	}
	if !fieldRedoShortcut(key.Name("Y"), key.ModShortcut) {
		t.Fatal("Shortcut+Y was not recognized as redo")
	}
	if fieldRedoShortcut(key.Name("Z"), key.ModShortcut) {
		t.Fatal("plain Shortcut+Z was recognized as redo")
	}
}

func TestTextAreaScrollTargetOwnsOnlyVisibleEnabledMultilineFields(t *testing.T) {
	area := &semantic.Node{
		ID: "notes", Type: "text_area", Enabled: true, Visible: true, InViewport: true,
		Bounds: &semantic.Rect{X: 10, Y: 10, Width: 100, Height: 60},
		Clip:   &semantic.Rect{X: 0, Y: 0, Width: 200, Height: 200},
	}
	root := &semantic.Node{Children: []*semantic.Node{area}}
	if got := textAreaScrollTarget(root, image.Pt(20, 20)); got != area {
		t.Fatalf("scroll target = %+v, want text area", got)
	}
	area.Enabled = false
	if got := textAreaScrollTarget(root, image.Pt(20, 20)); got != nil {
		t.Fatalf("disabled text area owned scrolling: %+v", got)
	}
	area.Enabled = true
	if got := textAreaScrollTarget(root, image.Pt(150, 20)); got != nil {
		t.Fatalf("outside point targeted text area: %+v", got)
	}
}

func TestStudioTextHitTargetsUseFinalPaintOrder(t *testing.T) {
	root := &semantic.Node{Children: []*semantic.Node{
		{Handle: "front-source", Role: "textbox", Type: "text_field", Enabled: true, Visible: true, InViewport: true,
			Bounds: &semantic.Rect{X: 0, Y: 0, Width: 100, Height: 50}, Clip: &semantic.Rect{X: 0, Y: 0, Width: 100, Height: 50}, PaintOrder: 20},
		{Handle: "back-source", Role: "textbox", Type: "text_field", Enabled: true, Visible: true, InViewport: true,
			Bounds: &semantic.Rect{X: 0, Y: 0, Width: 100, Height: 50}, Clip: &semantic.Rect{X: 0, Y: 0, Width: 100, Height: 50}, PaintOrder: 5},
	}}
	if got := hitTextField(root, image.Pt(20, 20)); got == nil || got.Handle != "front-source" {
		t.Fatalf("text-field hit = %+v, want final rank 20 node", got)
	}
	areaRoot := &semantic.Node{Children: []*semantic.Node{
		{Handle: "area-front", Type: "text_area", Role: "textbox", Enabled: true, Visible: true, InViewport: true,
			Bounds: &semantic.Rect{X: 0, Y: 0, Width: 100, Height: 50}, Clip: &semantic.Rect{X: 0, Y: 0, Width: 100, Height: 50}, PaintOrder: 30},
		{Handle: "area-back", Type: "text_area", Role: "textbox", Enabled: true, Visible: true, InViewport: true,
			Bounds: &semantic.Rect{X: 0, Y: 0, Width: 100, Height: 50}, Clip: &semantic.Rect{X: 0, Y: 0, Width: 100, Height: 50}, PaintOrder: 10},
	}}
	if got := textAreaScrollTarget(areaRoot, image.Pt(20, 20)); got == nil || got.Handle != "area-front" {
		t.Fatalf("text-area scroll hit = %+v, want final rank 30 node", got)
	}
}
