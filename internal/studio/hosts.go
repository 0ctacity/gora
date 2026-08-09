package studio

import (
	"context"
	"image"
	"math"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"gioui.org/app"
	"gioui.org/io/event"
	"gioui.org/io/key"
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
	server, err := session.Listen(socketPath, runtime.SessionHandler("app", func() {
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
	state.router.SyncTransient(snapshot.Transient)
	state.canvasViewport = pixelSize
	state.canvasSize = pixelSize
	state.canvasPan = image.Point{}
	runtime.SetScrollMetricScale(float64(gtx.Metric.PxPerDp))
	handleAppActions(gtx, runtime, state, snapshot, window)
	snapshot = runtime.Snapshot()

	hardClip := clip.Rect{Max: pixelSize}.Push(gtx.Ops)
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
	previewGtx := gtx
	previewGtx.Constraints = layout.Exact(pixelSize)
	result := state.preview.Layout(previewGtx, theme, snapshot.Root, snapshot.Viewport, liveRenderState(snapshot, gtx.Now, state.caretBlinkStart))
	state.runtimeTree = result.Tree
	runtime.PublishFrame(result.Tree, result.Scroll)
	state.router.Update(state.runtimeTree)
	if state.router.Transient() != snapshot.Transient {
		runtime.SetTransient(state.router.Transient())
		window.Invalidate()
	}
	runtime.PublishRouterSnapshot(state.router.Snapshot())
	hardClip.Pop()
	return layout.Dimensions{Size: pixelSize}
}

func handleAppActions(gtx layout.Context, runtime *Runtime, state *uiState, snapshot Snapshot, window *app.Window) {
	handleDocumentInteraction(gtx, runtime, state, window)
	scrollEvents := collectCanvasScrollEvents(gtx, state)
	// Command-modified scroll belongs to the host zoom gesture. The app host
	// has no zoom control, so consume the event without mutating document state.
	if scrollEvents.zoom != 0 || state.router.ScrollbarPointerOwned() || len(scrollEvents.events) == 0 {
		return
	}
	scale := gtx.Metric.PxPerDp
	if scale <= 0 {
		scale = 1
	}
	if routeScrollEvents(runtime, state, snapshot, scrollEvents.events, scale, nil) {
		window.Invalidate()
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
	server, err := session.Listen(socketPath, runtime.SessionHandler("headless", nil))
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
	return NewRuntimeAllowInvalid(root, entry), nil
}
