package render

import (
	"image"
	"image/color"
	"reflect"
	"testing"

	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/unit"
	"gioui.org/widget/material"

	"gora/internal/project"
	"gora/internal/semantic"
)

func fixedTestPlace(inset map[string]any) map[string]any {
	return map[string]any{"position": "fixed", "inset": inset}
}

func TestFixedSubtreePublishesViewportGeometryAndEscapesScroll(t *testing.T) {
	fixed := &project.Node{
		Handle: "fixed-button", Name: "fixed-button", Type: "button",
		Props:      map[string]any{"width": int64(40), "height": int64(20), "label": "Fixed"},
		Place:      fixedTestPlace(map[string]any{"top": int64(10), "right": nil, "bottom": nil, "left": int64(12)}),
		Breadcrumb: []string{"card", "instance"}, Scope: "component-scope",
		Children: []*project.Node{{Handle: "fixed-label", Type: "text", Props: map[string]any{"text": "Fixed"}}},
	}
	root := &project.Node{Handle: "scroll", Type: "scroll", Props: map[string]any{"axis": "vertical"}, Children: []*project.Node{{
		Handle: "content", Type: "stack", Props: map[string]any{"height": int64(320)}, Children: []*project.Node{
			{Handle: "before", Type: "surface", Props: map[string]any{"height": int64(80)}},
			fixed,
			{Handle: "after", Type: "surface", Props: map[string]any{"height": int64(240)}},
		},
	}}}
	viewport := image.Pt(120, 100)
	state := State{Scroll: map[string]image.Point{"scroll": image.Pt(0, 60)}}
	cpu := Render(root, viewport, state)
	want := image.Rect(12, 10, 52, 30)
	if got := cpu.Layout["fixed-button"].Final; got != want {
		t.Fatalf("fixed final = %v, want viewport rect %v", got, want)
	}
	if got := cpu.Geometry["fixed-button"].Bounds; got != want || cpu.Geometry["fixed-button"].Clip != want {
		t.Fatalf("fixed geometry = bounds %v clip %v, want %v", got, cpu.Geometry["fixed-button"].Bounds, cpu.Geometry["fixed-button"].Clip)
	}
	if got := cpu.Layout["fixed-label"].Final.Min; got != want.Min {
		t.Fatalf("fixed descendant did not follow root: label=%v root=%v", cpu.Layout["fixed-label"].Final, want)
	}
	if cpu.Layout["fixed-button"].ScrollAncestors != nil {
		t.Fatalf("fixed retained scroll ancestors despite viewport ownership: %v", cpu.Layout["fixed-button"].ScrollAncestors)
	}
	if cpu.Tree == nil {
		t.Fatal("fixed runtime tree missing")
	}
	var fixedSemantic *semantic.Node
	for _, node := range semantic.Flatten(cpu.Tree) {
		if node.Handle == "fixed-button" {
			fixedSemantic = node
			break
		}
	}
	if fixedSemantic == nil || fixedSemantic.Bounds == nil || fixedSemantic.FocusOrder < 0 || fixedSemantic.Role != "button" || len(fixedSemantic.Operations) == 0 || !reflect.DeepEqual(fixedSemantic.Breadcrumb, fixed.Breadcrumb) || fixedSemantic.Scope != fixed.Scope {
		t.Fatalf("fixed semantic node = %+v, want visible focused semantic button", fixedSemantic)
	}
	var ops op.Ops
	gio := LayoutGio(layout.Context{Ops: &ops, Metric: unit.Metric{PxPerDp: 1, PxPerSp: 1}, Constraints: layout.Exact(viewport)}, material.NewTheme(), root, viewport, state)
	if !reflect.DeepEqual(cpu.Layout, gio.Layout) || !reflect.DeepEqual(cpu.Geometry, gio.Geometry) || !reflect.DeepEqual(cpu.Tree, gio.Tree) {
		t.Fatalf("fixed CPU/Gio mismatch: layout=%v/%v geometry=%v/%v tree=%v/%v", cpu.Layout, gio.Layout, cpu.Geometry, gio.Geometry, cpu.Tree, gio.Tree)
	}

	var cache GioCache
	theme := material.NewTheme()
	var buildOps op.Ops
	cache.Layout(layout.Context{Ops: &buildOps, Metric: unit.Metric{PxPerDp: 1, PxPerSp: 1}, Constraints: layout.Exact(viewport)}, theme, root, viewport, State{Scroll: map[string]image.Point{"scroll": image.Pt(0, 0)}})
	var replayOps op.Ops
	replayed := cache.Layout(layout.Context{Ops: &replayOps, Metric: unit.Metric{PxPerDp: 1, PxPerSp: 1}, Constraints: layout.Exact(viewport)}, theme, root, viewport, state)
	if cache.builds != 1 {
		t.Fatalf("fixed scroll-only replay rebuilt scene %d times", cache.builds)
	}
	if replayed.Layout["fixed-button"].Final != want || replayed.Geometry["fixed-button"].Bounds != want || replayed.Geometry["fixed-button"].Clip != want {
		t.Fatalf("fixed replay moved with scroll: layout=%v bounds=%v clip=%v", replayed.Layout["fixed-button"].Final, replayed.Geometry["fixed-button"].Bounds, replayed.Geometry["fixed-button"].Clip)
	}
}

func TestFixedDescendantsUseIndependentViewportContexts(t *testing.T) {
	child := &project.Node{
		Handle: "fixed-child", Name: "fixed-child", Type: "surface",
		Props: map[string]any{"width": int64(20), "height": int64(10), "background": "#FF0000"},
		Place: fixedTestPlace(map[string]any{"top": int64(70), "right": int64(12), "bottom": nil, "left": nil}),
	}
	rootFixed := &project.Node{
		Handle: "fixed-parent", Name: "fixed-parent", Type: "surface",
		Props:    map[string]any{"width": int64(50), "height": int64(40), "background": "#0000FF"},
		Place:    fixedTestPlace(map[string]any{"top": int64(50), "right": nil, "bottom": nil, "left": int64(50)}),
		Children: []*project.Node{child},
	}
	container := &project.Node{Handle: "clipped-container", Type: "surface", Props: map[string]any{"width": int64(30), "height": int64(30), "clip": true}, Children: []*project.Node{rootFixed}}
	root := &project.Node{Handle: "root", Type: "surface", Props: map[string]any{"background": "#FFFFFF"}, Children: []*project.Node{container}}
	viewport := image.Pt(100, 100)
	result := Render(root, viewport, State{})
	if got, want := result.Layout["fixed-parent"].Final, image.Rect(50, 50, 100, 90); got != want {
		t.Fatalf("fixed parent final = %v, want %v", got, want)
	}
	if result.Geometry["fixed-parent"].Clip != result.Geometry["fixed-parent"].Bounds {
		t.Fatalf("fixed parent inherited ancestor clip: clip=%v bounds=%v", result.Geometry["fixed-parent"].Clip, result.Geometry["fixed-parent"].Bounds)
	}
	if got, want := result.Layout["fixed-child"].Final, image.Rect(68, 70, 88, 80); got != want {
		t.Fatalf("fixed child escaped viewport independently = %v, want %v", got, want)
	}
	if result.Geometry["fixed-child"].Clip != result.Geometry["fixed-child"].Bounds {
		t.Fatalf("fixed child clip = %v, bounds = %v", result.Geometry["fixed-child"].Clip, result.Geometry["fixed-child"].Bounds)
	}
	var ops op.Ops
	g := LayoutGio(layout.Context{Ops: &ops, Metric: unit.Metric{PxPerDp: 1, PxPerSp: 1}, Constraints: layout.Exact(viewport)}, material.NewTheme(), root, viewport, State{})
	if !reflect.DeepEqual(result.Layout, g.Layout) || !reflect.DeepEqual(result.Geometry, g.Geometry) {
		t.Fatalf("nested fixed CPU/Gio mismatch: CPU=%+v/%+v Gio=%+v/%+v", result.Layout, result.Geometry, g.Layout, g.Geometry)
	}
}

func TestFixedSubtreeResetsAncestorScrollContextForNestedScroll(t *testing.T) {
	innerScroll := &project.Node{
		Handle: "fixed-inner-scroll", Type: "scroll",
		Props: map[string]any{"axis": "both", "width": int64(60), "height": int64(40)},
		Children: []*project.Node{{
			Handle: "fixed-inner-content", Type: "surface",
			Props: map[string]any{"width": int64(140), "height": int64(100), "background": "#FF0000"},
		}},
	}
	fixed := &project.Node{
		Handle: "fixed-owner", Type: "surface",
		Props:    map[string]any{"width": int64(60), "height": int64(40), "background": "#0000FF"},
		Place:    fixedTestPlace(map[string]any{"top": int64(10), "right": nil, "bottom": nil, "left": int64(10)}),
		Children: []*project.Node{innerScroll},
	}
	root := &project.Node{
		Handle: "outer-scroll", Type: "scroll",
		Props: map[string]any{"axis": "vertical"},
		Children: []*project.Node{{
			Handle: "outer-content", Type: "stack", Props: map[string]any{"height": int64(260)},
			Children: []*project.Node{{Handle: "before", Type: "surface", Props: map[string]any{"height": int64(120)}}, fixed},
		}},
	}
	viewport := image.Pt(100, 100)
	state := State{Scroll: map[string]image.Point{
		"outer-scroll":       image.Pt(0, 40),
		"fixed-inner-scroll": image.Pt(20, 30),
	}}
	cpu := Render(root, viewport, state)
	innerWant := image.Rect(10, 10, 70, 50)
	contentWant := image.Rect(-10, -20, 130, 80)
	if got := cpu.Layout[innerScroll.Handle].Final; got != innerWant {
		t.Fatalf("nested fixed scroll viewport = %v, want %v", got, innerWant)
	}
	if got := cpu.Layout["fixed-inner-content"].Final; got != contentWant {
		t.Fatalf("nested fixed scroll content = %v, want %v", got, contentWant)
	}
	var ops op.Ops
	gio := LayoutGio(layout.Context{Ops: &ops, Metric: unit.Metric{PxPerDp: 1, PxPerSp: 1}, Constraints: layout.Exact(viewport)}, material.NewTheme(), root, viewport, state)
	if !reflect.DeepEqual(cpu.Layout, gio.Layout) || !reflect.DeepEqual(cpu.Geometry, gio.Geometry) {
		t.Fatalf("fixed nested scroll CPU/Gio context mismatch: layout=%v/%v geometry=%v/%v", cpu.Layout, gio.Layout, cpu.Geometry, gio.Geometry)
	}
}

func TestHiddenFixedRetainsHierarchyWithoutGeometry(t *testing.T) {
	fixed := &project.Node{
		Handle: "hidden-fixed", Type: "surface", Hidden: true,
		Props: map[string]any{"width": int64(20), "height": int64(20)},
		Place: fixedTestPlace(map[string]any{"top": int64(0), "right": nil, "bottom": nil, "left": int64(0)}),
	}
	root := &project.Node{Handle: "root", Type: "stack", Children: []*project.Node{fixed}}
	result := Render(root, image.Pt(80, 80), State{})
	if _, ok := result.Geometry["hidden-fixed"]; ok {
		t.Fatalf("hidden fixed geometry was published: %+v", result.Geometry["hidden-fixed"])
	}
	var found *semantic.Node
	for _, node := range semantic.Flatten(result.Tree) {
		if node.Handle == fixed.Handle {
			found = node
			break
		}
	}
	if found == nil || found.Visible || found.Bounds != nil || found.InViewport {
		t.Fatalf("hidden fixed semantic node = %+v, want hidden/null geometry", found)
	}
}

func TestFixedViewportResizeReevaluatesLogicalGeometry(t *testing.T) {
	fixed := &project.Node{
		Handle: "fixed", Type: "surface", Props: map[string]any{"width": int64(20), "height": int64(10)},
		Place: fixedTestPlace(map[string]any{"top": map[string]any{"percent": float64(10)}, "right": nil, "bottom": nil, "left": map[string]any{"percent": float64(10)}}),
	}
	root := &project.Node{Handle: "root", Type: "surface", Children: []*project.Node{fixed}}
	small := Render(root, image.Pt(100, 80), State{})
	large := Render(root, image.Pt(200, 160), State{})
	if small.Layout["fixed"].Final != image.Rect(10, 8, 30, 18) || large.Layout["fixed"].Final != image.Rect(20, 16, 40, 26) {
		t.Fatalf("fixed viewport resize geometry = %v/%v", small.Layout["fixed"].Final, large.Layout["fixed"].Final)
	}
	var cache GioCache
	theme := material.NewTheme()
	var smallOps op.Ops
	cache.Layout(layout.Context{Ops: &smallOps, Metric: unit.Metric{PxPerDp: 1, PxPerSp: 1}, Constraints: layout.Exact(image.Pt(100, 80))}, theme, root, image.Pt(100, 80), State{})
	var largeOps op.Ops
	cache.Layout(layout.Context{Ops: &largeOps, Metric: unit.Metric{PxPerDp: 1, PxPerSp: 1}, Constraints: layout.Exact(image.Pt(200, 160))}, theme, root, image.Pt(200, 160), State{})
	if cache.builds != 2 {
		t.Fatalf("fixed viewport resize did not invalidate retained scene: builds=%d", cache.builds)
	}
}

func TestFixedCaptureKeepsLogicalPlacementAtOneAndTwoScale(t *testing.T) {
	fixed := &project.Node{
		Handle: "fixed", Type: "surface", Props: map[string]any{"width": int64(20), "height": int64(20), "background": "#FF0000"},
		Place: fixedTestPlace(map[string]any{"top": int64(10), "right": nil, "bottom": nil, "left": int64(10)}),
	}
	root := &project.Node{Handle: "root", Type: "surface", Props: map[string]any{"background": "#0000FF"}, Children: []*project.Node{fixed}}
	viewport := image.Pt(60, 60)
	for _, scale := range []int{1, 2} {
		captured, err := captureGio(root, viewport, State{}, scale)
		if err != nil {
			skipMetalUnavailable(t, err)
			t.Fatal(err)
		}
		if captured.Bounds().Dx() != viewport.X*scale || captured.Bounds().Dy() != viewport.Y*scale {
			t.Fatalf("scale %d capture size = %v", scale, captured.Bounds())
		}
		inside := captured.At(15*scale, 15*scale)
		if got := color.RGBAModel.Convert(inside).(color.RGBA); got.R < 240 || got.A < 240 {
			t.Fatalf("scale %d fixed pixel = %#v, want opaque red", scale, got)
		}
	}
}

func TestFixedCPUReferenceScaleParityAndAncestorClipEscape(t *testing.T) {
	fixed := &project.Node{
		Handle: "fixed", Type: "surface",
		Props: map[string]any{"width": int64(20), "height": int64(20), "background": "#FF0000"},
		Place: fixedTestPlace(map[string]any{"top": int64(20), "right": nil, "bottom": nil, "left": int64(20)}),
	}
	clipped := &project.Node{
		Handle: "clipped", Type: "surface",
		Props:    map[string]any{"width": int64(10), "height": int64(10), "clip": true, "background": "#00FF00"},
		Children: []*project.Node{fixed},
	}
	root := &project.Node{
		Handle: "root", Type: "surface", Props: map[string]any{"background": "#0000FF"},
		Children: []*project.Node{clipped},
	}
	viewport := image.Pt(80, 60)
	one := renderScaled(root, viewport, State{}, 1)
	two := renderScaled(root, viewport, State{}, 2)
	if !reflect.DeepEqual(one.Layout, two.Layout) || !reflect.DeepEqual(one.Geometry, two.Geometry) {
		t.Fatalf("CPU fixed logical metadata changed with scale: 1x layout=%v geometry=%v; 2x layout=%v geometry=%v", one.Layout, one.Geometry, two.Layout, two.Geometry)
	}
	want := image.Rect(20, 20, 40, 40)
	if got := one.Layout[fixed.Handle].Final; got != want || one.Geometry[fixed.Handle].Bounds != want {
		t.Fatalf("fixed logical placement = %v/%v, want %v", got, one.Geometry[fixed.Handle].Bounds, want)
	}
	if one.Image.Bounds().Size() != viewport || two.Image.Bounds().Size() != image.Pt(viewport.X*2, viewport.Y*2) {
		t.Fatalf("CPU scaled image sizes = %v/%v, want %v/%v", one.Image.Bounds(), two.Image.Bounds(), viewport, image.Pt(viewport.X*2, viewport.Y*2))
	}
	for _, test := range []struct {
		name  string
		point image.Point
		want  color.RGBA
	}{
		{name: "fixed escapes ancestor clip at 1x", point: image.Pt(25, 25), want: color.RGBA{R: 255, A: 255}},
		{name: "root remains visible outside fixed", point: image.Pt(15, 5), want: color.RGBA{B: 255, A: 255}},
	} {
		got := color.RGBAModel.Convert(one.Image.At(test.point.X, test.point.Y)).(color.RGBA)
		if got != test.want {
			t.Fatalf("%s pixel = %#v, want %#v", test.name, got, test.want)
		}
		scaled := color.RGBAModel.Convert(two.Image.At(test.point.X*2, test.point.Y*2)).(color.RGBA)
		if scaled != test.want {
			t.Fatalf("%s at 2x = %#v, want %#v", test.name, scaled, test.want)
		}
	}
}
