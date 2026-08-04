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

func TestCPUAndGioUseIdenticalFieldGeometry(t *testing.T) {
	root := &project.Node{Handle: "field-box", Type: "field_box", Props: map[string]any{
		"width": float64(150), "height": float64(48),
		"padding": map[string]any{"top": float64(8), "right": float64(10), "bottom": float64(8), "left": float64(10)},
		"text":    "Ada שלום", "size": float64(16), "selection_start": float64(2), "selection_end": float64(7),
	}}
	viewport := image.Pt(180, 60)
	cpu := Render(root, viewport, State{})
	var operations op.Ops
	gio := LayoutGio(layout.Context{Ops: &operations, Metric: unit.Metric{PxPerDp: 1, PxPerSp: 1}, Constraints: layout.Exact(viewport)}, material.NewTheme(), root, viewport, State{})
	if cpu.Bounds["field-box"] != gio.Bounds["field-box"] {
		t.Fatalf("field bounds: CPU=%v Gio=%v", cpu.Bounds["field-box"], gio.Bounds["field-box"])
	}
	cpuGeometry, gioGeometry := cpu.Geometry["field-box"], gio.Geometry["field-box"]
	if cpuGeometry.Clip != gioGeometry.Clip {
		t.Fatalf("field clip: CPU=%v Gio=%v", cpuGeometry.Clip, gioGeometry.Clip)
	}
	for _, property := range []string{"internal_viewport_x", "internal_viewport_y", "internal_viewport_width", "internal_viewport_height"} {
		if cpuGeometry.Props[property] != gioGeometry.Props[property] {
			t.Fatalf("%s: CPU=%v Gio=%v", property, cpuGeometry.Props[property], gioGeometry.Props[property])
		}
	}
}

func TestCPUAndGioUseIdenticalSemanticSliderGeometry(t *testing.T) {
	minimum, maximum, step := 0.0, 100.0, 5.0
	root := &project.Node{
		Handle: "slider", Type: "slider", Name: "volume-slider", Binding: "volume",
		BindingState: &document.StateDeclaration{Type: "number", Min: &minimum, Max: &maximum, Step: &step},
		Props:        map[string]any{"label": "Volume", "value": float64(50), "orientation": "horizontal"},
		Children: []*project.Node{
			{Handle: "track", Type: "slider_track", Props: map[string]any{"height": float64(4)}},
			{Handle: "fill", Type: "slider_fill", Props: map[string]any{"height": float64(4)}},
			{Handle: "thumb", Type: "slider_thumb", Props: map[string]any{"width": float64(12), "height": float64(12)}},
		},
	}
	viewport := image.Pt(200, 30)
	cpu := Render(root, viewport, State{})
	var operations op.Ops
	gio := LayoutGio(layout.Context{Ops: &operations, Metric: unit.Metric{PxPerDp: 1, PxPerSp: 1}, Constraints: layout.Exact(viewport)}, material.NewTheme(), root, viewport, State{})
	for _, handle := range []string{"slider", "track", "fill", "thumb"} {
		if cpu.Bounds[handle] != gio.Bounds[handle] {
			t.Fatalf("%s bounds: CPU=%v Gio=%v", handle, cpu.Bounds[handle], gio.Bounds[handle])
		}
	}
	if got := cpu.Bounds["thumb"]; got.Min.X != 94 || got.Max.X != 106 || got.Min.Y != 9 || got.Max.Y != 21 {
		t.Fatalf("thumb bounds = %v", got)
	}
	if got := cpu.Bounds["fill"]; got.Min.X != 0 || got.Max.X != 100 {
		t.Fatalf("fill bounds = %v", got)
	}
}

func TestCPUAndGioUseIdenticalTabsAndOpenSelectGeometry(t *testing.T) {
	tabs := &project.Node{Handle: "tabs", Type: "tabs", Props: map[string]any{"orientation": "horizontal", "gap": float64(8), "panel_gap": float64(10)}, Children: []*project.Node{
		{Handle: "tab-a", Type: "tab", Props: map[string]any{"width": float64(70), "height": float64(28)}, Children: []*project.Node{{Handle: "tab-a-label", Type: "text", Props: map[string]any{"text": "A"}}}},
		{Handle: "tab-b", Type: "tab", Props: map[string]any{"width": float64(80), "height": float64(28)}, Children: []*project.Node{{Handle: "tab-b-label", Type: "text", Props: map[string]any{"text": "B"}}}},
		{Handle: "panel-a", Type: "tab_panel", Hidden: true},
		{Handle: "panel-b", Type: "tab_panel", Children: []*project.Node{{Handle: "panel-content", Type: "surface"}}},
	}}
	viewport := image.Pt(240, 140)
	cpu := Render(tabs, viewport, State{})
	var operations op.Ops
	gio := LayoutGio(layout.Context{Ops: &operations, Metric: unit.Metric{PxPerDp: 1, PxPerSp: 1}, Constraints: layout.Exact(viewport)}, material.NewTheme(), tabs, viewport, State{})
	for _, handle := range []string{"tabs", "tab-a", "tab-b", "panel-b", "panel-content"} {
		if cpu.Bounds[handle] != gio.Bounds[handle] {
			t.Fatalf("%s bounds: CPU=%v Gio=%v", handle, cpu.Bounds[handle], gio.Bounds[handle])
		}
	}
	if cpu.Bounds["tab-b"].Min.X != 78 || cpu.Bounds["panel-b"].Min.Y != 38 {
		t.Fatalf("tabs geometry = tab-b %v panel %v", cpu.Bounds["tab-b"], cpu.Bounds["panel-b"])
	}
}

func TestOpenSelectPopupUsesViewportClampedTopLayer(t *testing.T) {
	selectNode := &project.Node{Handle: "select", Type: "select", Props: map[string]any{"width": float64(100), "height": float64(30), "open": true}, Children: []*project.Node{
		{Handle: "trigger", Type: "select_trigger", Children: []*project.Node{{Handle: "trigger-label", Type: "text", Props: map[string]any{"text": "Design"}}}},
		{Handle: "popup", Type: "select_popup", Props: map[string]any{"gap": float64(2), "max_height": float64(70), "match_trigger_width": true}, Children: []*project.Node{
			{Handle: "option-a", Type: "option", Props: map[string]any{"height": float64(24)}, Children: []*project.Node{{Handle: "option-a-label", Type: "text", Props: map[string]any{"text": "Design"}}}},
			{Handle: "option-b", Type: "option", Props: map[string]any{"height": float64(24)}, Children: []*project.Node{{Handle: "option-b-label", Type: "text", Props: map[string]any{"text": "Engineering"}}}},
		}},
	}}
	root := &project.Node{Handle: "overlay", Type: "overlay", Children: []*project.Node{
		selectNode,
		{Handle: "later", Type: "surface", Props: map[string]any{"width": float64(140), "height": float64(80)}},
	}}
	viewport := image.Pt(160, 100)
	cpu := Render(root, viewport, State{})
	var operations op.Ops
	gio := LayoutGio(layout.Context{Ops: &operations, Metric: unit.Metric{PxPerDp: 1, PxPerSp: 1}, Constraints: layout.Exact(viewport)}, material.NewTheme(), root, viewport, State{})
	for _, handle := range []string{"trigger", "popup", "option-a", "option-b"} {
		if cpu.Bounds[handle] != gio.Bounds[handle] {
			t.Fatalf("%s bounds: CPU=%v Gio=%v", handle, cpu.Bounds[handle], gio.Bounds[handle])
		}
	}
	if got := cpu.Bounds["popup"]; got.Min.Y != 30 || got.Max.X != 100 || got.Max.Y > viewport.Y {
		t.Fatalf("popup bounds = %v", got)
	}
	if cpu.Geometry["popup"].PaintOrder <= cpu.Geometry["later"].PaintOrder {
		t.Fatalf("popup paint order=%d later=%d", cpu.Geometry["popup"].PaintOrder, cpu.Geometry["later"].PaintOrder)
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

func TestGioCacheBuildsImmutableSceneOnceAndRepositionsScrolledGeometry(t *testing.T) {
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
	if geometry := second.Geometry["feed-content"]; geometry.Clip != image.Rect(0, 0, 100, 100) {
		t.Fatalf("scrolled geometry clip = %v", geometry.Clip)
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
	if hovered.Tree == nil || hovered.Tree.Props["background"] != "#FF0000" || !hovered.Tree.Hovered {
		t.Fatalf("hovered runtime tree = %+v", hovered.Tree)
	}
}

func TestGioCacheReplaysFieldDecorationWithoutGeometryRebuild(t *testing.T) {
	box := &project.Node{Handle: "box", Type: "field_box", Props: map[string]any{
		"field_handle": "field", "text": "Ada", "selection_start": float64(1), "selection_end": float64(1),
		"background": "#FFFFFF", "color": "#111111", "caret_color": "#FF0000", "size": float64(16),
	}}
	root := &project.Node{
		Handle: "field", Type: "text_field", Name: "name-field", Props: map[string]any{"label": "Name"},
		Children: []*project.Node{box},
	}
	theme := material.NewTheme()
	viewport := image.Pt(100, 32)
	var cache GioCache
	layoutCached := func(state State) GioResult {
		var operations op.Ops
		return cache.Layout(layout.Context{
			Ops: &operations, Metric: unit.Metric{PxPerDp: 1, PxPerSp: 1}, Constraints: layout.Exact(viewport),
		}, theme, root, viewport, state)
	}
	layoutCached(State{})
	focused := layoutCached(State{Focused: "field"})
	if cache.builds != 1 {
		t.Fatalf("scene builds = %d, want 1", cache.builds)
	}
	foundField := false
	for _, item := range cache.scene.items {
		foundField = foundField || item.field != nil
	}
	if !foundField {
		t.Fatal("field box was retained as static paint instead of transient field paint")
	}
	if focused.Tree == nil || !focused.Tree.Focused {
		t.Fatalf("focused runtime tree = %+v", focused.Tree)
	}
}

func TestGioFieldTextUsesTheCaretGeometryShaper(t *testing.T) {
	theme := material.NewTheme()
	renderer := gioRenderer{theme: theme}
	field := &project.Node{Type: "field_box", Props: map[string]any{
		"field_handle": "name-field", "text": "Ada Lovelace", "size": float64(20),
	}}

	label := renderer.label(field)
	if label.Shaper != fieldShapeState.fallback {
		t.Fatal("field text and caret geometry use different fallback shapers")
	}
	if ordinary := renderer.label(&project.Node{Type: "text"}); ordinary.Shaper != theme.Shaper {
		t.Fatal("ordinary document text unexpectedly changed shaper")
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
