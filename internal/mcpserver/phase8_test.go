package mcpserver

import (
	"context"
	"encoding/json"
	"image"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"gora/internal/session"
	"gora/internal/studio"
)

func TestAttachedToolDiscoveryAndHeadlessRejection(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "app.gora"), []byte("gora: 1\nkind: app\nviewport: { width: 100, height: 80 }\nentry: main\nscreens:\n  main: { type: spacer }\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	service := NewServiceWithOptions(NewRegistry(), ServiceOptions{Automation: true})
	defer service.Close()
	ctx := context.Background()
	clientTransport, serverTransport := mcp.NewInMemoryTransports()
	if _, err := service.server.Connect(ctx, serverTransport, nil); err != nil {
		t.Fatal(err)
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "phase8-gating", Version: "1"}, nil)
	connection, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	tools, err := connection.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"gora_set_window", "gora_perform_window_action", "gora_set_studio_state"} {
		if !hasTool(tools.Tools, name) {
			t.Fatalf("attached tool %q missing", name)
		}
	}
	opened, err := connection.CallTool(ctx, &mcp.CallToolParams{Name: "gora_open_project", Arguments: map[string]any{"root": root}})
	if err != nil || opened.IsError {
		t.Fatalf("open project: err=%v result=%+v", err, opened)
	}
	projectID := stringField(opened.StructuredContent, "project_id")
	view, err := connection.CallTool(ctx, &mcp.CallToolParams{Name: "gora_open_view", Arguments: map[string]any{"project_id": projectID, "file": "app.gora"}})
	if err != nil || view.IsError {
		t.Fatalf("open view: err=%v result=%+v", err, view)
	}
	viewID := stringField(view.StructuredContent, "view_id")
	for _, test := range []struct {
		name string
		args map[string]any
	}{
		{name: "gora_set_window", args: map[string]any{"project_id": projectID, "view_id": viewID, "width": 200, "height": 100}},
		{name: "gora_perform_window_action", args: map[string]any{"project_id": projectID, "view_id": viewID, "action": "raise"}},
		{name: "gora_set_studio_state", args: map[string]any{"project_id": projectID, "view_id": viewID, "inspect": true}},
		{name: "gora_capture", args: map[string]any{"project_id": projectID, "view_id": viewID, "scale": 1, "target": "host_client"}},
	} {
		result, callErr := connection.CallTool(ctx, &mcp.CallToolParams{Name: test.name, Arguments: test.args})
		if callErr != nil {
			t.Fatal(callErr)
		}
		if !result.IsError {
			t.Fatalf("headless %s unexpectedly succeeded: %+v", test.name, result)
		}
	}
}

func TestAttachedHostSnapshotResourceState(t *testing.T) {
	root := t.TempDir()
	entry := filepath.Join(root, "app.gora")
	if err := os.WriteFile(entry, []byte("gora: 1\nkind: app\nviewport: { width: 100, height: 80 }\nentry: main\nscreens:\n  main: { type: spacer }\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	canonicalRoot, _ := canonicalDirectory(root)
	canonicalEntry, _ := containedFile(canonicalRoot, entry)
	identity := session.HostIdentity{InstanceID: "phase8-host", Root: canonicalRoot, Document: canonicalEntry, Mode: session.HostModeApp, PID: os.Getpid(), Automation: true, Capabilities: []string{"capture", "snapshot", "window"}}
	handshake, _ := json.Marshal(session.HandshakeResult{Protocol: session.ProtocolVersion, Host: identity})
	snapshot, _ := json.Marshal(studio.AutomationSnapshot{SchemaVersion: 1, Valid: true, Viewport: image.Pt(100, 80), FrameRevision: 4})
	hostSnapshot, _ := json.Marshal(studio.HostSnapshot{SchemaVersion: 1, HostProtocolVersion: 1, HostInstanceID: identity.InstanceID, Mode: "app", ConnectionState: "connected", ProcessID: identity.PID, WindowMode: "windowed", LogicalClientWidth: 100, LogicalClientHeight: 80, PhysicalClientWidth: 200, PhysicalClientHeight: 160, PxPerDp: 2, PxPerSp: 2, FrameRevision: 4})
	socket, err := session.SocketPath(canonicalRoot, canonicalEntry, "app")
	if err != nil {
		if strings.Contains(err.Error(), "operation not permitted") || strings.Contains(err.Error(), "permission denied") {
			t.Skipf("session socket unavailable in this environment: %v", err)
		}
		t.Fatal(err)
	}
	host, err := session.Listen(socket, func(_ context.Context, request session.Request) session.Response {
		switch request.Action {
		case session.ActionHandshake:
			return session.Response{Version: 1, RequestID: request.RequestID, OK: true, Data: handshake}
		case session.ActionSnapshot:
			return session.Response{Version: 1, RequestID: request.RequestID, OK: true, Data: snapshot}
		case session.ActionHostSnapshot:
			return session.Response{Version: 1, RequestID: request.RequestID, OK: true, Data: hostSnapshot}
		default:
			return session.Response{Version: 1, RequestID: request.RequestID, OK: true, Data: snapshot}
		}
	})
	if err != nil {
		if strings.Contains(err.Error(), "operation not permitted") || strings.Contains(err.Error(), "permission denied") {
			t.Skipf("session socket unavailable in this environment: %v", err)
		}
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
	got, err := registry.HostSnapshot(project.ID, view.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.PhysicalClientWidth != 200 || got.PxPerDp != 2 || got.FrameRevision != 4 {
		t.Fatalf("unexpected host snapshot: %+v", got)
	}
}

func TestAttachedCommandErrorKeepsHostConnected(t *testing.T) {
	root := t.TempDir()
	entry := filepath.Join(root, "app.gora")
	if err := os.WriteFile(entry, []byte("gora: 1\nkind: app\nviewport: { width: 100, height: 80 }\nentry: main\nscreens:\n  main: { type: spacer }\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	canonicalRoot, _ := canonicalDirectory(root)
	canonicalEntry, _ := containedFile(canonicalRoot, entry)
	identity := session.HostIdentity{InstanceID: "command-error-host", Root: canonicalRoot, Document: canonicalEntry, Mode: session.HostModeApp, PID: os.Getpid(), Automation: true, Capabilities: []string{"snapshot", "window"}}
	handshake, _ := json.Marshal(session.HandshakeResult{Protocol: session.ProtocolVersion, Host: identity})
	automationSnapshot, _ := json.Marshal(studio.AutomationSnapshot{SchemaVersion: 1, Valid: true, Viewport: image.Pt(100, 80), FrameRevision: 1})
	hostSnapshot, _ := json.Marshal(studio.HostSnapshot{SchemaVersion: 1, HostProtocolVersion: 1, HostInstanceID: identity.InstanceID, Mode: "app", ConnectionState: "connected", ProcessID: identity.PID, Capabilities: identity.Capabilities, WindowMode: "windowed", LogicalClientWidth: 100, LogicalClientHeight: 80, FrameRevision: 1})
	socket, err := session.SocketPath(canonicalRoot, canonicalEntry, "app")
	if err != nil {
		t.Fatal(err)
	}
	host, err := session.Listen(socket, func(_ context.Context, request session.Request) session.Response {
		switch request.Action {
		case session.ActionHandshake:
			return session.Response{Version: session.ProtocolVersion, RequestID: request.RequestID, OK: true, Data: handshake}
		case session.ActionSnapshot:
			return session.Response{Version: session.ProtocolVersion, RequestID: request.RequestID, OK: true, Data: automationSnapshot}
		case session.ActionHostSnapshot:
			return session.Response{Version: session.ProtocolVersion, RequestID: request.RequestID, OK: true, Data: hostSnapshot}
		case session.ActionCommand:
			return session.Response{Version: session.ProtocolVersion, RequestID: request.RequestID, Error: "window rejected"}
		default:
			return session.Response{Version: session.ProtocolVersion, RequestID: request.RequestID, Error: "unsupported"}
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
	if _, err := registry.HostCommandResult(context.Background(), project.ID, view.ID, "set_window", map[string]any{"width": 120, "height": 90}); err == nil || !strings.Contains(err.Error(), "window rejected") {
		t.Fatalf("unexpected command error: %v", err)
	}
	registry.mu.RLock()
	projectState := registry.projects[project.ID]
	registry.mu.RUnlock()
	projectState.mu.RLock()
	attached := projectState.views[view.ID].host
	projectState.mu.RUnlock()
	attached.mu.Lock()
	connectedAfterError := attached.connected
	attached.mu.Unlock()
	if !connectedAfterError {
		t.Fatal("application error marked the attached transport disconnected")
	}
	summary, err := registry.ViewSummary(project.ID, view.ID)
	if err != nil {
		t.Fatal(err)
	}
	if summary.ConnectionState != "connected" {
		t.Fatalf("application error disconnected healthy host: %+v", summary)
	}
}

func TestStudioStateInputDistinguishesOmittedAndExplicitEmptyOutput(t *testing.T) {
	var omitted StudioStateInput
	if err := json.Unmarshal([]byte(`{"project_id":"p","view_id":"v"}`), &omitted); err != nil {
		t.Fatal(err)
	}
	if omitted.Output != nil {
		t.Fatal("omitted capture output was treated as an empty request")
	}
	var explicit StudioStateInput
	if err := json.Unmarshal([]byte(`{"project_id":"p","view_id":"v","output":""}`), &explicit); err != nil {
		t.Fatal(err)
	}
	if explicit.Output == nil || *explicit.Output != "" {
		t.Fatalf("explicit empty capture output was lost: %+v", explicit)
	}
}

func TestDisconnectedHostResourceRetainsImmutableSnapshot(t *testing.T) {
	backend := &hostBackend{lastHost: studio.HostSnapshot{
		SchemaVersion: 1, HostInstanceID: "retained-host", Mode: "app", ConnectionState: "connected",
		LogicalClientWidth: 640, LogicalClientHeight: 480, HostRevision: 7, FrameRevision: 9,
	}}
	backend.connected = false
	backend.reason = "window closed"
	snapshot, err := backend.HostSnapshot(context.Background())
	if err == nil || !strings.Contains(err.Error(), "disconnected") {
		t.Fatalf("expected disconnected error, got %v", err)
	}
	if snapshot.ConnectionState != "disconnected" || snapshot.HostInstanceID != "retained-host" || snapshot.FrameRevision != 9 {
		t.Fatalf("retained host snapshot = %+v", snapshot)
	}
}
