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
	operations op.Ops
	items      []sceneItem
	geometries []sceneGeometry
}

type sceneItem struct {
	call      op.CallOp
	scrolls   []sceneScroll
	scrollbar *sceneScrollbar
	button    *sceneButton
	field     *sceneField
}

type sceneField struct {
	node    *project.Node
	bounds  image.Rectangle
	clip    image.Rectangle
	opacity float64
}

type sceneGeometry struct {
	handle   string
	geometry semantic.Geometry
	scrolls  []sceneScroll
	node     *project.Node
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
		return GioResult{Bounds: make(map[string]image.Rectangle), Geometry: make(map[string]semantic.Geometry)}
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
	scene := &gioScene{root: root}
	buildContext := gtx
	buildContext.Ops = &scene.operations
	result := GioResult{Bounds: make(map[string]image.Rectangle), Geometry: make(map[string]semantic.Geometry)}
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
		Bounds:   make(map[string]image.Rectangle, len(scene.geometries)),
		Geometry: make(map[string]semantic.Geometry, len(scene.geometries)),
	}
	for _, item := range scene.items {
		translation, viewportClip, clipped := sceneTransform(item.scrolls, state)
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
	for paintOrder, cached := range scene.geometries {
		translation, viewportClip, clipped := sceneTransform(cached.scrolls, state)
		geometry := cached.geometry
		geometry.Bounds = geometry.Bounds.Add(translation)
		geometry.Clip = geometry.Clip.Add(translation)
		if len(cached.scrolls) > 0 {
			geometry.Clip = geometry.Clip.Intersect(viewportClip)
		}
		if clipped {
			geometry.Clip = image.Rectangle{}
		}
		geometry.PaintOrder = paintOrder
		if cached.node != nil {
			geometry.Props = cloneMap(interactiveNodeForState(cached.node, state).Props)
		}
		result.Bounds[cached.handle] = geometry.Bounds
		result.Geometry[cached.handle] = geometry
	}
	result.Tree = semantic.Build(sceneRoot(scene), result.Geometry, semanticContext(state))
	return result
}

func sceneRoot(scene *gioScene) *project.Node {
	return scene.root
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
