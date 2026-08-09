package semantic

import (
	"image"
	"testing"
)

func TestTopmostAtUsesFinalPaintOrderAndExcludesHiddenOrClippedNodes(t *testing.T) {
	root := &Node{Handle: "root", Bounds: &Rect{X: 0, Y: 0, Width: 100, Height: 100}, Clip: &Rect{X: 0, Y: 0, Width: 100, Height: 100}, Visible: true}
	behind := &Node{Handle: "behind", Role: "button", Bounds: &Rect{X: 10, Y: 10, Width: 50, Height: 50}, Clip: &Rect{X: 0, Y: 0, Width: 100, Height: 100}, Visible: true, Enabled: true, PaintOrder: 4}
	later := &Node{Handle: "later", Role: "button", Bounds: &Rect{X: 20, Y: 20, Width: 50, Height: 50}, Clip: &Rect{X: 0, Y: 0, Width: 100, Height: 100}, Visible: true, Enabled: true, PaintOrder: 9}
	hidden := &Node{Handle: "hidden", Role: "button", Bounds: &Rect{X: 20, Y: 20, Width: 50, Height: 50}, Clip: &Rect{X: 0, Y: 0, Width: 100, Height: 100}, Visible: false, Enabled: true, PaintOrder: 20}
	offscreen := &Node{Handle: "offscreen", Role: "button", Bounds: &Rect{X: 20, Y: 20, Width: 50, Height: 50}, Clip: &Rect{X: 100, Y: 100, Width: 1, Height: 1}, Visible: true, Enabled: true, PaintOrder: 30}
	root.Children = []*Node{behind, later, hidden, offscreen}
	if got := TopmostAt(root, image.Pt(30, 30), func(node *Node) bool { return node.Role == "button" && node.Enabled }); got != later {
		t.Fatalf("topmost hit = %+v, want later rank=%d", got, later.PaintOrder)
	}
	if got := TopmostAt(root, image.Pt(90, 90), func(node *Node) bool { return node.Role == "button" && node.Enabled }); got != nil {
		t.Fatalf("outside hit = %+v, want nil", got)
	}
}

func TestTopmostAtUsesSourceOrderForEqualPaintRanks(t *testing.T) {
	root := &Node{Handle: "root", Visible: true}
	first := &Node{Handle: "first", Bounds: &Rect{X: 0, Y: 0, Width: 20, Height: 20}, Clip: &Rect{X: 0, Y: 0, Width: 20, Height: 20}, Visible: true, Enabled: true, PaintOrder: 3}
	second := &Node{Handle: "second", Bounds: &Rect{X: 0, Y: 0, Width: 20, Height: 20}, Clip: &Rect{X: 0, Y: 0, Width: 20, Height: 20}, Visible: true, Enabled: true, PaintOrder: 3}
	root.Children = []*Node{first, second}
	if got := TopmostAt(root, image.Pt(4, 4), nil); got != second {
		t.Fatalf("equal-rank hit = %+v, want later source node", got)
	}
}
