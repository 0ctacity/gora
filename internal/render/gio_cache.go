package render

import (
	"image"

	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/unit"
	"gioui.org/widget/material"

	"gora/internal/project"
	"gora/internal/semantic"
)

// GioCache lays out an immutable resolved document once and replays its paint
// operations while ephemeral scroll offsets change.
type GioCache struct {
	key    gioCacheKey
	scene  *gioScene
	builds int
}

type gioCacheKey struct {
	root        *project.Node
	theme       *material.Theme
	viewport    image.Point
	pixelsPerDp float32
	pixelsPerSp float32
}

type gioScene struct {
	root       *project.Node
	viewport   image.Rectangle
	operations op.Ops
	items      []sceneItem
	geometries []sceneGeometry
	scrolls    map[string]sceneScrollMetric
	layouts    map[string]LayoutRecord
}

type sceneItem struct {
	call     op.CallOp
	scrolls  []sceneScroll
	stickies []sceneSticky
	derived  *sceneDerived
	button   *sceneButton
	field    *sceneField
}

type sceneSticky struct {
	node          *project.Node
	record        LayoutRecord
	ancestorCount int
	delta         image.Point
}

type sceneField struct {
	node    *project.Node
	bounds  image.Rectangle
	clip    image.Rectangle
	opacity float64
}

type sceneGeometry struct {
	handle     string
	geometry   semantic.Geometry
	paintOrder int
	layout     LayoutRecord
	scrolls    []sceneScroll
	stickies   []sceneSticky
	node       *project.Node
}

type sceneButton struct {
	node    *project.Node
	bounds  image.Rectangle
	clip    image.Rectangle
	opacity float64
}

type sceneScroll struct {
	key      string
	metrics  ScrollMetrics
	stickies []sceneSticky
}

// sceneScrollMetric retains the metric's build-time geometry together with
// the scrollports and sticky ancestors that contain it. During replay, only
// those ancestor translations and sticky corrections are applied; the
// scrollport's own offset moves its content, not its viewport.
type sceneScrollMetric struct {
	metrics   ScrollMetrics
	ancestors []sceneScroll
	stickies  []sceneSticky
}

type sceneDerived struct {
	descriptor semantic.DerivedDescriptor
	scroll     sceneScroll
	ancestors  []sceneScroll
	stickies   []sceneSticky
}

// Layout replays the cached scene and applies current scroll state without
// rebuilding document geometry or inspection metadata.
func (c *GioCache) Layout(
	gtx layout.Context,
	theme *material.Theme,
	root *project.Node,
	viewport image.Point,
	state State,
) GioResult {
	if theme == nil {
		theme = material.NewTheme()
	}
	if root == nil || viewport.X <= 0 || viewport.Y <= 0 {
		return GioResult{Bounds: make(map[string]image.Rectangle), Geometry: make(map[string]semantic.Geometry), Layout: make(map[string]LayoutRecord), Scroll: make(map[string]ScrollMetrics)}
	}
	key := gioCacheKey{
		root: root, theme: theme, viewport: viewport,
		pixelsPerDp: gtx.Metric.PxPerDp, pixelsPerSp: gtx.Metric.PxPerSp,
	}
	if c.scene == nil || c.key != key {
		c.build(gtx, theme, root, viewport, key)
	}
	return c.scene.replay(gtx, theme, state)
}

// Invalidate drops all retained Gio operations and geometry.
func (c *GioCache) Invalidate() {
	c.key = gioCacheKey{}
	c.scene = nil
}

func (c *GioCache) build(
	gtx layout.Context,
	theme *material.Theme,
	root *project.Node,
	viewport image.Point,
	key gioCacheKey,
) {
	scene := &gioScene{root: root, viewport: image.Rectangle{Max: viewport}, scrolls: make(map[string]sceneScrollMetric), layouts: make(map[string]LayoutRecord)}
	buildContext := gtx
	buildContext.Ops = &scene.operations
	result := GioResult{Bounds: make(map[string]image.Rectangle), Geometry: make(map[string]semantic.Geometry), Layout: make(map[string]LayoutRecord), Scroll: make(map[string]ScrollMetrics)}
	renderer := gioRenderer{
		gtx: buildContext, theme: theme, result: result, opacity: 1, scene: scene,
		viewport: image.Rectangle{Max: viewport}, layoutMeta: layoutMeta{parentInner: image.Rectangle{Max: viewport}}, sourceRanks: sourceOrderRanks(root),
		rootHandle: root.Handle, deferred: make(map[string]map[string]positionedPlacement), painted: make(map[string]bool),
	}
	bounds := image.Rectangle{Max: viewport}
	renderer.layout(root, bounds, bounds)
	for index := 0; index < len(renderer.topLayers); index++ {
		layer := renderer.topLayers[index]
		renderer.layoutFinal(layer.node, layer.bounds, renderer.viewport)
	}
	for handle, record := range renderer.result.Layout {
		record.ScrollAncestors = append([]string(nil), record.ScrollAncestors...)
		scene.layouts[handle] = record
	}
	c.key = key
	c.scene = scene
	c.builds++
}

func (r *gioRenderer) recordPaint(paintNode func()) {
	if r.scene == nil {
		paintNode()
		return
	}
	recording := op.Record(r.gtx.Ops)
	paintNode()
	r.scene.items = append(r.scene.items, sceneItem{
		call:     recording.Stop(),
		scrolls:  append([]sceneScroll(nil), r.scrolls...),
		stickies: append([]sceneSticky(nil), r.stickies...),
	})
}

func (scene *gioScene) replay(gtx layout.Context, theme *material.Theme, state State) GioResult {
	result := GioResult{
		Bounds:   make(map[string]image.Rectangle, len(scene.geometries)),
		Geometry: make(map[string]semantic.Geometry, len(scene.geometries)),
		Layout:   make(map[string]LayoutRecord, len(scene.geometries)),
		Scroll:   make(map[string]ScrollMetrics, len(scene.scrolls)),
		Derived:  make([]semantic.DerivedDescriptor, 0),
	}
	for handle, record := range scene.layouts {
		record.ScrollAncestors = append([]string(nil), record.ScrollAncestors...)
		result.Layout[handle] = record
	}
	for key, published := range scene.scrolls {
		metrics := published.metrics
		translation := sceneScrollTranslation(published.ancestors, state)
		stickyCorrection := sceneStickyCorrection(published.stickies, published.ancestors, state, scene.viewport)
		metrics.Viewport = metrics.Viewport.Add(translation).Add(stickyCorrection)
		result.Scroll[key] = metrics
	}
	for _, item := range scene.items {
		translation, viewportClip, clipped := sceneTransform(item.scrolls, state, scene.viewport)
		translation = translation.Add(sceneStickyCorrection(item.stickies, item.scrolls, state, scene.viewport))
		if item.derived != nil {
			derived := replayDerivedScrollbar(item.derived, state, scene.viewport)
			if clipped {
				// Keep the derived node in the canonical tree even when an
				// ancestor scroll clip moves it completely offscreen. Its
				// geometry remains available for inspection with an empty clip,
				// while no pixels are painted.
				derived.Clip = image.Rectangle{}
				result.Derived = append(result.Derived, derived)
				continue
			}
			result.Derived = append(result.Derived, derived)
			clipStack := pushSceneClip(gtx, viewportClip)
			renderer := gioRenderer{gtx: gtx, theme: theme, state: state, opacity: 1}
			renderer.paintDerivedScrollbarGio(derived, derived.Clip)
			clipStack.Pop()
			continue
		}
		if clipped {
			continue
		}
		clipStack := pushSceneClip(gtx, viewportClip)
		if item.field != nil {
			field := item.field
			bounds := field.bounds.Add(translation)
			staticClip := field.clip.Add(translation)
			renderer := gioRenderer{gtx: gtx, theme: theme, state: state, opacity: field.opacity}
			renderer.paintFieldBoxGio(interactiveNodeForState(field.node, state), bounds, staticClip.Intersect(viewportClipOrBounds(viewportClip, bounds)))
		} else if item.button != nil {
			button := item.button
			bounds := button.bounds.Add(translation)
			staticClip := button.clip.Add(translation)
			renderer := gioRenderer{gtx: gtx, theme: theme, state: state, opacity: button.opacity}
			renderer.paintSurfaceGio(
				interactiveNodeForState(button.node, state),
				bounds,
				staticClip.Intersect(viewportClipOrBounds(viewportClip, bounds)),
			)
		} else {
			offset := op.Offset(logicalOffset(gtx, translation)).Push(gtx.Ops)
			item.call.Add(gtx.Ops)
			offset.Pop()
		}
		clipStack.Pop()
	}
	for _, cached := range scene.geometries {
		translation, viewportClip, clipped := sceneTransform(cached.scrolls, state, scene.viewport)
		stickyTotal := sceneStickyTransform(cached.stickies, cached.scrolls, state, scene.viewport)
		stickyCorrection := stickyTotal.Sub(sceneStickyBuildDelta(cached.stickies))
		translation = translation.Add(stickyCorrection)
		geometry := cached.geometry
		geometry.Bounds = geometry.Bounds.Add(translation)
		geometry.Clip = geometry.Clip.Add(translation)
		if len(cached.scrolls) > 0 {
			geometry.Clip = geometry.Clip.Intersect(viewportClip)
		}
		if clipped {
			geometry.Clip = image.Rectangle{}
		}
		geometry.PaintOrder = cached.paintOrder
		if cached.node != nil {
			geometry.Props = cloneMap(interactiveNodeForState(cached.node, state).Props)
		}
		result.Bounds[cached.handle] = geometry.Bounds
		result.Geometry[cached.handle] = geometry
		record := cached.layout
		record.Final = record.Normal.Add(sceneScrollTranslation(cached.scrolls, state)).Add(stickyTotal)
		record.ScrollAncestors = append([]string(nil), record.ScrollAncestors...)
		result.Layout[cached.handle] = record
	}
	result.Tree = semantic.Build(sceneRoot(scene), result.Geometry, semanticContext(state), result.Derived)
	return result
}

func replayDerivedScrollbar(cached *sceneDerived, state State, fallback image.Rectangle) semantic.DerivedDescriptor {
	base := cached.descriptor
	// Corner geometry is owned by the shared build-time plan. The replay plan
	// only recomputes the track/thumb position from the current offset; it does
	// not know whether the sibling axis was visible at build time, so preserve
	// the original corner descriptor explicitly.
	corner := base.Corner
	offset := cached.scroll.offset(state)
	plan := makeScrollbarPlan(base.Axis, base.Policy, base.Track, axisOffset(base.Axis, offset), base.Maximum, base.Viewport, base.Content)
	translation, viewportClip, _ := sceneTransform(cached.ancestors, state, fallback)
	translation = translation.Add(sceneStickyCorrection(cached.stickies, cached.ancestors, state, fallback))
	base.Track = plan.Track.Add(translation)
	base.Thumb = plan.Thumb.Add(translation)
	base.Corner = corner.Add(translation)
	base.Bounds = plan.Track.Add(translation)
	base.Clip = base.Clip.Add(translation)
	if !viewportClip.Empty() {
		base.Clip = base.Clip.Intersect(viewportClip)
	}
	base.Offset = plan.Offset
	base.Enabled = plan.Enabled
	return base
}

// sceneScrollTranslation returns the cumulative translation applied to content
// by the current offsets of the supplied ancestor scrollports.
func sceneScrollTranslation(scrolls []sceneScroll, state State) image.Point {
	translation := image.Point{}
	for _, scroll := range scrolls {
		translation = translation.Sub(scroll.offset(state))
	}
	return translation
}

// sceneScrollViewport resolves the nearest scrollport viewport at the current
// ancestor translation, including sticky corrections for scrollports nested
// inside moving sticky subtrees. A scrollport's own offset moves its content,
// not its viewport, so the final entry is read before applying its offset.
func sceneScrollViewport(scrolls []sceneScroll, state State, fallback image.Rectangle) image.Rectangle {
	viewport := fallback
	translation := image.Point{}
	for index, scroll := range scrolls {
		viewport = scroll.metrics.Viewport.Add(translation)
		viewport = viewport.Add(sceneStickyCorrection(scroll.stickies, scrolls[:index], state, fallback))
		translation = translation.Sub(scroll.offset(state))
	}
	return viewport
}

// sceneStickyTransform computes the cumulative sticky translation for a node
// from its immutable normal metadata and the current ancestor scroll offsets.
// Each sticky is evaluated against the nearest containing scrollport and the
// previous sticky translations in the same subtree.
func sceneStickyTransform(stickies []sceneSticky, scrolls []sceneScroll, state State, fallback image.Rectangle) image.Point {
	translation := image.Point{}
	for _, sticky := range stickies {
		depth := sticky.ancestorCount
		if depth > len(scrolls) {
			depth = len(scrolls)
		}
		ancestors := scrolls[:depth]
		baseTranslation := sceneScrollTranslation(ancestors, state)
		base := sticky.record.Normal.Add(baseTranslation).Add(translation)
		parent := sticky.record.ParentInner.Add(baseTranslation).Add(translation)
		viewport := sceneScrollViewport(ancestors, state, fallback)
		_, delta := planStickyRect(sticky.node, base, parent, viewport)
		translation = translation.Add(delta)
	}
	return translation
}

func sceneStickyBuildDelta(stickies []sceneSticky) image.Point {
	translation := image.Point{}
	for _, sticky := range stickies {
		translation = translation.Add(sticky.delta)
	}
	return translation
}

func sceneStickyCorrection(stickies []sceneSticky, scrolls []sceneScroll, state State, fallback image.Rectangle) image.Point {
	return sceneStickyTransform(stickies, scrolls, state, fallback).Sub(sceneStickyBuildDelta(stickies))
}

func axisOffset(axis string, offset image.Point) int {
	if axis == "vertical" {
		return offset.Y
	}
	return offset.X
}

func sceneRoot(scene *gioScene) *project.Node {
	return scene.root
}

func sceneTransform(scrolls []sceneScroll, state State, fallback image.Rectangle) (image.Point, image.Rectangle, bool) {
	translation := image.Point{}
	var viewportClip image.Rectangle
	for index, scroll := range scrolls {
		viewport := scroll.metrics.Viewport.Add(translation)
		viewport = viewport.Add(sceneStickyCorrection(scroll.stickies, scrolls[:index], state, fallback))
		if index == 0 {
			viewportClip = viewport
		} else {
			viewportClip = viewportClip.Intersect(viewport)
		}
		translation = translation.Sub(scroll.offset(state))
	}
	return translation, viewportClip, len(scrolls) > 0 && viewportClip.Empty()
}

func (scroll sceneScroll) offset(state State) image.Point {
	offset := state.Scroll[scroll.key]
	return clampScrollOffset(offset, scrollPlan{
		Viewport: scroll.metrics.Viewport, ContentSize: scroll.metrics.ContentSize,
		Maximum: scroll.metrics.Maximum, EnabledX: scroll.metrics.EnabledX, EnabledY: scroll.metrics.EnabledY,
	})
}

func (scroll sceneScroll) axis() string {
	if scroll.metrics.EnabledX && scroll.metrics.EnabledY {
		return "both"
	}
	if scroll.metrics.EnabledX {
		return "horizontal"
	}
	return "vertical"
}

func (scroll sceneScroll) contentSize() int {
	if scroll.axis() == "vertical" {
		return scroll.metrics.ContentSize.Y
	}
	return scroll.metrics.ContentSize.X
}

func pushSceneClip(gtx layout.Context, bounds image.Rectangle) clip.Stack {
	if bounds.Empty() {
		return clip.Rect{Max: gtx.Constraints.Max}.Push(gtx.Ops)
	}
	return clip.Rect{
		Min: logicalOffset(gtx, bounds.Min),
		Max: logicalOffset(gtx, bounds.Max),
	}.Push(gtx.Ops)
}

func logicalOffset(gtx layout.Context, point image.Point) image.Point {
	return image.Pt(gtx.Dp(unit.Dp(point.X)), gtx.Dp(unit.Dp(point.Y)))
}

func viewportClipOrBounds(viewportClip, bounds image.Rectangle) image.Rectangle {
	if viewportClip.Empty() {
		return bounds
	}
	return viewportClip
}
