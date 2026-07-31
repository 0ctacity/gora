package studio

import (
	"context"
	"image"
	"math"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"gioui.org/app"
	"gioui.org/io/event"
	"gioui.org/io/pointer"
	"gioui.org/io/system"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/unit"
	"gioui.org/widget/material"

	"gora/internal/interaction"
	"gora/internal/session"
)

func newAppUIState() *uiState {
	return &uiState{zoomValue: 1, zoomInitialized: true, router: interaction.NewRouter()}
}

// StartApp opens a content-only native document window.
func StartApp(root, entry, socketPath string) error {
	runtime, err := NewRuntime(root, entry)
	if err != nil {
		return err
	}
	snapshot := runtime.Snapshot()
	window := new(app.Window)
	window.Option(
		app.Title(filepath.Base(entry)),
		app.Size(unit.Dp(snapshot.Viewport.X), unit.Dp(snapshot.Viewport.Y)),
	)
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
	go appEventLoop(window, runtime, server, func() {
		signal.Stop(signals)
		cancel()
	})
	return nil
}

func appEventLoop(window *app.Window, runtime *Runtime, server *session.Server, cleanup func()) {
	defer cleanup()
	defer server.Close()
	theme := material.NewTheme()
	state := newAppUIState()
	var operations op.Ops
	for {
		event := window.Event()
		switch event := event.(type) {
		case app.DestroyEvent:
			return
		case app.FrameEvent:
			gtx := app.NewContext(&operations, event)
			layoutAppContent(gtx, theme, runtime, state, window)
			event.Frame(gtx.Ops)
		}
	}
}

func layoutAppContent(gtx layout.Context, theme *material.Theme, runtime *Runtime, state *uiState, window *app.Window) layout.Dimensions {
	pixelSize := gtx.Constraints.Max
	logicalSize := logicalViewport(pixelSize, gtx.Metric)
	if runtime.Snapshot().Viewport != logicalSize {
		runtime.SetViewport(logicalSize.X, logicalSize.Y)
	}
	snapshot := runtime.Snapshot()
	state.canvasViewport = pixelSize
	state.canvasSize = pixelSize
	state.canvasPan = image.Point{}
	handleAppActions(gtx, runtime, state, snapshot, window)
	snapshot = runtime.Snapshot()

	hardClip := clip.Rect{Max: pixelSize}.Push(gtx.Ops)
	event.Op(gtx.Ops, &state.zoomInput)
	event.Op(gtx.Ops, &state.interactionInput)
	previewGtx := gtx
	previewGtx.Constraints = layout.Exact(pixelSize)
	result := state.preview.Layout(previewGtx, theme, snapshot.Root, snapshot.Viewport, renderState(snapshot))
	layoutPreviewScrollbar(previewGtx, theme, runtime, state, snapshot, result, window)
	state.inspections = result.Inspections
	state.interactions = append(state.interactions[:0], result.Interactions...)
	state.router.Update(state.interactions)
	if state.router.Transient() != snapshot.Transient {
		runtime.SetTransient(state.router.Transient())
		window.Invalidate()
	}
	hardClip.Pop()
	return layout.Dimensions{Size: pixelSize}
}

func handleAppActions(gtx layout.Context, runtime *Runtime, state *uiState, snapshot Snapshot, window *app.Window) {
	handleDocumentInteraction(gtx, runtime, state, window)
	horizontal, vertical := appScrollEvents(gtx, state)
	axis, delta := dominantPageScroll(horizontal, vertical)
	if delta == 0 {
		return
	}
	scale := gtx.Metric.PxPerDp
	if scale <= 0 {
		scale = 1
	}
	logicalDelta := int(math.Round(float64(delta / scale)))
	if logicalDelta == 0 {
		logicalDelta = 1
		if delta < 0 {
			logicalDelta = -1
		}
	}
	scrollDocument(runtime, state, snapshot, axis, logicalDelta)
	window.Invalidate()
}

func appScrollEvents(gtx layout.Context, state *uiState) (horizontal, vertical float32) {
	filter := trackpadZoomFilter(state)
	for {
		raw, ok := gtx.Event(filter)
		if !ok {
			return horizontal, vertical
		}
		event, ok := raw.(pointer.Event)
		if !ok {
			continue
		}
		axis, delta := canvasScrollDelta(event)
		if axis == "horizontal" {
			horizontal += delta
		} else {
			vertical += delta
		}
	}
}

func logicalViewport(size image.Point, metric unit.Metric) image.Point {
	scale := metric.PxPerDp
	if scale <= 0 {
		scale = 1
	}
	return image.Pt(
		max(1, int(math.Round(float64(float32(size.X)/scale)))),
		max(1, int(math.Round(float64(float32(size.Y)/scale)))),
	)
}

// RunHeadless runs a live document session without opening a platform window.
func RunHeadless(root, entry, socketPath string) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return runHeadless(ctx, root, entry, socketPath)
}

func runHeadless(ctx context.Context, root, entry, socketPath string) error {
	runtime, err := runtimeAllowInvalid(root, entry)
	if err != nil {
		return err
	}
	server, err := session.Listen(socketPath, runtime.SessionHandler(nil))
	if err != nil {
		return err
	}
	defer server.Close()
	watchDone := make(chan error, 1)
	go func() { watchDone <- runtime.Watch(ctx, nil) }()
	select {
	case <-ctx.Done():
		return nil
	case err := <-watchDone:
		return err
	}
}

func runtimeAllowInvalid(root, entry string) (*Runtime, error) {
	runtime, err := NewRuntime(root, entry)
	if err == nil {
		return runtime, nil
	}
	runtime = &Runtime{root: root, entry: entry, scroll: make(map[string]image.Point), state: interaction.NewStore()}
	runtime.Reload()
	return runtime, nil
}
