package studio

import (
	"image"
	"testing"

	"gora/internal/render"
	"gora/internal/semantic"
)

func TestPlanScrollChainConsumesIndependentDiagonalAxesAcrossAncestors(t *testing.T) {
	root := scrollChainNode("outer", "both", "auto", image.Rect(0, 0, 100, 100), image.Rect(0, 0, 100, 100),
		scrollChainNode("inner", "horizontal", "auto", image.Rect(0, 0, 100, 100), image.Rect(0, 0, 100, 100)))
	metrics := map[string]render.ScrollMetrics{
		"outer": {Viewport: image.Rect(0, 0, 100, 100), Maximum: image.Pt(100, 100), EnabledX: true, EnabledY: true},
		"inner": {Viewport: image.Rect(0, 0, 100, 100), Maximum: image.Pt(100, 0), EnabledX: true},
	}
	plan := planScrollChain(root, metrics, map[string]image.Point{}, image.Pt(20, 20), image.Pt(30, 40))
	if plan.Updates["inner"].X != 30 || plan.Updates["outer"].Y != 40 {
		t.Fatalf("independent diagonal updates = %+v", plan.Updates)
	}
	if plan.Remaining != (image.Point{}) {
		t.Fatalf("remaining diagonal delta = %v, want zero", plan.Remaining)
	}
}

func TestPlanScrollChainPassesPartialResidualToAutoAncestor(t *testing.T) {
	root := scrollChainNode("outer", "vertical", "auto", image.Rect(0, 0, 100, 100), image.Rect(0, 0, 100, 100),
		scrollChainNode("inner", "vertical", "auto", image.Rect(0, 0, 100, 100), image.Rect(0, 0, 100, 100)))
	metrics := map[string]render.ScrollMetrics{
		"outer": {Viewport: image.Rect(0, 0, 100, 100), Maximum: image.Pt(0, 100), EnabledY: true},
		"inner": {Viewport: image.Rect(0, 0, 100, 100), Maximum: image.Pt(0, 10), EnabledY: true},
	}
	plan := planScrollChain(root, metrics, map[string]image.Point{"inner": image.Pt(0, 10)}, image.Pt(20, 20), image.Pt(0, 30))
	if plan.Updates["outer"].Y != 30 {
		t.Fatalf("outer residual update = %+v", plan.Updates)
	}
	if _, ok := plan.Updates["inner"]; ok {
		t.Fatalf("inner boundary generated an unexpected update: %+v", plan.Updates)
	}
	if plan.Remaining != (image.Point{}) {
		t.Fatalf("remaining residual = %v, want zero", plan.Remaining)
	}
}

func TestPlanScrollChainContainDropsEnabledResidualButPassesDisabledAxis(t *testing.T) {
	root := scrollChainNode("outer", "both", "auto", image.Rect(0, 0, 100, 100), image.Rect(0, 0, 100, 100),
		scrollChainNode("middle", "vertical", "contain", image.Rect(0, 0, 100, 100), image.Rect(0, 0, 100, 100),
			scrollChainNode("inner", "vertical", "auto", image.Rect(0, 0, 100, 100), image.Rect(0, 0, 100, 100))))
	metrics := map[string]render.ScrollMetrics{
		"outer":  {Viewport: image.Rect(0, 0, 100, 100), Maximum: image.Pt(100, 100), EnabledX: true, EnabledY: true},
		"middle": {Viewport: image.Rect(0, 0, 100, 100), Maximum: image.Pt(0, 10), EnabledY: true},
		"inner":  {Viewport: image.Rect(0, 0, 100, 100), Maximum: image.Pt(0, 0), EnabledY: true},
	}
	plan := planScrollChain(root, metrics, map[string]image.Point{}, image.Pt(20, 20), image.Pt(30, 30))
	if plan.Updates["outer"].X != 30 {
		t.Fatalf("disabled-axis residual did not reach outer: %+v", plan.Updates)
	}
	if _, ok := plan.Updates["outer"]; !ok || plan.Updates["middle"].Y != 10 {
		t.Fatalf("contain updates = %+v", plan.Updates)
	}
	if plan.Remaining != (image.Point{}) {
		t.Fatalf("contain residual = %v, want zero after discard", plan.Remaining)
	}
}

func TestPlanScrollChainChoosesDeepestTopmostAndRespectsClips(t *testing.T) {
	nested := scrollChainNode("nested", "vertical", "auto", image.Rect(0, 0, 100, 100), image.Rect(0, 0, 20, 20))
	first := scrollChainNode("first", "vertical", "auto", image.Rect(0, 0, 100, 100), image.Rect(0, 0, 100, 100), nested)
	second := scrollChainNode("second", "vertical", "auto", image.Rect(0, 0, 100, 100), image.Rect(0, 0, 100, 100))
	second.PaintOrder = 20
	root := &semantic.Node{Type: "overlay", Visible: true, InViewport: true, Bounds: semanticRect(0, 0, 100, 100), Clip: semanticRect(0, 0, 100, 100), Children: []*semantic.Node{first, second}}
	metrics := map[string]render.ScrollMetrics{
		"first":  {Viewport: image.Rect(0, 0, 100, 100), Maximum: image.Pt(0, 100), EnabledY: true},
		"nested": {Viewport: image.Rect(0, 0, 100, 100), Maximum: image.Pt(0, 100), EnabledY: true},
		"second": {Viewport: image.Rect(0, 0, 100, 100), Maximum: image.Pt(0, 100), EnabledY: true},
	}
	deep := planScrollChain(root, metrics, map[string]image.Point{}, image.Pt(10, 10), image.Pt(0, 10))
	if deep.Updates["second"].Y != 10 {
		t.Fatalf("topmost scroll update = %+v", deep.Updates)
	}
	if _, ok := deep.Updates["nested"]; ok {
		t.Fatalf("behind nested scroll consumed despite lower paint order: %+v", deep.Updates)
	}
	nested.Clip = semanticRect(0, 0, 5, 5)
	topmost := planScrollChain(root, metrics, map[string]image.Point{}, image.Pt(10, 10), image.Pt(0, 10))
	if topmost.Updates["second"].Y != 10 {
		t.Fatalf("clip-excluded nested scroll did not select topmost sibling: %+v", topmost.Updates)
	}
}

func TestPlanScrollChainUsesFinalPaintOrderBeforeNestingDepth(t *testing.T) {
	deep := scrollChainNode("deep", "vertical", "auto", image.Rect(0, 0, 100, 100), image.Rect(0, 0, 100, 100))
	branch := scrollChainNode("branch", "vertical", "auto", image.Rect(0, 0, 100, 100), image.Rect(0, 0, 100, 100), deep)
	later := scrollChainNode("later", "vertical", "auto", image.Rect(0, 0, 100, 100), image.Rect(0, 0, 100, 100))
	branch.PaintOrder = 1
	deep.PaintOrder = 2
	later.PaintOrder = 20
	root := &semantic.Node{Type: "overlay", Visible: true, InViewport: true, Bounds: semanticRect(0, 0, 100, 100), Clip: semanticRect(0, 0, 100, 100), Children: []*semantic.Node{branch, later}}
	metrics := map[string]render.ScrollMetrics{
		"branch": {Viewport: image.Rect(0, 0, 100, 100), Maximum: image.Pt(0, 100), EnabledY: true},
		"deep":   {Viewport: image.Rect(0, 0, 100, 100), Maximum: image.Pt(0, 100), EnabledY: true},
		"later":  {Viewport: image.Rect(0, 0, 100, 100), Maximum: image.Pt(0, 100), EnabledY: true},
	}
	plan := planScrollChain(root, metrics, map[string]image.Point{}, image.Pt(10, 10), image.Pt(0, 10))
	if plan.Updates["later"].Y != 10 {
		t.Fatalf("later-painted sibling did not win over deeper branch: %+v", plan.Updates)
	}
	if _, ok := plan.Updates["deep"]; ok {
		t.Fatalf("behind nested scroll consumed despite lower paint order: %+v", plan.Updates)
	}

	// A nested scroll whose own final rank is later than an overlapping sibling
	// still wins, and its chain includes the containing branch for residuals.
	deep.PaintOrder = 30
	later.PaintOrder = 20
	plan = planScrollChain(root, metrics, map[string]image.Point{}, image.Pt(10, 10), image.Pt(0, 10))
	if plan.Updates["deep"].Y != 10 {
		t.Fatalf("later-painted nested scroll did not win: %+v", plan.Updates)
	}
}

func TestPlanScrollChainBoundaryWithoutConsumptionIsNoOp(t *testing.T) {
	root := scrollChainNode("feed", "vertical", "auto", image.Rect(0, 0, 100, 100), image.Rect(0, 0, 100, 100))
	metrics := map[string]render.ScrollMetrics{"feed": {Viewport: image.Rect(0, 0, 100, 100), Maximum: image.Pt(0, 100), EnabledY: true}}
	plan := planScrollChain(root, metrics, map[string]image.Point{"feed": image.Pt(0, 100)}, image.Pt(10, 10), image.Pt(0, 20))
	if len(plan.Updates) != 0 || plan.Remaining != (image.Point{Y: 20}) {
		t.Fatalf("boundary no-op plan = %+v", plan)
	}
}

func TestLogicalScrollDeltaPreservesIndependentSubunitSigns(t *testing.T) {
	if got := logicalScrollDelta(0.2, -1.2, 2); got != image.Pt(1, -1) {
		t.Fatalf("logical diagonal conversion = %v, want (1,-1)", got)
	}
	if got := logicalScrollDelta(-4.9, 6.1, 2); got != image.Pt(-2, 3) {
		t.Fatalf("logical diagonal rounding = %v, want (-2,3)", got)
	}
	if got := logicalScrollDelta(3.9, -3.9, 2); got != image.Pt(2, -2) {
		t.Fatalf("logical one-unit boundary rounding = %v, want (2,-2)", got)
	}
}

func scrollChainNode(name, axis, chain string, bounds, clip image.Rectangle, children ...*semantic.Node) *semantic.Node {
	return &semantic.Node{
		Type: "scroll", Name: name, Handle: name, Visible: true, InViewport: true,
		Bounds: semanticRect(bounds.Min.X, bounds.Min.Y, bounds.Dx(), bounds.Dy()),
		Clip:   semanticRect(clip.Min.X, clip.Min.Y, clip.Dx(), clip.Dy()),
		Props:  map[string]any{"axis": axis, "scroll_chain": chain}, Children: children,
	}
}

func semanticRect(x, y, width, height int) *semantic.Rect {
	return &semantic.Rect{X: x, Y: y, Width: width, Height: height}
}
