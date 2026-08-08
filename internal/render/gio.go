package render

import (
	"image"
	"image/color"
	"math"
	"os"
	"path/filepath"
	"sync"
	"time"

	"gioui.org/f32"
	"gioui.org/font"
	"gioui.org/font/gofont"
	gioopentype "gioui.org/font/opentype"
	"gioui.org/gpu/headless"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/text"
	"gioui.org/unit"
	"gioui.org/widget"
	"gioui.org/widget/material"

	"gora/internal/project"
	"gora/internal/semantic"
)

// GioResult describes the nodes laid out into a native Gio frame.
type GioResult struct {
	Bounds   map[string]image.Rectangle
	Geometry map[string]semantic.Geometry
	Layout   map[string]LayoutRecord
	Scroll   map[string]ScrollMetrics
	Derived  []semantic.DerivedDescriptor
	Tree     *semantic.Node
}

type gioRenderer struct {
	gtx           layout.Context
	theme         *material.Theme
	state         State
	result        GioResult
	opacity       float64
	scene         *gioScene
	scrolls       []sceneScroll
	stickies      []sceneSticky
	paintOrder    int
	geometryOrder []string
	viewport      image.Rectangle
	topLayers     []topLayer
	layoutMeta    layoutMeta
	sourceOrder   int
	sourceRanks   map[string]int
	rootHandle    string
	paintOwner    string
	deferred      map[string]map[string]positionedPlacement
	painted       map[string]bool
}

type nativeFont struct {
	modified time.Time
	size     int64
	shaper   *text.Shaper
	typeface font.Typeface
}

type shadowLayer struct {
	bounds image.Rectangle
	color  color.NRGBA
	radius int
}

var nativeFontCache = struct {
	sync.Mutex
	fonts map[nativeFontKey]nativeFont
}{fonts: make(map[nativeFontKey]nativeFont)}

type nativeFontKey struct {
	theme *material.Theme
	path  string
}

// LayoutGio lays out and paints a resolved document directly into Gio operations.
// Bounds and viewport are expressed in document logical units.
func LayoutGio(gtx layout.Context, theme *material.Theme, root *project.Node, viewport image.Point, state State) GioResult {
	if theme == nil {
		theme = material.NewTheme()
	}
	result := GioResult{Bounds: make(map[string]image.Rectangle), Geometry: make(map[string]semantic.Geometry), Layout: make(map[string]LayoutRecord), Scroll: make(map[string]ScrollMetrics)}
	if root == nil || viewport.X <= 0 || viewport.Y <= 0 {
		return result
	}
	r := gioRenderer{gtx: gtx, theme: theme, state: state, result: result, opacity: 1, viewport: image.Rectangle{Max: viewport}, layoutMeta: layoutMeta{parentInner: image.Rectangle{Max: viewport}}, sourceRanks: sourceOrderRanks(root), deferred: make(map[string]map[string]positionedPlacement), painted: make(map[string]bool)}
	r.rootHandle = root.Handle
	bounds := image.Rectangle{Max: viewport}
	r.layout(root, bounds, bounds)
	for index := 0; index < len(r.topLayers); index++ {
		layer := r.topLayers[index]
		r.layoutFinal(layer.node, layer.bounds, r.viewport)
	}
	r.result.Tree = semantic.Build(root, r.result.Geometry, semanticContext(state), r.result.Derived)
	return r.result
}

func captureGio(root *project.Node, viewport image.Point, state State, scale int) (*image.RGBA, error) {
	size := image.Pt(viewport.X*scale, viewport.Y*scale)
	window, err := headless.NewWindow(size.X, size.Y)
	if err != nil {
		return nil, err
	}
	defer window.Release()
	var operations op.Ops
	gtx := layout.Context{
		Ops:         &operations,
		Now:         time.Now(),
		Metric:      unit.Metric{PxPerDp: float32(scale), PxPerSp: float32(scale)},
		Constraints: layout.Exact(size),
	}
	theme := material.NewTheme()
	defer releaseNativeFonts(theme)
	LayoutGio(gtx, theme, root, viewport, state)
	if err := window.Frame(&operations); err != nil {
		return nil, err
	}
	captured := image.NewRGBA(image.Rectangle{Max: size})
	if err := window.Screenshot(captured); err != nil {
		return nil, err
	}
	return captured, nil
}

func (r *gioRenderer) layout(node *project.Node, bounds, currentClip image.Rectangle) {
	r.layoutNode(node, bounds, currentClip, false)
}

func (r *gioRenderer) layoutFinal(node *project.Node, bounds, currentClip image.Rectangle) {
	r.layoutNode(node, bounds, currentClip, true)
}

func (r *gioRenderer) layoutChild(node *project.Node, bounds, currentClip, parentInner image.Rectangle, ancestors []string, normal image.Rectangle, final bool) {
	if !final {
		normal = applySize(node, normal)
	}
	previous := r.layoutMeta
	r.layoutMeta = layoutMeta{
		parentInner:         parentInner,
		scrollAncestors:     append([]string(nil), ancestors...),
		scrollports:         append([]stickyScrollport(nil), previous.scrollports...),
		ancestorTranslation: previous.ancestorTranslation,
		normal:              normal,
		hasNormal:           true,
	}
	r.layoutNode(node, bounds, currentClip, final)
	r.layoutMeta = previous
}

func (r *gioRenderer) layoutChildInScroll(node *project.Node, bounds, currentClip, parentInner image.Rectangle, ancestors []string, normal image.Rectangle, final bool, scrollports []stickyScrollport, translation image.Point) {
	previous := r.layoutMeta
	r.layoutMeta.scrollports = append([]stickyScrollport(nil), scrollports...)
	r.layoutMeta.ancestorTranslation = translation
	r.layoutChild(node, bounds, currentClip, parentInner, ancestors, normal, final)
	r.layoutMeta = previous
}

func (r *gioRenderer) deferPositioned(p positionedPlacement) {
	if p.node == nil || p.owner == "" {
		return
	}
	byOwner := r.deferred[p.owner]
	if byOwner == nil {
		byOwner = make(map[string]positionedPlacement)
		r.deferred[p.owner] = byOwner
	}
	byOwner[p.node.Handle] = p
}

func (r *gioRenderer) positionedPlacement(node *project.Node) (positionedPlacement, bool) {
	if node == nil {
		return positionedPlacement{}, false
	}
	p, ok := r.deferred[r.paintOwner][node.Handle]
	return p, ok
}

func (r *gioRenderer) deferPositionedChild(node *project.Node, bounds, clip, parentInner image.Rectangle, ancestors []string, normal image.Rectangle, final bool) {
	if node == nil || !isPositionedContext(node) {
		return
	}
	r.deferPositioned(positionedPlacement{node: node, bounds: bounds, clip: clip, parentInner: parentInner, ancestors: append([]string(nil), ancestors...), scrollports: append([]stickyScrollport(nil), r.layoutMeta.scrollports...), translation: r.layoutMeta.ancestorTranslation, scrolls: append([]sceneScroll(nil), r.scrolls...), stickies: append([]sceneSticky(nil), r.stickies...), normal: normal, final: final, owner: r.paintOwner})
}

func (r *gioRenderer) paintPositioned(p positionedPlacement) {
	if p.node == nil || r.painted[p.node.Handle] {
		return
	}
	r.painted[p.node.Handle] = true
	if isFixedPositioned(p.node) {
		r.layoutFixedNode(p.node)
		return
	}
	previous := r.layoutMeta
	previousScrolls := r.scrolls
	previousStickies := r.stickies
	r.layoutMeta.scrollports = append([]stickyScrollport(nil), p.scrollports...)
	r.layoutMeta.ancestorTranslation = p.translation
	r.scrolls = append([]sceneScroll(nil), p.scrolls...)
	r.stickies = append([]sceneSticky(nil), p.stickies...)
	r.layoutChild(p.node, p.bounds, p.clip, p.parentInner, p.ancestors, p.normal, p.final)
	r.layoutMeta = previous
	r.scrolls = previousScrolls
	r.stickies = previousStickies
}

func (r *gioRenderer) paintPositionedChild(node *project.Node, bounds, clip, parentInner image.Rectangle, ancestors []string, normal image.Rectangle, final bool) {
	if node == nil || !isPositionedContext(node) {
		return
	}
	if placement, ok := r.positionedPlacement(node); ok {
		r.paintPositioned(placement)
		return
	}
	r.paintPositioned(positionedPlacement{node: node, bounds: bounds, clip: clip, parentInner: parentInner, ancestors: append([]string(nil), ancestors...), scrollports: append([]stickyScrollport(nil), r.layoutMeta.scrollports...), translation: r.layoutMeta.ancestorTranslation, scrolls: append([]sceneScroll(nil), r.scrolls...), stickies: append([]sceneSticky(nil), r.stickies...), normal: normal, final: final, owner: r.paintOwner})
}

func (r *gioRenderer) paintPromotedPositioned(node *project.Node) {
	if node == nil || node.Handle != r.paintOwner {
		return
	}
	for _, child := range paintContextChildren(node.Children) {
		if !isPositionedContext(child) {
			continue
		}
		if placement, ok := r.positionedPlacement(child); ok {
			r.paintPositioned(placement)
		}
	}
}

// layoutFixedNode lays out a fixed subtree against the logical view viewport.
// Its positioning context is reset so ancestor scrollports, sticky deltas, and
// clips cannot affect the fixed subtree. Fixed descendants get the same fresh
// context when they are encountered recursively.
func (r *gioRenderer) layoutFixedNode(node *project.Node) {
	if node == nil || node.Hidden {
		return
	}
	planned, ok := planFixedViewport(node, r.viewport, fixedIntrinsicSize(node, r.viewport.Size(), r.intrinsicLeafSize))
	if !ok || planned.Empty() {
		return
	}
	previousMeta := r.layoutMeta
	previousScrolls := r.scrolls
	previousStickies := r.stickies
	previousOwner := r.paintOwner
	r.layoutMeta = layoutMeta{parentInner: r.viewport, normal: planned, hasNormal: true}
	r.scrolls = nil
	r.stickies = nil
	clone := *node
	clone.Place = cloneMap(node.Place)
	if clone.Place == nil {
		clone.Place = make(map[string]any)
	}
	clone.Place["position"] = "flow"
	r.paintOwner = node.Handle
	r.layoutNode(&clone, planned, r.viewport, true)
	r.paintOwner = previousOwner
	r.layoutMeta = previousMeta
	r.scrolls = previousScrolls
	r.stickies = previousStickies
}

func (r *gioRenderer) layoutNode(node *project.Node, bounds, currentClip image.Rectangle, final bool) {
	if node == nil {
		return
	}
	if isFixedPositioned(node) {
		r.layoutFixedNode(node)
		return
	}
	if node.Hidden || bounds.Empty() {
		return
	}
	previousOwner := r.paintOwner
	isContext := node.Handle == r.rootHandle || node.Handle == r.paintOwner || isPositionedContext(node)
	if isContext {
		r.paintOwner = node.Handle
	}
	defer func() {
		if isContext {
			r.paintPromotedPositioned(node)
		}
		r.paintOwner = previousOwner
	}()
	normalBounds := bounds
	if r.layoutMeta.hasNormal {
		normalBounds = r.layoutMeta.normal
	}
	if !final {
		bounds = applySize(node, bounds)
	}
	var stickyDelta image.Point
	if isStickyPositioned(node) {
		var planned image.Rectangle
		planned, stickyDelta = planStickyRect(node, bounds, stickyParentInner(r.layoutMeta), stickyViewport(r.layoutMeta, r.viewport))
		bounds = planned
		r.layoutMeta.ancestorTranslation = r.layoutMeta.ancestorTranslation.Add(stickyDelta)
	}
	node = interactiveNodeForState(node, r.state)
	if node.Type == "field_box" {
		node, _ = fieldNodeWithViewport(node, bounds)
	}
	nodeClip := currentClip.Intersect(bounds)
	previousOpacity := r.opacity
	r.opacity *= clamp(number(node.Props["opacity"], 1), 0, 1)
	defer func() { r.opacity = previousOpacity }()

	r.result.Bounds[node.Handle] = bounds
	record := r.result.Layout[node.Handle]
	if record.Normal.Empty() || !final {
		record.Normal = normalBounds
	}
	record.Final = bounds
	record.ParentInner = r.layoutMeta.parentInner
	record.ScrollAncestors = append([]string(nil), r.layoutMeta.scrollAncestors...)
	if rank, ok := r.sourceRanks[node.Handle]; ok {
		record.SourceOrder = rank
	} else {
		record.SourceOrder = r.sourceOrder
	}
	record.ContainingViewport = r.viewport
	r.result.Layout[node.Handle] = record
	r.sourceOrder++
	stickyDepth := len(r.stickies)
	if r.scene != nil && isStickyPositioned(node) {
		r.stickies = append(r.stickies, sceneSticky{
			node: node, record: record, ancestorCount: len(r.scrolls), delta: stickyDelta,
		})
		defer func() { r.stickies = r.stickies[:stickyDepth] }()
	}
	r.result.Geometry[node.Handle] = semantic.Geometry{
		Bounds: bounds, Clip: nodeClip, PaintOrder: r.paintOrder, Props: cloneMap(node.Props),
	}
	r.geometryOrder = append(r.geometryOrder, node.Handle)
	r.paintOrder++
	if r.scene != nil {
		r.scene.geometries = append(r.scene.geometries, sceneGeometry{
			handle: node.Handle, geometry: r.result.Geometry[node.Handle], paintOrder: r.result.Geometry[node.Handle].PaintOrder, layout: r.result.Layout[node.Handle], node: node,
			scrolls:  append([]sceneScroll(nil), r.scrolls...),
			stickies: append([]sceneSticky(nil), r.stickies...),
		})
	}

	switch node.Type {
	case "_viewport":
		r.recordPaint(func() {
			r.paintBackground(bounds, currentClip, node.Props["background"], 0)
		})
		children := node.Children
		if node.Handle == r.paintOwner {
			children = paintContextChildren(node.Children)
		}
		if len(children) == 1 || node.Handle == r.paintOwner {
			parentNormal := normalLayoutBounds(r.layoutMeta, bounds)
			for _, child := range children {
				if isPositionedContext(child) {
					if placement, ok := r.positionedPlacement(child); ok {
						r.paintPositioned(placement)
					} else if node.Handle == r.paintOwner {
						r.paintPositionedChild(child, bounds, nodeClip, parentNormal, r.layoutMeta.scrollAncestors, parentNormal, false)
					} else {
						r.deferPositionedChild(child, bounds, nodeClip, parentNormal, r.layoutMeta.scrollAncestors, parentNormal, false)
					}
					continue
				}
				r.layoutChild(child, bounds, nodeClip, parentNormal, r.layoutMeta.scrollAncestors, parentNormal, false)
			}
		}
	case "form", "surface", "toggle", "checkbox", "radio", "tab", "tab_panel", "option", "select_trigger", "field_support",
		"slider_track", "slider_fill", "slider_thumb", "stepper_decrement", "stepper_value", "stepper_increment":
		r.recordPaint(func() {
			r.paintSurfaceGio(node, bounds, currentClip)
		})
		children := node.Children
		if node.Handle == r.paintOwner {
			children = paintContextChildren(node.Children)
		}
		if len(children) == 1 || node.Handle == r.paintOwner {
			inner := inset(bounds, insets(node.Props["padding"]))
			normalInner := inset(normalLayoutBounds(r.layoutMeta, bounds), insets(node.Props["padding"]))
			childClip := chooseClip(node, currentClip, bounds)
			for _, child := range children {
				if isPositionedContext(child) {
					if placement, ok := r.positionedPlacement(child); ok {
						r.paintPositioned(placement)
					} else if node.Handle == r.paintOwner {
						r.paintPositionedChild(child, inner, childClip, normalInner, r.layoutMeta.scrollAncestors, normalInner, false)
					} else {
						r.deferPositionedChild(child, inner, childClip, normalInner, r.layoutMeta.scrollAncestors, normalInner, false)
					}
					continue
				}
				childBounds := r.surfaceChildBounds(child, inner)
				normalChildBounds := r.surfaceChildBounds(child, normalInner)
				r.layoutChild(child, childBounds, childClip, normalInner, r.layoutMeta.scrollAncestors, normalChildBounds, false)
			}
		}
	case "field_box":
		if r.scene == nil {
			r.paintFieldBoxGio(node, bounds, currentClip)
		} else {
			r.scene.items = append(r.scene.items, sceneItem{
				field:    &sceneField{node: node, bounds: bounds, clip: currentClip, opacity: r.opacity},
				scrolls:  append([]sceneScroll(nil), r.scrolls...),
				stickies: append([]sceneSticky(nil), r.stickies...),
			})
		}
	case "text_field", "text_area":
		labelBounds, contentBounds := fieldContentBounds(node, bounds)
		if !labelBounds.Empty() {
			label := *node
			label.Type = "text"
			label.Props = cloneMap(node.Props)
			label.Props["text"] = stringValue(node.Props["label"], "")
			label.Props["size"] = float64(14)
			label.Props["weight"] = float64(600)
			label.Props["color"] = "#39443D"
			r.recordPaint(func() { r.paintTextGio(&label, labelBounds, nodeClip) })
		}
		clone := *node
		clone.Props = cloneMap(node.Props)
		clone.Props["direction"] = "vertical"
		r.stackGio(&clone, contentBounds, nodeClip)
	case "button", "link":
		if r.scene == nil {
			r.paintSurfaceGio(node, bounds, currentClip)
		} else {
			r.scene.items = append(r.scene.items, sceneItem{
				button:   &sceneButton{node: node, bounds: bounds, clip: currentClip, opacity: r.opacity},
				scrolls:  append([]sceneScroll(nil), r.scrolls...),
				stickies: append([]sceneSticky(nil), r.stickies...),
			})
		}
		children := node.Children
		if node.Handle == r.paintOwner {
			children = paintContextChildren(node.Children)
		}
		if len(children) == 1 || node.Handle == r.paintOwner {
			inner := inset(bounds, insets(node.Props["padding"]))
			normalInner := inset(normalLayoutBounds(r.layoutMeta, bounds), insets(node.Props["padding"]))
			childClip := chooseClip(node, currentClip, bounds)
			for _, child := range children {
				if isPositionedContext(child) {
					if placement, ok := r.positionedPlacement(child); ok {
						r.paintPositioned(placement)
					} else if node.Handle == r.paintOwner {
						r.paintPositionedChild(child, inner, childClip, normalInner, r.layoutMeta.scrollAncestors, normalInner, false)
					} else {
						r.deferPositionedChild(child, inner, childClip, normalInner, r.layoutMeta.scrollAncestors, normalInner, false)
					}
					continue
				}
				childBounds := r.surfaceChildBounds(child, inner)
				normalChildBounds := r.surfaceChildBounds(child, normalInner)
				r.layoutChild(child, childBounds, childClip, normalInner, r.layoutMeta.scrollAncestors, normalChildBounds, false)
			}
		}
	case "stack", "radio_group":
		r.stackGio(node, bounds, nodeClip)
	case "stepper":
		clone := *node
		clone.Props = cloneMap(node.Props)
		clone.Props["direction"] = "horizontal"
		r.stackGio(&clone, bounds, nodeClip)
	case "slider":
		r.recordPaint(func() { r.paintSurfaceGio(node, bounds, currentClip) })
		parts := sliderParts(node, inset(bounds, insets(node.Props["padding"])))
		normalParts := sliderParts(node, inset(normalLayoutBounds(r.layoutMeta, bounds), insets(node.Props["padding"])))
		for _, child := range paintChildrenForNode(node, r.paintOwner) {
			if isPositionedContext(child) {
				if placement, ok := r.positionedPlacement(child); ok {
					r.paintPositioned(placement)
				} else if childBounds, ok := parts[child.Handle]; ok {
					r.paintPositionedChild(child, childBounds, nodeClip, inset(normalLayoutBounds(r.layoutMeta, bounds), insets(node.Props["padding"])), r.layoutMeta.scrollAncestors, normalParts[child.Handle], true)
				}
				continue
			}
			if childBounds, ok := parts[child.Handle]; ok && !childBounds.Empty() {
				r.layoutChild(child, childBounds, nodeClip, inset(normalLayoutBounds(r.layoutMeta, bounds), insets(node.Props["padding"])), r.layoutMeta.scrollAncestors, normalParts[child.Handle], true)
			}
		}
	case "tabs":
		parts := tabsParts(node, inset(bounds, insets(node.Props["padding"])), r.intrinsicLeafSize)
		normalParts := tabsParts(node, inset(normalLayoutBounds(r.layoutMeta, bounds), insets(node.Props["padding"])), r.intrinsicLeafSize)
		for _, child := range paintChildrenForNode(node, r.paintOwner) {
			if isPositionedContext(child) {
				if placement, ok := r.positionedPlacement(child); ok {
					r.paintPositioned(placement)
				} else if childBounds, ok := parts[child.Handle]; ok {
					r.paintPositionedChild(child, childBounds, nodeClip, inset(normalLayoutBounds(r.layoutMeta, bounds), insets(node.Props["padding"])), r.layoutMeta.scrollAncestors, normalParts[child.Handle], true)
				}
				continue
			}
			if childBounds, ok := parts[child.Handle]; ok && !childBounds.Empty() {
				r.layoutChild(child, childBounds, nodeClip, inset(normalLayoutBounds(r.layoutMeta, bounds), insets(node.Props["padding"])), r.layoutMeta.scrollAncestors, normalParts[child.Handle], true)
			}
		}
	case "select":
		trigger, popup, popupBounds := selectPopupBounds(node, bounds, r.viewport, r.intrinsicLeafSize)
		if trigger != nil {
			parentNormal := normalLayoutBounds(r.layoutMeta, bounds)
			r.layoutChild(trigger, bounds, nodeClip, parentNormal, r.layoutMeta.scrollAncestors, parentNormal, true)
		}
		if popup != nil && !popupBounds.Empty() {
			r.topLayers = append(r.topLayers, topLayer{node: popup, bounds: popupBounds})
		}
	case "select_popup":
		r.recordPaint(func() { r.paintSurfaceGio(node, bounds, currentClip) })
		clone := *node
		clone.Props = cloneMap(node.Props)
		clone.Props["direction"] = "vertical"
		r.stackGio(&clone, bounds, nodeClip)
	case "grid":
		r.gridGio(node, bounds, nodeClip)
	case "overlay":
		parentNormal := normalLayoutBounds(r.layoutMeta, bounds)
		children := node.Children
		if node.Handle == r.paintOwner {
			children = paintContextChildren(node.Children)
		}
		for _, child := range children {
			if child == nil || child.Hidden {
				continue
			}
			if isPositionedContext(child) {
				if placement, ok := r.positionedPlacement(child); ok {
					r.paintPositioned(placement)
					continue
				}
			}
			childBounds := overlayPlace(child, bounds)
			normalChildBounds := overlayPlace(child, parentNormal)
			if isPositionedContext(child) {
				if node.Handle == r.paintOwner {
					r.paintPositionedChild(child, childBounds, nodeClip, parentNormal, r.layoutMeta.scrollAncestors, normalChildBounds, false)
				} else {
					r.deferPositionedChild(child, childBounds, nodeClip, parentNormal, r.layoutMeta.scrollAncestors, normalChildBounds, false)
				}
				continue
			}
			r.layoutChild(child, childBounds, nodeClip, parentNormal, r.layoutMeta.scrollAncestors, normalChildBounds, false)
		}
	case "scroll":
		r.scrollGio(node, bounds, nodeClip)
	case "divider":
		r.recordPaint(func() {
			r.paintDividerGio(node, bounds, nodeClip)
		})
	case "text":
		r.recordPaint(func() {
			r.paintTextGio(node, bounds, nodeClip)
		})
	case "image":
		r.recordPaint(func() {
			r.paintImageGio(node, bounds, nodeClip)
		})
	default:
		parentNormal := normalLayoutBounds(r.layoutMeta, bounds)
		children := node.Children
		if node.Handle == r.paintOwner {
			children = paintContextChildren(node.Children)
		}
		for _, child := range children {
			if child == nil || child.Hidden {
				continue
			}
			if isPositionedContext(child) {
				if placement, ok := r.positionedPlacement(child); ok {
					r.paintPositioned(placement)
					continue
				}
			}
			childBounds := place(child, bounds)
			normalChildBounds := place(child, parentNormal)
			if isPositionedContext(child) {
				if node.Handle == r.paintOwner {
					r.paintPositionedChild(child, childBounds, nodeClip, parentNormal, r.layoutMeta.scrollAncestors, normalChildBounds, false)
				} else {
					r.deferPositionedChild(child, childBounds, nodeClip, parentNormal, r.layoutMeta.scrollAncestors, normalChildBounds, false)
				}
				continue
			}
			r.layoutChild(child, childBounds, nodeClip, parentNormal, r.layoutMeta.scrollAncestors, normalChildBounds, false)
		}
	}
}

func (r *gioRenderer) paintFieldBoxGio(node *project.Node, bounds, currentClip image.Rectangle) {
	nodeClip := currentClip.Intersect(bounds)
	r.paintSurfaceGio(node, bounds, currentClip)
	geometry := newFieldTextGeometry(node, bounds)
	selection, caret := geometry.Decorations()
	focused := r.state.Focused == stringValue(node.Props["field_handle"], "")
	if focused {
		for _, rectangle := range selection {
			r.fillRect(rectangle, nodeClip, colorValue(node.Props["selection_color"], color.RGBA{R: 103, G: 95, B: 242, A: 96}))
		}
	}
	text := *node
	text.Type = "text"
	textBounds := inset(bounds, insets(node.Props["padding"]))
	textBounds = textBounds.Sub(image.Pt(geometry.OffsetX, geometry.OffsetY*geometry.LineHeight))
	r.paintTextGio(&text, textBounds, nodeClip)
	if focused && !r.state.CaretHidden && !caret.Empty() {
		r.fillRect(caret, nodeClip, colorValue(node.Props["caret_color"], colorValue(node.Props["color"], color.RGBA{A: 255})))
	}
	if focused {
		for _, underline := range fieldCompositionUnderlines(node, bounds) {
			r.fillRect(underline, nodeClip, colorValue(node.Props["caret_color"], colorValue(node.Props["color"], color.RGBA{A: 255})))
		}
	}
}

func (r *gioRenderer) surfaceChildBounds(child *project.Node, bounds image.Rectangle) image.Rectangle {
	alignment := stringValue(child.Place["alignment"], "")
	if alignment == "" {
		return bounds
	}
	size := r.intrinsicSize(child, bounds.Size())
	if width := int(number(child.Props["width"], 0)); width > 0 {
		size.X = width
	}
	if height := int(number(child.Props["height"], 0)); height > 0 {
		size.Y = height
	}
	size.X = min(max(0, size.X), bounds.Dx())
	size.Y = min(max(0, size.Y), bounds.Dy())
	return alignedRect(bounds, size, alignment)
}

func alignedRect(parent image.Rectangle, size image.Point, alignment string) image.Rectangle {
	x, y := parent.Min.X, parent.Min.Y
	switch alignment {
	case "top", "center", "bottom":
		x += (parent.Dx() - size.X) / 2
	case "top_right", "right", "bottom_right", "end":
		x += parent.Dx() - size.X
	}
	switch alignment {
	case "left", "center", "right":
		y += (parent.Dy() - size.Y) / 2
	case "bottom_left", "bottom", "bottom_right", "end":
		y += parent.Dy() - size.Y
	}
	return image.Rect(x, y, x+size.X, y+size.Y)
}

func (r *gioRenderer) paintSurfaceGio(node *project.Node, bounds, currentClip image.Rectangle) {
	if shadow, ok := node.Props["shadow"].(map[string]any); ok {
		for _, layer := range shadowLayers(bounds, shadow) {
			r.fillRounded(layer.bounds, currentClip, layer.color, radiusValue(node.Props["radius"])+layer.radius)
		}
	}
	r.paintBackground(bounds, currentClip, node.Props["background"], radiusValue(node.Props["radius"]))
	if border, ok := node.Props["border"].(map[string]any); ok {
		thickness := max(1, int(number(border["thickness"], 1)))
		value := colorValue(border["color"], color.RGBA{A: 255})
		r.paintRoundedBorder(bounds, currentClip, value, thickness, radiusValue(node.Props["radius"]))
	}
}

func (r *gioRenderer) paintRoundedBorder(bounds, currentClip image.Rectangle, value color.Color, thickness, radius int) {
	pixelBounds := r.pxRect(bounds)
	pixelClip := r.pxRect(currentClip.Intersect(bounds))
	if pixelClip.Empty() {
		return
	}
	pixelThickness := max(1, r.gtx.Dp(unit.Dp(thickness)))
	clipStack := clip.Rect(pixelClip).Push(r.gtx.Ops)
	roundStack := clip.UniformRRect(pixelBounds, r.gtx.Dp(unit.Dp(radius))).Push(r.gtx.Ops)
	colorValue := r.nrgba(value)
	for _, edge := range []image.Rectangle{
		image.Rect(pixelBounds.Min.X, pixelBounds.Min.Y, pixelBounds.Max.X, pixelBounds.Min.Y+pixelThickness),
		image.Rect(pixelBounds.Min.X, pixelBounds.Max.Y-pixelThickness, pixelBounds.Max.X, pixelBounds.Max.Y),
		image.Rect(pixelBounds.Min.X, pixelBounds.Min.Y+pixelThickness, pixelBounds.Min.X+pixelThickness, pixelBounds.Max.Y-pixelThickness),
		image.Rect(pixelBounds.Max.X-pixelThickness, pixelBounds.Min.Y+pixelThickness, pixelBounds.Max.X, pixelBounds.Max.Y-pixelThickness),
	} {
		paint.FillShape(r.gtx.Ops, colorValue, clip.Rect(edge).Op())
	}
	roundStack.Pop()
	clipStack.Pop()
}

func shadowLayers(bounds image.Rectangle, shadow map[string]any) []shadowLayer {
	base := color.NRGBAModel.Convert(colorValue(shadow["color"], color.Transparent)).(color.NRGBA)
	if base.A == 0 {
		return nil
	}
	offset := image.Pt(int(number(shadow["x"], 0)), int(number(shadow["y"], 0)))
	offsetBounds := bounds.Add(offset)
	blur := max(0, int(number(shadow["blur"], 0)))
	if blur == 0 {
		return []shadowLayer{{bounds: offsetBounds, color: base}}
	}
	count := min(16, max(8, blur/2))
	weightTotal := count * (count + 1) / 2
	layers := make([]shadowLayer, 0, count)
	for index := count; index >= 1; index-- {
		expansion := max(1, int(math.Ceil(float64(blur*index)/float64(count*2))))
		weight := count - index + 1
		layerColor := base
		layerColor.A = uint8(max(1, int(base.A)*weight/weightTotal))
		layers = append(layers, shadowLayer{
			bounds: offsetBounds.Inset(-expansion),
			color:  layerColor,
			radius: expansion,
		})
	}
	return layers
}

func (r *gioRenderer) paintBackground(bounds, currentClip image.Rectangle, value any, radius int) {
	gradient, ok := value.(map[string]any)
	if !ok {
		r.fillRounded(bounds, currentClip, colorValue(value, color.Transparent), radius)
		return
	}
	stops := anySlice(gradient["stops"])
	if len(stops) < 2 {
		return
	}
	first, _ := stops[0].(map[string]any)
	last, _ := stops[len(stops)-1].(map[string]any)
	pixelBounds := r.pxRect(bounds)
	pixelClip := r.pxRect(currentClip.Intersect(bounds))
	if pixelClip.Empty() {
		return
	}
	angle := number(gradient["angle"], 0) * math.Pi / 180
	center := f32.Pt(float32(pixelBounds.Min.X+pixelBounds.Max.X)/2, float32(pixelBounds.Min.Y+pixelBounds.Max.Y)/2)
	extent := float32(math.Abs(math.Cos(angle))*float64(pixelBounds.Dx())+math.Abs(math.Sin(angle))*float64(pixelBounds.Dy())) / 2
	vector := f32.Pt(float32(math.Cos(angle))*extent, float32(math.Sin(angle))*extent)
	clipStack := clip.Rect(pixelClip).Push(r.gtx.Ops)
	roundStack := clip.UniformRRect(pixelBounds, r.gtx.Dp(unit.Dp(radius))).Push(r.gtx.Ops)
	paint.LinearGradientOp{
		Stop1: center.Sub(vector), Color1: r.nrgba(colorValue(first["color"], color.Transparent)),
		Stop2: center.Add(vector), Color2: r.nrgba(colorValue(last["color"], color.Transparent)),
	}.Add(r.gtx.Ops)
	paint.PaintOp{}.Add(r.gtx.Ops)
	roundStack.Pop()
	clipStack.Pop()
}

func (r *gioRenderer) fillRounded(bounds, currentClip image.Rectangle, value color.Color, radius int) {
	pixelBounds := r.pxRect(bounds)
	pixelClip := r.pxRect(currentClip.Intersect(bounds))
	if pixelClip.Empty() {
		return
	}
	clipStack := clip.Rect(pixelClip).Push(r.gtx.Ops)
	paint.FillShape(r.gtx.Ops, r.nrgba(value), clip.UniformRRect(pixelBounds, r.gtx.Dp(unit.Dp(radius))).Op(r.gtx.Ops))
	clipStack.Pop()
}

func (r *gioRenderer) fillRect(bounds, currentClip image.Rectangle, value color.Color) {
	target := r.pxRect(bounds.Intersect(currentClip))
	if !target.Empty() {
		paint.FillShape(r.gtx.Ops, r.nrgba(value), clip.Rect(target).Op())
	}
}

func (r *gioRenderer) nrgba(value color.Color) color.NRGBA {
	c := color.NRGBAModel.Convert(value).(color.NRGBA)
	opacity := clamp(r.opacity, 0, 1)
	c.A = uint8(math.Round(float64(c.A) * opacity))
	return c
}

func (r *gioRenderer) paintTextGio(node *project.Node, bounds, currentClip image.Rectangle) {
	label := r.label(node)
	if label.Shaper == fieldShapeState.fallback {
		fieldShapeState.Lock()
		defer fieldShapeState.Unlock()
	}
	pixelBounds := r.pxRect(bounds)
	pixelClip := r.pxRect(currentClip.Intersect(bounds))
	if pixelClip.Empty() {
		return
	}
	stack := clip.Rect(pixelClip).Push(r.gtx.Ops)
	offset := op.Offset(pixelBounds.Min).Push(r.gtx.Ops)
	gtx := r.gtx
	gtx.Constraints = layout.Exact(pixelBounds.Size())
	label.Layout(gtx)
	offset.Pop()
	stack.Pop()
}

func (r *gioRenderer) label(node *project.Node) material.LabelStyle {
	content := stringValue(node.Props["text"], stringValue(node.Props["content"], ""))
	label := material.Label(r.theme, unit.Sp(number(node.Props["size"], 16)), content)
	label.Color = r.nrgba(colorValue(node.Props["color"], color.RGBA{A: 255}))
	if number(node.Props["weight"], 400) >= 600 {
		label.Font.Weight = font.Bold
	} else if number(node.Props["weight"], 400) >= 500 {
		label.Font.Weight = font.Medium
	}
	if boolValue(node.Props["italic"], false) {
		label.Font.Style = font.Italic
	}
	fontPath := stringValue(node.Props["font"], "")
	if token, ok := node.Props["font"].(map[string]any); ok {
		fontPath = stringValue(token["src"], "")
	}
	if fontPath != "" {
		if !filepath.IsAbs(fontPath) {
			fontPath = filepath.Join(filepath.Dir(node.Source.File), fontPath)
		}
		if shaper, typeface, err := loadNativeFont(r.theme, fontPath); err == nil {
			label.Shaper = shaper
			label.Font.Typeface = typeface
		}
	} else if stringValue(node.Props["field_handle"], "") != "" {
		// Caret geometry is shaped with the deterministic fallback collection.
		// Paint field text with the same shaper so glyph advances cannot drift.
		label.Shaper = fieldShapeState.fallback
	}
	switch stringValue(node.Props["alignment"], "start") {
	case "center":
		label.Alignment = text.Middle
	case "end", "right":
		label.Alignment = text.End
	default:
		label.Alignment = text.Start
	}
	label.MaxLines = int(number(node.Props["max_lines"], 0))
	if stringValue(node.Props["overflow"], "clip") == "ellipsis" {
		label.Truncator = "…"
	}
	if lineHeight := number(node.Props["line_height"], 0); lineHeight > 0 {
		label.LineHeight = unit.Sp(lineHeight)
	}
	return label
}

func loadNativeFont(theme *material.Theme, path string) (*text.Shaper, font.Typeface, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, "", err
	}
	key := nativeFontKey{theme: theme, path: path}
	nativeFontCache.Lock()
	cached, ok := nativeFontCache.fonts[key]
	nativeFontCache.Unlock()
	if ok && cached.modified.Equal(info.ModTime()) && cached.size == info.Size() {
		return cached.shaper, cached.typeface, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, "", err
	}
	faces, err := gioopentype.ParseCollection(data)
	if err != nil {
		return nil, "", err
	}
	collection := make([]font.FontFace, 0, len(faces)+len(gofont.Collection()))
	collection = append(collection, faces...)
	collection = append(collection, gofont.Collection()...)
	shaper := text.NewShaper(text.NoSystemFonts(), text.WithCollection(collection))
	typeface := font.Typeface("")
	if len(faces) > 0 {
		typeface = faces[0].Font.Typeface
	}
	nativeFontCache.Lock()
	nativeFontCache.fonts[key] = nativeFont{
		modified: info.ModTime(), size: info.Size(), shaper: shaper, typeface: typeface,
	}
	nativeFontCache.Unlock()
	return shaper, typeface, nil
}

func releaseNativeFonts(theme *material.Theme) {
	nativeFontCache.Lock()
	for key := range nativeFontCache.fonts {
		if key.theme == theme {
			delete(nativeFontCache.fonts, key)
		}
	}
	nativeFontCache.Unlock()
}

func (r *gioRenderer) intrinsicSize(node *project.Node, limit image.Point) image.Point {
	return measureIntrinsic(node, limit, r.intrinsicLeafSize)
}

func (r *gioRenderer) intrinsicLeafSize(node *project.Node, limit image.Point) image.Point {
	switch node.Type {
	case "text":
		label := r.label(node)
		gtx := r.gtx
		maximum := r.pxPoint(limit)
		if maximum.X <= 0 {
			maximum.X = 1 << 20
		}
		if maximum.Y <= 0 {
			maximum.Y = 1 << 20
		}
		gtx.Constraints = layout.Constraints{Max: maximum}
		recording := op.Record(gtx.Ops)
		dimensions := label.Layout(gtx)
		_ = recording.Stop()
		return r.logicalPoint(dimensions.Size)
	case "divider":
		thickness := max(1, int(number(node.Props["thickness"], 1)))
		if stringValue(node.Props["orientation"], "horizontal") == "vertical" {
			return image.Pt(thickness, limit.Y)
		}
		return image.Pt(limit.X, thickness)
	case "image":
		source := stringValue(node.Props["src"], "")
		if source != "" {
			if !filepath.IsAbs(source) {
				source = filepath.Join(filepath.Dir(node.Source.File), source)
			}
			if decoded, err := loadImage(source); err == nil {
				return decoded.Bounds().Size()
			}
		}
		return image.Point{}
	default:
		return image.Point{}
	}
}

func (r *gioRenderer) paintImageGio(node *project.Node, bounds, currentClip image.Rectangle) {
	source := stringValue(node.Props["src"], "")
	if source == "" {
		return
	}
	if !filepath.IsAbs(source) {
		source = filepath.Join(filepath.Dir(node.Source.File), source)
	}
	decoded, err := loadImage(source)
	if err != nil {
		return
	}
	pixelBounds := r.pxRect(bounds)
	pixelClip := r.pxRect(currentClip.Intersect(bounds))
	stack := clip.Rect(pixelClip).Push(r.gtx.Ops)
	offset := op.Offset(pixelBounds.Min).Push(r.gtx.Ops)
	gtx := r.gtx
	gtx.Constraints = layout.Exact(pixelBounds.Size())
	imageWidget := widget.Image{
		Src:      paint.NewImageOp(decoded),
		Fit:      gioFit(stringValue(node.Props["fit"], "contain")),
		Position: gioDirection(stringValue(node.Props["alignment"], "center")),
		Scale:    1 / r.gtx.Metric.PxPerDp,
	}
	imageWidget.Layout(gtx)
	offset.Pop()
	stack.Pop()
}

func gioFit(value string) widget.Fit {
	switch value {
	case "cover":
		return widget.Cover
	case "fill":
		return widget.Fill
	default:
		return widget.Contain
	}
}

func gioDirection(value string) layout.Direction {
	switch value {
	case "top_left":
		return layout.NW
	case "top":
		return layout.N
	case "top_right":
		return layout.NE
	case "left":
		return layout.W
	case "right":
		return layout.E
	case "bottom_left":
		return layout.SW
	case "bottom":
		return layout.S
	case "bottom_right":
		return layout.SE
	default:
		return layout.Center
	}
}

func (r *gioRenderer) stackGio(node *project.Node, bounds, currentClip image.Rectangle) {
	children := visibleLayoutChildren(node.Children)
	clone := *node
	clone.Children = children
	normalBounds := normalLayoutBounds(r.layoutMeta, bounds)
	normalInner := inset(normalBounds, insets(node.Props["padding"]))
	normalRects := planStack(&clone, normalBounds, r.intrinsicSize)
	finalRects := planStack(&clone, bounds, r.intrinsicSize)
	flowRects := make(map[*project.Node][2]image.Rectangle, len(children))
	for index, child := range children {
		flowRects[child] = [2]image.Rectangle{finalRects[index], normalRects[index]}
	}
	for _, child := range node.Children {
		if child == nil || child.Hidden || !isPositionedContext(child) {
			continue
		}
		if rects, ok := flowRects[child]; ok {
			r.deferPositionedChild(child, rects[0], currentClip, normalInner, r.layoutMeta.scrollAncestors, rects[1], true)
		} else {
			r.deferPositionedChild(child, bounds, currentClip, normalInner, r.layoutMeta.scrollAncestors, bounds, false)
		}
	}
	for _, child := range paintChildrenForNode(node, r.paintOwner) {
		if isPositionedContext(child) {
			if placement, ok := r.positionedPlacement(child); ok {
				r.paintPositioned(placement)
			} else if rects, ok := flowRects[child]; ok {
				r.paintPositionedChild(child, rects[0], currentClip, normalInner, r.layoutMeta.scrollAncestors, rects[1], true)
			}
			continue
		}
		if child == nil || child.Hidden {
			continue
		}
		rects := flowRects[child]
		r.layoutChild(child, rects[0], currentClip, normalInner, r.layoutMeta.scrollAncestors, rects[1], true)
	}
}

func alignCross(bounds image.Rectangle, size int, vertical bool, alignment string) image.Rectangle {
	if vertical {
		switch alignment {
		case "center":
			bounds.Min.X += (bounds.Dx() - size) / 2
		case "end":
			bounds.Min.X = bounds.Max.X - size
		}
		bounds.Max.X = bounds.Min.X + size
		return bounds
	}
	switch alignment {
	case "center":
		bounds.Min.Y += (bounds.Dy() - size) / 2
	case "end":
		bounds.Min.Y = bounds.Max.Y - size
	}
	bounds.Max.Y = bounds.Min.Y + size
	return bounds
}

func (r *gioRenderer) gridGio(node *project.Node, bounds, currentClip image.Rectangle) {
	children := visibleLayoutChildren(node.Children)
	gap := int(number(node.Props["gap"], 0))
	columnDefs := anySlice(node.Props["columns"])
	if len(columnDefs) == 0 {
		count := max(1, int(number(node.Props["columns"], 1)))
		columnDefs = make([]any, count)
		for index := range columnDefs {
			columnDefs[index] = "1fr"
		}
	}
	columns := len(columnDefs)
	rows := max(1, int(math.Ceil(float64(len(children))/float64(columns))))
	for index, child := range children {
		row := index / columns
		if value, ok := child.Place["row"]; ok {
			row = int(number(value, float64(row)))
		}
		rows = max(rows, row+max(1, int(number(child.Place["row_span"], 1))))
	}
	rowDefs := anySlice(node.Props["rows"])
	for len(rowDefs) < rows {
		rowDefs = append(rowDefs, "1fr")
	}
	columnSizes := tracks(columnDefs, bounds.Dx(), gap)
	rowSizes := tracks(rowDefs, bounds.Dy(), gap)
	normalBounds := normalLayoutBounds(r.layoutMeta, bounds)
	normalColumnSizes := tracks(columnDefs, normalBounds.Dx(), gap)
	normalRowSizes := tracks(rowDefs, normalBounds.Dy(), gap)
	type gridRects struct {
		final  image.Rectangle
		normal image.Rectangle
	}
	flowRects := make(map[*project.Node]gridRects, len(children))
	flowIndex := 0
	for _, child := range node.Children {
		if isFixedPositioned(child) {
			continue
		}
		if child == nil || child.Hidden {
			continue
		}
		index := flowIndex
		column := index % columns
		row := index / columns
		if value, ok := child.Place["column"]; ok {
			column = int(number(value, float64(column)))
		}
		if value, ok := child.Place["row"]; ok {
			row = int(number(value, float64(row)))
		}
		column = max(0, min(column, columns-1))
		row = max(0, min(row, len(rowSizes)-1))
		columnSpan := max(1, min(int(number(child.Place["column_span"], 1)), columns-column))
		rowSpan := max(1, min(int(number(child.Place["row_span"], 1)), len(rowSizes)-row))
		x := bounds.Min.X + trackOffset(columnSizes, column, gap)
		y := bounds.Min.Y + trackOffset(rowSizes, row, gap)
		childBounds := image.Rect(
			x, y,
			x+trackSpan(columnSizes, column, columnSpan, gap),
			y+trackSpan(rowSizes, row, rowSpan, gap),
		)
		normalX := normalBounds.Min.X + trackOffset(normalColumnSizes, column, gap)
		normalY := normalBounds.Min.Y + trackOffset(normalRowSizes, row, gap)
		normalChildBounds := image.Rect(normalX, normalY, normalX+trackSpan(normalColumnSizes, column, columnSpan, gap), normalY+trackSpan(normalRowSizes, row, rowSpan, gap))
		flowRects[child] = gridRects{final: childBounds, normal: normalChildBounds}
		flowIndex++
	}
	for _, child := range node.Children {
		if child == nil || child.Hidden || !isPositionedContext(child) {
			continue
		}
		if rects, ok := flowRects[child]; ok {
			r.deferPositionedChild(child, rects.final, currentClip, normalBounds, r.layoutMeta.scrollAncestors, rects.normal, false)
		} else {
			r.deferPositionedChild(child, bounds, currentClip, normalBounds, r.layoutMeta.scrollAncestors, bounds, false)
		}
	}
	for _, child := range paintChildrenForNode(node, r.paintOwner) {
		if isPositionedContext(child) {
			if placement, ok := r.positionedPlacement(child); ok {
				r.paintPositioned(placement)
			} else if rects, ok := flowRects[child]; ok {
				r.paintPositionedChild(child, rects.final, currentClip, normalBounds, r.layoutMeta.scrollAncestors, rects.normal, false)
			}
			continue
		}
		if child == nil || child.Hidden {
			continue
		}
		rects, ok := flowRects[child]
		if !ok {
			continue
		}
		r.layoutChild(child, rects.final, currentClip, normalBounds, r.layoutMeta.scrollAncestors, rects.normal, false)
	}
}

func (r *gioRenderer) scrollGio(node *project.Node, bounds, currentClip image.Rectangle) {
	if len(node.Children) != 1 {
		return
	}
	plan := planScroll(node, bounds, r.intrinsicSize)
	normalPlan := planScroll(node, normalLayoutBounds(r.layoutMeta, bounds), r.intrinsicSize)
	offset := clampScrollOffset(r.state.Scroll[scrollKey(node)], plan)
	metrics := scrollMetrics(plan)
	r.result.Scroll[node.Handle] = metrics
	if r.scene != nil {
		r.scene.scrolls[node.Handle] = sceneScrollMetric{
			metrics:   metrics,
			ancestors: append([]sceneScroll(nil), r.scrolls...),
			stickies:  append([]sceneSticky(nil), r.stickies...),
		}
	}
	childBounds := plan.ContentRect.Sub(offset)
	visibleClip := currentClip.Intersect(plan.Viewport)
	ancestors := append(append([]string(nil), r.layoutMeta.scrollAncestors...), node.Handle)
	translation := r.layoutMeta.ancestorTranslation.Sub(offset)
	scrollports := append(append([]stickyScrollport(nil), r.layoutMeta.scrollports...), stickyScrollport{
		NormalViewport: normalPlan.Viewport, Viewport: plan.Viewport,
		Translation: r.layoutMeta.ancestorTranslation, Offset: offset,
	})
	if r.scene != nil {
		childBounds = plan.ContentRect
		scroll := sceneScroll{
			key:      scrollKey(node),
			metrics:  metrics,
			stickies: append([]sceneSticky(nil), r.stickies...),
		}
		r.scrolls = append(r.scrolls, scroll)
		child := node.Children[0]
		if isPositionedContext(child) {
			r.deferPositionedChild(child, childBounds, plan.ContentRect, normalPlan.Viewport, ancestors, normalPlan.ContentRect, true)
		} else {
			r.layoutChildInScroll(child, childBounds, plan.ContentRect, normalPlan.Viewport, ancestors, normalPlan.ContentRect, true, scrollports, translation)
		}
		r.scrolls = r.scrolls[:len(r.scrolls)-1]
		r.addDerivedScrollbars(node, plan, offset, currentClip.Intersect(plan.Viewport), scroll)
		return
	}
	hardClip := clip.Rect(r.pxRect(visibleClip)).Push(r.gtx.Ops)
	firstGeometry := len(r.geometryOrder)
	child := node.Children[0]
	if isPositionedContext(child) {
		r.deferPositionedChild(child, childBounds, visibleClip, normalPlan.Viewport, ancestors, normalPlan.ContentRect, true)
	} else {
		r.layoutChildInScroll(child, childBounds, visibleClip, normalPlan.Viewport, ancestors, normalPlan.ContentRect, true, scrollports, translation)
	}
	hardClip.Pop()
	for _, handle := range r.geometryOrder[firstGeometry:] {
		geometry := r.result.Geometry[handle]
		geometry.Clip = geometry.Clip.Intersect(visibleClip)
		r.result.Geometry[handle] = geometry
	}
	r.addDerivedScrollbars(node, plan, offset, currentClip.Intersect(plan.Viewport), sceneScroll{key: scrollKey(node), metrics: metrics})
}

func scrollClips(viewport, content image.Rectangle, axis string) (visible, prepaint image.Rectangle) {
	return viewport, viewport
}

func (r *gioRenderer) addDerivedScrollbars(node *project.Node, plan scrollPlan, offset image.Point, clip image.Rectangle, scroll sceneScroll) {
	bars := planScrollbars(node, plan, offset)
	for _, bar := range bars {
		descriptor := semantic.DerivedDescriptor{
			OwnerHandle: node.Handle, Axis: bar.Axis, Policy: bar.Policy,
			Track: bar.Track, Thumb: bar.Thumb, Corner: bar.Corner,
			Bounds: bar.Track, Clip: clip, PaintOrder: r.paintOrder,
			Offset: bar.Offset, Maximum: bar.Maximum, Viewport: bar.Viewport, Content: bar.Content, Enabled: bar.Enabled,
			ViewportSize: plan.Viewport.Size(), ContentSize: plan.ContentSize,
		}
		if r.scene != nil {
			r.scene.items = append(r.scene.items, sceneItem{
				scrolls:  append([]sceneScroll(nil), r.scrolls...),
				stickies: append([]sceneSticky(nil), r.stickies...),
				derived:  &sceneDerived{descriptor: descriptor, scroll: scroll, ancestors: append([]sceneScroll(nil), r.scrolls...), stickies: append([]sceneSticky(nil), r.stickies...)},
			})
		} else {
			r.result.Derived = append(r.result.Derived, descriptor)
			r.paintDerivedScrollbarGio(descriptor, clip)
		}
		if !bar.Corner.Empty() {
			r.paintOrder++
		}
		r.paintOrder += 3
	}
}

func (r *gioRenderer) paintDerivedScrollbarGio(descriptor semantic.DerivedDescriptor, clip image.Rectangle) {
	r.fillRounded(descriptor.Track, clip, scrollbarTrackColor, 4)
	r.fillRounded(descriptor.Thumb, clip, scrollbarThumbColor, 4)
	if !descriptor.Corner.Empty() {
		r.fillRect(descriptor.Corner, clip, scrollbarTrackColor)
	}
}

func (r *gioRenderer) paintDividerGio(node *project.Node, bounds, currentClip image.Rectangle) {
	thickness := max(1, int(number(node.Props["thickness"], 1)))
	line := bounds
	if stringValue(node.Props["orientation"], "horizontal") == "vertical" {
		line.Max.X = min(line.Max.X, line.Min.X+thickness)
	} else {
		line.Max.Y = min(line.Max.Y, line.Min.Y+thickness)
	}
	r.fillRect(line, currentClip, colorValue(node.Props["color"], color.RGBA{A: 255}))
}

func (r *gioRenderer) pxRect(value image.Rectangle) image.Rectangle {
	return image.Rectangle{Min: r.pxPoint(value.Min), Max: r.pxPoint(value.Max)}
}

func (r *gioRenderer) pxPoint(value image.Point) image.Point {
	return image.Pt(r.gtx.Dp(unit.Dp(value.X)), r.gtx.Dp(unit.Dp(value.Y)))
}

func (r *gioRenderer) logicalPoint(value image.Point) image.Point {
	scale := r.gtx.Metric.PxPerDp
	if scale <= 0 {
		scale = 1
	}
	return image.Pt(
		int(math.Ceil(float64(float32(value.X)/scale))),
		int(math.Ceil(float64(float32(value.Y)/scale))),
	)
}
