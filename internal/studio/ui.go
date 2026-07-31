package studio

import (
	"context"
	"fmt"
	"image"
	"image/color"
	"math"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unicode"

	"gioui.org/app"
	"gioui.org/io/event"
	"gioui.org/io/key"
	"gioui.org/io/pointer"
	"gioui.org/io/system"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/text"
	"gioui.org/unit"
	"gioui.org/widget"
	"gioui.org/widget/material"

	"gora/internal/project"
	"gora/internal/render"
	"gora/internal/session"
)

type uiState struct {
	nextScreen        widget.Clickable
	widthEditor       widget.Editor
	heightEditor      widget.Editor
	zoomOut           widget.Clickable
	zoomIn            widget.Clickable
	inspect           widget.Clickable
	canvas            widget.Clickable
	capture           widget.Clickable
	output            widget.Editor
	zoomValue         float32
	zoomInitialized   bool
	inspecting        bool
	selected          string
	selectedHandle    string
	status            string
	zoomInput         struct{}
	zoomScrolling     bool
	zoomBlockUntil    time.Time
	canvasViewport    image.Point
	canvasSize        image.Point
	canvasPan         image.Point
	scrollbar         widget.Scrollbar
	inspections       []render.Inspection
	preview           render.GioCache
	checkerboard      checkerboardCache
	previewScroll     previewScrollbarModel
	previewScrollRoot *project.Node
}

type checkerboardCache struct {
	operations op.Ops
	call       op.CallOp
	size       image.Point
	tile       int
	valid      bool
	builds     int
}

// Start creates the Gio window and live session. Call app.Main after it returns.
func Start(root, entry, socketPath string) error {
	runtime, err := NewRuntime(root, entry)
	if err != nil {
		// An invalid initial document is still a valid Studio state.
		runtime = &Runtime{root: root, entry: entry, scroll: make(map[string]image.Point)}
		runtime.Reload()
	}
	window := new(app.Window)
	window.Option(app.Title("Gora Studio — "+filepath.Base(entry)), app.Size(unit.Dp(1200), unit.Dp(820)))
	server, err := session.Listen(socketPath, runtime.SessionHandler(func() {
		window.Perform(system.ActionRaise)
		window.Invalidate()
	}))
	if err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(context.Background())
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM)
	go func() {
		select {
		case <-signals:
			_ = server.Close()
			cancel()
			os.Exit(0)
		case <-ctx.Done():
		}
	}()
	go func() {
		_ = runtime.Watch(ctx, window.Invalidate)
	}()
	go eventLoop(window, runtime, server, func() {
		signal.Stop(signals)
		cancel()
	})
	return nil
}

func eventLoop(window *app.Window, runtime *Runtime, server *session.Server, cleanup func()) {
	defer cleanup()
	defer server.Close()
	theme := material.NewTheme()
	state := &uiState{zoomValue: 1}
	state.output.SingleLine = true
	state.output.Alignment = text.End
	state.output.SetText(filepath.Join(filepath.Dir(runtime.entry), "gora-capture.png"))
	for _, editor := range []*widget.Editor{&state.widthEditor, &state.heightEditor} {
		editor.SingleLine = true
		editor.Submit = true
		editor.Alignment = text.Middle
		editor.Filter = "0123456789"
		editor.MaxLen = 6
	}
	var operations op.Ops
	for {
		event := window.Event()
		switch event := event.(type) {
		case app.DestroyEvent:
			return
		case app.FrameEvent:
			context := app.NewContext(&operations, event)
			layoutStudio(context, theme, runtime, state, window)
			event.Frame(context.Ops)
		}
	}
}

func layoutStudio(gtx layout.Context, theme *material.Theme, runtime *Runtime, state *uiState, window *app.Window) layout.Dimensions {
	snapshot := runtime.Snapshot()
	handleActions(gtx, runtime, state, snapshot, window)
	snapshot = runtime.Snapshot()
	if !state.zoomInitialized && snapshot.Root != nil {
		scale := gtx.Metric.PxPerDp
		if scale <= 0 {
			scale = 1
		}
		available := image.Pt(
			int(float32(gtx.Constraints.Max.X)/scale),
			max(1, int(float32(gtx.Constraints.Max.Y)/scale)-110),
		)
		state.zoomValue = fitZoom(snapshot.Viewport, available)
		state.zoomInitialized = true
	}

	return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return layoutToolbar(gtx, theme, state, snapshot)
		}),
		layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
			paint.FillShape(gtx.Ops, color.NRGBA{R: 31, G: 34, B: 40, A: 255}, clip.Rect{Max: gtx.Constraints.Max}.Op())
			if snapshot.Root == nil {
				label := material.H6(theme, diagnosticsText(snapshot))
				label.Color = color.NRGBA{R: 255, G: 150, B: 150, A: 255}
				return layout.Center.Layout(gtx, label.Layout)
			}
			return layoutStudioCanvas(gtx, theme, runtime, state, snapshot, window)
		}),
	)
}

func layoutStudioCanvas(gtx layout.Context, theme *material.Theme, runtime *Runtime, state *uiState, snapshot Snapshot, window *app.Window) layout.Dimensions {
	viewport := gtx.Constraints.Max
	previewGtx := gtx
	previewGtx.Metric.PxPerDp *= state.zoomValue
	previewGtx.Metric.PxPerSp *= state.zoomValue
	size := image.Pt(
		previewGtx.Dp(unit.Dp(snapshot.Viewport.X)),
		previewGtx.Dp(unit.Dp(snapshot.Viewport.Y)),
	)
	oldHorizontalOverflow := max(0, state.canvasSize.X-state.canvasViewport.X)
	state.canvasViewport = viewport
	state.canvasSize = size
	horizontalOverflow := max(0, size.X-viewport.X)
	if oldHorizontalOverflow == 0 && horizontalOverflow > 0 {
		state.canvasPan.X = horizontalOverflow / 2
	}
	state.canvasPan.X = min(max(0, state.canvasPan.X), horizontalOverflow)
	state.canvasPan.Y = max(0, size.Y-viewport.Y) / 2

	hardClip := clip.Rect{Max: viewport}.Push(gtx.Ops)
	event.Op(gtx.Ops, &state.zoomInput)
	position := canvasPosition(viewport, size, state.canvasPan)
	offset := op.Offset(position).Push(gtx.Ops)
	previewGtx.Constraints = layout.Exact(size)
	var result render.GioResult
	state.canvas.Layout(previewGtx, func(gtx layout.Context) layout.Dimensions {
		state.checkerboard.paint(gtx.Ops, size, gtx.Dp(unit.Dp(8)))
		result = state.preview.Layout(
			gtx,
			theme,
			snapshot.Root,
			snapshot.Viewport,
			render.State{Scroll: snapshot.Scroll},
		)
		if state.inspecting && state.selectedHandle != "" {
			paintHighlight(gtx, result.Bounds[state.selectedHandle])
		}
		layoutPreviewScrollbar(gtx, theme, runtime, state, snapshot, result, window)
		return layout.Dimensions{Size: size}
	})
	state.inspections = result.Inspections
	offset.Pop()
	hardClip.Pop()
	return layout.Dimensions{Size: viewport}
}

func canvasPosition(viewport, content, pan image.Point) image.Point {
	position := image.Point{}
	if content.X <= viewport.X {
		position.X = (viewport.X - content.X) / 2
	} else {
		position.X = -min(max(0, pan.X), content.X-viewport.X)
	}
	if content.Y <= viewport.Y {
		position.Y = (viewport.Y - content.Y) / 2
	} else {
		position.Y = -min(max(0, pan.Y), content.Y-viewport.Y)
	}
	return position
}

func fitZoom(viewport, available image.Point) float32 {
	if viewport.X <= 0 || viewport.Y <= 0 || available.X <= 0 || available.Y <= 0 {
		return 1
	}
	widthScale := float32(available.X) / float32(viewport.X)
	heightScale := float32(available.Y) / float32(viewport.Y)
	return min(float32(1), min(widthScale, heightScale))
}

func layoutPreview(gtx layout.Context, theme *material.Theme, snapshot Snapshot) render.GioResult {
	return render.LayoutGio(gtx, theme, snapshot.Root, snapshot.Viewport, render.State{Scroll: snapshot.Scroll})
}

func paintCheckerboard(operations *op.Ops, size image.Point, tile int) {
	if tile <= 0 {
		tile = 8
	}
	colors := [2]color.NRGBA{
		{R: 225, G: 228, B: 234, A: 255},
		{R: 245, G: 246, B: 249, A: 255},
	}
	for y := 0; y < size.Y; y += tile {
		for x := 0; x < size.X; x += tile {
			index := (x/tile + y/tile) % 2
			rectangle := image.Rect(x, y, min(x+tile, size.X), min(y+tile, size.Y))
			paint.FillShape(operations, colors[index], clip.Rect(rectangle).Op())
		}
	}
}

func (cache *checkerboardCache) paint(operations *op.Ops, size image.Point, tile int) {
	if tile <= 0 {
		tile = 8
	}
	if !cache.valid || cache.size != size || cache.tile != tile {
		cache.operations = op.Ops{}
		recording := op.Record(&cache.operations)
		paintCheckerboard(&cache.operations, size, tile)
		cache.call = recording.Stop()
		cache.size = size
		cache.tile = tile
		cache.valid = true
		cache.builds++
	}
	cache.call.Add(operations)
}

func paintHighlight(gtx layout.Context, bounds image.Rectangle) {
	if bounds.Empty() {
		return
	}
	pixelBounds := image.Rect(
		gtx.Dp(unit.Dp(bounds.Min.X)),
		gtx.Dp(unit.Dp(bounds.Min.Y)),
		gtx.Dp(unit.Dp(bounds.Max.X)),
		gtx.Dp(unit.Dp(bounds.Max.Y)),
	)
	value := color.NRGBA{R: 34, G: 135, B: 255, A: 255}
	const thickness = 2
	paint.FillShape(gtx.Ops, value, clip.Rect(image.Rect(pixelBounds.Min.X, pixelBounds.Min.Y, pixelBounds.Max.X, pixelBounds.Min.Y+thickness)).Op())
	paint.FillShape(gtx.Ops, value, clip.Rect(image.Rect(pixelBounds.Min.X, pixelBounds.Max.Y-thickness, pixelBounds.Max.X, pixelBounds.Max.Y)).Op())
	paint.FillShape(gtx.Ops, value, clip.Rect(image.Rect(pixelBounds.Min.X, pixelBounds.Min.Y, pixelBounds.Min.X+thickness, pixelBounds.Max.Y)).Op())
	paint.FillShape(gtx.Ops, value, clip.Rect(image.Rect(pixelBounds.Max.X-thickness, pixelBounds.Min.Y, pixelBounds.Max.X, pixelBounds.Max.Y)).Op())
}

type previewScrollbarModel struct {
	key         string
	axis        string
	bounds      image.Rectangle
	contentSize int
	viewport    int
	offset      int
	start       float32
	end         float32
}

func previewScrollbar(snapshot Snapshot, result render.GioResult) (previewScrollbarModel, bool) {
	var scrollNode *project.Node
	var find func(*project.Node)
	find = func(node *project.Node) {
		if node == nil || scrollNode != nil {
			return
		}
		if node.Type == "scroll" && boolProp(node.Props["scrollbar"]) && len(node.Children) == 1 {
			scrollNode = node
			return
		}
		for _, child := range node.Children {
			find(child)
		}
	}
	find(snapshot.Root)
	if scrollNode == nil {
		return previewScrollbarModel{}, false
	}
	bounds, ok := result.Bounds[scrollNode.Handle]
	if !ok || bounds.Empty() {
		return previewScrollbarModel{}, false
	}
	axis := stringProp(scrollNode.Props["axis"], "vertical")
	key := scrollNode.Name
	if key == "" {
		key = scrollNode.Handle
	}
	viewport := bounds.Dy()
	contentSize := max(viewport, intProp(scrollNode.Children[0].Props["height"], viewport))
	offset := snapshot.Scroll[key].Y
	if axis == "horizontal" {
		viewport = bounds.Dx()
		contentSize = max(viewport, intProp(scrollNode.Children[0].Props["width"], viewport))
		offset = snapshot.Scroll[key].X
	}
	offset = min(max(0, offset), max(0, contentSize-viewport))
	if contentSize <= viewport {
		return previewScrollbarModel{}, false
	}
	return previewScrollbarModel{
		key: key, axis: axis, bounds: bounds, contentSize: contentSize,
		viewport: viewport, offset: offset,
		start: float32(offset) / float32(contentSize),
		end:   float32(offset+viewport) / float32(contentSize),
	}, true
}

func (model previewScrollbarModel) offsetAfter(normalizedDelta float32) int {
	offset := model.offset + int(math.Round(float64(normalizedDelta*float32(model.contentSize))))
	return min(max(0, offset), max(0, model.contentSize-model.viewport))
}

func (model previewScrollbarModel) offsetBy(delta int) int {
	return min(max(0, model.offset+delta), max(0, model.contentSize-model.viewport))
}

func layoutPreviewScrollbar(
	gtx layout.Context,
	theme *material.Theme,
	runtime *Runtime,
	state *uiState,
	snapshot Snapshot,
	result render.GioResult,
	window *app.Window,
) {
	model, ok := previewScrollbar(snapshot, result)
	if !ok {
		state.previewScroll = previewScrollbarModel{}
		state.previewScrollRoot = nil
		return
	}
	state.previewScroll = model
	state.previewScrollRoot = snapshot.Root
	style := material.Scrollbar(theme, &state.scrollbar)
	style.Indicator.MinorWidth = unit.Dp(8)
	style.Indicator.CornerRadius = unit.Dp(4)
	style.Track.MinorPadding = unit.Dp(4)
	trackWidth := gtx.Dp(style.Width())
	pixelBounds := image.Rect(
		gtx.Dp(unit.Dp(model.bounds.Min.X)),
		gtx.Dp(unit.Dp(model.bounds.Min.Y)),
		gtx.Dp(unit.Dp(model.bounds.Max.X)),
		gtx.Dp(unit.Dp(model.bounds.Max.Y)),
	)
	axis := layout.Vertical
	position := image.Pt(pixelBounds.Max.X-trackWidth, pixelBounds.Min.Y)
	size := image.Pt(trackWidth, pixelBounds.Dy())
	if model.axis == "horizontal" {
		axis = layout.Horizontal
		position = image.Pt(pixelBounds.Min.X, pixelBounds.Max.Y-trackWidth)
		size = image.Pt(pixelBounds.Dx(), trackWidth)
	}
	offset := op.Offset(position).Push(gtx.Ops)
	scrollbarGtx := gtx
	scrollbarGtx.Constraints = layout.Exact(size)
	style.Layout(scrollbarGtx, axis, model.start, model.end)
	offset.Pop()
	if delta := state.scrollbar.ScrollDistance(); delta != 0 {
		runtime.SetScrollOffset(model.key, model.axis, model.offsetAfter(delta))
		window.Invalidate()
	}
}

func handleActions(gtx layout.Context, runtime *Runtime, state *uiState, snapshot Snapshot, window *app.Window) {
	zoomScroll, horizontalScroll, verticalScroll := canvasScrollEvents(gtx, state)
	axis, scroll := dominantPageScroll(horizontalScroll, verticalScroll)
	blockPageScroll := state.blockPageScroll(gtx.Now, zoomScroll, scroll)
	if zoomScroll != 0 {
		state.zoomValue = zoomAfterTrackpadScroll(state.zoomValue, zoomScroll)
	}
	if !blockPageScroll && scroll != 0 {
		if !state.panCanvas(axis, scroll) {
			scale := state.zoomValue * gtx.Metric.PxPerDp
			logicalScroll := int(math.Round(float64(float32(scroll) / scale)))
			if logicalScroll == 0 {
				logicalScroll = 1
				if scroll < 0 {
					logicalScroll = -1
				}
			}
			scrollDocument(runtime, state, snapshot, axis, logicalScroll)
		}
	}
	if state.nextScreen.Clicked(gtx) && len(snapshot.Screens) > 0 {
		index := 0
		for i, name := range snapshot.Screens {
			if name == snapshot.Screen {
				index = (i + 1) % len(snapshot.Screens)
				break
			}
		}
		runtime.SelectScreen(snapshot.Screens[index])
	}
	if viewportSubmitted(gtx, state) {
		viewport, err := viewportFromEditors(state)
		if err != nil {
			state.status = err.Error()
		} else {
			runtime.SetViewport(viewport.X, viewport.Y)
		}
	}
	if state.zoomOut.Clicked(gtx) {
		state.zoomValue = zoomByStep(state.zoomValue, -1)
	}
	if state.zoomIn.Clicked(gtx) {
		state.zoomValue = zoomByStep(state.zoomValue, 1)
	}
	if state.inspect.Clicked(gtx) {
		state.inspecting = !state.inspecting
		state.selected = ""
		state.selectedHandle = ""
	}
	if state.canvas.Clicked(gtx) && state.inspecting && snapshot.Root != nil {
		history := state.canvas.History()
		if len(history) != 0 {
			position := history[len(history)-1].Position
			scale := state.zoomValue * gtx.Metric.PxPerDp
			point := image.Pt(int(float32(position.X)/scale), int(float32(position.Y)/scale))
			for index := len(state.inspections) - 1; index >= 0; index-- {
				inspection := state.inspections[index]
				if point.In(inspection.Bounds.Intersect(inspection.Clip)) {
					state.selectedHandle = inspection.Handle
					state.selected = fmt.Sprintf("%s %q bounds=%v clip=%v props=%v · %s:%d · %v",
						inspection.Type, inspection.Name, inspection.Bounds,
						inspection.Clip, inspection.Props,
						filepath.Base(inspection.Source), inspection.Line, inspection.Breadcrumb)
					break
				}
			}
		}
	}
	if state.capture.Clicked(gtx) {
		warning, err := runtime.Capture(state.output.Text(), 1)
		if err != nil {
			state.status = err.Error()
		} else if warning != "" {
			state.status = warning
		} else {
			state.status = "captured " + state.output.Text()
		}
	}
	_ = window
}

func scrollDocument(runtime *Runtime, state *uiState, snapshot Snapshot, axis string, delta int) {
	model := state.previewScroll
	if state.previewScrollRoot == snapshot.Root && model.key != "" && model.axis == axis {
		runtime.SetScrollOffset(model.key, model.axis, model.offsetBy(delta))
		return
	}
	runtime.ScrollAxis(axis, delta)
}

func (state *uiState) panCanvas(axis string, delta float32) bool {
	if axis != "horizontal" {
		return false
	}
	maximum := max(0, state.canvasSize.X-state.canvasViewport.X)
	if maximum == 0 {
		state.canvasPan.X = 0
		return false
	}
	movement := int(math.Round(float64(delta)))
	if movement == 0 {
		movement = 1
		if delta < 0 {
			movement = -1
		}
	}
	state.canvasPan.X = min(max(0, state.canvasPan.X+movement), maximum)
	return true
}

func (state *uiState) blockPageScroll(now time.Time, zoomScroll, pageScroll float32) bool {
	const quietPeriod = 150 * time.Millisecond
	if zoomScroll != 0 {
		state.zoomScrolling = true
		state.zoomBlockUntil = now.Add(quietPeriod)
		return true
	}
	if !state.zoomScrolling {
		return false
	}
	if !now.Before(state.zoomBlockUntil) {
		state.zoomScrolling = false
		return false
	}
	if pageScroll != 0 {
		state.zoomBlockUntil = now.Add(quietPeriod)
		return true
	}
	return true
}

func trackpadZoomScroll(gtx layout.Context, state *uiState) float32 {
	zoom, _, _ := canvasScrollEvents(gtx, state)
	return zoom
}

func canvasScrollEvents(gtx layout.Context, state *uiState) (zoom, horizontal, vertical float32) {
	filter := trackpadZoomFilter(state)
	for {
		rawEvent, ok := gtx.Event(filter)
		if !ok {
			return zoom, horizontal, vertical
		}
		scrollEvent, ok := rawEvent.(pointer.Event)
		if !ok {
			continue
		}
		if isTrackpadZoom(scrollEvent.Modifiers) {
			zoom += trackpadZoomDelta(scrollEvent)
			continue
		}
		axis, delta := canvasScrollDelta(scrollEvent)
		if axis == "horizontal" {
			horizontal += delta
		} else {
			vertical += delta
		}
	}
}

func trackpadZoomDelta(scrollEvent pointer.Event) float32 {
	return scrollEvent.Scroll.Y
}

func canvasScrollDelta(scrollEvent pointer.Event) (string, float32) {
	if math.Abs(float64(scrollEvent.Scroll.X)) > math.Abs(float64(scrollEvent.Scroll.Y)) {
		return "horizontal", scrollEvent.Scroll.X
	}
	return "vertical", scrollEvent.Scroll.Y
}

func dominantPageScroll(horizontal, vertical float32) (string, float32) {
	if math.Abs(float64(horizontal)) > math.Abs(float64(vertical)) {
		return "horizontal", horizontal
	}
	return "vertical", vertical
}

func trackpadZoomFilter(state *uiState) pointer.Filter {
	scrollRange := pointer.ScrollRange{Min: -100000, Max: 100000}
	return pointer.Filter{
		Target:  &state.zoomInput,
		Kinds:   pointer.Scroll,
		ScrollX: scrollRange,
		ScrollY: scrollRange,
	}
}

func isTrackpadZoom(modifiers key.Modifiers) bool {
	return modifiers.Contain(key.ModShortcut)
}

func zoomAfterTrackpadScroll(current, delta float32) float32 {
	const sensitivity = .002
	zoom := current * float32(math.Exp(float64(-delta*sensitivity)))
	return min(max(zoom, minStudioZoom), maxStudioZoom)
}

func zoomByStep(current float32, direction int) float32 {
	const step = float32(.1)
	return min(max(current+float32(direction)*step, minStudioZoom), maxStudioZoom)
}

func viewportSubmitted(gtx layout.Context, state *uiState) bool {
	submitted := false
	for _, editor := range []*widget.Editor{&state.widthEditor, &state.heightEditor} {
		for {
			event, ok := editor.Update(gtx)
			if !ok {
				break
			}
			if _, ok := event.(widget.SubmitEvent); ok {
				submitted = true
			}
		}
	}
	return submitted
}

func viewportFromEditors(state *uiState) (image.Point, error) {
	width, widthErr := strconv.Atoi(state.widthEditor.Text())
	height, heightErr := strconv.Atoi(state.heightEditor.Text())
	if widthErr != nil || heightErr != nil || width <= 0 || height <= 0 {
		return image.Point{}, fmt.Errorf("viewport width and height must be positive integers")
	}
	return image.Pt(width, height), nil
}

func boolProp(value any) bool {
	result, _ := value.(bool)
	return result
}

func stringProp(value any, fallback string) string {
	if result, ok := value.(string); ok {
		return result
	}
	return fallback
}

func intProp(value any, fallback int) int {
	switch value := value.(type) {
	case int:
		return value
	case int64:
		return int(value)
	case float64:
		return int(value)
	default:
		return fallback
	}
}

type toolbarModel struct {
	screen     string
	viewport   string
	zoom       string
	status     string
	valid      bool
	inspecting bool
}

const (
	minStudioZoom = float32(.25)
	maxStudioZoom = float32(4)
)

func makeToolbarModel(snapshot Snapshot, state *uiState) toolbarModel {
	status := "Valid"
	valid := len(snapshot.Diagnostics) == 0
	if !valid {
		status = fmt.Sprintf("%d issues", len(snapshot.Diagnostics))
	}
	return toolbarModel{
		screen:     displayName(emptyFallback(snapshot.Screen, "fixture")),
		viewport:   fmt.Sprintf("%d × %d", snapshot.Viewport.X, snapshot.Viewport.Y),
		zoom:       fmt.Sprintf("%.0f%%", state.zoomValue*100),
		status:     status,
		valid:      valid,
		inspecting: state.inspecting,
	}
}

func displayName(value string) string {
	value = strings.ReplaceAll(value, "_", " ")
	if value == "" {
		return value
	}
	runes := []rune(value)
	runes[0] = unicode.ToUpper(runes[0])
	return string(runes)
}

func layoutToolbar(gtx layout.Context, theme *material.Theme, state *uiState, snapshot Snapshot) layout.Dimensions {
	if !gtx.Focused(&state.widthEditor) {
		state.widthEditor.SetText(strconv.Itoa(snapshot.Viewport.X))
	}
	if !gtx.Focused(&state.heightEditor) {
		state.heightEditor.SetText(strconv.Itoa(snapshot.Viewport.Y))
	}
	model := makeToolbarModel(snapshot, state)
	background := color.NRGBA{R: 247, G: 248, B: 250, A: 255}
	inset := layout.Inset{Top: unit.Dp(10), Right: unit.Dp(14), Bottom: unit.Dp(10), Left: unit.Dp(14)}
	return layout.Background{}.Layout(gtx,
		func(gtx layout.Context) layout.Dimensions {
			size := gtx.Constraints.Min
			paint.FillShape(gtx.Ops, background, clip.Rect{Max: size}.Op())
			paint.FillShape(gtx.Ops, color.NRGBA{R: 224, G: 228, B: 235, A: 255},
				clip.Rect(image.Rect(0, max(0, size.Y-1), size.X, size.Y)).Op())
			return layout.Dimensions{Size: size}
		},
		func(gtx layout.Context) layout.Dimensions {
			return inset.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
				return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
					layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
							layout.Rigid(toolbarButton(theme, &state.nextScreen, model.screen, false)),
							layout.Rigid(horizontalSpace(18)),
							layout.Rigid(func(gtx layout.Context) layout.Dimensions {
								return layoutViewportControl(gtx, theme, state)
							}),
							layout.Rigid(horizontalSpace(18)),
							layout.Rigid(func(gtx layout.Context) layout.Dimensions {
								return layoutZoomControl(gtx, theme, state, model.zoom)
							}),
							layout.Rigid(horizontalSpace(8)),
							layout.Rigid(toolbarButton(theme, &state.inspect, "Inspect", model.inspecting)),
							layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
								return layout.Dimensions{Size: gtx.Constraints.Min}
							}),
							layout.Rigid(func(gtx layout.Context) layout.Dimensions {
								return layoutStatusPill(gtx, theme, model)
							}),
						)
					}),
					layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						return layout.Spacer{Height: unit.Dp(8)}.Layout(gtx)
					}),
					layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
							layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
								return layoutPathField(gtx, theme, &state.output)
							}),
							layout.Rigid(func(gtx layout.Context) layout.Dimensions {
								message := state.status
								if state.selected != "" {
									message = state.selected
								}
								if message == "" {
									return layout.Dimensions{}
								}
								gtx.Constraints.Max.X = min(gtx.Constraints.Max.X, gtx.Dp(unit.Dp(280)))
								label := material.Caption(theme, message)
								label.Color = color.NRGBA{R: 99, G: 107, B: 122, A: 255}
								label.MaxLines = 1
								return layout.Inset{Left: unit.Dp(12), Right: unit.Dp(12)}.Layout(gtx, label.Layout)
							}),
							layout.Rigid(horizontalSpace(8)),
							layout.Rigid(primaryButton(theme, &state.capture, "Capture")),
						)
					}),
				)
			})
		},
	)
}

func layoutZoomControl(gtx layout.Context, theme *material.Theme, state *uiState, value string) layout.Dimensions {
	return widget.Border{
		Color: color.NRGBA{R: 216, G: 221, B: 230, A: 255}, CornerRadius: unit.Dp(8), Width: unit.Dp(1),
	}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		return layout.Background{}.Layout(gtx,
			func(gtx layout.Context) layout.Dimensions {
				size := gtx.Constraints.Min
				paint.FillShape(gtx.Ops, color.NRGBA{R: 255, G: 255, B: 255, A: 255},
					clip.UniformRRect(image.Rectangle{Max: size}, gtx.Dp(unit.Dp(8))).Op(gtx.Ops))
				return layout.Dimensions{Size: size}
			},
			func(gtx layout.Context) layout.Dimensions {
				return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
					layout.Rigid(zoomStepButton(theme, &state.zoomOut, "−")),
					layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						width := gtx.Dp(unit.Dp(52))
						gtx.Constraints = layout.Exact(image.Pt(width, gtx.Dp(unit.Dp(34))))
						label := material.Body2(theme, value)
						label.Color = color.NRGBA{R: 42, G: 48, B: 60, A: 255}
						return layout.Center.Layout(gtx, label.Layout)
					}),
					layout.Rigid(zoomStepButton(theme, &state.zoomIn, "+")),
				)
			},
		)
	})
}

func zoomStepButton(theme *material.Theme, clickable *widget.Clickable, label string) layout.Widget {
	return func(gtx layout.Context) layout.Dimensions {
		button := material.Button(theme, clickable, label)
		button.TextSize = unit.Sp(16)
		button.CornerRadius = unit.Dp(6)
		button.Inset = layout.Inset{Top: unit.Dp(6), Right: unit.Dp(10), Bottom: unit.Dp(6), Left: unit.Dp(10)}
		button.Background = color.NRGBA{R: 235, G: 238, B: 243, A: 255}
		button.Color = color.NRGBA{R: 42, G: 48, B: 60, A: 255}
		return button.Layout(gtx)
	}
}

func horizontalSpace(width unit.Dp) layout.Widget {
	return func(gtx layout.Context) layout.Dimensions {
		return layout.Spacer{Width: width}.Layout(gtx)
	}
}

func toolbarButton(theme *material.Theme, clickable *widget.Clickable, label string, selected bool) layout.Widget {
	return func(gtx layout.Context) layout.Dimensions {
		button := material.Button(theme, clickable, label)
		button.TextSize = unit.Sp(14)
		button.CornerRadius = unit.Dp(8)
		button.Inset = layout.Inset{Top: unit.Dp(8), Right: unit.Dp(12), Bottom: unit.Dp(8), Left: unit.Dp(12)}
		button.Background = color.NRGBA{R: 235, G: 238, B: 243, A: 255}
		button.Color = color.NRGBA{R: 42, G: 48, B: 60, A: 255}
		if selected {
			button.Background = color.NRGBA{R: 229, G: 228, B: 255, A: 255}
			button.Color = color.NRGBA{R: 75, G: 68, B: 210, A: 255}
		}
		return button.Layout(gtx)
	}
}

func primaryButton(theme *material.Theme, clickable *widget.Clickable, label string) layout.Widget {
	return func(gtx layout.Context) layout.Dimensions {
		button := material.Button(theme, clickable, label)
		button.TextSize = unit.Sp(14)
		button.CornerRadius = unit.Dp(8)
		button.Inset = layout.Inset{Top: unit.Dp(9), Right: unit.Dp(16), Bottom: unit.Dp(9), Left: unit.Dp(16)}
		button.Background = color.NRGBA{R: 91, G: 83, B: 232, A: 255}
		button.Color = color.NRGBA{R: 255, G: 255, B: 255, A: 255}
		return button.Layout(gtx)
	}
}

func layoutViewportControl(gtx layout.Context, theme *material.Theme, state *uiState) layout.Dimensions {
	background := func(gtx layout.Context) layout.Dimensions {
		size := gtx.Constraints.Min
		paint.FillShape(gtx.Ops, color.NRGBA{R: 255, G: 255, B: 255, A: 255},
			clip.UniformRRect(image.Rectangle{Max: size}, gtx.Dp(unit.Dp(8))).Op(gtx.Ops))
		return layout.Dimensions{Size: size}
	}
	content := func(gtx layout.Context) layout.Dimensions {
		return layout.Inset{Top: unit.Dp(7), Right: unit.Dp(10), Bottom: unit.Dp(7), Left: unit.Dp(10)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
			return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
				layout.Rigid(viewportEditor(theme, &state.widthEditor, "W")),
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					label := material.Body2(theme, "×")
					label.Color = color.NRGBA{R: 137, G: 144, B: 157, A: 255}
					return layout.Inset{Left: unit.Dp(5), Right: unit.Dp(5)}.Layout(gtx, label.Layout)
				}),
				layout.Rigid(viewportEditor(theme, &state.heightEditor, "H")),
			)
		})
	}
	return widget.Border{
		Color: color.NRGBA{R: 216, G: 221, B: 230, A: 255}, CornerRadius: unit.Dp(8), Width: unit.Dp(1),
	}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		return layout.Background{}.Layout(gtx, background, content)
	})
}

func viewportEditor(theme *material.Theme, editor *widget.Editor, hint string) layout.Widget {
	return func(gtx layout.Context) layout.Dimensions {
		width := gtx.Dp(unit.Dp(48))
		gtx.Constraints.Min.X = width
		gtx.Constraints.Max.X = width
		style := material.Editor(theme, editor, hint)
		style.TextSize = unit.Sp(14)
		style.Color = color.NRGBA{R: 42, G: 48, B: 60, A: 255}
		style.HintColor = color.NRGBA{R: 137, G: 144, B: 157, A: 255}
		return style.Layout(gtx)
	}
}

func layoutStatusPill(gtx layout.Context, theme *material.Theme, model toolbarModel) layout.Dimensions {
	background := color.NRGBA{R: 232, G: 247, B: 239, A: 255}
	foreground := color.NRGBA{R: 26, G: 124, B: 78, A: 255}
	if !model.valid {
		background = color.NRGBA{R: 255, G: 235, B: 235, A: 255}
		foreground = color.NRGBA{R: 180, G: 58, B: 58, A: 255}
	}
	return layout.Background{}.Layout(gtx,
		func(gtx layout.Context) layout.Dimensions {
			size := gtx.Constraints.Min
			paint.FillShape(gtx.Ops, background,
				clip.UniformRRect(image.Rectangle{Max: size}, gtx.Dp(unit.Dp(10))).Op(gtx.Ops))
			return layout.Dimensions{Size: size}
		},
		func(gtx layout.Context) layout.Dimensions {
			label := material.Caption(theme, model.status)
			label.Color = foreground
			return layout.Inset{Top: unit.Dp(5), Right: unit.Dp(9), Bottom: unit.Dp(5), Left: unit.Dp(9)}.Layout(gtx, label.Layout)
		},
	)
}

func layoutPathField(gtx layout.Context, theme *material.Theme, editor *widget.Editor) layout.Dimensions {
	return widget.Border{
		Color: color.NRGBA{R: 222, G: 226, B: 234, A: 255}, CornerRadius: unit.Dp(8), Width: unit.Dp(1),
	}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		return layout.Background{}.Layout(gtx,
			func(gtx layout.Context) layout.Dimensions {
				size := gtx.Constraints.Min
				paint.FillShape(gtx.Ops, color.NRGBA{R: 240, G: 242, B: 246, A: 255},
					clip.UniformRRect(image.Rectangle{Max: size}, gtx.Dp(unit.Dp(8))).Op(gtx.Ops))
				return layout.Dimensions{Size: size}
			},
			func(gtx layout.Context) layout.Dimensions {
				return layout.Inset{Top: unit.Dp(8), Right: unit.Dp(11), Bottom: unit.Dp(8), Left: unit.Dp(11)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
					style := material.Editor(theme, editor, "Capture path…")
					style.TextSize = unit.Sp(13)
					style.Color = color.NRGBA{R: 91, G: 99, B: 113, A: 255}
					style.HintColor = color.NRGBA{R: 137, G: 144, B: 157, A: 255}
					return style.Layout(gtx)
				})
			},
		)
	})
}

func diagnosticsText(snapshot Snapshot) string {
	if len(snapshot.Diagnostics) == 0 {
		return "valid"
	}
	return fmt.Sprintf("%d diagnostic(s): %s", len(snapshot.Diagnostics), snapshot.Diagnostics[0].Message)
}

func emptyFallback(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}
