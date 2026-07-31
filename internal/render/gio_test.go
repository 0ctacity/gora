package render

import (
	"image"
	"image/color"
	"path/filepath"
	"testing"
	"time"

	"gioui.org/gpu/headless"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/unit"
	"gioui.org/widget/material"

	"gora/internal/document"
	"gora/internal/project"
)

func TestLayoutGioUsesGrowSpaceAndCentersSurfaceContent(t *testing.T) {
	root := &project.Node{
		Handle: "header",
		Type:   "stack",
		Props:  map[string]any{"direction": "horizontal", "alignment": "center"},
		Children: []*project.Node{
			{Handle: "title", Type: "text", Props: map[string]any{"text": "Good morning, Ada", "size": int64(30), "weight": int64(700)}},
			{Handle: "space", Type: "spacer", Place: map[string]any{"grow": int64(1)}},
			{
				Handle: "button",
				Type:   "surface",
				Props:  map[string]any{"width": int64(150), "height": int64(42), "background": "#635BFF", "radius": int64(10)},
				Children: []*project.Node{{
					Handle: "button-label",
					Type:   "text",
					Props:  map[string]any{"text": "Export report", "size": int64(14), "color": "#FFFFFF"},
					Place:  map[string]any{"alignment": "center"},
				}},
			},
		},
	}

	var operations op.Ops
	gtx := layout.Context{
		Ops:         &operations,
		Metric:      unit.Metric{PxPerDp: 1, PxPerSp: 1},
		Constraints: layout.Exact(image.Pt(600, 60)),
	}
	result := LayoutGio(gtx, material.NewTheme(), root, image.Pt(600, 60), State{})

	if got := result.Bounds["button"]; got != image.Rect(450, 9, 600, 51) {
		t.Fatalf("button bounds = %v", got)
	}
	if title := result.Bounds["title"]; title.Min.Y <= 0 || title.Max.Y >= 60 {
		t.Fatalf("title should use its intrinsic height and be vertically centered, got %v", title)
	}
	label := result.Bounds["button-label"]
	button := result.Bounds["button"]
	if label.Empty() || label.Min.X <= button.Min.X || label.Max.X >= button.Max.X {
		t.Fatalf("centered label bounds %v are not inside button %v", label, button)
	}
	if got := label.Min.X + label.Max.X; absInt(got-(button.Min.X+button.Max.X)) > 1 {
		t.Fatalf("label horizontal center = %d, button center = %d", got, button.Min.X+button.Max.X)
	}
	if got := label.Min.Y + label.Max.Y; absInt(got-(button.Min.Y+button.Max.Y)) > 1 {
		t.Fatalf("label vertical center = %d, button center = %d", got, button.Min.Y+button.Max.Y)
	}
}

func TestCPUAndGioUseIdenticalStackGeometry(t *testing.T) {
	root := &project.Node{
		Handle: "stack", Type: "stack",
		Props: map[string]any{"direction": "horizontal", "wrap": true, "column_gap": int64(8), "row_gap": int64(6), "alignment": "start"},
		Children: []*project.Node{
			{Handle: "a", Type: "surface", Place: map[string]any{"basis": map[string]any{"percent": int64(45)}}, Props: map[string]any{"height": int64(20)}},
			{Handle: "b", Type: "surface", Place: map[string]any{"basis": map[string]any{"percent": int64(45)}}, Props: map[string]any{"aspect_ratio": map[string]any{"width": int64(2), "height": int64(1)}}},
			{Handle: "c", Type: "surface", Place: map[string]any{"basis": int64(80)}, Props: map[string]any{"height": int64(10)}},
		},
	}
	viewport := image.Pt(180, 80)
	cpu := Render(root, viewport, State{})
	var operations op.Ops
	gio := LayoutGio(layout.Context{
		Ops: &operations, Metric: unit.Metric{PxPerDp: 1, PxPerSp: 1}, Constraints: layout.Exact(viewport),
	}, material.NewTheme(), root, viewport, State{})
	for _, handle := range []string{"stack", "a", "b", "c"} {
		if cpu.Bounds[handle] != gio.Bounds[handle] {
			t.Fatalf("%s bounds: CPU=%v Gio=%v", handle, cpu.Bounds[handle], gio.Bounds[handle])
		}
	}
}

func TestGioLabelLoadsDocumentLocalFontIntoNativeShaper(t *testing.T) {
	theme := material.NewTheme()
	renderer := gioRenderer{theme: theme, opacity: 1}
	fontPath, err := filepath.Abs(filepath.Join("..", "..", "examples", "dashboard", "assets", "StudioSans.ttf"))
	if err != nil {
		t.Fatal(err)
	}
	label := renderer.label(&project.Node{
		Type: "text",
		Props: map[string]any{
			"text": "Native",
			"font": fontPath,
		},
	})
	if label.Shaper == theme.Shaper {
		t.Fatal("local font was ignored and the theme shaper was reused")
	}
}

func TestNativeShadowUsesSoftExpandedLayers(t *testing.T) {
	layers := shadowLayers(image.Rect(10, 10, 110, 60), map[string]any{
		"x": int64(0), "y": int64(8), "blur": int64(28), "color": "#11182712",
	})
	if len(layers) < 8 {
		t.Fatalf("blurred shadow has only %d layers", len(layers))
	}
	offsetBounds := image.Rect(10, 18, 110, 68)
	if !offsetBounds.In(layers[0].bounds.Inset(1)) {
		t.Fatalf("outer shadow layer %v does not surround offset surface %v", layers[0].bounds, offsetBounds)
	}
	if layers[0].color.A >= layers[len(layers)-1].color.A {
		t.Fatalf("outer alpha %d should be softer than inner alpha %d", layers[0].color.A, layers[len(layers)-1].color.A)
	}
	if layers[len(layers)-1].color == (color.NRGBA{}) {
		t.Fatal("inner shadow layer is transparent")
	}
}

func TestRoundedSurfaceBorderDoesNotSquareOffOuterCorners(t *testing.T) {
	root := &project.Node{
		Handle: "button", Type: "button",
		Props: map[string]any{
			"label": "Save", "background": "#FFFFFF", "radius": float64(12),
			"border": map[string]any{"thickness": float64(2), "color": "#172033"},
		},
		Children: []*project.Node{{Handle: "label", Type: "text", Props: map[string]any{"content": "Save"}}},
	}
	captured, err := captureGio(root, image.Pt(100, 40), State{}, 1)
	if err != nil {
		t.Fatal(err)
	}
	if corner := captured.RGBAAt(0, 0); corner.A != 0 {
		t.Fatalf("rounded border painted square corner %#v", corner)
	}
}

func TestScrollClipsDoNotPaintBeyondVisibleViewport(t *testing.T) {
	viewport := image.Rect(0, 0, 100, 100)
	content := image.Rect(0, 0, 100, 300)

	visible, prepaint := scrollClips(viewport, content, "vertical")

	if visible != viewport {
		t.Fatalf("visible clip = %v", visible)
	}
	if want := viewport; prepaint != want {
		t.Fatalf("prepaint clip = %v, want %v", prepaint, want)
	}
}

func TestGioCacheBuildsImmutableSceneOnceAndRepositionsScrolledInspection(t *testing.T) {
	root := &project.Node{
		Handle: "feed-scroll",
		Name:   "feed",
		Type:   "scroll",
		Props:  map[string]any{"axis": "vertical"},
		Children: []*project.Node{{
			Handle: "feed-content",
			Type:   "surface",
			Props:  map[string]any{"height": int64(300), "background": "#FFFFFF"},
		}},
	}
	theme := material.NewTheme()
	viewport := image.Pt(100, 100)
	gtx := func() layout.Context {
		var operations op.Ops
		return layout.Context{
			Ops:         &operations,
			Metric:      unit.Metric{PxPerDp: 1, PxPerSp: 1},
			Constraints: layout.Exact(viewport),
		}
	}

	var cache GioCache
	first := cache.Layout(gtx(), theme, root, viewport, State{})
	second := cache.Layout(gtx(), theme, root, viewport, State{
		Scroll: map[string]image.Point{"feed": image.Pt(0, 40)},
	})

	if cache.builds != 1 {
		t.Fatalf("scene builds = %d, want 1", cache.builds)
	}
	if got := first.Bounds["feed-content"]; got != image.Rect(0, 0, 100, 300) {
		t.Fatalf("initial content bounds = %v", got)
	}
	if got := second.Bounds["feed-content"]; got != image.Rect(0, -40, 100, 260) {
		t.Fatalf("scrolled content bounds = %v", got)
	}
	for _, inspection := range second.Inspections {
		if inspection.Handle == "feed-content" && inspection.Clip != image.Rect(0, 0, 100, 100) {
			t.Fatalf("scrolled inspection clip = %v", inspection.Clip)
		}
	}
}

func TestGioCacheInvalidatesForViewportMetricAndResolvedRootChanges(t *testing.T) {
	root := &project.Node{Handle: "root", Type: "surface", Props: map[string]any{"background": "#FFFFFF"}}
	theme := material.NewTheme()
	var cache GioCache
	layoutCached := func(root *project.Node, viewport image.Point, scale float32) {
		var operations op.Ops
		cache.Layout(layout.Context{
			Ops:         &operations,
			Metric:      unit.Metric{PxPerDp: scale, PxPerSp: scale},
			Constraints: layout.Exact(image.Pt(int(float32(viewport.X)*scale), int(float32(viewport.Y)*scale))),
		}, theme, root, viewport, State{})
	}

	layoutCached(root, image.Pt(100, 100), 1)
	layoutCached(root, image.Pt(100, 100), 1)
	layoutCached(root, image.Pt(120, 100), 1)
	layoutCached(root, image.Pt(120, 100), 2)
	layoutCached(&project.Node{Handle: "replacement", Type: "surface"}, image.Pt(120, 100), 2)

	if cache.builds != 4 {
		t.Fatalf("scene builds = %d, want 4", cache.builds)
	}
}

func TestGioCacheReplaysTransientButtonPaintWithoutGeometryRebuild(t *testing.T) {
	root := &project.Node{
		Handle: "button", Type: "button", Props: map[string]any{"label": "Save", "background": "#000000"},
		Variants: []document.Variant{{When: document.Condition{Interaction: "hovered"}, Props: map[string]any{"background": "#FF0000"}}},
		Children: []*project.Node{{Handle: "label", Type: "text", Props: map[string]any{"content": "Save"}}},
	}
	theme := material.NewTheme()
	viewport := image.Pt(100, 40)
	var cache GioCache
	layoutCached := func(state State) GioResult {
		var operations op.Ops
		return cache.Layout(layout.Context{
			Ops: &operations, Metric: unit.Metric{PxPerDp: 1, PxPerSp: 1}, Constraints: layout.Exact(viewport),
		}, theme, root, viewport, state)
	}
	layoutCached(State{})
	hovered := layoutCached(State{Hovered: "button"})
	if cache.builds != 1 {
		t.Fatalf("scene builds = %d, want 1", cache.builds)
	}
	if len(hovered.Inspections) == 0 || hovered.Inspections[0].Props["background"] != "#FF0000" || !hovered.Inspections[0].Hovered {
		t.Fatalf("hovered inspection = %+v", hovered.Inspections)
	}
}

func TestGioCacheReplaysOffscreenScrollContentAcrossNativeFrames(t *testing.T) {
	root := &project.Node{
		Handle: "scroll",
		Name:   "feed",
		Type:   "scroll",
		Props:  map[string]any{"axis": "vertical"},
		Children: []*project.Node{{
			Handle: "content",
			Type:   "stack",
			Props:  map[string]any{"height": int64(40), "direction": "vertical"},
			Children: []*project.Node{
				{Handle: "red", Type: "surface", Props: map[string]any{"height": int64(20), "background": "#FF0000"}},
				{Handle: "blue", Type: "surface", Props: map[string]any{"height": int64(20), "background": "#0000FF"}},
			},
		}},
	}
	window, err := headless.NewWindow(20, 20)
	if err != nil {
		t.Fatal(err)
	}
	defer window.Release()
	theme := material.NewTheme()
	var cache GioCache
	renderFrame := func(state State) color.RGBA {
		var operations op.Ops
		cache.Layout(layout.Context{
			Ops:         &operations,
			Now:         time.Now(),
			Metric:      unit.Metric{PxPerDp: 1, PxPerSp: 1},
			Constraints: layout.Exact(image.Pt(20, 20)),
		}, theme, root, image.Pt(20, 20), state)
		if err := window.Frame(&operations); err != nil {
			t.Fatal(err)
		}
		captured := image.NewRGBA(image.Rect(0, 0, 20, 20))
		if err := window.Screenshot(captured); err != nil {
			t.Fatal(err)
		}
		return captured.RGBAAt(10, 10)
	}

	if got := renderFrame(State{}); got != (color.RGBA{R: 255, A: 255}) {
		t.Fatalf("first cached frame pixel = %#v", got)
	}
	if got := renderFrame(State{Scroll: map[string]image.Point{"feed": image.Pt(0, 20)}}); got != (color.RGBA{B: 255, A: 255}) {
		t.Fatalf("scrolled cached frame pixel = %#v", got)
	}
	if cache.builds != 1 {
		t.Fatalf("native frame scene builds = %d, want 1", cache.builds)
	}
}

func absInt(value int) int {
	if value < 0 {
		return -value
	}
	return value
}
