package studio

import (
	"context"
	"image"
	"os"
	"path/filepath"
	"testing"
	"time"

	"gioui.org/app"
	"gioui.org/f32"
	"gioui.org/io/input"
	"gioui.org/io/key"
	"gioui.org/io/pointer"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/unit"
	"gioui.org/widget/material"

	"gora/internal/semantic"
	"gora/internal/session"
)

func TestLayoutAppContentUsesNativeWindowViewport(t *testing.T) {
	repositoryRoot, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := NewRuntime(repositoryRoot, filepath.Join(repositoryRoot, "examples", "interactivity", "app.gora"))
	if err != nil {
		t.Fatal(err)
	}
	var operations op.Ops
	state := newAppUIState()
	gtx := layout.Context{
		Ops: &operations, Metric: unit.Metric{PxPerDp: 1, PxPerSp: 1},
		Constraints: layout.Exact(image.Pt(640, 480)),
	}
	dimensions := layoutAppContent(gtx, material.NewTheme(), runtime, state, new(app.Window))
	if dimensions.Size != image.Pt(640, 480) {
		t.Fatalf("dimensions = %v", dimensions.Size)
	}
	if viewport := runtime.Snapshot().Viewport; viewport != image.Pt(640, 480) {
		t.Fatalf("viewport = %v", viewport)
	}
	if len(semantic.Flatten(state.runtimeTree)) <= 1 {
		t.Fatal("content-only app did not expose document interactions")
	}
}

func TestAppContentRoutesDiagonalScrollThroughPublishedGeometry(t *testing.T) {
	dir := t.TempDir()
	entry := filepath.Join(dir, "app.gora")
	if err := os.WriteFile(entry, []byte(`gora: 1
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
	runtime, err := NewRuntime(dir, entry)
	if err != nil {
		t.Fatal(err)
	}
	state := newAppUIState()
	var inputRouter input.Router
	var operations op.Ops
	gtx := layout.Context{
		Ops: &operations, Source: inputRouter.Source(),
		Metric: unit.Metric{PxPerDp: 1, PxPerSp: 1}, Constraints: layout.Exact(image.Pt(100, 80)),
	}
	frame := func() {
		gtx.Reset()
		gtx.Source = inputRouter.Source()
		gtx.Constraints = layout.Exact(image.Pt(100, 80))
		layoutAppContent(gtx, material.NewTheme(), runtime, state, new(app.Window))
		inputRouter.Frame(gtx.Ops)
	}
	frame()
	before := runtime.Snapshot()
	inputRouter.Queue(pointer.Event{Kind: pointer.Scroll, Position: f32.Pt(40, 40), Scroll: f32.Pt(30, 20)})
	frame()
	after := runtime.Snapshot()
	if got := after.Scroll["workspace"]; got != image.Pt(30, 20) {
		t.Fatalf("app diagonal scroll = %v, want (30,20)", got)
	}
	if after.RuntimeRevision != before.RuntimeRevision+1 {
		t.Fatalf("app diagonal scroll revision = %d, want one atomic commit from %d", after.RuntimeRevision, before.RuntimeRevision)
	}
	next, err := runtime.RuntimeTree()
	if err != nil {
		t.Fatal(err)
	}
	content := next.Children[0]
	if content == nil || content.Bounds == nil || content.Bounds.X != -30 || content.Bounds.Y != -20 {
		t.Fatalf("app next-frame content bounds = %+v, want x=-30 y=-20", content)
	}
}

func TestAppContentScrollbarThumbDragUpdatesNextFrameGeometry(t *testing.T) {
	dir := t.TempDir()
	entry := filepath.Join(dir, "app.gora")
	if err := os.WriteFile(entry, []byte(`gora: 1
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
	runtime, err := NewRuntime(dir, entry)
	if err != nil {
		t.Fatal(err)
	}
	state := newAppUIState()
	var inputRouter input.Router
	var operations op.Ops
	_, _ = inputRouter.Event(trackpadZoomFilter(state))
	_, _ = inputRouter.Event(pointer.Filter{Target: &state.interactionInput, Kinds: pointer.Enter | pointer.Leave | pointer.Move | pointer.Press | pointer.Release | pointer.Cancel})
	frame := func() {
		operations.Reset()
		gtx := layout.Context{Ops: &operations, Source: inputRouter.Source(), Metric: unit.Metric{PxPerDp: 1, PxPerSp: 1}, Constraints: layout.Exact(image.Pt(100, 80))}
		layoutAppContent(gtx, material.NewTheme(), runtime, state, new(app.Window))
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
		t.Fatalf("app thumb drag did not update runtime offset: %v", got)
	}
	beforeThumb := thumb
	frame()
	updated := findScrollbar("vertical")
	if updated == nil || len(updated.Children) < 2 || updated.Children[1].Bounds == nil {
		t.Fatalf("next-frame vertical scrollbar = %+v", updated)
	}
	if got := updated.Children[1].Bounds.ImageRectangle(); got == beforeThumb {
		t.Fatalf("app next-frame thumb remained at %v after drag", got)
	}
}

func TestAppContentDoesNotRouteCommandModifiedScrollToDocument(t *testing.T) {
	dir := t.TempDir()
	entry := filepath.Join(dir, "app.gora")
	if err := os.WriteFile(entry, []byte(`gora: 1
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
	runtime, err := NewRuntime(dir, entry)
	if err != nil {
		t.Fatal(err)
	}
	state := newAppUIState()
	var inputRouter input.Router
	var operations op.Ops
	gtx := layout.Context{
		Ops: &operations, Source: inputRouter.Source(),
		Metric: unit.Metric{PxPerDp: 1, PxPerSp: 1}, Constraints: layout.Exact(image.Pt(100, 80)),
	}
	frame := func() {
		gtx.Reset()
		gtx.Source = inputRouter.Source()
		gtx.Constraints = layout.Exact(image.Pt(100, 80))
		layoutAppContent(gtx, material.NewTheme(), runtime, state, new(app.Window))
		inputRouter.Frame(gtx.Ops)
	}
	frame()
	before := runtime.Snapshot()
	inputRouter.Queue(pointer.Event{Kind: pointer.Scroll, Position: f32.Pt(40, 40), Scroll: f32.Pt(30, 20), Modifiers: key.ModShortcut})
	frame()
	after := runtime.Snapshot()
	if got := after.Scroll["workspace"]; got != (image.Point{}) {
		t.Fatalf("command-modified app scroll = %v, want no document mutation", got)
	}
	if after.RuntimeRevision != before.RuntimeRevision {
		t.Fatalf("command-modified app scroll revision = %d, want %d", after.RuntimeRevision, before.RuntimeRevision)
	}
}

func TestRunHeadlessServesSessionUntilCanceled(t *testing.T) {
	dir := t.TempDir()
	entry := filepath.Join(dir, "app.gora")
	if err := os.WriteFile(entry, []byte("gora: 1\nkind: app\nviewport: { width: 100, height: 80 }\nentry: main\nscreens:\n  main: { type: spacer }\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	socketDir, err := os.MkdirTemp("/tmp", "gora-headless-test-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(socketDir) })
	socket := filepath.Join(socketDir, "headless.sock")
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- runHeadless(ctx, dir, entry, socket) }()

	deadline := time.Now().Add(2 * time.Second)
	for {
		response, err := session.Send(socket, session.Request{Action: "focus"}, 100*time.Millisecond)
		if err == nil && response.OK {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("headless session did not start: %v", err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("headless session did not stop after cancellation")
	}
}
