package studio

import (
	"image"
	"math"

	"gora/internal/render"
	"gora/internal/semantic"
)

// scrollChainPlan is the renderer-neutral result of routing one logical wheel
// event through the deepest scrollport under its document point.
type scrollChainPlan struct {
	Updates   map[string]image.Point
	Remaining image.Point
}

// planScrollChain routes each enabled axis independently through the selected
// scrollport's inner-to-outer ancestor chain. Published renderer metrics are
// authoritative for axis enablement and maximum extents.
func planScrollChain(root *semantic.Node, metrics map[string]render.ScrollMetrics, offsets map[string]image.Point, point, delta image.Point) scrollChainPlan {
	plan := scrollChainPlan{Updates: make(map[string]image.Point), Remaining: delta}
	chain := scrollChainAt(root, point)
	if len(chain) == 0 {
		return plan
	}
	working := make(map[string]image.Point, len(offsets))
	for key, offset := range offsets {
		working[key] = offset
	}
	remainingX, remainingY := delta.X, delta.Y
	for index := len(chain) - 1; index >= 0; index-- {
		node := chain[index]
		metric, ok := metrics[node.Handle]
		if !ok {
			continue
		}
		key := semanticScrollKey(node)
		previous := working[key]
		current := clampScrollOffsetForMetric(previous, metric)
		consumedX := 0
		consumedY := 0
		if metric.EnabledX {
			current.X, consumedX = consumeScrollAxis(current.X, metric.Maximum.X, remainingX)
		}
		if metric.EnabledY {
			current.Y, consumedY = consumeScrollAxis(current.Y, metric.Maximum.Y, remainingY)
		}
		if current != previous {
			working[key] = current
			plan.Updates[key] = current
		}
		remainingX -= consumedX
		remainingY -= consumedY
		if scrollChainContain(node) {
			if metric.EnabledX {
				remainingX = 0
			}
			if metric.EnabledY {
				remainingY = 0
			}
		}
	}
	plan.Remaining = image.Pt(remainingX, remainingY)
	return plan
}

func scrollChainAt(root *semantic.Node, point image.Point) []*semantic.Node {
	var best []*semantic.Node
	bestPaintOrder := -1
	bestFound := false
	visitOrder := 0
	bestVisitOrder := -1
	var walk func(*semantic.Node, []*semantic.Node)
	walk = func(node *semantic.Node, ancestors []*semantic.Node) {
		if node == nil || !node.Visible || node.Bounds == nil || node.Clip == nil {
			return
		}
		bounds := node.Bounds.ImageRectangle().Intersect(node.Clip.ImageRectangle())
		if bounds.Empty() || !point.In(bounds) {
			return
		}
		chain := ancestors
		if node.Type == "scroll" {
			chain = append(append([]*semantic.Node(nil), ancestors...), node)
			visitOrder++
			if !bestFound || node.PaintOrder > bestPaintOrder || node.PaintOrder == bestPaintOrder && (len(chain) > len(best) || len(chain) == len(best) && visitOrder > bestVisitOrder) {
				best = append([]*semantic.Node(nil), chain...)
				bestPaintOrder = node.PaintOrder
				bestVisitOrder = visitOrder
				bestFound = true
			}
		}
		for _, child := range node.Children {
			walk(child, chain)
		}
	}
	walk(root, nil)
	return best
}

func scrollChainContain(node *semantic.Node) bool {
	if node == nil {
		return false
	}
	chain, _ := node.Props["scroll_chain"].(string)
	return chain == "contain"
}

func consumeScrollAxis(current, maximum, delta int) (next, consumed int) {
	maximum = max(0, maximum)
	current = min(max(0, current), maximum)
	if delta > 0 {
		consumed = min(delta, maximum-current)
	} else if delta < 0 {
		consumed = max(delta, -current)
	}
	return current + consumed, consumed
}

func clampScrollOffsetForMetric(offset image.Point, metric render.ScrollMetrics) image.Point {
	if metric.EnabledX {
		offset.X = min(max(0, offset.X), max(0, metric.Maximum.X))
	} else {
		offset.X = 0
	}
	if metric.EnabledY {
		offset.Y = min(max(0, offset.Y), max(0, metric.Maximum.Y))
	} else {
		offset.Y = 0
	}
	return offset
}

// logicalScrollDelta converts physical wheel movement into independent
// logical integer components. A nonzero sub-unit component retains its sign.
func logicalScrollDelta(x, y, scale float32) image.Point {
	if scale <= 0 {
		scale = 1
	}
	return image.Pt(logicalScrollComponent(x, scale), logicalScrollComponent(y, scale))
}

func logicalScrollComponent(value, scale float32) int {
	logical := int(math.Round(float64(value / scale)))
	if logical == 0 && value != 0 {
		if value < 0 {
			return -1
		}
		return 1
	}
	return logical
}
