package semantic

import (
	"image"
	"testing"

	"gora/internal/project"
)

func TestBuildAppendsDerivedScrollbarAfterOwnerContent(t *testing.T) {
	root := &project.Node{
		Handle: "owner", Type: "scroll", Name: "feed", Props: map[string]any{"axis": "vertical"},
		Children: []*project.Node{{Handle: "content", Type: "surface"}},
	}
	geometry := map[string]Geometry{
		"owner":   {Bounds: image.Rect(0, 0, 100, 80), Clip: image.Rect(0, 0, 100, 80), PaintOrder: 0},
		"content": {Bounds: image.Rect(0, 0, 100, 160), Clip: image.Rect(0, 0, 100, 80), PaintOrder: 1},
	}
	tree := Build(root, geometry, Context{Screen: "main"}, []DerivedDescriptor{{
		OwnerHandle: "owner", Axis: "vertical", Policy: "auto", Track: image.Rect(90, 2, 98, 78), Thumb: image.Rect(90, 2, 98, 40),
		Bounds: image.Rect(90, 2, 98, 78), Clip: image.Rect(0, 0, 100, 80), PaintOrder: 2,
		Offset: 0, Maximum: 80, Viewport: 80, Content: 160, Enabled: true,
	}})
	if len(tree.Children) != 2 || tree.Children[1].Role != "scrollbar" {
		t.Fatalf("owner children = %+v", tree.Children)
	}
	bar := tree.Children[1]
	if bar.ID != tree.ID+"/scrollbar/vertical" || bar.Orientation != "vertical" || bar.Value != 0 || bar.Max == nil || *bar.Max != 80 {
		t.Fatalf("scrollbar semantics = %+v", bar)
	}
	if bar.ViewportSize == nil || bar.ViewportSize.Height != 80 || bar.ContentSize == nil || bar.ContentSize.Height != 160 {
		t.Fatalf("scrollbar extents = viewport:%+v content:%+v", bar.ViewportSize, bar.ContentSize)
	}
	if len(bar.Children) != 2 || bar.Children[0].ID != bar.ID+"/track" || bar.Children[1].ID != bar.ID+"/thumb" {
		t.Fatalf("scrollbar parts = %+v", bar.Children)
	}
	if bar.FocusOrder < 0 || bar.Children[1].FocusOrder != -1 || len(bar.Actions) != 0 {
		t.Fatalf("derived scrollbar focus/actions = order:%d thumb:%d actions:%v", bar.FocusOrder, bar.Children[1].FocusOrder, bar.Actions)
	}
}

func TestBuildDerivedScrollbarAxesHaveStableDistinctIDsAndMetrics(t *testing.T) {
	root := &project.Node{
		Handle: "owner", Type: "scroll", Name: "workspace", Props: map[string]any{"axis": "both"},
		Children: []*project.Node{{Handle: "content", Type: "surface"}},
	}
	geometry := map[string]Geometry{
		"owner":   {Bounds: image.Rect(0, 0, 100, 80), Clip: image.Rect(0, 0, 100, 80)},
		"content": {Bounds: image.Rect(0, 0, 180, 140), Clip: image.Rect(0, 0, 100, 80)},
	}
	descriptors := []DerivedDescriptor{
		{OwnerHandle: "owner", Axis: "horizontal", Track: image.Rect(2, 70, 90, 78), Thumb: image.Rect(2, 70, 40, 78), Bounds: image.Rect(2, 70, 90, 78), Clip: image.Rect(0, 0, 100, 80), Offset: 20, Maximum: 80, Viewport: 100, Content: 180, ViewportSize: image.Pt(100, 80), ContentSize: image.Pt(180, 140), Enabled: true},
		{OwnerHandle: "owner", Axis: "vertical", Track: image.Rect(90, 2, 98, 70), Thumb: image.Rect(90, 2, 98, 35), Bounds: image.Rect(90, 2, 98, 70), Clip: image.Rect(0, 0, 100, 80), Offset: 30, Maximum: 60, Viewport: 80, Content: 140, ViewportSize: image.Pt(100, 80), ContentSize: image.Pt(180, 140), Enabled: true},
	}
	first := Build(root, geometry, Context{Screen: "main"}, descriptors)
	second := Build(root, geometry, Context{Screen: "main"}, descriptors)
	var bars []*Node
	for _, node := range Flatten(first) {
		if node.Role == "scrollbar" {
			bars = append(bars, node)
		}
	}
	var repeat []*Node
	for _, node := range Flatten(second) {
		if node.Role == "scrollbar" {
			repeat = append(repeat, node)
		}
	}
	if len(bars) != 2 || len(repeat) != 2 || bars[0].ID == bars[1].ID || bars[0].ID != repeat[0].ID || bars[1].ID != repeat[1].ID {
		t.Fatalf("derived IDs are not distinct/stable: first=%v second=%v", nodeIDs(bars), nodeIDs(repeat))
	}
	for _, bar := range bars {
		if bar.ViewportSize == nil || bar.ViewportSize.Width != 100 || bar.ViewportSize.Height != 80 || bar.ContentSize == nil || bar.ContentSize.Width != 180 || bar.ContentSize.Height != 140 {
			t.Fatalf("complete derived metrics missing: %+v", bar)
		}
	}
}

func nodeIDs(nodes []*Node) []string {
	ids := make([]string, 0, len(nodes))
	for _, node := range nodes {
		ids = append(ids, node.ID)
	}
	return ids
}
