package render

import (
	"image"

	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/unit"
	"gioui.org/widget/material"

	"gora/internal/project"
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
	operations   op.Ops
	items        []sceneItem
	inspections  []sceneInspection
	interactions []sceneInteraction
}

type sceneInteraction struct {
	region  InteractionRegion
	scrolls []sceneScroll
}

type sceneItem struct {
	call      op.CallOp
	scrolls   []sceneScroll
	scrollbar *sceneScrollbar
	button    *sceneButton
}

type sceneInspection struct {
	inspection Inspection
	scrolls    []sceneScroll
	button     *project.Node
}

type sceneButton struct {
	node    *project.Node
	bounds  image.Rectangle
	clip    image.Rectangle
	opacity float64
}

type sceneScroll struct {
	key         string
	axis        string
	viewport    image.Rectangle
	contentSize int
}

type sceneScrollbar struct {
	bounds  image.Rectangle
	clip    image.Rectangle
	scroll  sceneScroll
	opacity float64
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
		return GioResult{Bounds: make(map[string]image.Rectangle)}
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
	scene := new(gioScene)
	buildContext := gtx
	buildContext.Ops = &scene.operations
	result := GioResult{Bounds: make(map[string]image.Rectangle)}
	renderer := gioRenderer{
		gtx: buildContext, theme: theme, result: result, opacity: 1, scene: scene,
	}
	bounds := image.Rectangle{Max: viewport}
	renderer.layout(root, bounds, bounds)
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
		call:    recording.Stop(),
		scrolls: append([]sceneScroll(nil), r.scrolls...),
	})
}

func (scene *gioScene) replay(gtx layout.Context, theme *material.Theme, state State) GioResult {
	result := GioResult{
		Bounds:       make(map[string]image.Rectangle, len(scene.inspections)),
		Inspections:  make([]Inspection, 0, len(scene.inspections)),
		Interactions: make([]InteractionRegion, 0, len(scene.interactions)),
	}
	for _, item := range scene.items {
		translation, viewportClip, clipped := sceneTransform(item.scrolls, state)
		if clipped {
			continue
		}
		clipStack := pushSceneClip(gtx, viewportClip)
		if item.button != nil {
			button := item.button
			bounds := button.bounds.Add(translation)
			staticClip := button.clip.Add(translation)
			renderer := gioRenderer{gtx: gtx, theme: theme, state: state, opacity: button.opacity}
			renderer.paintSurfaceGio(
				buttonNodeForState(button.node, state),
				bounds,
				staticClip.Intersect(viewportClipOrBounds(viewportClip, bounds)),
			)
		} else if item.scrollbar != nil {
			scrollbar := item.scrollbar
			bounds := scrollbar.bounds.Add(translation)
			staticClip := scrollbar.clip.Add(translation)
			renderer := gioRenderer{
				gtx: gtx, theme: theme, state: state, opacity: scrollbar.opacity,
			}
			renderer.paintScrollbarGio(
				bounds,
				staticClip.Intersect(viewportClipOrBounds(viewportClip, bounds)),
				scrollbar.scroll.axis,
				scrollbar.scroll.contentSize,
				scrollbar.scroll.offset(state),
			)
		} else {
			offset := op.Offset(logicalOffset(gtx, translation)).Push(gtx.Ops)
			item.call.Add(gtx.Ops)
			offset.Pop()
		}
		clipStack.Pop()
	}
	for _, cached := range scene.inspections {
		translation, viewportClip, clipped := sceneTransform(cached.scrolls, state)
		inspection := cached.inspection
		if cached.button != nil {
			effective := buttonNodeForState(cached.button, state)
			inspection.Props = cloneMap(effective.Props)
			inspection.Hovered = state.Hovered == effective.Handle
			inspection.Pressed = state.Pressed == effective.Handle
			inspection.Focused = state.Focused == effective.Handle
			inspection.State = cloneMap(state.Values[effective.Scope])
		}
		inspection.Bounds = inspection.Bounds.Add(translation)
		inspection.Clip = inspection.Clip.Add(translation)
		if len(cached.scrolls) > 0 {
			inspection.Clip = inspection.Clip.Intersect(viewportClip)
		}
		if clipped {
			inspection.Clip = image.Rectangle{}
		}
		result.Bounds[inspection.Handle] = inspection.Bounds
		result.Inspections = append(result.Inspections, inspection)
	}
	for _, cached := range scene.interactions {
		translation, viewportClip, clipped := sceneTransform(cached.scrolls, state)
		region := cached.region
		region.Bounds = region.Bounds.Add(translation)
		region.Clip = region.Clip.Add(translation)
		if len(cached.scrolls) > 0 {
			region.Clip = region.Clip.Intersect(viewportClip)
		}
		if clipped {
			region.Clip = image.Rectangle{}
		}
		result.Interactions = append(result.Interactions, region)
	}
	return result
}

func sceneTransform(scrolls []sceneScroll, state State) (image.Point, image.Rectangle, bool) {
	translation := image.Point{}
	var viewportClip image.Rectangle
	for index, scroll := range scrolls {
		viewport := scroll.viewport.Add(translation)
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
	if scroll.axis == "vertical" {
		offset.X = 0
		offset.Y = min(max(0, offset.Y), scroll.contentSize-scroll.viewport.Dy())
		return offset
	}
	offset.Y = 0
	offset.X = min(max(0, offset.X), scroll.contentSize-scroll.viewport.Dx())
	return offset
}

func (scroll sceneScroll) viewportSize() int {
	if scroll.axis == "vertical" {
		return scroll.viewport.Dy()
	}
	return scroll.viewport.Dx()
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
