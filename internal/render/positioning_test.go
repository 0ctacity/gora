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
	"gora/internal/semantic"
)

func fixedNode(handle string, props map[string]any, place map[string]any) *project.Node {
	return &project.Node{Handle: handle, Type: "surface", Props: props, Place: place}
}

func fixedPlace(inset map[string]any) map[string]any {
	return map[string]any{"position": "fixed", "inset": inset}
}

func TestFixedChildrenDoNotContributeToStackFlowOrIntrinsicSize(t *testing.T) {
	root := &project.Node{
		Handle: "stack", Type: "stack",
		Props: map[string]any{"direction": "vertical", "gap": int64(10)},
		Children: []*project.Node{
			{Handle: "first", Type: "surface", Props: map[string]any{"height": int64(20)}},
			fixedNode("fixed", map[string]any{"width": int64(90), "height": int64(80)}, fixedPlace(map[string]any{"top": int64(0), "right": nil, "bottom": nil, "left": int64(0)})),
			{Handle: "last", Type: "surface", Props: map[string]any{"height": int64(30)}},
		},
	}

	result := Render(root, image.Pt(120, 200), State{})
	if got, want := result.Bounds["last"], image.Rect(0, 30, 120, 60); got != want {
		t.Fatalf("last flow bounds = %v, want %v (fixed child must not consume a gap)", got, want)
	}
	if got, want := measureIntrinsic(root, image.Pt(120, 200), testLayoutMeasure), image.Pt(0, 60); got != want {
		t.Fatalf("stack intrinsic size = %v, want %v", got, want)
	}
}

func TestFixedChildrenDoNotContributeToWrappingGridOrOverlay(t *testing.T) {
	stack := &project.Node{
		Handle: "wrap", Type: "stack",
		Props: map[string]any{"direction": "horizontal", "wrap": true, "gap": int64(4)},
		Children: []*project.Node{
			{Handle: "a", Type: "surface", Place: map[string]any{"basis": int64(45)}, Props: map[string]any{"height": int64(10)}},
			fixedNode("fixed-wrap", map[string]any{"width": int64(100), "height": int64(10)}, fixedPlace(map[string]any{"top": int64(0), "right": nil, "bottom": nil, "left": int64(0)})),
			{Handle: "b", Type: "surface", Place: map[string]any{"basis": int64(45)}, Props: map[string]any{"height": int64(10)}},
		},
	}
	if got := Render(stack, image.Pt(100, 40), State{}).Bounds["b"]; got.Min.Y != 0 {
		t.Fatalf("wrapped flow child moved to a second line by fixed sibling: %v", got)
	}

	grid := &project.Node{
		Handle: "grid", Type: "grid", Props: map[string]any{"columns": int64(1), "gap": int64(5)},
		Children: []*project.Node{
			{Handle: "one", Type: "surface", Props: map[string]any{"height": int64(20)}},
			fixedNode("fixed-grid", map[string]any{"height": int64(100)}, fixedPlace(map[string]any{"top": int64(0), "right": nil, "bottom": nil, "left": int64(0)})),
			{Handle: "two", Type: "surface", Props: map[string]any{"height": int64(30)}},
		},
	}
	if got, want := Render(grid, image.Pt(100, 100), State{}).Bounds["two"], image.Rect(0, 53, 100, 83); got != want {
		t.Fatalf("grid flow bounds = %v, want %v", got, want)
	}

	overlay := &project.Node{
		Handle: "overlay", Type: "overlay",
		Children: []*project.Node{
			{Handle: "content", Type: "surface", Props: map[string]any{"width": int64(20), "height": int64(20)}},
			fixedNode("fixed-overlay", map[string]any{"width": int64(200), "height": int64(200)}, map[string]any{
				"position": "fixed", "inset": map[string]any{"top": int64(0), "right": nil, "bottom": nil, "left": int64(0)}, "x": int64(100), "y": int64(100),
			}),
		},
	}
	if got, want := measureIntrinsic(overlay, image.Pt(300, 300), testLayoutMeasure), image.Pt(20, 20); got != want {
		t.Fatalf("overlay intrinsic size = %v, want %v", got, want)
	}
}

func TestFixedViewportPlanner(t *testing.T) {
	viewport := image.Rect(0, 0, 800, 600)
	tests := []struct {
		name      string
		node      *project.Node
		intrinsic image.Point
		want      image.Rectangle
		valid     bool
	}{
		{
			name:      "definite size top left wins",
			node:      fixedNode("fixed", map[string]any{"width": int64(120), "height": int64(80)}, fixedPlace(map[string]any{"top": int64(20), "right": int64(30), "bottom": int64(40), "left": int64(50)})),
			intrinsic: image.Pt(10, 10), want: image.Rect(50, 20, 170, 100), valid: true,
		},
		{
			name:      "top right anchor",
			node:      fixedNode("fixed", map[string]any{"width": int64(120), "height": int64(80)}, fixedPlace(map[string]any{"top": int64(20), "right": int64(30), "bottom": nil, "left": nil})),
			intrinsic: image.Pt(10, 10), want: image.Rect(650, 20, 770, 100), valid: true,
		},
		{
			name:      "bottom left anchor",
			node:      fixedNode("fixed", map[string]any{"width": int64(120), "height": int64(80)}, fixedPlace(map[string]any{"top": nil, "right": nil, "bottom": int64(40), "left": int64(50)})),
			intrinsic: image.Pt(10, 10), want: image.Rect(50, 480, 170, 560), valid: true,
		},
		{
			name:      "bottom right anchor",
			node:      fixedNode("fixed", map[string]any{"width": int64(120), "height": int64(80)}, fixedPlace(map[string]any{"top": nil, "right": int64(30), "bottom": int64(40), "left": nil})),
			intrinsic: image.Pt(10, 10), want: image.Rect(650, 480, 770, 560), valid: true,
		},
		{
			name:      "opposing insets stretch auto axes",
			node:      fixedNode("fixed", map[string]any{}, fixedPlace(map[string]any{"top": int64(10), "right": int64(20), "bottom": int64(30), "left": int64(40)})),
			intrinsic: image.Pt(90, 70), want: image.Rect(40, 10, 780, 570), valid: true,
		},
		{
			name:      "single inset uses intrinsic size",
			node:      fixedNode("fixed", map[string]any{}, fixedPlace(map[string]any{"top": int64(15), "right": nil, "bottom": nil, "left": int64(25)})),
			intrinsic: image.Pt(90, 70), want: image.Rect(25, 15, 115, 85), valid: true,
		},
		{
			name:      "percentage insets",
			node:      fixedNode("fixed", map[string]any{"width": int64(100), "height": int64(50)}, fixedPlace(map[string]any{"top": map[string]any{"percent": float64(10)}, "right": nil, "bottom": nil, "left": map[string]any{"percent": float64(25)}})),
			intrinsic: image.Pt(1, 1), want: image.Rect(200, 60, 300, 110), valid: true,
		},
		{
			name:      "negative insets",
			node:      fixedNode("fixed", map[string]any{"width": int64(100), "height": int64(50)}, fixedPlace(map[string]any{"top": -10.0, "right": nil, "bottom": nil, "left": -20.0})),
			intrinsic: image.Pt(1, 1), want: image.Rect(-20, -10, 80, 40), valid: true,
		},
		{
			name:      "stretch respects max constraint",
			node:      fixedNode("fixed", map[string]any{"max_width": int64(500)}, fixedPlace(map[string]any{"top": int64(0), "right": int64(20), "bottom": nil, "left": int64(40)})),
			intrinsic: image.Pt(10, 10), want: image.Rect(40, 0, 540, 10), valid: true,
		},
		{
			name:      "aspect ratio derives automatic height",
			node:      fixedNode("fixed", map[string]any{"width": int64(200), "aspect_ratio": map[string]any{"width": int64(2), "height": int64(1)}}, fixedPlace(map[string]any{"top": int64(12), "right": nil, "bottom": nil, "left": int64(18)})),
			intrinsic: image.Pt(1, 1), want: image.Rect(18, 12, 218, 112), valid: true,
		},
		{
			name:      "missing axis pair invalid",
			node:      fixedNode("fixed", map[string]any{"width": int64(100)}, fixedPlace(map[string]any{"top": int64(10), "right": nil, "bottom": nil, "left": nil})),
			intrinsic: image.Pt(1, 1), want: image.Rectangle{}, valid: false,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, valid := planFixedViewport(test.node, viewport, test.intrinsic)
			if valid != test.valid || !reflect.DeepEqual(got, test.want) {
				t.Fatalf("fixed plan = %v/%v, want %v/%v", got, valid, test.want, test.valid)
			}
		})
	}
}

func TestLayoutRecordsNormalAndFinalContextWithoutMutatingFlowGeometry(t *testing.T) {
	root := &project.Node{
		Handle: "scroll", Type: "scroll",
		Props: map[string]any{"axis": "both", "padding": map[string]any{
			"top": int64(4), "right": int64(5), "bottom": int64(6), "left": int64(7),
		}},
		Children: []*project.Node{{
			Handle: "content", Type: "surface",
			Props:    map[string]any{"width": int64(180), "height": int64(140)},
			Children: []*project.Node{{Handle: "label", Type: "text", Props: map[string]any{"text": "content"}, Breadcrumb: []string{"card", "instance"}}},
		}},
	}
	viewport := image.Pt(100, 80)
	first := Render(root, viewport, State{Scroll: map[string]image.Point{"scroll": image.Pt(0, 0)}})
	second := Render(root, viewport, State{Scroll: map[string]image.Point{"scroll": image.Pt(30, 20)}})
	record := first.Layout["content"]
	if record.Normal != image.Rect(7, 4, 187, 144) || record.Final != record.Normal {
		t.Fatalf("initial content layout = %+v", record)
	}
	if !reflect.DeepEqual(record.ScrollAncestors, []string{"scroll"}) || record.ParentInner != image.Rect(7, 4, 95, 74) || record.ContainingViewport != image.Rect(0, 0, 100, 80) {
		t.Fatalf("content context = %+v", record)
	}
	shifted := second.Layout["content"]
	if shifted.Normal != record.Normal || shifted.Final != image.Rect(-23, -16, 157, 124) {
		t.Fatalf("scroll changed normal/final records = %+v, want normal %v and final %v", shifted, record.Normal, image.Rect(-23, -16, 157, 124))
	}

	var operations op.Ops
	gio := LayoutGio(layout.Context{Ops: &operations, Metric: unit.Metric{PxPerDp: 1, PxPerSp: 1}, Constraints: layout.Exact(viewport)}, material.NewTheme(), root, viewport, State{Scroll: map[string]image.Point{"scroll": image.Pt(30, 20)}})
	if !reflect.DeepEqual(gio.Layout, second.Layout) {
		t.Fatalf("CPU/Gio layout records differ:\nCPU=%+v\nGio=%+v", second.Layout, gio.Layout)
	}
	var cache GioCache
	var cachedOps op.Ops
	cachedZero := cache.Layout(layout.Context{Ops: &cachedOps, Metric: unit.Metric{PxPerDp: 1, PxPerSp: 1}, Constraints: layout.Exact(viewport)}, material.NewTheme(), root, viewport, State{})
	var replayOps op.Ops
	cachedShifted := cache.Layout(layout.Context{Ops: &replayOps, Metric: unit.Metric{PxPerDp: 1, PxPerSp: 1}, Constraints: layout.Exact(viewport)}, material.NewTheme(), root, viewport, State{Scroll: map[string]image.Point{"scroll": image.Pt(30, 20)}})
	zeroRecord := cachedZero.Layout["content"]
	shiftRecord := cachedShifted.Layout["content"]
	if zeroRecord.Normal != shiftRecord.Normal || zeroRecord.ParentInner != shiftRecord.ParentInner || shiftRecord.Final != image.Rect(-23, -16, 157, 124) {
		t.Fatalf("retained layout changed immutable context: zero=%+v shifted=%+v", zeroRecord, shiftRecord)
	}
}

func TestCPUAndGioNormalRectIncludesAuthoredSize(t *testing.T) {
	root := &project.Node{
		Handle: "root", Type: "surface", Props: map[string]any{"padding": map[string]any{"top": int64(5), "right": int64(5), "bottom": int64(5), "left": int64(5)}},
		Children: []*project.Node{{Handle: "child", Type: "surface", Props: map[string]any{"width": int64(20), "height": int64(10)}}},
	}
	viewport := image.Pt(100, 100)
	cpu := Render(root, viewport, State{})
	var operations op.Ops
	gio := LayoutGio(layout.Context{Ops: &operations, Metric: unit.Metric{PxPerDp: 1, PxPerSp: 1}, Constraints: layout.Exact(viewport)}, material.NewTheme(), root, viewport, State{})
	want := image.Rect(5, 5, 25, 15)
	if cpu.Layout["child"].Normal != want || cpu.Layout["child"].Final != want {
		t.Fatalf("CPU child layout = %+v, want own authored rect %v", cpu.Layout["child"], want)
	}
	if !reflect.DeepEqual(cpu.Layout["child"], gio.Layout["child"]) {
		t.Fatalf("CPU/Gio authored normal layout differ: CPU=%+v Gio=%+v", cpu.Layout["child"], gio.Layout["child"])
	}
}

func TestFixedNodesRemainInSemanticHierarchyWithFinalGeometry(t *testing.T) {
	fixed := fixedNode("fixed", map[string]any{"width": int64(40), "height": int64(20)}, fixedPlace(map[string]any{"top": int64(4), "right": nil, "bottom": nil, "left": int64(8)}))
	fixed.Name = "fixed-banner"
	fixed.Breadcrumb = []string{"card", "instance"}
	button := &project.Node{Handle: "button", Type: "button", Name: "button", Props: map[string]any{"label": "Action", "width": int64(40), "height": int64(20)}, Breadcrumb: []string{"card", "instance"}}
	root := &project.Node{Handle: "root", Type: "stack", Children: []*project.Node{
		button, fixed,
	}}
	result := Render(root, image.Pt(120, 80), State{})
	var found *semantic.Node
	for _, node := range semantic.Flatten(result.Tree) {
		if node.Handle == "fixed" {
			found = node
			break
		}
	}
	if found == nil || !found.Visible || found.Bounds == nil || !reflect.DeepEqual(found.Breadcrumb, fixed.Breadcrumb) {
		t.Fatalf("fixed semantic node = %+v, want retained visible hierarchy with final geometry", found)
	}
	if found.FocusOrder != -1 {
		t.Fatalf("non-interactive fixed node unexpectedly entered focus order: %d", found.FocusOrder)
	}
	var normal *semantic.Node
	for _, node := range semantic.Flatten(result.Tree) {
		if node.Handle == "button" {
			normal = node
			break
		}
	}
	if normal == nil || normal.FocusOrder < 0 || !reflect.DeepEqual(normal.Breadcrumb, button.Breadcrumb) {
		t.Fatalf("normal component control lost breadcrumb/focus order: %+v", normal)
	}
}

func TestLayoutRecordsNestedScrollAncestorsAndUnscrolledNormals(t *testing.T) {
	content := &project.Node{Handle: "content", Type: "surface", Props: map[string]any{"width": int64(240), "height": int64(220)}}
	inner := &project.Node{Handle: "inner", Type: "scroll", Props: map[string]any{"axis": "both", "width": int64(80), "height": int64(70)}, Children: []*project.Node{content}}
	flow := &project.Node{Handle: "flow", Type: "stack", Props: map[string]any{"width": int64(240), "height": int64(220), "alignment": "start"}, Children: []*project.Node{inner}}
	outer := &project.Node{Handle: "outer", Type: "scroll", Props: map[string]any{"axis": "both"}, Children: []*project.Node{flow}}
	state := State{Scroll: map[string]image.Point{"outer": image.Pt(12, 8), "inner": image.Pt(20, 16)}}
	viewport := image.Pt(100, 90)
	cpu := Render(outer, viewport, state)
	innerRecord := cpu.Layout["inner"]
	contentRecord := cpu.Layout["content"]
	if !reflect.DeepEqual(innerRecord.ScrollAncestors, []string{"outer"}) || !reflect.DeepEqual(contentRecord.ScrollAncestors, []string{"outer", "inner"}) {
		t.Fatalf("nested scroll ancestors = inner %v/content %v", innerRecord.ScrollAncestors, contentRecord.ScrollAncestors)
	}
	if contentRecord.Normal.Min != (image.Point{}) || contentRecord.Final.Min != image.Pt(-32, -24) {
		t.Fatalf("nested content normal/final = %v/%v", contentRecord.Normal, contentRecord.Final)
	}
	var operations op.Ops
	gio := LayoutGio(layout.Context{Ops: &operations, Metric: unit.Metric{PxPerDp: 1, PxPerSp: 1}, Constraints: layout.Exact(viewport)}, material.NewTheme(), outer, viewport, state)
	if !reflect.DeepEqual(gio.Layout["content"], contentRecord) {
		t.Fatalf("nested CPU/Gio content layout differ: CPU=%+v Gio=%+v", contentRecord, gio.Layout["content"])
	}
}

func TestFixedPlannerAspectRatioComposesWithInsetStretch(t *testing.T) {
	viewport := image.Rect(0, 0, 800, 600)
	widthStretch := fixedNode("width", map[string]any{"aspect_ratio": map[string]any{"width": int64(2), "height": int64(1)}}, fixedPlace(map[string]any{"top": int64(10), "right": int64(20), "bottom": nil, "left": int64(40)}))
	if got, ok := planFixedViewport(widthStretch, viewport, image.Pt(10, 10)); !ok || got != image.Rect(40, 10, 780, 380) {
		t.Fatalf("width stretch aspect plan = %v/%v, want (40,10)-(780,380)/true", got, ok)
	}
	heightStretch := fixedNode("height", map[string]any{"aspect_ratio": map[string]any{"width": int64(2), "height": int64(1)}}, fixedPlace(map[string]any{"top": int64(10), "right": nil, "bottom": int64(20), "left": int64(40)}))
	if got, ok := planFixedViewport(heightStretch, viewport, image.Pt(10, 10)); !ok || got != image.Rect(40, 10, 1180, 580) {
		t.Fatalf("height stretch aspect plan = %v/%v, want (40,10)-(1180,580)/true", got, ok)
	}
	bothStretch := fixedNode("both", map[string]any{"aspect_ratio": map[string]any{"width": int64(2), "height": int64(1)}}, fixedPlace(map[string]any{"top": int64(10), "right": int64(20), "bottom": int64(30), "left": int64(40)}))
	if got, ok := planFixedViewport(bothStretch, viewport, image.Pt(10, 10)); !ok || got != image.Rect(40, 10, 780, 570) {
		t.Fatalf("two-axis stretch aspect plan = %v/%v, want opposing insets to win", got, ok)
	}
}

func TestFixedSubtreesRetainSourceOrderContextWithFinalGeometry(t *testing.T) {
	fixed := fixedNode("fixed", map[string]any{"width": int64(40), "height": int64(20)}, fixedPlace(map[string]any{"top": int64(4), "right": nil, "bottom": nil, "left": int64(8)}))
	fixed.Children = []*project.Node{{Handle: "fixed-label", Type: "text", Props: map[string]any{"text": "fixed"}}}
	root := &project.Node{Handle: "root", Type: "stack", Props: map[string]any{"direction": "vertical"}, Children: []*project.Node{
		{Handle: "first", Type: "surface", Props: map[string]any{"height": int64(10)}},
		fixed,
		{Handle: "last", Type: "surface", Props: map[string]any{"height": int64(10)}},
	}}
	result := Render(root, image.Pt(100, 100), State{})
	var operations op.Ops
	gio := LayoutGio(layout.Context{Ops: &operations, Metric: unit.Metric{PxPerDp: 1, PxPerSp: 1}, Constraints: layout.Exact(image.Pt(100, 100))}, material.NewTheme(), root, image.Pt(100, 100), State{})
	fixedRecord, labelRecord, lastRecord := result.Layout["fixed"], result.Layout["fixed-label"], result.Layout["last"]
	if fixedRecord.Final.Empty() || labelRecord.Final.Empty() || result.Geometry["fixed"].Bounds.Empty() {
		t.Fatalf("fixed geometry was not published: fixed=%+v label=%+v geometry=%+v", fixedRecord, labelRecord, result.Geometry["fixed"])
	}
	if fixedRecord.SourceOrder >= labelRecord.SourceOrder || labelRecord.SourceOrder >= lastRecord.SourceOrder {
		t.Fatalf("source order does not retain fixed subtree slots: fixed=%+d label=%+d last=%+d", fixedRecord.SourceOrder, labelRecord.SourceOrder, lastRecord.SourceOrder)
	}
	if fixedRecord.ContainingViewport != image.Rect(0, 0, 100, 100) || labelRecord.ParentInner.Empty() {
		t.Fatalf("fixed context missing: fixed=%+v label=%+v", fixedRecord, labelRecord)
	}
	if !reflect.DeepEqual(result.Layout, gio.Layout) {
		t.Fatalf("CPU/Gio fixed layout differs: CPU=%+v Gio=%+v", result.Layout, gio.Layout)
	}
}

func TestFixedAutoIntrinsicSizingMatchesCPUGioAndRetainedReplay(t *testing.T) {
	fixed := fixedNode("fixed", map[string]any{}, fixedPlace(map[string]any{"top": int64(12), "right": nil, "bottom": nil, "left": int64(18)}))
	fixed.Children = []*project.Node{{Handle: "fixed-content", Type: "surface", Props: map[string]any{"width": int64(20), "height": int64(10)}}}
	root := &project.Node{Handle: "root", Type: "stack", Props: map[string]any{"width": int64(100), "height": int64(100)}, Children: []*project.Node{fixed}}
	viewport := image.Pt(100, 100)
	cpu := Render(root, viewport, State{})
	var operations op.Ops
	gio := LayoutGio(layout.Context{Ops: &operations, Metric: unit.Metric{PxPerDp: 1, PxPerSp: 1}, Constraints: layout.Exact(viewport)}, material.NewTheme(), root, viewport, State{})
	want := image.Rect(18, 12, 38, 22)
	if cpu.Layout["fixed"].Normal != want || gio.Layout["fixed"].Normal != want {
		t.Fatalf("fixed auto intrinsic rect CPU=%v Gio=%v, want %v", cpu.Layout["fixed"].Normal, gio.Layout["fixed"].Normal, want)
	}
	if !reflect.DeepEqual(cpu.Layout["fixed"], gio.Layout["fixed"]) || cpu.Layout["fixed"].Final != want || gio.Layout["fixed"].Final != want {
		t.Fatalf("fixed CPU/Gio metadata mismatch or final missing: CPU=%+v Gio=%+v", cpu.Layout["fixed"], gio.Layout["fixed"])
	}
	var cache GioCache
	var cachedOps op.Ops
	cached := cache.Layout(layout.Context{Ops: &cachedOps, Metric: unit.Metric{PxPerDp: 1, PxPerSp: 1}, Constraints: layout.Exact(viewport)}, material.NewTheme(), root, viewport, State{})
	if !reflect.DeepEqual(cached.Layout["fixed"], gio.Layout["fixed"]) {
		t.Fatalf("retained fixed metadata differs: cached=%+v fresh=%+v", cached.Layout["fixed"], gio.Layout["fixed"])
	}
}
