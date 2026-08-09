package studio

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
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

	"gora/internal/automation"
	"gora/internal/interaction"
	"gora/internal/session"
)

func newAppUIState() *uiState {
	return &uiState{zoomValue: 1, zoomInitialized: true, router: interaction.NewRouter()}
}

// StartApp opens a content-only native document window.
func StartApp(root, entry, socketPath string) error {
	return StartAppWithAutomation(root, entry, socketPath, false)
}

// StartAppWithAutomation opens a plain app host and optionally advertises the
// versioned automation bridge to MCP clients.
func StartAppWithAutomation(root, entry, socketPath string, automationEnabled bool) error {
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
	controller := NewHostController(64)
	driver := NewHostAutomationDriver(runtime)
	identity := hostIdentity(root, entry, session.HostModeApp, automationEnabled)
	go func() {
		for {
			select {
			case <-controller.Wake():
				window.Invalidate()
			case <-controller.Done():
				return
			}
		}
	}()
	server, err := session.Listen(socketPath, runtime.SessionHandlerWithController("app", identity, controller, func() {
		window.Perform(system.ActionRaise)
		window.Invalidate()
	}, driver))
	if err != nil {
		controller.Close()
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
		_ = runtime.WatchWithReload(ctx, window.Invalidate, func() {
			_ = controller.TrySubmit(HostCommand{RequestID: fmt.Sprintf("watch-%d", time.Now().UnixNano()), Apply: func() error {
				runtime.Reload()
				return nil
			}})
		})
	}()
	go appEventLoop(window, runtime, server, controller, driver, func() {
		signal.Stop(signals)
		cancel()
	})
	return nil
}

func appEventLoop(window *app.Window, runtime *Runtime, server *session.Server, controller *HostController, driver *automation.Driver, cleanup func()) {
	defer cleanup()
	defer server.Close()
	defer controller.Close()
	theme := material.NewTheme()
	state := newAppUIState()
	var operations op.Ops
	for {
		event := window.Event()
		switch event := event.(type) {
		case app.DestroyEvent:
			return
		case app.FrameEvent:
			commands := controller.Drain()
			applied := make([]struct {
				command HostCommand
				err     error
			}, len(commands))
			for index, command := range commands {
				applied[index] = struct {
					command HostCommand
					err     error
				}{command: command, err: command.Apply()}
			}
			gtx := app.NewContext(&operations, event)
			layoutAppContent(gtx, theme, runtime, state, window)
			if driver != nil {
				if tree, treeErr := runtime.CurrentRuntimeTree(); treeErr == nil {
					driver.Update(tree)
				}
			}
			snapshot := runtime.Snapshot()
			for _, item := range applied {
				var data json.RawMessage
				if item.err == nil && item.command.Result != nil {
					data, item.err = item.command.Result()
				}
				controller.Complete(item.command.RequestID, item.err, data)
			}
			controller.Publish(HostPublication{RuntimeRevision: snapshot.PublishedRuntimeRevision, GeometryRevision: snapshot.PublishedGeometryRevision, FrameRevision: snapshot.FrameRevision})
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
		if snapshot.Clock.Mode != "frozen" {
			gtx.Execute(op.InvalidateCmd{At: frameTime(gtx.Now).Add(500 * time.Millisecond)})
		}
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

func hostIdentity(root, entry string, mode session.HostMode, automationEnabled bool) session.HostIdentity {
	if canonical, err := filepath.EvalSymlinks(root); err == nil {
		root = canonical
	}
	if canonical, err := filepath.EvalSymlinks(entry); err == nil {
		entry = canonical
	}
	bytes := make([]byte, 16)
	if _, err := rand.Read(bytes); err != nil {
		bytes = []byte(filepath.Base(entry) + time.Now().String())
	}
	return session.HostIdentity{InstanceID: hex.EncodeToString(bytes), Root: root, Document: entry, Mode: mode, PID: os.Getpid(), Automation: automationEnabled, Capabilities: []string{"activation", "capture", "clock", "command", "editing", "faults", "input", "overlay", "reset", "scroll", "selection", "snapshot", "state", "trace", "tree", "viewport", "wait"}}
}

func runtimeAllowInvalid(root, entry string) (*Runtime, error) {
	runtime, err := NewRuntime(root, entry)
	if err == nil {
		return runtime, nil
	}
	return NewRuntimeAllowInvalid(root, entry), nil
}
