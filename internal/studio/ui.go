package studio

import (
	"context"
	"fmt"
	"image"
	"image/color"
	"io"
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
	"gioui.org/f32"
	"gioui.org/io/clipboard"
	"gioui.org/io/event"
	"gioui.org/io/key"
	"gioui.org/io/pointer"
	"gioui.org/io/system"
	"gioui.org/io/transfer"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/text"
	"gioui.org/unit"
	"gioui.org/widget"
	"gioui.org/widget/material"

	"gora/internal/interaction"
	"gora/internal/project"
	"gora/internal/render"
	"gora/internal/scrollinput"
	"gora/internal/semantic"
	"gora/internal/session"
)

const fieldClipboardMIME = "application/text"

type uiState struct {
	nextScreen        widget.Clickable
	widthEditor       widget.Editor
	heightEditor      widget.Editor
	zoomOut           widget.Clickable
	zoomIn            widget.Clickable
	inspect           widget.Clickable
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
	runtimeTree       *semantic.Node
	preview           render.GioCache
	checkerboard      checkerboardCache
	previewScroll     previewScrollbarModel
	previewScrollRoot *project.Node
	router            *interaction.Router
	interactionInput  struct{}
	inspectPointerID  int
	inspectPressed    bool
	inspectPending    bool
	inspectPoint      image.Point
	resetState        widget.Clickable
	fieldPointerID    int
	fieldSelectionID  string
	fieldAnchor       int
	lastFieldClick    time.Duration
	lastFieldClickID  string
	caretBlinkStart   time.Time
	fieldClickCount   int
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
		runtime = &Runtime{root: root, entry: entry, scroll: make(map[string]image.Point), state: interaction.NewStore()}
		runtime.Reload()
	}
	window := new(app.Window)
	window.Option(app.Title("Gora Studio — "+filepath.Base(entry)), app.Size(unit.Dp(1200), unit.Dp(820)))
	server, err := session.Listen(socketPath, runtime.SessionHandler("studio", func() {
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
	state := &uiState{zoomValue: 1, router: interaction.NewRouter()}
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
	state.router.SyncTransient(snapshot.Transient)
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
	event.Op(gtx.Ops, &state.interactionInput)
	if field := focusedTextField(state); field != nil {
		hint := key.HintText
		if _, ok := field.Props["committed"].(float64); ok {
			hint = key.HintNumeric
		}
		key.InputHintOp{Tag: &state.interactionInput, Hint: hint}.Add(gtx.Ops)
		gtx.Execute(op.InvalidateCmd{At: frameTime(gtx.Now).Add(500 * time.Millisecond)})
	}
	position := canvasPosition(viewport, size, state.canvasPan)
	offset := op.Offset(position).Push(gtx.Ops)
	previewGtx.Constraints = layout.Exact(size)
	state.checkerboard.paint(gtx.Ops, size, previewGtx.Dp(unit.Dp(8)))
	result := state.preview.Layout(
		previewGtx,
		theme,
		snapshot.Root,
		snapshot.Viewport,
		liveRenderState(snapshot, gtx.Now, state.caretBlinkStart),
	)
	if state.inspecting && state.selectedHandle != "" {
		paintHighlight(previewGtx, result.Bounds[state.selectedHandle])
	}
	state.runtimeTree = result.Tree
	runtime.PublishFrame(result.Tree, result.Scroll)
	if state.router == nil {
		state.router = interaction.NewRouter()
	}
	state.router.Update(state.runtimeTree)
	if state.router.Transient() != snapshot.Transient {
		runtime.SetTransient(state.router.Transient())
		window.Invalidate()
	}
	runtime.PublishRouterSnapshot(state.router.Snapshot())
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
	return render.LayoutGio(gtx, theme, snapshot.Root, snapshot.Viewport, renderState(snapshot))
}

func renderState(snapshot Snapshot) render.State {
	return render.State{
		Screen: snapshot.Screen, Scroll: snapshot.Scroll, Values: snapshot.StateValues,
		Hovered: snapshot.Transient.Hovered, Pressed: snapshot.Transient.Pressed, Focused: snapshot.Transient.Focused,
		OpenSelect: snapshot.Transient.OpenSelect, ActiveOption: snapshot.Transient.ActiveOption,
	}
}

func liveRenderState(snapshot Snapshot, now, caretBlinkStart time.Time) render.State {
	state := renderState(snapshot)
	now = frameTime(now)
	if snapshot.Transient.Focused == "" {
		return state
	}
	if !caretBlinkStart.IsZero() {
		elapsed := now.Sub(caretBlinkStart)
		if elapsed < 0 {
			elapsed = 0
		}
		state.CaretHidden = (elapsed/(500*time.Millisecond))%2 == 1
		return state
	}
	state.CaretHidden = (now.UnixMilli()/500)%2 == 1
	return state
}

func frameTime(now time.Time) time.Time {
	if now.IsZero() {
		return time.Now()
	}
	return now
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
	key := project.ScrollKey(scrollNode)
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
	if state.router == nil {
		state.router = interaction.NewRouter()
	}
	handleDocumentInteraction(gtx, runtime, state, window)
	scrollEvents := collectCanvasScrollEvents(gtx, state)
	blockPageScroll := state.blockPageScroll(gtx.Now, scrollEvents.zoom, float32(len(scrollEvents.events)))
	if scrollEvents.zoom != 0 {
		state.zoomValue = zoomAfterTrackpadScroll(state.zoomValue, scrollEvents.zoom)
	}
	if !blockPageScroll && !state.router.ScrollbarPointerOwned() && len(scrollEvents.events) != 0 {
		scale := state.zoomValue * gtx.Metric.PxPerDp
		if routeScrollEvents(runtime, state, snapshot, scrollEvents.events, scale, func(delta float32) bool {
			return state.panCanvas("horizontal", delta)
		}) {
			window.Invalidate()
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
		state.inspectPressed = false
		state.inspectPending = false
		clearFieldSelectionOwnership(state)
		state.router.SetInspecting(state.inspecting)
		runtime.SetTransient(state.router.Transient())
	}
	if state.resetState.Clicked(gtx) {
		runtime.ResetState()
		state.router.Update(nil)
		state.status = "state reset"
	}
	if state.inspectPending && state.inspecting && snapshot.Root != nil {
		state.inspectPending = false
		node := semantic.TopmostAt(state.runtimeTree, state.inspectPoint, func(node *semantic.Node) bool {
			return node.Visible
		})
		if node != nil {
			state.selectedHandle = node.Handle
			state.selected = fmt.Sprintf("%s %q role=%s label=%q enabled=%t hovered=%t pressed=%t focused=%t scope=%s state=%v actions=%v bounds=%v clip=%v props=%v · %s:%d · %v",
				node.Type, node.Name,
				node.Role, node.Label, node.Enabled, node.Hovered, node.Pressed, node.Focused,
				node.Scope, node.State, node.Effects, node.Bounds.ImageRectangle(), node.Clip.ImageRectangle(), node.Props,
				filepath.Base(node.Source.File), node.Source.Line, node.Breadcrumb)
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

func clearFieldSelectionOwnership(state *uiState) {
	if state == nil {
		return
	}
	state.fieldPointerID = 0
	state.fieldSelectionID = ""
	state.fieldAnchor = 0
}

func handleDocumentInteraction(gtx layout.Context, runtime *Runtime, state *uiState, window *app.Window) {
	before := state.router.Transient()
	mutated := false
	if field := focusedTextField(state); field != nil {
		text, _ := field.Value.(string)
		start, end, _ := runtime.FieldRuneSelection(field.ID)
		gtx.Execute(key.SnippetCmd{Tag: &state.interactionInput, Snippet: key.Snippet{Range: key.Range{Start: 0, End: len([]rune(text))}, Text: text}})
		gtx.Execute(key.SelectionCmd{Tag: &state.interactionInput, Range: key.Range{Start: start, End: end}})
	}
	pointerFilter := pointer.Filter{
		Target: &state.interactionInput,
		Kinds:  pointer.Enter | pointer.Leave | pointer.Move | pointer.Drag | pointer.Press | pointer.Release | pointer.Cancel,
	}
	for {
		raw, ok := gtx.Event(pointerFilter)
		if !ok {
			break
		}
		event, ok := raw.(pointer.Event)
		if !ok {
			continue
		}
		point := documentPoint(state, gtx.Metric, event.Position)
		touch := event.Source == pointer.Touch
		source := "mouse"
		if touch {
			source = "touch"
		}
		state.router.SetPointerMetadata(source, int(event.Buttons), point)
		switch event.Kind {
		case pointer.Enter, pointer.Move, pointer.Drag:
			state.router.MovePointer(int(event.PointerID), point, touch)
			if event.Kind == pointer.Drag && state.fieldSelectionID != "" && state.fieldPointerID == int(event.PointerID) {
				if field := semanticNodeByID(state.runtimeTree, state.fieldSelectionID); field != nil {
					caret := fieldRuneAtPoint(field, point)
					if err := runtime.SetFieldSelection(field.ID, state.fieldAnchor, caret); err == nil {
						mutated = true
					}
				}
			}
		case pointer.Leave:
			state.router.MovePointer(int(event.PointerID), image.Pt(-1, -1), touch)
		case pointer.Press:
			if touch || event.Buttons.Contain(pointer.ButtonPrimary) {
				if state.inspecting {
					state.inspectPointerID = int(event.PointerID)
					state.inspectPressed = true
					gtx.Execute(pointer.GrabCmd{Tag: &state.interactionInput, ID: event.PointerID})
					continue
				}
				if state.router.Press(int(event.PointerID), point) {
					if state.router.ScrollbarPointerOwned() {
						gtx.Execute(pointer.GrabCmd{Tag: &state.interactionInput, ID: event.PointerID})
						gtx.Execute(key.FocusCmd{Tag: &state.interactionInput})
						continue
					}
					if field := hitTextField(state.runtimeTree, point); fieldAllowsPointerSelection(field) {
						caret := fieldRuneAtPoint(field, point)
						if state.lastFieldClickID == field.ID && event.Time-state.lastFieldClick <= 400*time.Millisecond {
							state.fieldClickCount++
						} else {
							state.fieldClickCount = 1
						}
						state.lastFieldClick = event.Time
						state.lastFieldClickID = field.ID
						start, end := caret, caret
						if event.Modifiers.Contain(key.ModShift) {
							start, _, _ = runtime.FieldRuneSelection(field.ID)
						} else if state.fieldClickCount == 2 {
							start, end = wordRuneRange(fmt.Sprint(field.Value), caret)
						} else if state.fieldClickCount >= 3 {
							start, end = fieldVisualLineRange(field, caret)
							state.fieldClickCount = 0
						}
						_ = runtime.SetFieldSelection(field.ID, start, end)
						state.fieldPointerID = int(event.PointerID)
						state.fieldSelectionID = field.ID
						state.fieldAnchor = start
						mutated = true
					}
					gtx.Execute(pointer.GrabCmd{Tag: &state.interactionInput, ID: event.PointerID})
					gtx.Execute(key.FocusCmd{Tag: &state.interactionInput})
				}
			}
		case pointer.Release:
			if state.fieldPointerID == int(event.PointerID) {
				state.fieldSelectionID = ""
			}
			if state.inspecting {
				if state.inspectPressed && state.inspectPointerID == int(event.PointerID) {
					state.inspectPressed = false
					state.inspectPoint = point
					state.inspectPending = true
				}
				continue
			}
			if activation, activated := state.router.Release(int(event.PointerID), point); activated {
				if err := runtime.Activate(activation); err != nil {
					state.status = err.Error()
				} else {
					mutated = true
				}
			}
		case pointer.Cancel:
			if state.fieldPointerID == int(event.PointerID) {
				state.fieldSelectionID = ""
			}
			if state.inspecting && state.inspectPointerID == int(event.PointerID) {
				state.inspectPressed = false
				continue
			}
			state.router.Cancel(int(event.PointerID))
		}
	}

	movementModifiers := key.ModShift | key.ModAlt | key.ModCtrl | key.ModShortcut
	filters := []key.Filter{
		{Focus: &state.interactionInput, Name: key.NameTab, Optional: key.ModShift},
		{Focus: &state.interactionInput, Name: key.NameReturn, Optional: key.ModShortcut},
		{Focus: &state.interactionInput, Name: key.NameEnter, Optional: key.ModShortcut},
		{Focus: &state.interactionInput, Name: key.NameSpace},
		{Focus: &state.interactionInput, Name: key.NameEscape},
		{Focus: &state.interactionInput, Name: key.NameLeftArrow, Optional: movementModifiers},
		{Focus: &state.interactionInput, Name: key.NameRightArrow, Optional: movementModifiers},
		{Focus: &state.interactionInput, Name: key.NameUpArrow, Optional: movementModifiers},
		{Focus: &state.interactionInput, Name: key.NameDownArrow, Optional: movementModifiers},
		{Focus: &state.interactionInput, Name: key.NameHome, Optional: movementModifiers},
		{Focus: &state.interactionInput, Name: key.NameEnd, Optional: movementModifiers},
		{Focus: &state.interactionInput, Name: key.NameDeleteBackward, Optional: movementModifiers},
		{Focus: &state.interactionInput, Name: key.NameDeleteForward, Optional: movementModifiers},
		{Focus: &state.interactionInput, Name: key.NamePageUp},
		{Focus: &state.interactionInput, Name: key.NamePageDown},
		{Focus: &state.interactionInput, Name: key.Name("C"), Required: key.ModShortcut},
		{Focus: &state.interactionInput, Name: key.Name("X"), Required: key.ModShortcut},
		{Focus: &state.interactionInput, Name: key.Name("V"), Required: key.ModShortcut},
		{Focus: &state.interactionInput, Name: key.Name("A"), Required: key.ModShortcut},
		{Focus: &state.interactionInput, Name: key.Name("Z"), Required: key.ModShortcut, Optional: key.ModShift},
		{Focus: &state.interactionInput, Name: key.Name("Y"), Required: key.ModShortcut},
	}
	for _, filter := range filters {
		for {
			raw, ok := gtx.Event(filter)
			if !ok {
				break
			}
			event, ok := raw.(key.Event)
			if !ok {
				continue
			}
			if event.Name == key.NameTab && event.State == key.Press {
				if field := focusedTextField(state); field != nil {
					mutated = runtime.TouchField(field.ID) || mutated
				}
				state.router.FocusNext(event.Modifiers.Contain(key.ModShift))
				continue
			}
			if field := focusedTextField(state); field != nil && event.State == key.Press && event.Modifiers.Contain(key.ModShortcut) {
				switch event.Name {
				case key.Name("C"), key.Name("X"):
					if selected, ok := runtime.FieldSelectedText(field.ID); ok {
						gtx.Execute(clipboard.WriteCmd{Type: fieldClipboardMIME, Data: io.NopCloser(strings.NewReader(selected))})
					}
					if event.Name == key.Name("X") && !field.ReadOnly {
						start, end, _ := runtime.FieldRuneSelection(field.ID)
						_ = runtime.ApplyFieldEdit(field.ID, start, end, "")
						mutated = true
					}
				case key.Name("V"):
					if !field.ReadOnly {
						gtx.Execute(clipboard.ReadCmd{Tag: &state.interactionInput})
					}
				case key.Name("A"):
					if draft, ok := runtime.FieldDraft(field.ID); ok {
						_ = runtime.SetFieldSelection(field.ID, 0, len([]rune(draft)))
						mutated = true
					}
				case key.Name("Z"), key.Name("Y"):
					if fieldRedoShortcut(event.Name, event.Modifiers) {
						mutated = runtime.RedoField(field.ID) || mutated
					} else {
						mutated = runtime.UndoField(field.ID) || mutated
					}
				}
				continue
			}
			if field := focusedTextField(state); field != nil && event.State == key.Press && (event.Name == key.NameReturn || event.Name == key.NameEnter) {
				if field.Type == "text_area" && !event.Modifiers.Contain(key.ModShortcut) {
					start, end, _ := runtime.FieldRuneSelection(field.ID)
					if err := runtime.ApplyFieldEdit(field.ID, start, end, "\n"); err != nil {
						state.status = err.Error()
					} else {
						mutated = true
					}
					continue
				}
				if form := semanticNodeByHandle(state.runtimeTree, field.FormHandle); form != nil {
					if err := runtime.SubmitForm(form.ID); err != nil {
						state.status = err.Error()
					} else {
						mutated = true
					}
				}
				continue
			}
			if field := focusedTextField(state); field != nil {
				if event.State != key.Press {
					continue
				}
				extend := event.Modifiers.Contain(key.ModShift)
				word := event.Modifiers.Contain(key.ModAlt) || event.Modifiers.Contain(key.ModCtrl)
				movement := ""
				switch event.Name {
				case key.NameLeftArrow:
					movement = "grapheme-left"
					if word {
						movement = "word-left"
					}
				case key.NameRightArrow:
					movement = "grapheme-right"
					if word {
						movement = "word-right"
					}
				case key.NameUpArrow:
					if field.Multiline {
						movement = "line-up"
					}
				case key.NameDownArrow:
					if field.Multiline {
						movement = "line-down"
					}
				case key.NameHome:
					movement = "line-start"
					if event.Modifiers.Contain(key.ModShortcut) {
						movement = "document-start"
					}
				case key.NameEnd:
					movement = "line-end"
					if event.Modifiers.Contain(key.ModShortcut) {
						movement = "document-end"
					}
				case key.NameDeleteBackward, key.NameDeleteForward:
					if !field.ReadOnly {
						mutated = runtime.DeleteFieldSelection(field.ID, event.Name == key.NameDeleteBackward, word) || mutated
					}
					continue
				case key.NameEscape:
					mutated = runtime.CancelFieldComposition(field.ID) || mutated
					continue
				case key.NameSpace:
					// Text insertion arrives through key.EditEvent.
					continue
				}
				if movement != "" {
					if field.Multiline {
						if columns, ok := fieldVisualColumns(field); ok {
							mutated = runtime.SetFieldVisualColumns(field.ID, columns) || mutated
						}
					}
					state.caretBlinkStart = frameTime(gtx.Now)
					mutated = runtime.MoveFieldSelection(field.ID, movement, extend) || mutated
					continue
				}
			}
			name := ""
			switch event.Name {
			case key.NameReturn, key.NameEnter:
				name = "Enter"
			case key.NameSpace:
				name = "Space"
			case key.NameEscape:
				name = "Escape"
			case key.NameLeftArrow:
				name = "ArrowLeft"
			case key.NameRightArrow:
				name = "ArrowRight"
			case key.NameUpArrow:
				name = "ArrowUp"
			case key.NameDownArrow:
				name = "ArrowDown"
			case key.NameHome:
				name = "Home"
			case key.NameEnd:
				name = "End"
			case key.NamePageUp:
				name = "PageUp"
			case key.NamePageDown:
				name = "PageDown"
			}
			var activation interaction.Activation
			var activated bool
			if event.State == key.Press {
				activation, activated = state.router.KeyDown(name)
			} else {
				activation, activated = state.router.KeyUp(name)
			}
			if activated {
				if err := runtime.Activate(activation); err != nil {
					state.status = err.Error()
				} else {
					mutated = true
				}
			}
		}
	}
	for {
		raw, ok := gtx.Event(key.FocusFilter{Target: &state.interactionInput})
		if !ok {
			break
		}
		field := focusedTextField(state)
		if field == nil || field.ReadOnly || !field.Enabled {
			continue
		}
		switch event := raw.(type) {
		case key.EditEvent:
			if err := runtime.ApplyFieldEdit(field.ID, event.Range.Start, event.Range.End, event.Text); err != nil {
				state.status = err.Error()
			} else {
				mutated = true
			}
		case key.SelectionEvent:
			rangeValue := key.Range(event)
			if err := runtime.SetFieldSelection(field.ID, rangeValue.Start, rangeValue.End); err == nil {
				mutated = true
			}
		case key.CompositionEvent:
			rangeValue := key.Range(event)
			if err := runtime.SetFieldComposition(field.ID, rangeValue.Start, rangeValue.End); err == nil {
				mutated = true
			}
		}
	}
	for {
		raw, ok := gtx.Event(transfer.TargetFilter{Target: &state.interactionInput, Type: fieldClipboardMIME})
		if !ok {
			break
		}
		data, ok := raw.(transfer.DataEvent)
		field := focusedTextField(state)
		if !ok || field == nil || field.ReadOnly || !field.Enabled {
			continue
		}
		reader := data.Open()
		if reader == nil {
			continue
		}
		contents, readErr := io.ReadAll(reader)
		_ = reader.Close()
		if readErr != nil {
			state.status = readErr.Error()
			continue
		}
		start, end, _ := runtime.FieldRuneSelection(field.ID)
		if err := runtime.ApplyFieldEdit(field.ID, start, end, string(contents)); err != nil {
			state.status = err.Error()
		} else {
			mutated = true
		}
	}
	if change, ok := state.router.TakeValueChange(); ok {
		if _, err := runtime.SetControlValue(change.ID, change.Value); err != nil {
			state.status = err.Error()
		} else {
			mutated = true
		}
	}
	if change, ok := state.router.TakeScrollChange(); ok {
		if err := runtime.ScrollSemanticID(change.ID, change.Mode, change.X, change.Y); err != nil {
			state.status = err.Error()
		} else {
			mutated = true
		}
	}
	transient := state.router.Transient()
	if transient != before {
		runtime.SetTransient(transient)
	}
	if transient != before || mutated {
		window.Invalidate()
	}
}

func fieldRedoShortcut(name key.Name, modifiers key.Modifiers) bool {
	if !modifiers.Contain(key.ModShortcut) {
		return false
	}
	return name == key.Name("Y") || name == key.Name("Z") && modifiers.Contain(key.ModShift)
}

func focusedTextField(state *uiState) *semantic.Node {
	if state == nil || state.runtimeTree == nil || state.router == nil {
		return nil
	}
	handle := state.router.Transient().Focused
	for _, node := range semantic.Flatten(state.runtimeTree) {
		if node.Handle == handle && node.Role == "textbox" {
			return node
		}
	}
	return nil
}

func semanticNodeByID(root *semantic.Node, id string) *semantic.Node {
	for _, node := range semantic.Flatten(root) {
		if node.ID == id {
			return node
		}
	}
	return nil
}

func hitTextField(root *semantic.Node, point image.Point) *semantic.Node {
	return semantic.TopmostAt(root, point, func(node *semantic.Node) bool {
		return node.Role == "textbox"
	})
}

func fieldAllowsPointerSelection(field *semantic.Node) bool {
	return field != nil && field.Role == "textbox" && field.Enabled
}

func fieldRuneAtPoint(field *semantic.Node, point image.Point) int {
	box := field
	for _, child := range field.Children {
		if child != nil && child.Type == "field_box" && child.Bounds != nil {
			box = child
			break
		}
	}
	if box.Bounds == nil {
		return 0
	}
	return render.FieldRuneAtPoint(box.Props, box.Bounds.ImageRectangle(), point)
}

func fieldVisualLineRange(field *semantic.Node, caret int) (int, int) {
	for _, child := range field.Children {
		if child != nil && child.Type == "field_box" && child.Bounds != nil {
			return render.FieldLineRange(child.Props, child.Bounds.ImageRectangle(), caret)
		}
	}
	return lineRuneRange(fmt.Sprint(field.Value), caret)
}

func fieldVisualColumns(field *semantic.Node) (int, bool) {
	for _, child := range field.Children {
		if child != nil && child.Type == "field_box" && child.Bounds != nil {
			return render.FieldVisibleColumns(child.Props, child.Bounds.ImageRectangle()), true
		}
	}
	return 0, false
}

func wordRuneRange(text string, caret int) (int, int) {
	runes := []rune(text)
	caret = min(max(0, caret), len(runes))
	start, end := caret, caret
	for start > 0 && (unicode.IsLetter(runes[start-1]) || unicode.IsDigit(runes[start-1]) || runes[start-1] == '_') {
		start--
	}
	for end < len(runes) && (unicode.IsLetter(runes[end]) || unicode.IsDigit(runes[end]) || runes[end] == '_') {
		end++
	}
	return start, end
}

func lineRuneRange(text string, caret int) (int, int) {
	runes := []rune(text)
	caret = min(max(0, caret), len(runes))
	start, end := caret, caret
	for start > 0 && runes[start-1] != '\n' {
		start--
	}
	for end < len(runes) && runes[end] != '\n' {
		end++
	}
	return start, end
}

func numberValue(value any, fallback float64) float64 {
	switch value := value.(type) {
	case float64:
		return value
	case int64:
		return float64(value)
	case int:
		return float64(value)
	default:
		return fallback
	}
}

func documentPoint(state *uiState, metric unit.Metric, position f32.Point) image.Point {
	scale := state.zoomValue * metric.PxPerDp
	if scale <= 0 {
		scale = 1
	}
	origin := canvasPosition(state.canvasViewport, state.canvasSize, state.canvasPan)
	return image.Pt(
		int((position.X-float32(origin.X))/scale),
		int((position.Y-float32(origin.Y))/scale),
	)
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
	result := collectCanvasScrollEvents(gtx, state)
	return result.zoom, result.horizontal, result.vertical
}

type fieldScrollInput struct {
	field *semantic.Node
	delta float32
}

type canvasScrollInput struct {
	zoom       float32
	horizontal float32
	vertical   float32
	fields     []fieldScrollInput
	events     []canvasScrollEvent
}

type canvasScrollEvent struct {
	point image.Point
	delta f32.Point
}

func collectCanvasScrollEvents(gtx layout.Context, state *uiState) (result canvasScrollInput) {
	filter := trackpadZoomFilter(state)
	for {
		rawEvent, ok := gtx.Event(filter)
		if !ok {
			return result
		}
		scrollEvent, ok := rawEvent.(pointer.Event)
		if !ok {
			continue
		}
		if isTrackpadZoom(scrollEvent.Modifiers) {
			result.zoom += trackpadZoomDelta(scrollEvent)
			continue
		}
		point := documentPoint(state, gtx.Metric, scrollEvent.Position)
		result.events = append(result.events, canvasScrollEvent{point: point, delta: scrollEvent.Scroll})
		axis, delta := canvasScrollDelta(scrollEvent)
		if axis == "horizontal" {
			result.horizontal += delta
		} else {
			point := documentPoint(state, gtx.Metric, scrollEvent.Position)
			if field := textAreaScrollTarget(state.runtimeTree, point); field != nil {
				merged := false
				for index := range result.fields {
					if result.fields[index].field.ID == field.ID {
						result.fields[index].delta += delta
						merged = true
						break
					}
				}
				if !merged {
					result.fields = append(result.fields, fieldScrollInput{field: field, delta: delta})
				}
				continue
			}
			result.vertical += delta
		}
	}
}

func routeScrollEvents(runtime *Runtime, state *uiState, snapshot Snapshot, events []canvasScrollEvent, scale float32, panHorizontal func(float32) bool) bool {
	if runtime == nil || state == nil || state.runtimeTree == nil {
		return false
	}
	if scale <= 0 {
		scale = 1
	}
	runtime.SetScrollMetricScale(float64(scale))
	changed := false
	for _, event := range events {
		deltaX, deltaY := float64(event.delta.X), float64(event.delta.Y)
		if deltaX != 0 && panHorizontal != nil && panHorizontal(event.delta.X) {
			deltaX = 0
		}
		outcome, err := runtime.RouteScroll(scrollinput.Event{Source: "trackpad", Point: event.point, DeltaX: deltaX, DeltaY: deltaY, Units: "physical_pixels", Phase: "update", Momentum: "none"})
		if err == nil && outcome.Changed {
			changed = true
		}
	}
	return changed
}

func textAreaScrollTarget(root *semantic.Node, point image.Point) *semantic.Node {
	return semantic.TopmostAt(root, point, func(node *semantic.Node) bool {
		return node.Type == "text_area" && node.Enabled && node.InViewport
	})
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
							layout.Rigid(func(gtx layout.Context) layout.Dimensions {
								if !snapshot.HasState {
									return layout.Dimensions{}
								}
								return layout.Inset{Left: unit.Dp(8)}.Layout(gtx, toolbarButton(theme, &state.resetState, "Reset state", false))
							}),
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
