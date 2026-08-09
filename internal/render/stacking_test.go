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

	"gora/internal/interaction"
	"gora/internal/project"
	"gora/internal/semantic"
)

func stackingInset() map[string]any {
	return map[string]any{"top": int64(0), "right": nil, "bottom": nil, "left": int64(0)}
}

func stackingButton(handle string, background string, place map[string]any) *project.Node {
	return &project.Node{
		Handle: handle,
		Name:   handle,
		Type:   "button",
		Props:  map[string]any{"width": int64(40), "height": int64(40), "background": background, "label": handle},
		Place:  place,
		Children: []*project.Node{{
			Handle: handle + "-label", Type: "text", Props: map[string]any{"text": handle},
		}},
	}
}

func stackingFixedButton(handle string, background string, z int64) *project.Node {
	return stackingButton(handle, background, map[string]any{
		"position": "fixed", "inset": stackingInset(), "z_index": z,
	})
}

func TestPaintOrderedChildrenUsesStableCategoryAndZIndexOrdering(t *testing.T) {
	children := []*project.Node{
		stackingFixedButton("positive-two-a", "", 2),
		stackingButton("flow", "", nil),
		stackingFixedButton("negative-one-a", "", -1),
		stackingFixedButton("zero", "", 0),
		stackingFixedButton("positive-two-b", "", 2),
		stackingFixedButton("negative-two", "", -2),
		stackingFixedButton("negative-one-b", "", -1),
	}
	ordered := paintOrderedChildren(children)
	want := []string{"negative-two", "negative-one-a", "negative-one-b", "flow", "zero", "positive-two-a", "positive-two-b"}
	for index, node := range ordered {
		if node.Handle != want[index] {
			t.Fatalf("ordered[%d] = %q, want %q", index, node.Handle, want[index])
		}
	}
}

func TestStackingPaintOrderSortsNegativeFlowZeroAndPositiveContexts(t *testing.T) {
	// Deliberately scramble authored order. The final pixel must follow the
	// stacking categories rather than source order.
	root := &project.Node{Handle: "root", Type: "overlay", Children: []*project.Node{
		stackingFixedButton("positive-high", "#FFFF00", 5),
		stackingButton("flow", "#00FF00", nil),
		stackingFixedButton("negative-high", "#FF0000", -1),
		stackingFixedButton("zero", "#0000FF", 0),
		stackingFixedButton("positive-low", "#00FFFF", 1),
		stackingFixedButton("negative-low", "#FF00FF", -3),
	}}
	viewport := image.Pt(60, 60)
	cpu := Render(root, viewport, State{})
	if got := color.RGBAModel.Convert(cpu.Image.At(30, 30)).(color.RGBA); got != (color.RGBA{R: 255, G: 255, A: 255}) {
		t.Fatalf("topmost stacking pixel = %#v, want positive-high yellow", got)
	}
	wantOrder := []string{"negative-low", "negative-high", "flow", "zero", "positive-low", "positive-high"}
	for index := 0; index+1 < len(wantOrder); index++ {
		left := cpu.Geometry[wantOrder[index]].PaintOrder
		right := cpu.Geometry[wantOrder[index+1]].PaintOrder
		if left >= right {
			t.Fatalf("stacking paint order %s=%d, %s=%d; want source-independent category order %v", wantOrder[index], left, wantOrder[index+1], right, wantOrder)
		}
	}
	if cpu.Tree == nil {
		t.Fatal("missing semantic tree")
	}
	var focus []string
	for _, node := range semantic.Flatten(cpu.Tree) {
		if node == nil || node.Name == "" {
			continue
		}
		focus = append(focus, node.Name)
	}
	if !reflect.DeepEqual(focus, []string{"positive-high", "flow", "negative-high", "zero", "positive-low", "negative-low"}) {
		t.Fatalf("focus/source order changed with paint order: %v", focus)
	}

	var ops op.Ops
	gio := LayoutGio(layout.Context{Ops: &ops, Metric: unit.Metric{PxPerDp: 1, PxPerSp: 1}, Constraints: layout.Exact(viewport)}, material.NewTheme(), root, viewport, State{})
	if !reflect.DeepEqual(cpu.Geometry, gio.Geometry) || !reflect.DeepEqual(cpu.Layout, gio.Layout) || !reflect.DeepEqual(cpu.Tree, gio.Tree) {
		t.Fatalf("CPU/Gio stacking mismatch: geometry=%v/%v layout=%v/%v tree=%v/%v", cpu.Geometry, gio.Geometry, cpu.Layout, gio.Layout, cpu.Tree, gio.Tree)
	}
	var cache GioCache
	theme := material.NewTheme()
	var buildOps op.Ops
	cache.Layout(layout.Context{Ops: &buildOps, Metric: unit.Metric{PxPerDp: 1, PxPerSp: 1}, Constraints: layout.Exact(viewport)}, theme, root, viewport, State{})
	var replayOps op.Ops
	replayed := cache.Layout(layout.Context{Ops: &replayOps, Metric: unit.Metric{PxPerDp: 1, PxPerSp: 1}, Constraints: layout.Exact(viewport)}, theme, root, viewport, State{})
	if cache.builds != 1 {
		t.Fatalf("stacking cache rebuilt on replay: builds=%d", cache.builds)
	}
	if !reflect.DeepEqual(cpu.Geometry, replayed.Geometry) || !reflect.DeepEqual(cpu.Layout, replayed.Layout) || !reflect.DeepEqual(cpu.Tree, replayed.Tree) {
		t.Fatalf("retained stacking rank mismatch: CPU=%v/%v/%v replay=%v/%v/%v", cpu.Geometry, cpu.Layout, cpu.Tree, replayed.Geometry, replayed.Layout, replayed.Tree)
	}

	router := interaction.NewRouter()
	router.Update(cpu.Tree)
	if !router.Press(1, image.Pt(10, 10)) {
		t.Fatal("overlapping topmost control was not pressed")
	}
	if got := router.Transient().Focused; got != "positive-high" {
		t.Fatalf("reverse hit selected %q, want positive-high", got)
	}
}

func TestStackingContextsKeepDescendantZInsidePositionedOwner(t *testing.T) {
	sibling := stackingButton("sibling", "#00FF00", nil)
	parent := &project.Node{
		Handle: "negative-owner", Name: "negative-owner", Type: "surface",
		Props: map[string]any{"width": int64(40), "height": int64(40), "background": "#FF0000"},
		Place: map[string]any{"position": "fixed", "inset": stackingInset(), "z_index": int64(-1)},
		Children: []*project.Node{{
			Handle: "escaped-child", Name: "escaped-child", Type: "surface",
			Props: map[string]any{"width": int64(40), "height": int64(40), "background": "#FFFF00"},
			Place: map[string]any{"position": "fixed", "inset": stackingInset(), "z_index": int64(100)},
		}},
	}
	root := &project.Node{Handle: "root", Type: "overlay", Children: []*project.Node{sibling, parent}}
	cpu := Render(root, image.Pt(60, 60), State{})
	if got := color.RGBAModel.Convert(cpu.Image.At(30, 30)).(color.RGBA); got != (color.RGBA{R: 0, G: 255, B: 0, A: 255}) {
		t.Fatalf("descendant z escaped owner pixel = %#v, want normal sibling green", got)
	}
	if !(cpu.Geometry[parent.Handle].PaintOrder < cpu.Geometry[parent.Children[0].Handle].PaintOrder && cpu.Geometry[parent.Children[0].Handle].PaintOrder < cpu.Geometry[sibling.Handle].PaintOrder) {
		t.Fatalf("nested context ranks = owner:%d child:%d sibling:%d; want atomic owner subtree before sibling", cpu.Geometry[parent.Handle].PaintOrder, cpu.Geometry[parent.Children[0].Handle].PaintOrder, cpu.Geometry[sibling.Handle].PaintOrder)
	}
}

func TestStackingPromotesPositionedDescendantThroughFlowWrapper(t *testing.T) {
	fixed := stackingFixedButton("promoted-fixed", "#FFFF00", 5)
	wrapper := &project.Node{
		Handle: "flow-wrapper", Type: "surface",
		Props:    map[string]any{"width": int64(60), "height": int64(60), "background": "#0000FF"},
		Children: []*project.Node{fixed},
	}
	later := stackingButton("later-flow", "#00FF00", nil)
	root := &project.Node{Handle: "root", Type: "overlay", Children: []*project.Node{wrapper, later}}
	result := Render(root, image.Pt(60, 60), State{})
	if got := color.RGBAModel.Convert(result.Image.At(30, 30)).(color.RGBA); got != (color.RGBA{R: 255, G: 255, A: 255}) {
		t.Fatalf("promoted fixed pixel = %#v, want yellow above later flow", got)
	}
	if !(result.Geometry[wrapper.Handle].PaintOrder < result.Geometry[later.Handle].PaintOrder && result.Geometry[later.Handle].PaintOrder < result.Geometry[fixed.Handle].PaintOrder) {
		t.Fatalf("wrapper/later/promoted ranks = %d/%d/%d, want fixed promoted after flow", result.Geometry[wrapper.Handle].PaintOrder, result.Geometry[later.Handle].PaintOrder, result.Geometry[fixed.Handle].PaintOrder)
	}
	var ops op.Ops
	gio := LayoutGio(layout.Context{Ops: &ops, Metric: unit.Metric{PxPerDp: 1, PxPerSp: 1}, Constraints: layout.Exact(image.Pt(60, 60))}, material.NewTheme(), root, image.Pt(60, 60), State{})
	if !reflect.DeepEqual(result.Geometry, gio.Geometry) || !reflect.DeepEqual(result.Layout, gio.Layout) || !reflect.DeepEqual(result.Tree, gio.Tree) {
		t.Fatalf("promoted CPU/Gio mismatch: geometry=%v/%v layout=%v/%v tree=%v/%v", result.Geometry, gio.Geometry, result.Layout, gio.Layout, result.Tree, gio.Tree)
	}
	var cache GioCache
	theme := material.NewTheme()
	var buildOps op.Ops
	cache.Layout(layout.Context{Ops: &buildOps, Metric: unit.Metric{PxPerDp: 1, PxPerSp: 1}, Constraints: layout.Exact(image.Pt(60, 60))}, theme, root, image.Pt(60, 60), State{})
	var replayOps op.Ops
	replayed := cache.Layout(layout.Context{Ops: &replayOps, Metric: unit.Metric{PxPerDp: 1, PxPerSp: 1}, Constraints: layout.Exact(image.Pt(60, 60))}, theme, root, image.Pt(60, 60), State{})
	if cache.builds != 1 || !reflect.DeepEqual(result.Geometry, replayed.Geometry) || !reflect.DeepEqual(result.Tree, replayed.Tree) {
		t.Fatalf("promoted retained mismatch/builds=%d: CPU=%v/%v replay=%v/%v", cache.builds, result.Geometry, result.Tree, replayed.Geometry, replayed.Tree)
	}
	router := interaction.NewRouter()
	router.Update(result.Tree)
	if !router.Press(1, image.Pt(30, 30)) || router.Transient().Focused != fixed.Handle {
		t.Fatalf("promoted fixed hit = %q, want %q", router.Transient().Focused, fixed.Handle)
	}
}

func TestStackingPromotesNegativePositionedDescendantBeforeFlowWrapper(t *testing.T) {
	fixed := stackingFixedButton("promoted-negative", "#FF0000", -1)
	wrapper := &project.Node{
		Handle: "flow-wrapper-negative", Type: "surface",
		Props:    map[string]any{"width": int64(60), "height": int64(60), "background": "#0000FF"},
		Children: []*project.Node{fixed},
	}
	later := stackingButton("later-flow-negative", "#00FF00", nil)
	root := &project.Node{Handle: "root-negative", Type: "overlay", Children: []*project.Node{wrapper, later}}
	result := Render(root, image.Pt(60, 60), State{})
	if got := color.RGBAModel.Convert(result.Image.At(30, 30)).(color.RGBA); got != (color.RGBA{G: 255, A: 255}) {
		t.Fatalf("negative fixed pixel = %#v, want later flow green above negative", got)
	}
	if !(result.Geometry[fixed.Handle].PaintOrder < result.Geometry[wrapper.Handle].PaintOrder && result.Geometry[wrapper.Handle].PaintOrder < result.Geometry[later.Handle].PaintOrder) {
		t.Fatalf("negative/wrapper/later ranks = %d/%d/%d, want negative before flow", result.Geometry[fixed.Handle].PaintOrder, result.Geometry[wrapper.Handle].PaintOrder, result.Geometry[later.Handle].PaintOrder)
	}
}

func TestStackingNestedFlowWrappersDoNotCreateContexts(t *testing.T) {
	fixed := stackingFixedButton("deep-promoted", "#FFFF00", 2)
	inner := &project.Node{Handle: "inner-wrapper", Type: "surface", Props: map[string]any{"width": int64(60), "height": int64(60), "background": "#0000FF"}, Children: []*project.Node{fixed}}
	outer := &project.Node{Handle: "outer-wrapper", Type: "surface", Props: map[string]any{"width": int64(60), "height": int64(60), "background": "#0000FF"}, Children: []*project.Node{inner}}
	later := stackingButton("deep-later", "#00FF00", nil)
	root := &project.Node{Handle: "root-deep", Type: "overlay", Children: []*project.Node{outer, later}}
	result := Render(root, image.Pt(60, 60), State{})
	if got := color.RGBAModel.Convert(result.Image.At(30, 30)).(color.RGBA); got != (color.RGBA{R: 255, G: 255, A: 255}) {
		t.Fatalf("deep promoted pixel = %#v, want yellow", got)
	}
	if !(result.Geometry[later.Handle].PaintOrder < result.Geometry[fixed.Handle].PaintOrder) {
		t.Fatalf("deep wrapper promotion ranks later=%d fixed=%d, want fixed at root positive tier", result.Geometry[later.Handle].PaintOrder, result.Geometry[fixed.Handle].PaintOrder)
	}
}

func TestStackingPromotedDescendantRemainsAtomicInsidePositionedWrapper(t *testing.T) {
	inner := stackingFixedButton("inner-atomic", "#FFFF00", 100)
	positioned := &project.Node{
		Handle: "positioned-wrapper", Type: "surface",
		Props:    map[string]any{"width": int64(60), "height": int64(60), "background": "#FF0000"},
		Place:    map[string]any{"position": "fixed", "inset": stackingInset(), "z_index": int64(-1)},
		Children: []*project.Node{{Handle: "flow-inside-positioned", Type: "surface", Props: map[string]any{"width": int64(60), "height": int64(60)}, Children: []*project.Node{inner}}},
	}
	later := stackingButton("atomic-later", "#00FF00", nil)
	root := &project.Node{Handle: "root-atomic", Type: "overlay", Children: []*project.Node{positioned, later}}
	result := Render(root, image.Pt(60, 60), State{})
	if got := color.RGBAModel.Convert(result.Image.At(30, 30)).(color.RGBA); got != (color.RGBA{G: 255, A: 255}) {
		t.Fatalf("positioned wrapper atomic pixel = %#v, want later flow green", got)
	}
	if !(result.Geometry[positioned.Handle].PaintOrder < result.Geometry[inner.Handle].PaintOrder && result.Geometry[inner.Handle].PaintOrder < result.Geometry[later.Handle].PaintOrder) {
		t.Fatalf("positioned/inner/later ranks = %d/%d/%d, want atomic owner subtree", result.Geometry[positioned.Handle].PaintOrder, result.Geometry[inner.Handle].PaintOrder, result.Geometry[later.Handle].PaintOrder)
	}
}

func TestStackingRanksStickyAndFixedOverlapTogether(t *testing.T) {
	sticky := stackingButton("sticky", "#00FF00", map[string]any{
		"position": "sticky", "inset": stackingInset(), "z_index": int64(1),
	})
	fixed := stackingFixedButton("fixed", "#FF0000", 0)
	root := &project.Node{Handle: "root", Type: "overlay", Children: []*project.Node{sticky, fixed}}
	result := Render(root, image.Pt(60, 60), State{})
	if got := color.RGBAModel.Convert(result.Image.At(30, 30)).(color.RGBA); got != (color.RGBA{G: 255, A: 255}) {
		t.Fatalf("sticky/fixed overlap pixel = %#v, want sticky green", got)
	}
	if result.Geometry[fixed.Handle].PaintOrder >= result.Geometry[sticky.Handle].PaintOrder {
		t.Fatalf("fixed/sticky ranks = %d/%d, want fixed before positive sticky", result.Geometry[fixed.Handle].PaintOrder, result.Geometry[sticky.Handle].PaintOrder)
	}
}

func TestStackingRanksScrollbarAfterOwnerContentBeforeLaterParticipant(t *testing.T) {
	owner := &project.Node{
		Handle: "owner", Type: "scroll",
		Props: map[string]any{"width": int64(80), "height": int64(60), "axis": "vertical", "scrollbar_y": "always"},
		Children: []*project.Node{{
			Handle: "content", Type: "surface", Props: map[string]any{"width": int64(80), "height": int64(180), "background": "#00FF00"},
		}},
	}
	later := &project.Node{Handle: "later", Type: "surface", Props: map[string]any{"width": int64(80), "height": int64(60), "background": "#FF0000"}}
	root := &project.Node{Handle: "root", Type: "overlay", Children: []*project.Node{owner, later}}
	result := Render(root, image.Pt(100, 80), State{})
	if len(result.Derived) != 1 {
		t.Fatalf("derived bars = %d, want one vertical bar", len(result.Derived))
	}
	bar := result.Derived[0]
	if !(result.Geometry[owner.Handle].PaintOrder < result.Geometry["content"].PaintOrder && result.Geometry["content"].PaintOrder < bar.PaintOrder && bar.PaintOrder < result.Geometry[later.Handle].PaintOrder) {
		t.Fatalf("owner/content/bar/later ranks = %d/%d/%d/%d; want content < bar < later", result.Geometry[owner.Handle].PaintOrder, result.Geometry["content"].PaintOrder, bar.PaintOrder, result.Geometry[later.Handle].PaintOrder)
	}
	var semanticBar *semantic.Node
	for _, node := range semantic.Flatten(result.Tree) {
		if node.Role == "scrollbar" {
			semanticBar = node
			break
		}
	}
	if semanticBar == nil || semanticBar.PaintOrder != bar.PaintOrder || semanticBar.PaintOrder >= result.Tree.Children[len(result.Tree.Children)-1].PaintOrder {
		t.Fatalf("semantic scrollbar rank = %+v, want descriptor rank %d before later participant", semanticBar, bar.PaintOrder)
	}
	var cache GioCache
	theme := material.NewTheme()
	var buildOps op.Ops
	cache.Layout(layout.Context{Ops: &buildOps, Metric: unit.Metric{PxPerDp: 1, PxPerSp: 1}, Constraints: layout.Exact(image.Pt(100, 80))}, theme, root, image.Pt(100, 80), State{})
	var replayOps op.Ops
	cached := cache.Layout(layout.Context{Ops: &replayOps, Metric: unit.Metric{PxPerDp: 1, PxPerSp: 1}, Constraints: layout.Exact(image.Pt(100, 80))}, theme, root, image.Pt(100, 80), State{})
	if len(cached.Derived) != 1 || cached.Derived[0].PaintOrder != bar.PaintOrder || cached.Geometry[later.Handle].PaintOrder != result.Geometry[later.Handle].PaintOrder {
		t.Fatalf("cached scrollbar rank = derived:%v later:%d, want derived:%d later:%d", cached.Derived, cached.Geometry[later.Handle].PaintOrder, bar.PaintOrder, result.Geometry[later.Handle].PaintOrder)
	}
}

func TestStackingRetainsOffscreenNodeButExcludesItFromRouterHit(t *testing.T) {
	offscreen := stackingFixedButton("offscreen", "#FF0000", 10)
	offscreen.Place["inset"] = map[string]any{"top": int64(-100), "right": nil, "bottom": nil, "left": int64(-100)}
	root := &project.Node{Handle: "root", Type: "overlay", Children: []*project.Node{offscreen}}
	result := Render(root, image.Pt(60, 60), State{})
	var found bool
	for _, node := range semantic.Flatten(result.Tree) {
		if node.Handle == offscreen.Handle {
			found = true
			if !node.Visible || node.Bounds == nil || node.InViewport {
				t.Fatalf("offscreen semantic node = %+v, want retained visible/null-viewport geometry", node)
			}
		}
	}
	if !found {
		t.Fatal("offscreen node was removed from semantic tree")
	}
	router := interaction.NewRouter()
	router.Update(result.Tree)
	if router.Press(1, image.Pt(10, 10)) {
		t.Fatal("offscreen fixed node accepted a hit")
	}
}
