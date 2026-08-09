package render

import (
	"image"
	"reflect"
	"testing"

	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/unit"
	"gioui.org/widget/material"

	"gora/internal/project"
)

func stickyNode(inset map[string]any, props map[string]any) *project.Node {
	return &project.Node{Handle: "sticky", Type: "surface", Props: props, Place: map[string]any{"position": "sticky", "inset": inset}}
}

func TestPlanStickyRectClampsEachAxisAndResolvesInsets(t *testing.T) {
	viewport := image.Rect(0, 0, 200, 100)
	tests := []struct {
		name   string
		node   *project.Node
		base   image.Rectangle
		parent image.Rectangle
		want   image.Rectangle
		delta  image.Point
	}{
		{name: "top", node: stickyNode(map[string]any{"top": int64(10), "right": nil, "bottom": nil, "left": nil}, nil), base: image.Rect(20, -20, 80, 20), parent: viewport, want: image.Rect(20, 10, 80, 50), delta: image.Pt(0, 30)},
		{name: "bottom", node: stickyNode(map[string]any{"top": nil, "right": nil, "bottom": int64(10), "left": nil}, nil), base: image.Rect(20, 90, 80, 130), parent: viewport, want: image.Rect(20, 50, 80, 90), delta: image.Pt(0, -40)},
		{name: "left", node: stickyNode(map[string]any{"top": nil, "right": nil, "bottom": nil, "left": int64(12)}, nil), base: image.Rect(-20, 20, 30, 50), parent: viewport, want: image.Rect(12, 20, 62, 50), delta: image.Pt(32, 0)},
		{name: "right", node: stickyNode(map[string]any{"top": nil, "right": int64(8), "bottom": nil, "left": nil}, nil), base: image.Rect(180, 20, 240, 50), parent: viewport, want: image.Rect(132, 20, 192, 50), delta: image.Pt(-48, 0)},
		{name: "negative and percentage", node: stickyNode(map[string]any{"top": map[string]any{"percent": float64(10)}, "right": nil, "bottom": nil, "left": -5.0}, nil), base: image.Rect(-20, -30, 40, 10), parent: viewport, want: image.Rect(0, 10, 60, 50), delta: image.Pt(20, 40)},
		{name: "observable negative inset", node: stickyNode(map[string]any{"top": -12.0, "right": nil, "bottom": nil, "left": nil}, nil), base: image.Rect(20, -30, 80, -10), parent: image.Rect(0, -100, 200, 200), want: image.Rect(20, -12, 80, 8), delta: image.Pt(0, 18)},
		{name: "percentage end inset", node: stickyNode(map[string]any{"top": nil, "right": nil, "bottom": map[string]any{"percent": float64(10)}, "left": nil}, nil), base: image.Rect(20, 90, 80, 130), parent: image.Rect(0, -100, 200, 200), want: image.Rect(20, 50, 80, 90), delta: image.Pt(0, -40)},
		{name: "percentage deterministic rounding", node: stickyNode(map[string]any{"top": map[string]any{"percent": float64(1.5)}, "right": nil, "bottom": nil, "left": nil}, nil), base: image.Rect(20, -20, 80, 20), parent: viewport, want: image.Rect(20, 2, 80, 42), delta: image.Pt(0, 22)},
		{name: "parent containment", node: stickyNode(map[string]any{"top": int64(0), "right": nil, "bottom": nil, "left": nil}, nil), base: image.Rect(10, 0, 50, 40), parent: image.Rect(0, 20, 100, 80), want: image.Rect(10, 20, 50, 60), delta: image.Pt(0, 20)},
		{name: "overconstrained start wins", node: stickyNode(map[string]any{"top": int64(70), "right": nil, "bottom": int64(10), "left": nil}, nil), base: image.Rect(10, 0, 50, 120), parent: viewport, want: image.Rect(10, 70, 50, 190), delta: image.Pt(0, 70)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, delta := planStickyRect(test.node, test.base, test.parent, viewport)
			if got != test.want || delta != test.delta {
				t.Fatalf("sticky plan = %v delta %v, want %v delta %v", got, delta, test.want, test.delta)
			}
		})
	}
}

func TestStickyLayoutTranslatesSubtreeAndMatchesCPUAndGio(t *testing.T) {
	sticky := stickyNode(map[string]any{"top": int64(8), "right": nil, "bottom": nil, "left": nil}, map[string]any{"height": int64(30)})
	sticky.Children = []*project.Node{{Handle: "sticky-label", Type: "text", Props: map[string]any{"text": "Pinned"}}}
	root := &project.Node{
		Handle: "scroll", Type: "scroll", Props: map[string]any{"axis": "vertical"},
		Children: []*project.Node{{
			Handle: "content", Type: "stack", Props: map[string]any{"height": int64(300)},
			Children: []*project.Node{
				sticky,
				{Handle: "after", Type: "surface", Props: map[string]any{"height": int64(40)}},
			},
		}},
	}
	viewport := image.Pt(120, 100)
	start := Render(root, viewport, State{Scroll: map[string]image.Point{"scroll": image.Pt(0, 0)}})
	middle := Render(root, viewport, State{Scroll: map[string]image.Point{"scroll": image.Pt(0, 40)}})
	if start.Layout["sticky"].Final.Min.Y != 8 || start.Layout["sticky"].Normal.Min.Y != 0 {
		t.Fatalf("sticky start geometry = normal %v final %v", start.Layout["sticky"].Normal, start.Layout["sticky"].Final)
	}
	if middle.Layout["sticky"].Final.Min.Y != 8 || middle.Layout["sticky-label"].Final.Min.Y != middle.Layout["sticky"].Final.Min.Y {
		t.Fatalf("sticky/subtree translation = sticky %+v label %+v", middle.Layout["sticky"], middle.Layout["sticky-label"])
	}
	if middle.Layout["sticky"].Normal != start.Layout["sticky"].Normal || middle.Layout["sticky"].ParentInner != start.Layout["sticky"].ParentInner {
		t.Fatalf("sticky normal metadata mutated across scroll: start=%+v middle=%+v", start.Layout["sticky"], middle.Layout["sticky"])
	}
	var operations op.Ops
	gio := LayoutGio(layout.Context{Ops: &operations, Metric: unit.Metric{PxPerDp: 1, PxPerSp: 1}, Constraints: layout.Exact(viewport)}, material.NewTheme(), root, viewport, State{Scroll: map[string]image.Point{"scroll": image.Pt(0, 40)}})
	if !reflect.DeepEqual(middle.Layout, gio.Layout) || !reflect.DeepEqual(middle.Geometry, gio.Geometry) {
		t.Fatalf("CPU/Gio sticky geometry differs: CPU layout=%+v Gio layout=%+v", middle.Layout, gio.Layout)
	}
	if middle.Tree == nil || middle.Tree.Children == nil {
		t.Fatalf("canonical sticky tree missing: %+v", middle.Tree)
	}
	for index, handle := range []string{"scroll", "content", "after", "sticky", "sticky-label"} {
		if got := middle.Geometry[handle].PaintOrder; got != index {
			t.Fatalf("sticky source paint order for %q = %d, want %d", handle, got, index)
		}
	}
	var cache GioCache
	theme := material.NewTheme()
	var buildOps op.Ops
	cache.Layout(layout.Context{Ops: &buildOps, Metric: unit.Metric{PxPerDp: 1, PxPerSp: 1}, Constraints: layout.Exact(viewport)}, theme, root, viewport, State{Scroll: map[string]image.Point{"scroll": image.Pt(0, 0)}})
	var replayOps op.Ops
	cached := cache.Layout(layout.Context{Ops: &replayOps, Metric: unit.Metric{PxPerDp: 1, PxPerSp: 1}, Constraints: layout.Exact(viewport)}, theme, root, viewport, State{Scroll: map[string]image.Point{"scroll": image.Pt(0, 40)}})
	if cache.builds != 1 {
		t.Fatalf("sticky replay rebuilt scene %d times", cache.builds)
	}
	if !reflect.DeepEqual(cached.Layout, middle.Layout) || !reflect.DeepEqual(cached.Geometry, middle.Geometry) {
		t.Fatalf("sticky cache replay differs from fresh: cached=%+v/%+v fresh=%+v/%+v", cached.Layout, cached.Geometry, middle.Layout, middle.Geometry)
	}
}

func TestStickyUsesNearestNestedScrollportAndViewportFallback(t *testing.T) {
	innerSticky := stickyNode(map[string]any{"top": int64(4), "right": nil, "bottom": nil, "left": nil}, map[string]any{"height": int64(20)})
	root := &project.Node{
		Handle: "outer", Type: "scroll", Props: map[string]any{"axis": "vertical"},
		Children: []*project.Node{{
			Handle: "outer-content", Type: "stack", Props: map[string]any{"height": int64(420)},
			Children: []*project.Node{
				{Handle: "before", Type: "surface", Props: map[string]any{"height": int64(80)}},
				{Handle: "inner", Type: "scroll", Props: map[string]any{"axis": "vertical", "height": int64(120)}, Children: []*project.Node{{
					Handle: "inner-content", Type: "stack", Props: map[string]any{"height": int64(320)}, Children: []*project.Node{innerSticky},
				}}},
			},
		}},
	}
	viewport := image.Pt(120, 100)
	result := Render(root, viewport, State{Scroll: map[string]image.Point{"outer": image.Pt(0, 50), "inner": image.Pt(0, 40)}})
	innerViewport := result.Scroll["inner"].Viewport
	if innerViewport != image.Rect(0, 30, 120, 150) {
		t.Fatalf("nested viewport = %v, want translated inner viewport %v", innerViewport, image.Rect(0, 30, 120, 150))
	}
	if got := result.Layout["sticky"].Final.Min.Y; got != innerViewport.Min.Y+4 {
		t.Fatalf("sticky escaped nearest viewport: final y=%d viewport=%v", got, innerViewport)
	}
	if result.Layout["sticky"].Final.Min.Y < result.Scroll["outer"].Viewport.Min.Y {
		t.Fatalf("sticky escaped outer viewport: final=%v outer=%v", result.Layout["sticky"].Final, result.Scroll["outer"].Viewport)
	}
	var operations op.Ops
	gio := LayoutGio(layout.Context{Ops: &operations, Metric: unit.Metric{PxPerDp: 1, PxPerSp: 1}, Constraints: layout.Exact(viewport)}, material.NewTheme(), root, viewport, State{Scroll: map[string]image.Point{"outer": image.Pt(0, 50), "inner": image.Pt(0, 40)}})
	if !reflect.DeepEqual(result.Layout, gio.Layout) || !reflect.DeepEqual(result.Geometry, gio.Geometry) {
		t.Fatalf("nested sticky CPU/Gio mismatch: cpu=%+v/%+v gio=%+v/%+v", result.Layout, result.Geometry, gio.Layout, gio.Geometry)
	}
	var cache GioCache
	theme := material.NewTheme()
	var buildOps op.Ops
	cache.Layout(layout.Context{Ops: &buildOps, Metric: unit.Metric{PxPerDp: 1, PxPerSp: 1}, Constraints: layout.Exact(viewport)}, theme, root, viewport, State{})
	var replayOps op.Ops
	cached := cache.Layout(layout.Context{Ops: &replayOps, Metric: unit.Metric{PxPerDp: 1, PxPerSp: 1}, Constraints: layout.Exact(viewport)}, theme, root, viewport, State{Scroll: map[string]image.Point{"outer": image.Pt(0, 50), "inner": image.Pt(0, 40)}})
	if cache.builds != 1 {
		t.Fatalf("nested sticky replay rebuilt scene %d times", cache.builds)
	}
	if !reflect.DeepEqual(cached.Layout, gio.Layout) || !reflect.DeepEqual(cached.Geometry, gio.Geometry) {
		t.Fatalf("nested sticky cache/Gio mismatch: cache=%+v/%+v gio=%+v/%+v", cached.Layout, cached.Geometry, gio.Layout, gio.Geometry)
	}

	overlay := &project.Node{Handle: "fallback", Type: "overlay", Children: []*project.Node{{
		Handle: "fallback-sticky", Type: "surface", Props: map[string]any{"width": int64(40), "height": int64(20)}, Place: map[string]any{
			"position": "sticky", "inset": map[string]any{"top": map[string]any{"percent": float64(10)}}, "x": int64(0), "y": int64(-20),
		},
	}}}
	res100 := Render(overlay, image.Pt(100, 100), State{})
	res200 := Render(overlay, image.Pt(100, 200), State{})
	if res100.Layout["fallback-sticky"].Final.Min.Y != 10 || res200.Layout["fallback-sticky"].Final.Min.Y != 20 {
		t.Fatalf("sticky viewport fallback percentage = %v/%v, want 10/20", res100.Layout["fallback-sticky"].Final, res200.Layout["fallback-sticky"].Final)
	}
}

func TestStickyNestedSubtreeReplayPreservesEachStickyDelta(t *testing.T) {
	inner := stickyNode(map[string]any{"top": int64(12), "right": nil, "bottom": nil, "left": nil}, map[string]any{"height": int64(20)})
	inner.Handle = "inner-sticky"
	outerSticky := stickyNode(map[string]any{"top": int64(8), "right": nil, "bottom": nil, "left": nil}, map[string]any{"height": int64(60)})
	outerSticky.Handle = "outer-sticky"
	outerSticky.Children = []*project.Node{inner}
	root := &project.Node{Handle: "scroll", Type: "scroll", Props: map[string]any{"axis": "vertical"}, Children: []*project.Node{{
		Handle: "content", Type: "stack", Props: map[string]any{"height": int64(300)}, Children: []*project.Node{outerSticky},
	}}}
	viewport := image.Pt(120, 100)
	state := State{Scroll: map[string]image.Point{"scroll": image.Pt(0, 40)}}
	cpu := Render(root, viewport, state)
	if got := cpu.Layout["outer-sticky"].Final.Min.Y; got != 8 {
		t.Fatalf("outer sticky final y=%d, want 8", got)
	}
	// The inner node uses its own inset while retaining the outer sticky's
	// translation; it should sit at the 12-unit top threshold, not drift with
	// either scroll offset.
	if got := cpu.Layout["inner-sticky"].Final.Min.Y; got != 12 {
		t.Fatalf("inner sticky final y=%d, want 12; outer=%+v inner=%+v", got, cpu.Layout["outer-sticky"], cpu.Layout["inner-sticky"])
	}
	var operations op.Ops
	gio := LayoutGio(layout.Context{Ops: &operations, Metric: unit.Metric{PxPerDp: 1, PxPerSp: 1}, Constraints: layout.Exact(viewport)}, material.NewTheme(), root, viewport, state)
	if !reflect.DeepEqual(cpu.Layout, gio.Layout) || !reflect.DeepEqual(cpu.Geometry, gio.Geometry) {
		t.Fatalf("nested sticky CPU/Gio mismatch: cpu=%+v/%+v gio=%+v/%+v", cpu.Layout, cpu.Geometry, gio.Layout, gio.Geometry)
	}
	var cache GioCache
	theme := material.NewTheme()
	var buildOps op.Ops
	cache.Layout(layout.Context{Ops: &buildOps, Metric: unit.Metric{PxPerDp: 1, PxPerSp: 1}, Constraints: layout.Exact(viewport)}, theme, root, viewport, State{})
	var replayOps op.Ops
	cached := cache.Layout(layout.Context{Ops: &replayOps, Metric: unit.Metric{PxPerDp: 1, PxPerSp: 1}, Constraints: layout.Exact(viewport)}, theme, root, viewport, state)
	if cache.builds != 1 {
		t.Fatalf("nested sticky cache rebuilt scene %d times", cache.builds)
	}
	if !reflect.DeepEqual(cached.Layout, gio.Layout) || !reflect.DeepEqual(cached.Geometry, gio.Geometry) {
		t.Fatalf("nested sticky cache mismatch: cache=%+v/%+v gio=%+v/%+v", cached.Layout, cached.Geometry, gio.Layout, gio.Geometry)
	}
}

func TestStickyParentContainmentReleasesAtContentEnd(t *testing.T) {
	sticky := stickyNode(map[string]any{"top": int64(8), "right": nil, "bottom": nil, "left": nil}, map[string]any{"height": int64(20)})
	sticky.Handle = "sticky-release"
	root := &project.Node{Handle: "scroll", Type: "scroll", Props: map[string]any{"axis": "vertical"}, Children: []*project.Node{{
		Handle: "content", Type: "stack", Props: map[string]any{"height": int64(400)}, Children: []*project.Node{
			{Handle: "panel", Type: "surface", Props: map[string]any{"height": int64(100)}, Children: []*project.Node{sticky}},
			{Handle: "after", Type: "surface", Props: map[string]any{"height": int64(300)}},
		},
	}}}
	viewport := image.Pt(120, 100)
	start := Render(root, viewport, State{Scroll: map[string]image.Point{"scroll": image.Pt(0, 0)}})
	middle := Render(root, viewport, State{Scroll: map[string]image.Point{"scroll": image.Pt(0, 50)}})
	end := Render(root, viewport, State{Scroll: map[string]image.Point{"scroll": image.Pt(0, 300)}})
	if start.Layout["sticky-release"].Final.Min.Y != 8 || middle.Layout["sticky-release"].Final.Min.Y != 8 {
		t.Fatalf("sticky should pin before parent end: start=%v middle=%v", start.Layout["sticky-release"].Final, middle.Layout["sticky-release"].Final)
	}
	if end.Layout["sticky-release"].Final.Min.Y >= 8 {
		t.Fatalf("sticky did not release at parent content end: end=%v", end.Layout["sticky-release"].Final)
	}
	if end.Layout["sticky-release"].Final.Max.Y != end.Layout["panel"].Final.Max.Y {
		t.Fatalf("sticky release should preserve panel bottom containment: sticky=%v panel=%v", end.Layout["sticky-release"].Final, end.Layout["panel"].Final)
	}
}

func TestStickyCacheMovesNestedScrollportMetricsAndClip(t *testing.T) {
	innerSticky := stickyNode(map[string]any{"top": int64(4), "right": nil, "bottom": nil, "left": nil}, map[string]any{"height": int64(20)})
	innerSticky.Handle = "inner-sticky"
	innerContent := &project.Node{Handle: "inner-content", Type: "stack", Props: map[string]any{"height": int64(180)}, Children: []*project.Node{innerSticky}}
	innerScroll := &project.Node{Handle: "inner-scroll", Type: "scroll", Props: map[string]any{"axis": "vertical", "height": int64(60)}, Children: []*project.Node{innerContent}}
	outerSticky := stickyNode(map[string]any{"top": int64(10), "right": nil, "bottom": nil, "left": nil}, map[string]any{"height": int64(80)})
	outerSticky.Handle = "outer-sticky"
	outerSticky.Children = []*project.Node{innerScroll}
	root := &project.Node{Handle: "outer-scroll", Type: "scroll", Props: map[string]any{"axis": "vertical"}, Children: []*project.Node{{
		Handle: "outer-content", Type: "stack", Props: map[string]any{"height": int64(360)}, Children: []*project.Node{
			{Handle: "before", Type: "surface", Props: map[string]any{"height": int64(20)}},
			outerSticky,
		},
	}}}
	viewport := image.Pt(120, 100)
	buildState := State{Scroll: map[string]image.Point{"outer-scroll": image.Pt(0, 0), "inner-scroll": image.Pt(0, 0)}}
	replayState := State{Scroll: map[string]image.Point{"outer-scroll": image.Pt(0, 30), "inner-scroll": image.Pt(0, 10)}}
	var freshOps op.Ops
	fresh := LayoutGio(layout.Context{Ops: &freshOps, Metric: unit.Metric{PxPerDp: 1, PxPerSp: 1}, Constraints: layout.Exact(viewport)}, material.NewTheme(), root, viewport, replayState)
	var cache GioCache
	theme := material.NewTheme()
	var buildOps op.Ops
	cache.Layout(layout.Context{Ops: &buildOps, Metric: unit.Metric{PxPerDp: 1, PxPerSp: 1}, Constraints: layout.Exact(viewport)}, theme, root, viewport, buildState)
	var replayOps op.Ops
	cached := cache.Layout(layout.Context{Ops: &replayOps, Metric: unit.Metric{PxPerDp: 1, PxPerSp: 1}, Constraints: layout.Exact(viewport)}, theme, root, viewport, replayState)
	if cache.builds != 1 {
		t.Fatalf("nested scrollport sticky replay rebuilt scene %d times", cache.builds)
	}
	if !reflect.DeepEqual(cached.Scroll["inner-scroll"], fresh.Scroll["inner-scroll"]) {
		t.Fatalf("nested scroll metric stale after sticky move: cached=%+v fresh=%+v", cached.Scroll["inner-scroll"], fresh.Scroll["inner-scroll"])
	}
	for _, handle := range []string{"outer-sticky", "inner-scroll", "inner-sticky", "inner-content"} {
		if cached.Layout[handle].Final != fresh.Layout[handle].Final || cached.Geometry[handle].Clip != fresh.Geometry[handle].Clip {
			t.Fatalf("nested sticky replay geometry stale for %q: cached layout=%v clip=%v, fresh layout=%v clip=%v; cached all=%+v fresh all=%+v", handle, cached.Layout[handle].Final, cached.Geometry[handle].Clip, fresh.Layout[handle].Final, fresh.Geometry[handle].Clip, cached.Layout, fresh.Layout)
		}
	}
}
