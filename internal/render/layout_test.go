package render

import (
	"image"
	"reflect"
	"testing"

	"gora/internal/project"
)

func TestPlanScrollPreservesOneAxisExtents(t *testing.T) {
	tests := []struct {
		name     string
		axis     string
		viewport image.Point
		child    image.Point
		wantSize image.Point
		wantMax  image.Point
	}{
		{name: "default vertical", viewport: image.Pt(100, 80), child: image.Pt(100, 240), wantSize: image.Pt(100, 240), wantMax: image.Pt(0, 160)},
		{name: "vertical", axis: "vertical", viewport: image.Pt(100, 80), child: image.Pt(100, 240), wantSize: image.Pt(100, 240), wantMax: image.Pt(0, 160)},
		{name: "horizontal", axis: "horizontal", viewport: image.Pt(100, 80), child: image.Pt(260, 80), wantSize: image.Pt(260, 80), wantMax: image.Pt(160, 0)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			node := &project.Node{Type: "scroll", Props: map[string]any{}, Children: []*project.Node{{Type: "surface", Props: map[string]any{
				"width": int64(test.child.X), "height": int64(test.child.Y),
			}}}}
			if test.axis != "" {
				node.Props["axis"] = test.axis
			}
			plan := planScroll(node, image.Rect(0, 0, test.viewport.X, test.viewport.Y), testLayoutMeasure)
			if plan.Viewport != image.Rect(0, 0, test.viewport.X, test.viewport.Y) {
				t.Fatalf("viewport = %v", plan.Viewport)
			}
			if plan.ContentSize != test.wantSize || plan.Maximum != test.wantMax {
				t.Fatalf("size/max = %v/%v, want %v/%v", plan.ContentSize, plan.Maximum, test.wantSize, test.wantMax)
			}
			if plan.ContentRect != image.Rect(0, 0, test.wantSize.X, test.wantSize.Y) || plan.Clip != plan.Viewport {
				t.Fatalf("content/clip = %v/%v", plan.ContentRect, plan.Clip)
			}
		})
	}
}

func TestPlanScrollBothAxisOverflowCombinations(t *testing.T) {
	tests := []struct {
		name     string
		child    image.Point
		wantSize image.Point
		wantMax  image.Point
	}{
		{name: "smaller", child: image.Pt(40, 30), wantSize: image.Pt(100, 80), wantMax: image.Point{}},
		{name: "horizontal only", child: image.Pt(140, 30), wantSize: image.Pt(140, 80), wantMax: image.Pt(40, 0)},
		{name: "vertical only", child: image.Pt(40, 120), wantSize: image.Pt(100, 120), wantMax: image.Pt(0, 40)},
		{name: "both", child: image.Pt(140, 120), wantSize: image.Pt(140, 120), wantMax: image.Pt(40, 40)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			node := testScrollNode("both", test.child)
			plan := planScroll(node, image.Rect(0, 0, 100, 80), testLayoutMeasure)
			if plan.ContentSize != test.wantSize || plan.Maximum != test.wantMax {
				t.Fatalf("size/max = %v/%v, want %v/%v", plan.ContentSize, plan.Maximum, test.wantSize, test.wantMax)
			}
			if !plan.EnabledX || !plan.EnabledY {
				t.Fatalf("enabled axes = %v/%v, want true/true", plan.EnabledX, plan.EnabledY)
			}
		})
	}
}

func TestPlanScrollPreservesPaddingAndIgnoresOffsets(t *testing.T) {
	node := testScrollNode("both", image.Pt(140, 110))
	node.Props["padding"] = map[string]any{"top": int64(5), "right": int64(7), "bottom": int64(3), "left": int64(9)}
	node.Props["scroll_offset_x"] = int64(999)
	node.Props["scroll_offset_y"] = int64(999)
	plan := planScroll(node, image.Rect(0, 0, 100, 80), testLayoutMeasure)
	if plan.Viewport != image.Rect(9, 5, 93, 77) {
		t.Fatalf("padded viewport = %v", plan.Viewport)
	}
	if plan.ContentSize != image.Pt(140, 110) || plan.Maximum != image.Pt(56, 38) {
		t.Fatalf("padded size/max = %v/%v", plan.ContentSize, plan.Maximum)
	}
	if plan.ContentRect != image.Rect(9, 5, 149, 115) || plan.Clip != plan.Viewport {
		t.Fatalf("padded content/clip = %v/%v", plan.ContentRect, plan.Clip)
	}
}

func TestPlanScrollResolvesFinitePercentagesBeforeUnboundedMeasure(t *testing.T) {
	node := testScrollNode("both", image.Point{})
	node.Children[0].Props = map[string]any{
		"width":  map[string]any{"percent": float64(125)},
		"height": map[string]any{"percent": float64(50)},
	}
	plan := planScroll(node, image.Rect(0, 0, 100, 80), testLayoutMeasure)
	if plan.ContentSize != image.Pt(125, 80) || plan.Maximum != image.Pt(25, 0) {
		t.Fatalf("percentage size/max = %v/%v, want (125,80)/(25,0)", plan.ContentSize, plan.Maximum)
	}
}

func TestPlanScrollFillAndAutoUseViewportMinimum(t *testing.T) {
	node := testScrollNode("both", image.Point{})
	node.Children[0].Props = map[string]any{
		"width": "fill", "height": "auto",
		"intrinsic_width": int64(30), "intrinsic_height": int64(40),
	}
	plan := planScroll(node, image.Rect(0, 0, 100, 80), testLayoutMeasure)
	if plan.ContentSize != image.Pt(100, 80) || plan.Maximum != (image.Point{}) {
		t.Fatalf("fill/auto size/max = %v/%v, want (100,80)/(0,0)", plan.ContentSize, plan.Maximum)
	}
}

func TestPlanScrollEnabledAxesHonorFixedMinMaxAndAspectRatio(t *testing.T) {
	tests := []struct {
		name     string
		props    map[string]any
		wantSize image.Point
		wantMax  image.Point
	}{
		{
			name: "fixed",
			props: map[string]any{
				"width": int64(125), "height": int64(95),
			},
			wantSize: image.Pt(125, 95), wantMax: image.Pt(25, 15),
		},
		{
			name: "minimum",
			props: map[string]any{
				"intrinsic_width": int64(40), "intrinsic_height": int64(30),
				"min_width": int64(130), "min_height": int64(100),
			},
			wantSize: image.Pt(130, 100), wantMax: image.Pt(30, 20),
		},
		{
			name: "maximum",
			props: map[string]any{
				"intrinsic_width": int64(220), "intrinsic_height": int64(170),
				"max_width": int64(120), "max_height": int64(90),
			},
			wantSize: image.Pt(120, 90), wantMax: image.Pt(20, 10),
		},
		{
			name: "aspect ratio from width",
			props: map[string]any{
				"width":        int64(120),
				"aspect_ratio": map[string]any{"width": int64(4), "height": int64(3)},
			},
			wantSize: image.Pt(120, 90), wantMax: image.Pt(20, 10),
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			node := testScrollNode("both", image.Point{})
			node.Children[0].Props = test.props
			plan := planScroll(node, image.Rect(0, 0, 100, 80), testLayoutMeasure)
			if plan.ContentSize != test.wantSize || plan.Maximum != test.wantMax {
				t.Fatalf("size/max = %v/%v, want %v/%v", plan.ContentSize, plan.Maximum, test.wantSize, test.wantMax)
			}
		})
	}
}

func TestPlanScrollRoundsPercentagesAtOneUnitBoundary(t *testing.T) {
	node := testScrollNode("both", image.Point{})
	node.Children[0].Props = map[string]any{
		"width":  map[string]any{"percent": float64(150)},
		"height": map[string]any{"percent": float64(150)},
	}
	plan := planScroll(node, image.Rect(0, 0, 3, 3), testLayoutMeasure)
	if plan.ContentSize != image.Pt(5, 5) || plan.Maximum != image.Pt(2, 2) {
		t.Fatalf("rounded percentage size/max = %v/%v, want (5,5)/(2,2)", plan.ContentSize, plan.Maximum)
	}
}

func TestPlanScrollNestedBothAxisUsesSharedIntrinsicPath(t *testing.T) {
	node := &project.Node{
		Type: "scroll", Props: map[string]any{"axis": "both"}, Children: []*project.Node{{
			Type: "scroll", Props: map[string]any{
				"axis":    "both",
				"padding": map[string]any{"top": int64(5), "right": int64(7), "bottom": int64(3), "left": int64(9)},
			}, Children: []*project.Node{{
				Type: "surface", Props: map[string]any{
					"width": int64(160), "height": int64(120),
				},
			}},
		}},
	}
	measure := func(node *project.Node, limit image.Point) image.Point {
		return measureIntrinsic(node, limit, cpuIntrinsicLeaf)
	}
	plan := planScroll(node, image.Rect(0, 0, 100, 80), measure)
	if plan.ContentSize != image.Pt(176, 128) || plan.Maximum != image.Pt(76, 48) {
		t.Fatalf("nested size/max = %v/%v, want (176,128)/(76,48)", plan.ContentSize, plan.Maximum)
	}
}

func TestMaterializeScrollDimensionsOnlyResolvesContentRoot(t *testing.T) {
	nested := &project.Node{Props: map[string]any{
		"width":     map[string]any{"percent": float64(75)},
		"height":    "fill",
		"min_width": map[string]any{"percent": float64(10)},
	}}
	root := &project.Node{
		Props: map[string]any{
			"width":      map[string]any{"percent": float64(50)},
			"height":     "fill",
			"min_width":  map[string]any{"percent": float64(25)},
			"max_width":  map[string]any{"percent": float64(125)},
			"min_height": map[string]any{"percent": float64(20)},
			"max_height": map[string]any{"percent": float64(150)},
		},
		Place:    map[string]any{"basis": map[string]any{"percent": float64(40)}},
		Children: []*project.Node{nested},
	}
	materialized := materializeScrollDimensions(root, image.Pt(100, 80))
	if got, want := materialized.Props, map[string]any{
		"width":      int64(50),
		"height":     int64(80),
		"min_width":  int64(25),
		"max_width":  int64(125),
		"min_height": int64(16),
		"max_height": int64(120),
	}; !reflect.DeepEqual(got, want) {
		t.Fatalf("root props = %#v, want %#v", got, want)
	}
	if got, want := materialized.Place["basis"], int64(40); got != want {
		t.Fatalf("root basis = %#v, want %#v", got, want)
	}
	if got := materialized.Children[0].Props; !reflect.DeepEqual(got, nested.Props) {
		t.Fatalf("nested props = %#v, want authored %#v", got, nested.Props)
	}
}

func TestMeasureIntrinsicBothAxisUnboundsBothContentAxes(t *testing.T) {
	node := &project.Node{
		Type: "scroll", Props: map[string]any{"axis": "both"}, Children: []*project.Node{{
			Type: "leaf", Props: map[string]any{},
		}},
	}
	var gotLimit image.Point
	leaf := func(_ *project.Node, limit image.Point) image.Point {
		gotLimit = limit
		return image.Point{}
	}
	measureIntrinsic(node, image.Pt(100, 80), leaf)
	if gotLimit != image.Pt(scrollUnboundedLimit, scrollUnboundedLimit) {
		t.Fatalf("both-axis child limit = %v, want %v", gotLimit, image.Pt(scrollUnboundedLimit, scrollUnboundedLimit))
	}
}

func testScrollNode(axis string, child image.Point) *project.Node {
	return &project.Node{
		Type: "scroll", Props: map[string]any{"axis": axis}, Children: []*project.Node{{Type: "surface", Props: map[string]any{
			"width": int64(child.X), "height": int64(child.Y),
		}}},
	}
}

func testLayoutMeasure(node *project.Node, limit image.Point) image.Point {
	if node == nil || node.Hidden {
		return image.Point{}
	}
	if node.Type == "scroll" {
		return image.Point{}
	}
	leaf := func(key string) int {
		return int(number(node.Props[key], 0))
	}
	preferred := image.Pt(leaf("intrinsic_width"), leaf("intrinsic_height"))
	if width, ok := resolveDimension(node.Props["width"], limit.X); ok {
		preferred.X = width
	}
	if height, ok := resolveDimension(node.Props["height"], limit.Y); ok {
		preferred.Y = height
	}
	return constrainIntrinsic(node, preferred, limit)
}
