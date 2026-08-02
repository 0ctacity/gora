package studio

import (
	"context"
	"image"
	"os"
	"path/filepath"
	"testing"
	"time"

	"gioui.org/app"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/unit"
	"gioui.org/widget/material"

	"gora/internal/semantic"
	"gora/internal/session"
)

func TestLayoutAppContentUsesNativeWindowViewport(t *testing.T) {
	repositoryRoot, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := NewRuntime(repositoryRoot, filepath.Join(repositoryRoot, "examples", "interactivity", "app.gora"))
	if err != nil {
		t.Fatal(err)
	}
	var operations op.Ops
	state := newAppUIState()
	gtx := layout.Context{
		Ops: &operations, Metric: unit.Metric{PxPerDp: 1, PxPerSp: 1},
		Constraints: layout.Exact(image.Pt(640, 480)),
	}
	dimensions := layoutAppContent(gtx, material.NewTheme(), runtime, state, new(app.Window))
	if dimensions.Size != image.Pt(640, 480) {
		t.Fatalf("dimensions = %v", dimensions.Size)
	}
	if viewport := runtime.Snapshot().Viewport; viewport != image.Pt(640, 480) {
		t.Fatalf("viewport = %v", viewport)
	}
	if len(semantic.Flatten(state.runtimeTree)) <= 1 {
		t.Fatal("content-only app did not expose document interactions")
	}
}

func TestRunHeadlessServesSessionUntilCanceled(t *testing.T) {
	dir := t.TempDir()
	entry := filepath.Join(dir, "app.gora")
	if err := os.WriteFile(entry, []byte("gora: 1\nkind: app\nviewport: { width: 100, height: 80 }\nentry: main\nscreens:\n  main: { type: spacer }\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	socketDir, err := os.MkdirTemp("/tmp", "gora-headless-test-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(socketDir) })
	socket := filepath.Join(socketDir, "headless.sock")
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- runHeadless(ctx, dir, entry, socket) }()

	deadline := time.Now().Add(2 * time.Second)
	for {
		response, err := session.Send(socket, session.Request{Action: "focus"}, 100*time.Millisecond)
		if err == nil && response.OK {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("headless session did not start: %v", err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("headless session did not stop after cancellation")
	}
}
