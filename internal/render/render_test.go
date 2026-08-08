package render

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/unit"
	"gioui.org/widget/material"

	"gora/internal/project"
	"gora/internal/semantic"
)

func TestRenderStackRecordsBoundsAndPaintsSurface(t *testing.T) {
	root := &project.Node{
		Handle: "root",
		Type:   "stack",
		Props:  map[string]any{"direction": "vertical", "gap": int64(4), "padding": map[string]any{"top": int64(2), "right": int64(2), "bottom": int64(2), "left": int64(2)}},
		Children: []*project.Node{
			{Handle: "a", Type: "surface", Props: map[string]any{"height": int64(10), "background": "#FF0000"}},
			{Handle: "b", Type: "surface", Props: map[string]any{"height": int64(10), "background": "#00FF00"}},
		},
	}

	result := Render(root, image.Pt(40, 30), State{})
	if got := result.Bounds["a"]; got != image.Rect(2, 2, 38, 12) {
		t.Fatalf("first child bounds = %v", got)
	}
	if got := result.Bounds["b"]; got != image.Rect(2, 16, 38, 26) {
		t.Fatalf("second child bounds = %v", got)
	}
	if got := result.Image.RGBAAt(3, 3); got != (color.RGBA{R: 255, A: 255}) {
		t.Fatalf("surface pixel = %#v", got)
	}
}

func TestCaptureRefusesExistingOutput(t *testing.T) {
	path := filepath.Join(t.TempDir(), "capture.png")
	if err := os.WriteFile(path, []byte("existing"), 0o600); err != nil {
		t.Fatal(err)
	}
	err := Capture(path, &project.Node{Handle: "root", Type: "spacer"}, image.Pt(8, 8), State{}, 1)
	if err == nil {
		t.Fatal("Capture unexpectedly overwrote an existing file")
	}
}

func TestGridHonorsFractionalTracksAndSpans(t *testing.T) {
	root := &project.Node{
		Handle: "grid", Type: "grid",
		Props: map[string]any{"columns": []any{"1fr", "2fr"}, "gap": int64(3)},
		Children: []*project.Node{
			{Handle: "first", Type: "spacer"},
			{Handle: "span", Type: "spacer", Place: map[string]any{"column": int64(0), "row": int64(1), "column_span": int64(2)}},
		},
	}
	result := Render(root, image.Pt(93, 40), State{})
	if got := result.Bounds["first"]; got != image.Rect(0, 0, 30, 19) {
		t.Fatalf("first track bounds = %v", got)
	}
	if got := result.Bounds["span"]; got != image.Rect(0, 22, 93, 40) {
		t.Fatalf("spanning bounds = %v", got)
	}
}

func TestStackDistributesRemainingSpaceByGrowWeight(t *testing.T) {
	root := &project.Node{
		Handle: "stack", Type: "stack", Props: map[string]any{"direction": "horizontal"},
		Children: []*project.Node{
			{Handle: "fixed", Type: "spacer", Props: map[string]any{"width": int64(20)}},
			{Handle: "one", Type: "spacer", Place: map[string]any{"grow": int64(1)}},
			{Handle: "two", Type: "spacer", Place: map[string]any{"grow": int64(2)}},
		},
	}
	result := Render(root, image.Pt(80, 10), State{})
	if got := result.Bounds["one"].Dx(); got != 20 {
		t.Fatalf("grow=1 width = %d", got)
	}
	if got := result.Bounds["two"].Dx(); got != 40 {
		t.Fatalf("grow=2 width = %d", got)
	}
}

func TestStackResolvesPercentageAndAspectRatio(t *testing.T) {
	root := &project.Node{
		Handle: "stack", Type: "stack",
		Props: map[string]any{"direction": "horizontal", "alignment": "start"},
		Children: []*project.Node{{
			Handle: "media", Type: "surface",
			Props: map[string]any{
				"width":        map[string]any{"percent": float64(50)},
				"aspect_ratio": map[string]any{"width": int64(2), "height": int64(1)},
			},
		}},
	}

	result := Render(root, image.Pt(200, 120), State{})
	if got := result.Bounds["media"]; got != image.Rect(0, 0, 100, 50) {
		t.Fatalf("percentage ratio bounds = %v, want (0,0)-(100,50)", got)
	}
}

func TestStackWrapsWithDirectionalGaps(t *testing.T) {
	root := &project.Node{
		Handle: "stack", Type: "stack",
		Props: map[string]any{
			"direction": "horizontal", "wrap": true,
			"gap": int64(1), "column_gap": int64(10), "row_gap": int64(5),
		},
		Children: []*project.Node{
			{Handle: "a", Type: "surface", Props: map[string]any{"width": int64(45), "height": int64(20)}},
			{Handle: "b", Type: "surface", Props: map[string]any{"width": int64(45), "height": int64(20)}},
			{Handle: "c", Type: "surface", Props: map[string]any{"width": int64(45), "height": int64(20)}},
		},
	}

	result := Render(root, image.Pt(100, 60), State{})
	if got := result.Bounds["b"]; got != image.Rect(55, 0, 100, 20) {
		t.Fatalf("second wrapped item = %v", got)
	}
	if got := result.Bounds["c"]; got != image.Rect(0, 25, 45, 45) {
		t.Fatalf("third wrapped item = %v", got)
	}
}

func TestStackShrinksProportionallyAndFreezesAtMinimum(t *testing.T) {
	root := &project.Node{
		Handle: "stack", Type: "stack", Props: map[string]any{"direction": "horizontal"},
		Children: []*project.Node{
			{Handle: "a", Type: "surface", Props: map[string]any{"min_width": int64(55)}, Place: map[string]any{"basis": int64(60), "shrink": int64(1)}},
			{Handle: "b", Type: "surface", Place: map[string]any{"basis": int64(60), "shrink": int64(1)}},
		},
	}

	result := Render(root, image.Pt(100, 20), State{})
	if got := result.Bounds["a"].Dx(); got != 55 {
		t.Fatalf("minimum-frozen width = %d, want 55", got)
	}
	if got := result.Bounds["b"].Dx(); got != 45 {
		t.Fatalf("remaining shrink width = %d, want 45", got)
	}
}

func TestStackRedistributesGrowthAfterMaximumFreeze(t *testing.T) {
	root := &project.Node{
		Handle: "stack", Type: "stack", Props: map[string]any{"direction": "horizontal"},
		Children: []*project.Node{
			{Handle: "a", Type: "surface", Place: map[string]any{"basis": int64(20), "grow": int64(1)}},
			{Handle: "b", Type: "surface", Props: map[string]any{"max_width": int64(30)}, Place: map[string]any{"basis": int64(20), "grow": int64(1)}},
		},
	}
	result := Render(root, image.Pt(100, 20), State{})
	if got := result.Bounds["a"].Dx(); got != 70 {
		t.Fatalf("redistributed grow width = %d, want 70", got)
	}
	if got := result.Bounds["b"].Dx(); got != 30 {
		t.Fatalf("maximum-frozen width = %d, want 30", got)
	}
}

func TestStackChildAlignmentOverridesContainer(t *testing.T) {
	root := &project.Node{
		Handle: "stack", Type: "stack",
		Props: map[string]any{"direction": "horizontal", "alignment": "start"},
		Children: []*project.Node{{
			Handle: "child", Type: "surface", Props: map[string]any{"width": int64(20), "height": int64(10)},
			Place: map[string]any{"alignment": "end"},
		}},
	}

	result := Render(root, image.Pt(80, 40), State{})
	if got := result.Bounds["child"]; got != image.Rect(0, 30, 20, 40) {
		t.Fatalf("child alignment bounds = %v", got)
	}
}

func TestStackMeasuresNestedAutoSurface(t *testing.T) {
	root := &project.Node{
		Handle: "stack", Type: "stack", Props: map[string]any{"direction": "horizontal", "alignment": "start"},
		Children: []*project.Node{{
			Handle: "card", Type: "surface",
			Props:    map[string]any{"padding": map[string]any{"top": int64(5), "right": int64(5), "bottom": int64(5), "left": int64(5)}},
			Children: []*project.Node{{Handle: "content", Type: "spacer", Props: map[string]any{"width": int64(30), "height": int64(10)}}},
		}},
	}

	result := Render(root, image.Pt(100, 50), State{})
	if got := result.Bounds["card"]; got != image.Rect(0, 0, 40, 20) {
		t.Fatalf("nested auto surface bounds = %v", got)
	}
}

func TestStackPercentageAboveHundredOverflows(t *testing.T) {
	root := &project.Node{
		Handle: "stack", Type: "stack", Props: map[string]any{"direction": "horizontal", "alignment": "start"},
		Children: []*project.Node{{
			Handle: "overflow", Type: "spacer",
			Props: map[string]any{"width": map[string]any{"percent": int64(125)}, "height": int64(10)},
		}},
	}
	result := Render(root, image.Pt(80, 20), State{})
	if got := result.Bounds["overflow"]; got != image.Rect(0, 0, 100, 10) {
		t.Fatalf("overflow percentage bounds = %v", got)
	}
}

func TestVerticalStackWrapsIntoColumns(t *testing.T) {
	root := &project.Node{
		Handle: "stack", Type: "stack",
		Props: map[string]any{"direction": "vertical", "wrap": true, "row_gap": int64(10), "column_gap": int64(5)},
		Children: []*project.Node{
			{Handle: "a", Type: "spacer", Props: map[string]any{"width": int64(15), "height": int64(20)}},
			{Handle: "b", Type: "spacer", Props: map[string]any{"width": int64(15), "height": int64(20)}},
			{Handle: "c", Type: "spacer", Props: map[string]any{"width": int64(15), "height": int64(20)}},
		},
	}
	result := Render(root, image.Pt(60, 50), State{})
	if got := result.Bounds["c"]; got != image.Rect(20, 0, 35, 20) {
		t.Fatalf("third vertical item = %v", got)
	}
}

func TestStackBoundaryFitAndOneUnitOverflow(t *testing.T) {
	makeRoot := func() *project.Node {
		return &project.Node{
			Handle: "stack", Type: "stack", Props: map[string]any{"direction": "horizontal", "wrap": true, "gap": int64(5)},
			Children: []*project.Node{
				{Handle: "a", Type: "spacer", Props: map[string]any{"width": int64(30), "height": int64(10)}},
				{Handle: "b", Type: "spacer", Props: map[string]any{"width": int64(30), "height": int64(10)}},
				{Handle: "c", Type: "spacer", Props: map[string]any{"width": int64(30), "height": int64(10)}},
			},
		}
	}
	if got := Render(makeRoot(), image.Pt(100, 30), State{}).Bounds["c"].Min.Y; got != 0 {
		t.Fatalf("boundary-fit item y = %d, want 0", got)
	}
	if got := Render(makeRoot(), image.Pt(99, 30), State{}).Bounds["c"].Min.Y; got != 15 {
		t.Fatalf("one-unit-overflow item y = %d, want 15", got)
	}
}

func TestScrollUsesIntrinsicContentExtentAndClampsOffset(t *testing.T) {
	root := &project.Node{
		Handle: "scroll", Name: "feed", Type: "scroll", Props: map[string]any{"axis": "vertical"},
		Children: []*project.Node{{
			Handle: "content", Type: "stack", Props: map[string]any{"direction": "vertical"},
			Children: []*project.Node{
				{Handle: "a", Type: "spacer", Props: map[string]any{"height": int64(50)}},
				{Handle: "b", Type: "spacer", Props: map[string]any{"height": int64(50)}},
				{Handle: "c", Type: "spacer", Props: map[string]any{"height": int64(50)}},
			},
		}},
	}
	result := Render(root, image.Pt(100, 100), State{Scroll: map[string]image.Point{"feed": image.Pt(0, 100)}})
	if got := result.Bounds["content"]; got != image.Rect(0, -50, 100, 100) {
		t.Fatalf("intrinsic scrolled content bounds = %v", got)
	}
}

// oneAxisScrollNode keeps scroll characterization fixtures small and makes
// the expected viewport/content rectangles explicit at each call site.
func oneAxisScrollNode(axis string, content image.Point) *project.Node {
	props := map[string]any{}
	if axis != "" {
		props["axis"] = axis
	}
	childProps := map[string]any{"width": int64(content.X), "height": int64(content.Y)}
	return &project.Node{
		Handle: "scroll", Name: "feed", Type: "scroll", Props: props,
		Children: []*project.Node{{Handle: "content", Type: "surface", Props: childProps}},
	}
}

func nestedVerticalScrollNode() *project.Node {
	inner := &project.Node{
		Handle: "inner-scroll", Name: "inner", Type: "scroll",
		Props: map[string]any{"axis": "vertical", "height": int64(200)},
		Children: []*project.Node{{
			Handle: "inner-content", Type: "surface",
			Props: map[string]any{"height": int64(300), "background": "#FFFFFF"},
		}},
	}
	return &project.Node{
		Handle: "outer-scroll", Name: "outer", Type: "scroll",
		Props: map[string]any{"axis": "vertical"}, Children: []*project.Node{inner},
	}
}

func TestCPUAndGioPinOneAxisScrollGeometry(t *testing.T) {
	tests := []struct {
		name       string
		axis       string
		viewport   image.Point
		content    image.Point
		offset     image.Point
		wantBounds image.Rectangle
	}{
		{
			name: "default vertical", axis: "", viewport: image.Pt(100, 80),
			content: image.Pt(100, 240), offset: image.Pt(17, 30),
			wantBounds: image.Rect(0, -30, 100, 210),
		},
		{
			name: "vertical", axis: "vertical", viewport: image.Pt(100, 80),
			content: image.Pt(100, 240), offset: image.Pt(17, 30),
			wantBounds: image.Rect(0, -30, 100, 210),
		},
		{
			name: "horizontal clamps beyond maximum", axis: "horizontal", viewport: image.Pt(100, 80),
			content: image.Pt(260, 80), offset: image.Pt(400, 19),
			wantBounds: image.Rect(-160, 0, 100, 80),
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := oneAxisScrollNode(test.axis, test.content)
			state := State{Scroll: map[string]image.Point{"feed": test.offset}}
			cpu := Render(root, test.viewport, state)
			var operations op.Ops
			gio := LayoutGio(layout.Context{
				Ops: &operations, Metric: unit.Metric{PxPerDp: 1, PxPerSp: 1},
				Constraints: layout.Exact(test.viewport),
			}, material.NewTheme(), root, test.viewport, state)

			wantViewport := image.Rect(0, 0, test.viewport.X, test.viewport.Y)
			for _, result := range []struct {
				name     string
				bounds   map[string]image.Rectangle
				geometry map[string]semantic.Geometry
			}{
				{name: "CPU", bounds: cpu.Bounds, geometry: cpu.Geometry},
				{name: "Gio", bounds: gio.Bounds, geometry: gio.Geometry},
			} {
				t.Run(result.name, func(t *testing.T) {
					if got := result.bounds["scroll"]; got != wantViewport {
						t.Fatalf("scroll viewport = %v, want %v", got, wantViewport)
					}
					if got := result.bounds["content"]; got != test.wantBounds {
						t.Fatalf("content bounds = %v, want %v", got, test.wantBounds)
					}
					if got := result.geometry["scroll"].Clip; got != wantViewport {
						t.Fatalf("scroll clip = %v, want %v", got, wantViewport)
					}
					if got := result.geometry["content"].Clip; got != wantViewport {
						t.Fatalf("content clip = %v, want %v", got, wantViewport)
					}
					if result.geometry["scroll"].PaintOrder != 0 || result.geometry["content"].PaintOrder != 1 {
						t.Fatalf("paint order = scroll:%d content:%d, want 0:1", result.geometry["scroll"].PaintOrder, result.geometry["content"].PaintOrder)
					}
					gotOffset := result.bounds["scroll"].Min.Sub(result.bounds["content"].Min)
					if gotOffset != image.Pt(absInt(test.wantBounds.Min.X), absInt(test.wantBounds.Min.Y)) {
						t.Fatalf("effective offset = %v, want %v", gotOffset, image.Pt(absInt(test.wantBounds.Min.X), absInt(test.wantBounds.Min.Y)))
					}
				})
			}
			for _, handle := range []string{"scroll", "content"} {
				cpuGeometry, gioGeometry := cpu.Geometry[handle], gio.Geometry[handle]
				if cpu.Bounds[handle] != gio.Bounds[handle] || cpuGeometry.Bounds != gioGeometry.Bounds || cpuGeometry.Clip != gioGeometry.Clip || cpuGeometry.PaintOrder != gioGeometry.PaintOrder {
					t.Fatalf("%s CPU/Gio geometry differs: CPU bounds=%v geometry=%+v; Gio bounds=%v geometry=%+v", handle, cpu.Bounds[handle], cpuGeometry, gio.Bounds[handle], gioGeometry)
				}
			}
		})
	}
}

func TestCPUAndGioComposeNestedOneAxisScrollClips(t *testing.T) {
	viewport := image.Pt(100, 100)
	state := State{Scroll: map[string]image.Point{
		"outer": image.Pt(0, 20), "inner": image.Pt(0, 40),
	}}
	root := nestedVerticalScrollNode()
	cpu := Render(root, viewport, state)
	var operations op.Ops
	gio := LayoutGio(layout.Context{
		Ops: &operations, Metric: unit.Metric{PxPerDp: 1, PxPerSp: 1}, Constraints: layout.Exact(viewport),
	}, material.NewTheme(), root, viewport, state)
	wantOuter := image.Rect(0, 0, 100, 100)
	wantInner := image.Rect(0, -20, 100, 180)
	wantContent := image.Rect(0, -60, 100, 240)
	for _, result := range []struct {
		name     string
		bounds   map[string]image.Rectangle
		geometry map[string]semantic.Geometry
	}{
		{name: "CPU", bounds: cpu.Bounds, geometry: cpu.Geometry},
		{name: "Gio", bounds: gio.Bounds, geometry: gio.Geometry},
	} {
		t.Run(result.name, func(t *testing.T) {
			if got := result.bounds["outer-scroll"]; got != wantOuter {
				t.Fatalf("outer viewport = %v, want %v", got, wantOuter)
			}
			if got := result.bounds["inner-scroll"]; got != wantInner {
				t.Fatalf("inner viewport = %v, want %v", got, wantInner)
			}
			if got := result.bounds["inner-content"]; got != wantContent {
				t.Fatalf("nested content = %v, want %v", got, wantContent)
			}
			for _, handle := range []string{"outer-scroll", "inner-scroll", "inner-content"} {
				if got := result.geometry[handle].Clip; got != wantOuter {
					t.Fatalf("%s clip = %v, want %v", handle, got, wantOuter)
				}
			}
		})
	}
}

func bothAxisScrollNode(content image.Point) *project.Node {
	return &project.Node{
		Handle: "both-scroll", Name: "workspace", Type: "scroll",
		Props: map[string]any{"axis": "both"},
		Children: []*project.Node{{
			Handle: "both-content", Type: "surface",
			Props: map[string]any{"width": int64(content.X), "height": int64(content.Y)},
		}},
	}
}

func TestCPUAndGioPinBothAxisScrollGeometryAndMetrics(t *testing.T) {
	viewport := image.Pt(100, 80)
	content := image.Pt(180, 140)
	tests := []struct {
		name       string
		offset     image.Point
		wantBounds image.Rectangle
		wantOffset image.Point
	}{
		{name: "independent offsets", offset: image.Pt(35, 25), wantBounds: image.Rect(-35, -25, 145, 115), wantOffset: image.Pt(35, 25)},
		{name: "horizontal clamp", offset: image.Pt(500, 25), wantBounds: image.Rect(-80, -25, 100, 115), wantOffset: image.Pt(80, 25)},
		{name: "vertical clamp", offset: image.Pt(35, 500), wantBounds: image.Rect(-35, -60, 145, 80), wantOffset: image.Pt(35, 60)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := bothAxisScrollNode(content)
			state := State{Scroll: map[string]image.Point{"workspace": test.offset}}
			cpu := Render(root, viewport, state)
			var operations op.Ops
			gio := LayoutGio(layout.Context{
				Ops: &operations, Metric: unit.Metric{PxPerDp: 1, PxPerSp: 1}, Constraints: layout.Exact(viewport),
			}, material.NewTheme(), root, viewport, state)
			wantViewport := image.Rect(0, 0, viewport.X, viewport.Y)
			for _, result := range []struct {
				name     string
				bounds   map[string]image.Rectangle
				geometry map[string]semantic.Geometry
				scroll   map[string]ScrollMetrics
			}{
				{name: "CPU", bounds: cpu.Bounds, geometry: cpu.Geometry, scroll: cpu.Scroll},
				{name: "Gio", bounds: gio.Bounds, geometry: gio.Geometry, scroll: gio.Scroll},
			} {
				t.Run(result.name, func(t *testing.T) {
					if got := result.bounds["both-scroll"]; got != wantViewport {
						t.Fatalf("scroll viewport = %v, want %v", got, wantViewport)
					}
					if got := result.bounds["both-content"]; got != test.wantBounds {
						t.Fatalf("content bounds = %v, want %v", got, test.wantBounds)
					}
					if got := result.geometry["both-content"].Clip; got != wantViewport {
						t.Fatalf("content clip = %v, want %v", got, wantViewport)
					}
					metrics, ok := result.scroll["both-scroll"]
					if !ok {
						t.Fatal("missing published scroll metrics")
					}
					if metrics.Viewport != wantViewport || metrics.ContentSize != content || metrics.Maximum != image.Pt(80, 60) || !metrics.EnabledX || !metrics.EnabledY {
						t.Fatalf("scroll metrics = %+v", metrics)
					}
					if got := result.bounds["both-scroll"].Min.Sub(result.bounds["both-content"].Min); got != test.wantOffset {
						t.Fatalf("effective offset = %v, want %v", got, test.wantOffset)
					}
				})
			}
		})
	}
}

func TestCPUAndGioComposeNestedBothAxisScrollClips(t *testing.T) {
	viewport := image.Pt(100, 100)
	root := &project.Node{
		Handle: "outer-both", Name: "outer", Type: "scroll", Props: map[string]any{"axis": "both"},
		Children: []*project.Node{{
			Handle: "inner-both", Name: "inner", Type: "scroll",
			Props: map[string]any{"axis": "both", "width": int64(120), "height": int64(120)},
			Children: []*project.Node{{
				Handle: "nested-content", Type: "surface",
				Props: map[string]any{"width": int64(180), "height": int64(160)},
			}},
		}},
	}
	state := State{Scroll: map[string]image.Point{
		"outer": image.Pt(20, 10), "inner": image.Pt(30, 40),
	}}
	cpu := Render(root, viewport, state)
	var operations op.Ops
	gio := LayoutGio(layout.Context{
		Ops: &operations, Metric: unit.Metric{PxPerDp: 1, PxPerSp: 1}, Constraints: layout.Exact(viewport),
	}, material.NewTheme(), root, viewport, state)
	want := map[string]image.Rectangle{
		"outer-both":     image.Rect(0, 0, 100, 100),
		"inner-both":     image.Rect(-20, -10, 100, 110),
		"nested-content": image.Rect(-50, -50, 130, 110),
	}
	for _, result := range []struct {
		name     string
		bounds   map[string]image.Rectangle
		geometry map[string]semantic.Geometry
	}{
		{name: "CPU", bounds: cpu.Bounds, geometry: cpu.Geometry},
		{name: "Gio", bounds: gio.Bounds, geometry: gio.Geometry},
	} {
		t.Run(result.name, func(t *testing.T) {
			for handle, wantBounds := range want {
				if got := result.bounds[handle]; got != wantBounds {
					t.Fatalf("%s bounds = %v, want %v", handle, got, wantBounds)
				}
				if got := result.geometry[handle].Clip; got != image.Rect(0, 0, 100, 100) {
					t.Fatalf("%s clip = %v, want viewport clip", handle, got)
				}
			}
		})
	}
}

func TestCaptureScalesPixelsWithoutStudioOverlays(t *testing.T) {
	path := filepath.Join(t.TempDir(), "capture.png")
	root := &project.Node{Handle: "root", Type: "surface", Props: map[string]any{"background": "#123456"}}
	if err := Capture(path, root, image.Pt(3, 2), State{}, 2); err != nil {
		skipMetalUnavailable(t, err)
		t.Fatal(err)
	}
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	captured, err := png.Decode(file)
	if err != nil {
		t.Fatal(err)
	}
	if captured.Bounds().Size() != image.Pt(6, 4) {
		t.Fatalf("capture size = %v", captured.Bounds().Size())
	}
	if got := color.RGBAModel.Convert(captured.At(1, 1)).(color.RGBA); got != (color.RGBA{R: 0x12, G: 0x34, B: 0x56, A: 0xff}) {
		t.Fatalf("capture pixel = %#v", got)
	}
}

func TestCaptureAndReferenceUseTheSameVerticalScrollOffset(t *testing.T) {
	root := &project.Node{
		Handle: "scroll", Name: "feed", Type: "scroll", Props: map[string]any{"axis": "vertical"},
		Children: []*project.Node{{
			Handle: "content", Type: "stack", Props: map[string]any{"direction": "vertical"},
			Children: []*project.Node{
				{Handle: "red", Type: "surface", Props: map[string]any{"height": int64(20), "background": "#FF0000"}},
				{Handle: "blue", Type: "surface", Props: map[string]any{"height": int64(20), "background": "#0000FF"}},
			},
		}},
	}
	viewport := image.Pt(20, 20)
	state := State{Scroll: map[string]image.Point{"feed": image.Pt(0, 20)}}
	reference := Render(root, viewport, state)
	if got := reference.Bounds["blue"]; got != image.Rect(0, 0, 20, 20) {
		t.Fatalf("reference blue bounds = %v, want viewport", got)
	}
	encoded, err := CapturePNG(root, viewport, state, 1)
	if err != nil {
		skipMetalUnavailable(t, err)
		t.Fatal(err)
	}
	captured, err := png.Decode(bytes.NewReader(encoded))
	if err != nil {
		t.Fatal(err)
	}
	if got := color.RGBAModel.Convert(captured.At(10, 10)).(color.RGBA); got != (color.RGBA{B: 255, A: 255}) {
		t.Fatalf("captured scrolled pixel = %#v, want blue", got)
	}
}

func skipMetalUnavailable(t *testing.T, err error) {
	t.Helper()
	if err != nil && strings.Contains(strings.ToLower(err.Error()), "metal device") {
		t.Skipf("headless capture requires Metal: %v", err)
	}
}

func TestFieldTextGeometryMapsWrappedLinesAndMultilineSelection(t *testing.T) {
	node := &project.Node{Type: "field_box", Props: map[string]any{
		"text": "abcd\nefgh", "field_multiline": true, "size": float64(10), "line_height": float64(12),
		"padding":         map[string]any{"top": float64(0), "right": float64(0), "bottom": float64(0), "left": float64(0)},
		"selection_start": float64(2), "selection_end": float64(8),
	}}
	geometry := newFieldTextGeometry(node, image.Rect(0, 0, 18, 48))
	if got := geometry.RuneAt(image.Pt(13, 25)); got != 7 {
		t.Fatalf("rune at second logical line = %d, want 7", got)
	}
	selection, _ := geometry.Decorations()
	if len(selection) < 3 {
		t.Fatalf("wrapped multiline selection has %d rectangles, want at least 3", len(selection))
	}
}

func TestFieldTextGeometryKeepsSingleLineCaretInsideHorizontalViewport(t *testing.T) {
	node := &project.Node{Type: "field_box", Props: map[string]any{
		"text": "abcdefghij", "size": float64(10), "line_height": float64(12),
		"selection_start": float64(10), "selection_end": float64(10),
	}}
	geometry := newFieldTextGeometry(node, image.Rect(0, 0, 24, 20))
	if geometry.OffsetX == 0 {
		t.Fatal("long single-line field did not establish a horizontal viewport offset")
	}
	_, caret := geometry.Decorations()
	if caret.Empty() || caret.Min.X < 0 || caret.Max.X > 24 {
		t.Fatalf("caret = %v outside field viewport", caret)
	}
	if got := geometry.RuneAt(image.Pt(caret.Min.X, caret.Min.Y)); got != 10 {
		t.Fatalf("rune at scrolled caret = %d, want 10", got)
	}
}

func TestFieldGeometryPublishesDerivedInternalTextViewport(t *testing.T) {
	node := &project.Node{Handle: "box", Type: "field_box", Props: map[string]any{
		"text": "abcdefghij", "size": float64(10), "line_height": float64(12),
		"selection_start": float64(10), "selection_end": float64(10),
	}}
	result := Render(node, image.Pt(24, 20), State{})
	props := result.Geometry["box"].Props
	if number(props["internal_viewport_x"], 0) <= 0 || number(props["internal_viewport_y"], -1) != 0 {
		t.Fatalf("derived field offsets = %#v", props)
	}
	if number(props["internal_viewport_width"], 0) != 24 || number(props["internal_viewport_height"], 0) != 20 {
		t.Fatalf("derived field viewport = %#v", props)
	}
}

func TestFieldTextGeometryUsesProportionalFontMetrics(t *testing.T) {
	geometryFor := func(text string) fieldTextGeometry {
		return newFieldTextGeometry(&project.Node{Type: "field_box", Props: map[string]any{
			"text": text, "size": float64(20), "selection_start": float64(1), "selection_end": float64(1),
		}}, image.Rect(0, 0, 200, 40))
	}
	_, wideCaret := geometryFor("WW").Decorations()
	_, narrowCaret := geometryFor("ii").Decorations()
	if wideCaret.Min.X <= narrowCaret.Min.X {
		t.Fatalf("proportional carets wide=%v narrow=%v", wideCaret, narrowCaret)
	}
}

func TestFieldTextGeometryUsesShapedBidirectionalCaretPositions(t *testing.T) {
	node := &project.Node{Type: "field_box", Props: map[string]any{
		"text": "سلام", "size": float64(20), "selection_start": float64(0), "selection_end": float64(4),
	}}
	geometry := newFieldTextGeometry(node, image.Rect(0, 0, 240, 40))
	if len(geometry.positions) != len([]rune("سلام"))+1 {
		t.Fatalf("caret positions = %d", len(geometry.positions))
	}
	if geometry.positions[0].x <= geometry.positions[len(geometry.positions)-1].x {
		t.Fatalf("RTL logical carets run left-to-right: start=%d end=%d", geometry.positions[0].x, geometry.positions[len(geometry.positions)-1].x)
	}
	selection, _ := geometry.Decorations()
	if len(selection) == 0 || selection[0].Empty() {
		t.Fatalf("RTL selection rectangles = %+v", selection)
	}
	if got := geometry.RuneAt(image.Pt(geometry.positions[0].x, 10)); got != 0 {
		t.Fatalf("right-side RTL pointer mapped to rune %d, want 0", got)
	}
	if got := geometry.RuneAt(image.Pt(geometry.positions[len(geometry.positions)-1].x, 10)); got != 4 {
		t.Fatalf("left-side RTL pointer mapped to rune %d, want 4", got)
	}
}

func TestFieldBoxIntrinsicHeightClampsToMinAndMaxLines(t *testing.T) {
	box := &project.Node{Handle: "box", Type: "field_box", Props: map[string]any{
		"text": "one", "field_multiline": true, "field_min_lines": float64(3), "field_max_lines": float64(4),
		"size": float64(10), "line_height": float64(12),
	}}
	root := &project.Node{Handle: "stack", Type: "stack", Props: map[string]any{"direction": "vertical", "alignment": "start"}, Children: []*project.Node{box}}
	result := Render(root, image.Pt(100, 100), State{})
	if got := result.Bounds["box"].Dy(); got != 36 {
		t.Fatalf("minimum line height = %d, want 36", got)
	}
	box.Props["text"] = "one\ntwo\nthree\nfour\nfive"
	result = Render(root, image.Pt(100, 100), State{})
	if got := result.Bounds["box"].Dy(); got != 48 {
		t.Fatalf("maximum line height = %d, want 48", got)
	}
}

func TestFieldGeometryRespectsManualInternalScrollAwayFromCaret(t *testing.T) {
	node := &project.Node{Type: "field_box", Props: map[string]any{
		"text": "one\ntwo\nthree", "field_multiline": true, "size": float64(10), "line_height": float64(12),
		"selection_start": float64(13), "selection_end": float64(13), "internal_offset": float64(0), "manual_internal_scroll": true,
	}}
	manual := newFieldTextGeometry(node, image.Rect(0, 0, 100, 24))
	if manual.OffsetY != 0 {
		t.Fatalf("manual internal offset = %d, want 0", manual.OffsetY)
	}
	delete(node.Props, "manual_internal_scroll")
	automatic := newFieldTextGeometry(node, image.Rect(0, 0, 100, 24))
	if automatic.OffsetY != 1 {
		t.Fatalf("automatic caret reveal offset = %d, want 1", automatic.OffsetY)
	}
}

func TestFieldCompositionUnderlinesFollowTheCompositionRange(t *testing.T) {
	node := &project.Node{Type: "field_box", Props: map[string]any{
		"text": "abcdef", "field_multiline": true, "size": float64(10), "line_height": float64(12),
		"composition_start": float64(1), "composition_end": float64(5), "composing": true,
	}}
	underlines := fieldCompositionUnderlines(node, image.Rect(0, 0, 18, 36))
	if len(underlines) != 2 {
		t.Fatalf("composition underlines = %v, want two wrapped segments", underlines)
	}
	for _, underline := range underlines {
		if underline.Dy() != 1 {
			t.Fatalf("composition underline thickness = %d, want 1", underline.Dy())
		}
	}
}
