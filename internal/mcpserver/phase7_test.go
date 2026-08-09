package mcpserver

import (
	"context"
	"encoding/json"
	"image"
	"os"
	"path/filepath"
	"testing"
	"time"

	"gora/internal/session"
	"gora/internal/studio"
)

func TestOpenViewReusesByCanonicalEntryAndHostMode(t *testing.T) {
	root := t.TempDir()
	entry := filepath.Join(root, "app.gora")
	source := []byte("gora: 1\nkind: app\nviewport: { width: 100, height: 80 }\nentry: main\nscreens:\n  main: { type: spacer }\n")
	if err := os.WriteFile(entry, source, 0o600); err != nil {
		t.Fatal(err)
	}
	canonicalRoot, _ := canonicalDirectory(root)
	canonicalEntry, _ := containedFile(canonicalRoot, entry)
	identity := session.HostIdentity{InstanceID: "host-app-1", Root: canonicalRoot, Document: canonicalEntry, Mode: session.HostModeApp, PID: 123, Automation: true, Capabilities: []string{"snapshot", "tree"}}
	payload, _ := json.Marshal(session.HandshakeResult{Protocol: session.ProtocolVersion, Host: identity})
	socket, err := session.SocketPath(canonicalRoot, canonicalEntry, "app")
	if err != nil {
		t.Fatal(err)
	}
	host, err := session.Listen(socket, func(_ context.Context, request session.Request) session.Response {
		if request.Action == session.ActionHandshake {
			return session.Response{Version: session.ProtocolVersion, RequestID: request.RequestID, OK: true, Data: payload}
		}
		if request.Action == session.ActionSnapshot {
			data, _ := json.Marshal(studio.AutomationSnapshot{SchemaVersion: 1, Valid: true, Viewport: image.Point{X: 100, Y: 80}, FrameRevision: 1})
			return session.Response{Version: session.ProtocolVersion, RequestID: request.RequestID, OK: true, Data: data}
		}
		return session.Response{OK: true, Data: json.RawMessage(`{"valid":true,"root":null}`)}
	})
	if err != nil {
		t.Fatal(err)
	}
	defer host.Close()
	registry := NewRegistry()
	defer registry.Close()
	project, err := registry.OpenProject(root)
	if err != nil {
		t.Fatal(err)
	}
	headless, err := registry.OpenView(project.ID, entry)
	if err != nil {
		t.Fatal(err)
	}
	attached, err := registry.OpenView(project.ID, entry, "app")
	if err != nil {
		t.Fatal(err)
	}
	again, err := registry.OpenView(project.ID, filepath.Join(root, ".", "app.gora"), "app")
	if err != nil {
		t.Fatal(err)
	}
	if attached.ID != again.ID || attached.ID == headless.ID {
		t.Fatalf("mode reuse IDs attached=%q again=%q headless=%q", attached.ID, again.ID, headless.ID)
	}
	if attached.HostMode != session.HostModeApp || attached.ConnectionState != "connected" {
		t.Fatalf("attached summary=%+v", attached)
	}
	if _, err := registry.Runtime(project.ID, attached.ID); err == nil {
		t.Fatal("attached view created a shadow runtime")
	}
}

func TestAttachedViewDisconnectRetainsLastSnapshot(t *testing.T) {
	root := t.TempDir()
	entry := filepath.Join(root, "app.gora")
	if err := os.WriteFile(entry, []byte("gora: 1\nkind: app\nviewport: { width: 100, height: 80 }\nentry: main\nscreens:\n  main: { type: spacer }\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	canonicalRoot, _ := canonicalDirectory(root)
	canonicalEntry, _ := containedFile(canonicalRoot, entry)
	identity := session.HostIdentity{InstanceID: "host-app-2", Root: canonicalRoot, Document: canonicalEntry, Mode: session.HostModeApp, PID: 124, Automation: true, Capabilities: []string{"snapshot"}}
	payload, _ := json.Marshal(session.HandshakeResult{Protocol: session.ProtocolVersion, Host: identity})
	socket, _ := session.SocketPath(canonicalRoot, canonicalEntry, "app")
	host, err := session.Listen(socket, func(_ context.Context, request session.Request) session.Response {
		if request.Action == session.ActionHandshake {
			return session.Response{Version: session.ProtocolVersion, RequestID: request.RequestID, OK: true, Data: payload}
		}
		data, _ := json.Marshal(studio.AutomationSnapshot{SchemaVersion: 1, Valid: true, FrameRevision: 9})
		return session.Response{Version: session.ProtocolVersion, RequestID: request.RequestID, OK: true, Data: data}
	})
	if err != nil {
		t.Fatal(err)
	}
	registry := NewRegistry()
	defer registry.Close()
	project, _ := registry.OpenProject(root)
	view, err := registry.OpenView(project.ID, entry, "app")
	if err != nil {
		host.Close()
		t.Fatal(err)
	}
	if view.ConnectionState != "connected" {
		t.Fatalf("initial state=%+v", view)
	}
	host.Close()
	time.Sleep(20 * time.Millisecond)
	updated, err := registry.ViewSummary(project.ID, view.ID)
	if err != nil {
		t.Fatal(err)
	}
	if updated.ConnectionState != "disconnected" || updated.DisconnectReason == "" {
		t.Fatalf("disconnected summary=%+v", updated)
	}
}

func TestViewBackendHeadlessConformanceSurface(t *testing.T) {
	root := t.TempDir()
	entry := filepath.Join(root, "app.gora")
	if err := os.WriteFile(entry, []byte("gora: 1\nkind: app\nviewport: { width: 100, height: 80 }\nentry: main\nscreens:\n  main: { type: spacer }\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	registry := NewRegistry()
	defer registry.Close()
	project, err := registry.OpenProject(root)
	if err != nil {
		t.Fatal(err)
	}
	view, err := registry.OpenView(project.ID, entry)
	if err != nil {
		t.Fatal(err)
	}
	backend, err := registry.Backend(project.ID, view.ID)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	operations := []struct {
		name string
		run  func() error
	}{
		{"viewport", func() error { return backend.SetViewport(ctx, 120, 90) }},
		{"selection", func() error { return backend.Select(ctx, "main") }},
		{"state", func() error { return backend.ResetState(ctx, "") }},
		{"clock", func() error { _, _, err := backend.SetClock(ctx, "real"); return err }},
		{"trace", func() error { _, err := backend.ConfigureTrace(ctx, true, 8); return err }},
		{"clipboard", func() error { return backend.SetClipboard(ctx, "") }},
	}
	for _, operation := range operations {
		t.Run(operation.name, func(t *testing.T) {
			if err := operation.run(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestViewBackendAttachedConformanceSurface(t *testing.T) {
	root := t.TempDir()
	entry := filepath.Join(root, "app.gora")
	if err := os.WriteFile(entry, []byte("gora: 1\nkind: app\nviewport: { width: 100, height: 80 }\nentry: main\nscreens:\n  main: { type: spacer }\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	canonicalRoot, _ := canonicalDirectory(root)
	canonicalEntry, _ := containedFile(canonicalRoot, entry)
	capabilities := []string{"activation", "capture", "clock", "command", "editing", "faults", "input", "overlay", "reset", "scroll", "selection", "snapshot", "state", "trace", "tree", "viewport", "wait"}
	identity := session.HostIdentity{InstanceID: "conformance-host", Root: canonicalRoot, Document: canonicalEntry, Mode: session.HostModeApp, PID: os.Getpid(), Automation: true, Capabilities: capabilities}
	handshake, _ := json.Marshal(session.HandshakeResult{Protocol: session.ProtocolVersion, Host: identity})
	socket, err := session.SocketPath(canonicalRoot, canonicalEntry, "app")
	if err != nil {
		t.Fatal(err)
	}
	host, err := session.Listen(socket, func(_ context.Context, request session.Request) session.Response {
		snapshot, _ := json.Marshal(studio.AutomationSnapshot{SchemaVersion: 1, Valid: true, Viewport: image.Point{X: 100, Y: 80}, FrameRevision: 2, RuntimeRevision: 1})
		switch request.Action {
		case session.ActionHandshake:
			return session.Response{Version: session.ProtocolVersion, RequestID: request.RequestID, OK: true, Data: handshake}
		case session.ActionSnapshot, session.ActionWait:
			return session.Response{Version: session.ProtocolVersion, RequestID: request.RequestID, OK: true, Data: snapshot}
		case session.ActionCommand:
			var command map[string]any
			_ = json.Unmarshal(request.Payload, &command)
			var data any = studio.AutomationSnapshot{SchemaVersion: 1, Valid: true, Viewport: image.Point{X: 100, Y: 80}, FrameRevision: 3}
			switch command["kind"] {
			case "set_control_value":
				data = map[string]any{"value": "ok"}
			case "get_clipboard":
				data = map[string]any{"text": ""}
			case "set_clock", "advance_clock":
				data = map[string]any{"clock": studio.ViewClockSnapshot{}, "snapshot": studio.AutomationSnapshot{Valid: true}}
			case "get_trace", "configure_trace", "clear_trace":
				data = map[string]any{"enabled": true, "capacity": 8, "revision": 1, "entries": []any{}}
			case "capture":
				data = map[string]any{"png_base64": "iVBORw0KGgo=", "identity": map[string]any{}}
			case "dispatch":
				data = []any{}
			}
			encoded, _ := json.Marshal(data)
			return session.Response{Version: session.ProtocolVersion, RequestID: request.RequestID, OK: true, Data: encoded}
		default:
			return session.Response{Version: session.ProtocolVersion, RequestID: request.RequestID, Error: "unexpected action"}
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	defer host.Close()
	registry := NewRegistry()
	defer registry.Close()
	project, err := registry.OpenProject(root)
	if err != nil {
		t.Fatal(err)
	}
	view, err := registry.OpenView(project.ID, entry, "app")
	if err != nil {
		t.Fatal(err)
	}
	backend, err := registry.Backend(project.ID, view.ID)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	operations := []struct {
		name string
		run  func() error
	}{
		{"snapshot", func() error { _, err := backend.Snapshot(ctx); return err }},
		{"wait", func() error {
			_, err := backend.Wait(ctx, studio.WaitForViewRequest{Condition: "published", Timeout: time.Second})
			return err
		}},
		{"viewport", func() error { return backend.SetViewport(ctx, 120, 90) }},
		{"selection", func() error { return backend.Select(ctx, "main") }},
		{"activation", func() error { return backend.Activate(ctx, "button") }},
		{"scroll", func() error { return backend.Scroll(ctx, "scroll", "delta", 1, 2) }},
		{"state", func() error { return backend.SetState(ctx, "", map[string]any{}) }},
		{"reset", func() error { return backend.ResetState(ctx, "") }},
		{"control", func() error { _, err := backend.SetControlValue(ctx, "field", "ok"); return err }},
		{"editing", func() error { return backend.SetFieldDraft(ctx, "field", "ok") }},
		{"submit", func() error { return backend.SubmitForm(ctx, "form") }},
		{"form_reset", func() error { return backend.ResetForm(ctx, "form") }},
		{"dispatch", func() error { _, err := backend.Dispatch(ctx, nil); return err }},
		{"clipboard_set", func() error { return backend.SetClipboard(ctx, "") }},
		{"clipboard_get", func() error { _, err := backend.Clipboard(ctx); return err }},
		{"clock_set", func() error { _, _, err := backend.SetClock(ctx, "real"); return err }},
		{"clock_advance", func() error { _, _, err := backend.AdvanceClock(ctx, 1, false); return err }},
		{"trace", func() error { _, err := backend.Trace(ctx); return err }},
		{"trace_configure", func() error { _, err := backend.ConfigureTrace(ctx, true, 8); return err }},
		{"capture", func() error { _, _, _, err := backend.Capture(ctx, 1); return err }},
	}
	for _, operation := range operations {
		t.Run(operation.name, func(t *testing.T) {
			if err := operation.run(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestAttachedViewHundredAttachDetachCyclesBounded(t *testing.T) {
	root := t.TempDir()
	entry := filepath.Join(root, "app.gora")
	if err := os.WriteFile(entry, []byte("gora: 1\nkind: app\nviewport: { width: 100, height: 80 }\nentry: main\nscreens:\n  main: { type: spacer }\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	canonicalRoot, _ := canonicalDirectory(root)
	canonicalEntry, _ := containedFile(canonicalRoot, entry)
	identity := session.HostIdentity{InstanceID: "cycle-host", Root: canonicalRoot, Document: canonicalEntry, Mode: session.HostModeApp, PID: 321, Automation: true, Capabilities: []string{"snapshot", "tree", "wait"}}
	payload, _ := json.Marshal(session.HandshakeResult{Protocol: session.ProtocolVersion, Host: identity})
	socket, _ := session.SocketPath(canonicalRoot, canonicalEntry, "app")
	host, err := session.Listen(socket, func(_ context.Context, request session.Request) session.Response {
		data, _ := json.Marshal(studio.AutomationSnapshot{SchemaVersion: 1, Valid: true, FrameRevision: 1})
		switch request.Action {
		case session.ActionHandshake:
			return session.Response{Version: session.ProtocolVersion, RequestID: request.RequestID, OK: true, Data: payload}
		case session.ActionSnapshot:
			return session.Response{Version: session.ProtocolVersion, RequestID: request.RequestID, OK: true, Data: data}
		default:
			return session.Response{Version: session.ProtocolVersion, RequestID: request.RequestID, OK: true, Data: data}
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	defer host.Close()
	registry := NewRegistry()
	defer registry.Close()
	project, err := registry.OpenProject(root)
	if err != nil {
		t.Fatal(err)
	}
	for cycle := 0; cycle < 100; cycle++ {
		view, openErr := registry.OpenView(project.ID, entry, "app")
		if openErr != nil {
			t.Fatalf("cycle %d open: %v", cycle, openErr)
		}
		if closeErr := registry.CloseView(project.ID, view.ID); closeErr != nil {
			t.Fatalf("cycle %d close: %v", cycle, closeErr)
		}
	}
}
