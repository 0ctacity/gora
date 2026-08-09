package mcpserver

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"golang.org/x/image/font/gofont/goregular"
	"gora/internal/document"
	"gora/internal/project"
	"gora/internal/render"
	"gora/internal/studio"
)

func TestPhase6AutomationOverlayToolsAreDiscoverable(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "app.gora"), []byte("gora: 1\nkind: app\nviewport: {width: 10, height: 10}\nentry: main\nscreens: {main: {type: text, props: {text: hello}}}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	service := NewServiceWithOptions(NewRegistry(), ServiceOptions{Automation: true})
	defer service.Close()
	ctx := context.Background()
	clientTransport, serverTransport := mcp.NewInMemoryTransports()
	if _, err := service.server.Connect(ctx, serverTransport, nil); err != nil {
		t.Fatal(err)
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "phase6-red", Version: "1"}, nil)
	session, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	tools, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"gora_apply_test_overlay", "gora_clear_test_overlay", "gora_inject_reload_events", "gora_configure_test_faults", "gora_clear_test_faults"} {
		if !hasTool(tools.Tools, name) {
			t.Fatalf("missing phase6 tool %q", name)
		}
	}
}

func TestPhase6ViewLocalOverlayStagingInstallAndClear(t *testing.T) {
	root := t.TempDir()
	entry := filepath.Join(root, "app.gora")
	disk := []byte("gora: 1\nkind: app\nviewport: {width: 10, height: 10}\nentry: main\nscreens: {main: {type: text, props: {text: hello}}}\n")
	if err := os.WriteFile(entry, disk, 0o600); err != nil {
		t.Fatal(err)
	}
	registry := NewRegistry()
	defer registry.Close()
	projectSummary, err := registry.OpenProject(root)
	if err != nil {
		t.Fatal(err)
	}
	viewSummary, err := registry.OpenView(projectSummary.ID, "app.gora")
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := registry.Runtime(projectSummary.ID, viewSummary.ID)
	if err != nil {
		t.Fatal(err)
	}
	before := findText(runtime.Snapshot().Root)
	if before != "hello" {
		t.Fatalf("initial text = %q", before)
	}
	staged, err := registry.ApplyTestOverlay(projectSummary.ID, viewSummary.ID, overlayBase(registry, projectSummary.ID, viewSummary.ID), []TestOverlayEntry{{Path: "app.gora", Kind: "source", Text: string(bytes.Replace(disk, []byte("hello"), []byte("staged"), 1))}}, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(staged.Staged) != 1 || findText(runtime.Snapshot().Root) != "hello" {
		t.Fatalf("staged overlay changed publication: %+v text=%q", staged, findText(runtime.Snapshot().Root))
	}
	installed, err := registry.ApplyTestOverlay(projectSummary.ID, viewSummary.ID, staged.Revision, []TestOverlayEntry{}, true)
	if err != nil {
		t.Fatal(err)
	}
	if !installed.Installed || len(installed.Entries) != 1 || findText(runtime.Snapshot().Root) != "staged" {
		t.Fatalf("installed overlay not published: %+v text=%q", installed, findText(runtime.Snapshot().Root))
	}
	if got, _ := os.ReadFile(entry); !bytes.Equal(got, disk) {
		t.Fatalf("overlay mutated disk bytes")
	}
	if _, err := registry.ApplyTestOverlay(projectSummary.ID, viewSummary.ID, "sha256:stale", []TestOverlayEntry{{Path: "app.gora", Kind: "source", Text: string(disk)}}, true); err == nil {
		t.Fatal("stale overlay revision accepted")
	}
	cleared, err := registry.ClearTestOverlay(projectSummary.ID, viewSummary.ID, overlayBase(registry, projectSummary.ID, viewSummary.ID), nil, true)
	if err != nil {
		t.Fatal(err)
	}
	if cleared.Installed || findText(runtime.Snapshot().Root) != "hello" {
		t.Fatalf("clear did not restore disk: %+v text=%q", cleared, findText(runtime.Snapshot().Root))
	}

	service := NewServiceWithOptions(registry, ServiceOptions{Automation: true})
	defer service.Close()
	resource, err := service.registry.OverlaySnapshot(projectSummary.ID, viewSummary.ID)
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(resource)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(data, []byte("source")) || bytes.Contains(data, []byte("hello")) {
		t.Fatalf("overlay resource leaked raw bytes: %s", data)
	}
}

func findText(node *project.Node) string {
	if node == nil {
		return ""
	}
	if text, ok := node.Props["text"].(string); ok {
		return text
	}
	for _, child := range node.Children {
		if text := findText(child); text != "" {
			return text
		}
	}
	return ""
}

func overlayBase(registry *Registry, projectID, viewID string) string {
	snapshot, _ := registry.OverlaySnapshot(projectID, viewID)
	return snapshot.Revision
}

func TestPhase6MCPOverlayEventsAndCountedFaults(t *testing.T) {
	root := t.TempDir()
	entry := filepath.Join(root, "app.gora")
	original := []byte("gora: 1\nkind: app\nviewport: {width: 10, height: 10}\nentry: main\nscreens: {main: {type: text, props: {text: hello}}}\n")
	if err := os.WriteFile(entry, original, 0o600); err != nil {
		t.Fatal(err)
	}
	service := NewServiceWithOptions(NewRegistry(), ServiceOptions{Automation: true})
	defer service.Close()
	ctx := context.Background()
	clientTransport, serverTransport := mcp.NewInMemoryTransports()
	if _, err := service.server.Connect(ctx, serverTransport, nil); err != nil {
		t.Fatal(err)
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "phase6-mcp", Version: "1"}, nil)
	session, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	opened, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "gora_open_project", Arguments: map[string]any{"root": root}})
	if err != nil || opened.IsError {
		t.Fatalf("open project: %v %+v", err, opened)
	}
	projectID := stringField(opened.StructuredContent, "project_id")
	openedView, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "gora_open_view", Arguments: map[string]any{"project_id": projectID, "file": "app.gora"}})
	if err != nil || openedView.IsError {
		t.Fatalf("open view: %v %+v", err, openedView)
	}
	viewID := stringField(openedView.StructuredContent, "view_id")
	initialOverlay, _ := service.registry.OverlaySnapshot(projectID, viewID)
	updated := string(bytes.Replace(original, []byte("hello"), []byte("updated"), 1))
	result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "gora_apply_test_overlay", Arguments: map[string]any{
		"project_id": projectID, "view_id": viewID, "base_overlay_revision": initialOverlay.Revision, "install": true,
		"entries": []any{map[string]any{"path": "app.gora", "kind": "source", "text": updated}},
	}})
	if err != nil || result.IsError {
		t.Fatalf("apply overlay: %v %+v", err, result)
	}
	resourceURI := "gora://project/" + projectID + "/views/" + viewID + "/automation/overlay"
	resource, err := session.ReadResource(ctx, &mcp.ReadResourceParams{URI: resourceURI})
	if err != nil || len(resource.Contents) != 1 {
		t.Fatalf("overlay resource: %v %+v", err, resource)
	}
	if strings.Contains(resource.Contents[0].Text, "updated") {
		t.Fatalf("overlay resource leaked raw source: %s", resource.Contents[0].Text)
	}
	installedOverlay, _ := service.registry.OverlaySnapshot(projectID, viewID)
	if _, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "gora_configure_test_faults", Arguments: map[string]any{
		"project_id": projectID, "view_id": viewID,
		"rules": []any{map[string]any{"kind": "candidate_cancel", "remaining": 1}},
	}}); err != nil {
		t.Fatal(err)
	}
	failed, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "gora_inject_reload_events", Arguments: map[string]any{
		"project_id": projectID, "view_id": viewID, "base_overlay_revision": installedOverlay.Revision,
		"events": []any{map[string]any{"kind": "write", "path": "app.gora"}},
	}})
	if err != nil || !failed.IsError {
		t.Fatalf("counted candidate fault not consumed: %v %+v", err, failed)
	}
	recovered, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "gora_inject_reload_events", Arguments: map[string]any{
		"project_id": projectID, "view_id": viewID, "base_overlay_revision": installedOverlay.Revision,
		"events": []any{map[string]any{"kind": "write", "path": "app.gora"}},
	}})
	if err != nil || recovered.IsError {
		t.Fatalf("reload after fault did not recover: %v %+v", err, recovered)
	}
}

func TestPhase6OverlayBoundsContainmentAndAtomicEvents(t *testing.T) {
	root := t.TempDir()
	entry := filepath.Join(root, "app.gora")
	source := []byte("gora: 1\nkind: app\nviewport: {width: 10, height: 10}\nentry: main\nscreens: {main: {type: text, props: {text: hello}}}\n")
	if err := os.WriteFile(entry, source, 0o600); err != nil {
		t.Fatal(err)
	}
	registry := NewRegistry()
	defer registry.Close()
	p, err := registry.OpenProject(root)
	if err != nil {
		t.Fatal(err)
	}
	v, err := registry.OpenView(p.ID, "app.gora")
	if err != nil {
		t.Fatal(err)
	}
	tooMany := make([]TestOverlayEntry, maxOverlayEntries+1)
	for index := range tooMany {
		tooMany[index] = TestOverlayEntry{Path: fmt.Sprintf("%d.gora", index), Kind: "source", Text: string(source)}
	}
	if _, err := registry.ApplyTestOverlay(p.ID, v.ID, overlayBase(registry, p.ID, v.ID), tooMany, false); err == nil {
		t.Fatal("overlay entry bound not enforced")
	}
	if _, err := registry.ApplyTestOverlay(p.ID, v.ID, overlayBase(registry, p.ID, v.ID), []TestOverlayEntry{{Path: "app.gora", Kind: "bytes", DataBase64: base64.StdEncoding.EncodeToString(make([]byte, maxOverlayEntry+1))}}, false); err == nil {
		t.Fatal("overlay byte bound not enforced")
	}
	outside := filepath.Join(filepath.Dir(root), "phase6-outside.gora")
	if err := os.WriteFile(outside, source, 0o600); err != nil {
		t.Fatal(err)
	}
	defer os.Remove(outside)
	if _, err := registry.ApplyTestOverlay(p.ID, v.ID, overlayBase(registry, p.ID, v.ID), []TestOverlayEntry{{Path: outside, Kind: "source", Text: string(source)}}, false); err == nil {
		t.Fatal("absolute outside overlay accepted")
	}
	if _, err := registry.ApplyTestOverlay(p.ID, v.ID, overlayBase(registry, p.ID, v.ID), []TestOverlayEntry{{Path: "../phase6-outside.gora", Kind: "source", Text: string(source)}}, false); err == nil {
		t.Fatal("traversal overlay accepted")
	}
	before, err := registry.OverlaySnapshot(p.ID, v.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := registry.InjectReloadEvents(p.ID, v.ID, before.Revision, "", []ReloadEvent{
		{Kind: "write", Path: "app.gora"},
		{Kind: "rename", Path: "app.gora", To: "renamed.gora"},
		{Kind: "remove", Path: "renamed.gora"},
	}); err != nil {
		t.Fatal(err)
	}
	after, err := registry.OverlaySnapshot(p.ID, v.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(after.Entries) != 0 || after.Revision == before.Revision {
		t.Fatalf("ordered event final state = %+v before=%+v", after, before)
	}
	failed, err := registry.InjectReloadEvents(p.ID, v.ID, after.Revision, "", []ReloadEvent{{Kind: "write", Path: "app.gora"}, {Kind: "invalid", Path: "missing.gora"}})
	if err == nil || failed.Revision != "" {
		t.Fatalf("invalid event batch was not atomic: result=%+v err=%v", failed, err)
	}
	unchanged, _ := registry.OverlaySnapshot(p.ID, v.ID)
	if unchanged.Revision != after.Revision || len(unchanged.Entries) != 0 {
		t.Fatalf("invalid event batch changed overlay: %+v", unchanged)
	}
}

func TestPhase6ExternalReloadRetainsViewOverlayAndDiskBytes(t *testing.T) {
	root := t.TempDir()
	entry := filepath.Join(root, "app.gora")
	disk := []byte("gora: 1\nkind: app\nviewport: {width: 10, height: 10}\nentry: main\nscreens: {main: {type: text, props: {text: disk}}}\n")
	if err := os.WriteFile(entry, disk, 0o600); err != nil {
		t.Fatal(err)
	}
	registry := NewRegistry()
	defer registry.Close()
	p, err := registry.OpenProject(root)
	if err != nil {
		t.Fatal(err)
	}
	v, err := registry.OpenView(p.ID, "app.gora")
	if err != nil {
		t.Fatal(err)
	}
	_, err = registry.ApplyTestOverlay(p.ID, v.ID, overlayBase(registry, p.ID, v.ID), []TestOverlayEntry{{Path: "app.gora", Kind: "source", Text: string(bytes.Replace(disk, []byte("disk"), []byte("overlay"), 1))}}, true)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(entry, []byte(bytes.Replace(disk, []byte("disk"), []byte("external"), 1)), 0o600); err != nil {
		t.Fatal(err)
	}
	project, err := registry.project(p.ID)
	if err != nil {
		t.Fatal(err)
	}
	project.mu.Lock()
	project.reloadAffectedViewsLocked(map[string]bool{entry: true})
	project.mu.Unlock()
	runtime, err := registry.Runtime(p.ID, v.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got := findText(runtime.Snapshot().Root); got != "overlay" {
		t.Fatalf("external reload replaced view-local overlay with %q", got)
	}
}

func TestPhase6InvalidOverlayRetainsLastGoodAndRecovers(t *testing.T) {
	root := t.TempDir()
	entry := filepath.Join(root, "app.gora")
	disk := []byte("gora: 1\nkind: app\nviewport: {width: 10, height: 10}\nentry: main\nscreens: {main: {type: text, props: {text: good}}}\n")
	if err := os.WriteFile(entry, disk, 0o600); err != nil {
		t.Fatal(err)
	}
	registry := NewRegistry()
	defer registry.Close()
	p, err := registry.OpenProject(root)
	if err != nil {
		t.Fatal(err)
	}
	v, err := registry.OpenView(p.ID, "app.gora")
	if err != nil {
		t.Fatal(err)
	}
	invalid, err := registry.ApplyTestOverlay(p.ID, v.ID, overlayBase(registry, p.ID, v.ID), []TestOverlayEntry{{Path: "app.gora", Kind: "source", Text: "gora: [invalid"}}, true)
	if err != nil {
		t.Fatal(err)
	}
	if invalid.Valid || len(invalid.Diagnostics) == 0 || !invalid.LastGoodAvailable {
		t.Fatalf("invalid overlay metadata = %+v", invalid)
	}
	runtime, _ := registry.Runtime(p.ID, v.ID)
	if !runtime.Snapshot().Invalid || findText(runtime.Snapshot().Root) != "good" {
		t.Fatalf("invalid overlay lost last-good tree: %+v", runtime.Snapshot())
	}
	recovered, err := registry.ApplyTestOverlay(p.ID, v.ID, invalid.Revision, []TestOverlayEntry{{Path: "app.gora", Kind: "source", Text: string(bytes.Replace(disk, []byte("good"), []byte("recovered"), 1))}}, true)
	if err != nil {
		t.Fatal(err)
	}
	if !recovered.Valid || len(recovered.Diagnostics) != 0 || findText(runtime.Snapshot().Root) != "recovered" {
		t.Fatalf("overlay recovery failed: %+v text=%q", recovered, findText(runtime.Snapshot().Root))
	}
}

func TestPhase6SourceBytesMissingIsolationAndBoundedCycles(t *testing.T) {
	makeProject := func() string {
		root := t.TempDir()
		if err := os.WriteFile(filepath.Join(root, "app.gora"), []byte("gora: 1\nkind: app\nviewport: {width: 10, height: 10}\nentry: main\nscreens: {main: {type: text, props: {text: base}}}\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		return root
	}
	registry := NewRegistry()
	defer registry.Close()
	rootA, rootB := makeProject(), makeProject()
	pA, err := registry.OpenProject(rootA)
	if err != nil {
		t.Fatal(err)
	}
	pB, err := registry.OpenProject(rootB)
	if err != nil {
		t.Fatal(err)
	}
	vA, err := registry.OpenView(pA.ID, "app.gora")
	if err != nil {
		t.Fatal(err)
	}
	runtimeA, _ := registry.Runtime(pA.ID, vA.ID)
	if err := runtimeA.ConfigureEventTrace(true, 4); err != nil {
		t.Fatal(err)
	}
	vB, err := registry.OpenView(pB.ID, "app.gora")
	if err != nil {
		t.Fatal(err)
	}
	bytesData := []byte{1, 2, 3, 4}
	if _, err := registry.ApplyTestOverlay(pA.ID, vA.ID, overlayBase(registry, pA.ID, vA.ID), []TestOverlayEntry{
		{Path: "assets/blob.bin", Kind: "bytes", DataBase64: base64.StdEncoding.EncodeToString(bytesData)},
		{Path: "components/missing.gora", Kind: "missing"},
	}, true); err != nil {
		t.Fatal(err)
	}
	a, _ := registry.OverlaySnapshot(pA.ID, vA.ID)
	b, _ := registry.OverlaySnapshot(pB.ID, vB.ID)
	if len(a.Entries) != 2 || len(b.Entries) != 0 || a.Revision == b.Revision {
		t.Fatalf("overlay isolation/source kinds failed: A=%+v B=%+v", a, b)
	}
	projectA, _ := registry.project(pA.ID)
	projectA.mu.RLock()
	for sourcePath := range projectA.sources {
		if strings.Contains(sourcePath, "assets") || strings.Contains(sourcePath, "missing.gora") {
			t.Fatalf("view-local overlay polluted project source set: %s", sourcePath)
		}
	}
	projectA.mu.RUnlock()
	for cycle := 0; cycle < 100; cycle++ {
		if _, err := registry.ApplyTestOverlay(pA.ID, vA.ID, overlayBase(registry, pA.ID, vA.ID), []TestOverlayEntry{{Path: "app.gora", Kind: "source", Text: fmt.Sprintf("gora: [invalid-%d", cycle)}}, true); err != nil {
			t.Fatal(err)
		}
		if _, err := registry.ClearTestOverlay(pA.ID, vA.ID, overlayBase(registry, pA.ID, vA.ID), nil, true); err != nil {
			t.Fatal(err)
		}
	}
	final, _ := registry.OverlaySnapshot(pA.ID, vA.ID)
	if len(final.Entries) != 0 || len(final.Staged) != 0 {
		t.Fatalf("overlay retained entries after bounded cycles: %+v", final)
	}
	if trace := runtimeA.EventTrace(); len(trace.Entries) > trace.Capacity {
		t.Fatalf("trace exceeded bounded capacity: %+v", trace)
	}
}

func TestPhase6OverlayResourceSubscriptionAndCaptureFault(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "app.gora"), []byte("gora: 1\nkind: app\nviewport: {width: 10, height: 10}\nentry: main\nscreens: {main: {type: text, props: {text: base}}}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	service := NewServiceWithOptions(NewRegistry(), ServiceOptions{Automation: true})
	defer service.Close()
	ctx := context.Background()
	clientTransport, serverTransport := mcp.NewInMemoryTransports()
	if _, err := service.server.Connect(ctx, serverTransport, nil); err != nil {
		t.Fatal(err)
	}
	updates := make(chan string, 8)
	client := mcp.NewClient(&mcp.Implementation{Name: "phase6-sub", Version: "1"}, &mcp.ClientOptions{ResourceUpdatedHandler: func(_ context.Context, request *mcp.ResourceUpdatedNotificationRequest) {
		if request != nil && request.Params != nil {
			updates <- request.Params.URI
		}
	}})
	session, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	opened, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "gora_open_project", Arguments: map[string]any{"root": root}})
	if err != nil || opened.IsError {
		t.Fatal(err)
	}
	projectID := stringField(opened.StructuredContent, "project_id")
	openedView, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "gora_open_view", Arguments: map[string]any{"project_id": projectID, "file": "app.gora"}})
	if err != nil || openedView.IsError {
		t.Fatal(err)
	}
	viewID := stringField(openedView.StructuredContent, "view_id")
	initialOverlay, _ := service.registry.OverlaySnapshot(projectID, viewID)
	uri := "gora://project/" + projectID + "/views/" + viewID + "/automation/overlay"
	if err := session.Subscribe(ctx, &mcp.SubscribeParams{URI: uri}); err != nil {
		t.Fatal(err)
	}
	if _, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "gora_apply_test_overlay", Arguments: map[string]any{"project_id": projectID, "view_id": viewID, "base_overlay_revision": initialOverlay.Revision, "install": true, "entries": []any{map[string]any{"path": "app.gora", "kind": "source", "text": "gora: [invalid"}}}}); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-updates:
		if got != uri {
			t.Fatalf("overlay notification URI=%q", got)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for overlay notification")
	}
	if _, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "gora_configure_test_faults", Arguments: map[string]any{"project_id": projectID, "view_id": viewID, "rules": []any{map[string]any{"kind": "capture_failure", "remaining": 1}}}}); err != nil {
		t.Fatal(err)
	}
	output := filepath.Join(root, "should-not-write.png")
	result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "gora_capture", Arguments: map[string]any{"project_id": projectID, "view_id": viewID, "scale": 1, "output": filepath.Base(output)}})
	if err != nil || !result.IsError {
		t.Fatalf("capture fault result=%+v err=%v", result, err)
	}
	if _, err := os.Stat(output); !os.IsNotExist(err) {
		t.Fatalf("capture fault wrote output: %v", err)
	}
}

func TestPhase6OverlayTraceRecordsCandidateReadsFaultsAndReconciliation(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "app.gora"), []byte("gora: 1\nkind: app\nviewport: {width: 10, height: 10}\nentry: main\nscreens: {main: {type: text, props: {text: base}}}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	registry := NewRegistry()
	defer registry.Close()
	p, err := registry.OpenProject(root)
	if err != nil {
		t.Fatal(err)
	}
	v, err := registry.OpenView(p.ID, "app.gora")
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := registry.Runtime(p.ID, v.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := runtime.ConfigureEventTrace(true, 64); err != nil {
		t.Fatal(err)
	}
	if err := registry.ConfigureTestFaults(p.ID, v.ID, []TestFaultRule{{Kind: "source_read", Path: "app.gora", Remaining: 1}}); err != nil {
		t.Fatal(err)
	}
	if result, err := registry.ApplyTestOverlay(p.ID, v.ID, overlayBase(registry, p.ID, v.ID), nil, true); err != nil || result.Valid {
		t.Fatalf("source fault candidate unexpectedly valid: %+v err=%v", result, err)
	}
	seen := map[string]bool{}
	for _, entry := range runtime.EventTrace().Entries {
		if entry.Stage == "overlay" {
			seen[entry.Type] = true
		}
	}
	for _, typ := range []string{"candidate", "read", "fault", "diagnostics", "reconciliation", "publication"} {
		if !seen[typ] {
			t.Fatalf("overlay trace omitted %q: %+v", typ, runtime.EventTrace())
		}
	}
}

func TestPhase6OverlayTraceOmitsUnusedFaultPaths(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "app.gora"), []byte("gora: 1\nkind: app\nviewport: {width: 10, height: 10}\nentry: main\nscreens: {main: {type: text, props: {text: base}}}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	registry := NewRegistry()
	defer registry.Close()
	p, err := registry.OpenProject(root)
	if err != nil {
		t.Fatal(err)
	}
	v, err := registry.OpenView(p.ID, "app.gora")
	if err != nil {
		t.Fatal(err)
	}
	runtime, _ := registry.Runtime(p.ID, v.ID)
	if err := runtime.ConfigureEventTrace(true, 64); err != nil {
		t.Fatal(err)
	}
	if err := registry.ConfigureTestFaults(p.ID, v.ID, []TestFaultRule{{Kind: "source_read", Path: "unused.gora", Remaining: 1}}); err != nil {
		t.Fatal(err)
	}
	result, err := registry.ApplyTestOverlay(p.ID, v.ID, overlayBase(registry, p.ID, v.ID), nil, true)
	if err != nil || !result.Valid {
		t.Fatalf("unused fault changed candidate validity: %+v err=%v", result, err)
	}
	for _, entry := range runtime.EventTrace().Entries {
		if entry.TargetID == "unused.gora" {
			t.Fatalf("unused provider path appeared in trace: %+v", entry)
		}
	}
	overlay, _ := registry.OverlaySnapshot(p.ID, v.ID)
	if len(overlay.Faults) != 1 || overlay.Faults[0].Remaining != 1 {
		t.Fatalf("unused fault was consumed: %+v", overlay.Faults)
	}
}

func TestPhase6DelayedCandidateReleasedByClear(t *testing.T) {
	root := t.TempDir()
	base := "gora: 1\nkind: app\nviewport: {width: 10, height: 10}\nentry: main\nscreens: {main: {type: text, props: {text: base}}}\n"
	if err := os.WriteFile(filepath.Join(root, "app.gora"), []byte(base), 0o600); err != nil {
		t.Fatal(err)
	}
	registry := NewRegistry()
	defer registry.Close()
	p, err := registry.OpenProject(root)
	if err != nil {
		t.Fatal(err)
	}
	v, err := registry.OpenView(p.ID, "app.gora")
	if err != nil {
		t.Fatal(err)
	}
	if err := registry.ConfigureTestFaults(p.ID, v.ID, []TestFaultRule{{Kind: "delayed_candidate", Remaining: 1000}}); err != nil {
		t.Fatal(err)
	}
	if pendingResult, err := registry.ApplyTestOverlay(p.ID, v.ID, overlayBase(registry, p.ID, v.ID), []TestOverlayEntry{{Path: "app.gora", Kind: "source", Text: base}}, true); err != nil || !pendingResult.CandidateReload {
		t.Fatalf("delayed candidate was not returned as structured pending result: %+v err=%v", pendingResult, err)
	}
	pending, _ := registry.OverlaySnapshot(p.ID, v.ID)
	if !pending.CandidateReload || pending.Installed {
		t.Fatalf("delayed candidate was not retained as pending: %+v", pending)
	}
	runtime, _ := registry.Runtime(p.ID, v.ID)
	baseline := runtime.AutomationSnapshot().FrameRevision
	waitCtx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, waitErr := runtime.WaitForView(waitCtx, studio.WaitForViewRequest{Condition: "published", StableFrames: 1, Timeout: 10 * time.Millisecond, AfterFrameSet: true, AfterFrameRevision: baseline, AllowAlreadySatisfied: false}); waitErr == nil {
		t.Fatal("delayed candidate unexpectedly published while pending")
	}
	if err := registry.ClearTestFaults(p.ID, v.ID, "delayed_candidate", "", false); err != nil {
		t.Fatal(err)
	}
	// ClearTestFaults releases the pending candidate; this subsequent install
	// is a normal idempotent refresh and must not publish a second release.
	if _, err := registry.ApplyTestOverlay(p.ID, v.ID, overlayBase(registry, p.ID, v.ID), []TestOverlayEntry{{Path: "app.gora", Kind: "source", Text: base}}, true); err != nil {
		t.Fatal("clear did not release delayed candidate: " + err.Error())
	}
	released, _ := registry.OverlaySnapshot(p.ID, v.ID)
	if released.CandidateReload || !released.Installed {
		t.Fatalf("delayed candidate did not publish once released: %+v", released)
	}
	if _, waitErr := runtime.WaitForView(context.Background(), studio.WaitForViewRequest{Condition: "published", StableFrames: 1, Timeout: time.Second, AfterFrameSet: true, AfterFrameRevision: baseline, AllowAlreadySatisfied: true}); waitErr != nil {
		t.Fatalf("released candidate did not satisfy publication wait: %v", waitErr)
	}
}

func TestPhase6MCPDelayedCandidateIsStructuredAndReleasesOnce(t *testing.T) {
	root := t.TempDir()
	entry := filepath.Join(root, "app.gora")
	base := []byte("gora: 1\nkind: app\nviewport: {width: 10, height: 10}\nentry: main\nscreens: {main: {type: text, props: {text: hello}}}\n")
	if err := os.WriteFile(entry, base, 0o600); err != nil {
		t.Fatal(err)
	}
	service := NewServiceWithOptions(NewRegistry(), ServiceOptions{Automation: true})
	defer service.Close()
	ctx := context.Background()
	clientTransport, serverTransport := mcp.NewInMemoryTransports()
	if _, err := service.server.Connect(ctx, serverTransport, nil); err != nil {
		t.Fatal(err)
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "phase6-delayed", Version: "1"}, nil)
	session, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	opened, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "gora_open_project", Arguments: map[string]any{"root": root}})
	if err != nil || opened.IsError {
		t.Fatalf("open project: %v %+v", err, opened)
	}
	projectID := stringField(opened.StructuredContent, "project_id")
	openedView, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "gora_open_view", Arguments: map[string]any{"project_id": projectID, "file": "app.gora"}})
	if err != nil || openedView.IsError {
		t.Fatalf("open view: %v %+v", err, openedView)
	}
	viewID := stringField(openedView.StructuredContent, "view_id")
	initialOverlay, _ := service.registry.OverlaySnapshot(projectID, viewID)
	if configured, callErr := session.CallTool(ctx, &mcp.CallToolParams{Name: "gora_configure_test_faults", Arguments: map[string]any{"project_id": projectID, "view_id": viewID, "rules": []any{map[string]any{"kind": "delayed_candidate", "remaining": 1}}}}); callErr != nil || configured.IsError {
		t.Fatalf("configure delayed fault: %v %+v", callErr, configured)
	}
	if frozen, callErr := session.CallTool(ctx, &mcp.CallToolParams{Name: "gora_set_view_clock", Arguments: map[string]any{"project_id": projectID, "view_id": viewID, "mode": "frozen"}}); callErr != nil || frozen.IsError {
		t.Fatalf("freeze clock: %v %+v", callErr, frozen)
	}
	updated := string(bytes.Replace(base, []byte("hello"), []byte("pending"), 1))
	pending, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "gora_apply_test_overlay", Arguments: map[string]any{"project_id": projectID, "view_id": viewID, "base_overlay_revision": initialOverlay.Revision, "install": true, "wait": "published", "timeout_ms": 20, "entries": []any{map[string]any{"path": "app.gora", "kind": "source", "text": updated}}}})
	if err != nil || pending.IsError {
		t.Fatalf("delayed candidate was returned as a tool error: %v %+v", err, pending)
	}
	pendingJSON, _ := json.Marshal(pending.StructuredContent)
	if !bytes.Contains(pendingJSON, []byte(`"candidate_reload":true`)) || !bytes.Contains(pendingJSON, []byte(`"pending_revision"`)) {
		t.Fatalf("pending candidate metadata missing: %s", pendingJSON)
	}
	runtime, err := service.registry.Runtime(projectID, viewID)
	if err != nil {
		t.Fatal(err)
	}
	baseline := runtime.Snapshot().FrameRevision
	waitCtx, cancel := context.WithTimeout(ctx, 30*time.Millisecond)
	defer cancel()
	waited, waitErr := session.CallTool(waitCtx, &mcp.CallToolParams{Name: "gora_wait_for_view", Arguments: map[string]any{"project_id": projectID, "view_id": viewID, "condition": "published", "after_frame_revision": baseline + 1, "timeout_ms": 20}})
	if waitErr != nil || !waited.IsError {
		t.Fatalf("delayed candidate unexpectedly satisfied wait: %v %+v", waitErr, waited)
	}
	advanced, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "gora_advance_view_clock", Arguments: map[string]any{"project_id": projectID, "view_id": viewID, "delta_ms": 1}})
	if err != nil || advanced.IsError {
		t.Fatalf("clock release: %v %+v", err, advanced)
	}
	published := runtime.Snapshot().FrameRevision
	if published <= baseline || findText(runtime.Snapshot().Root) != "pending" {
		t.Fatalf("delayed candidate did not publish on release: baseline=%d now=%d text=%q", baseline, published, findText(runtime.Snapshot().Root))
	}
	if _, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "gora_advance_view_clock", Arguments: map[string]any{"project_id": projectID, "view_id": viewID, "delta_ms": 1}}); err != nil {
		t.Fatal(err)
	}
	if got := runtime.Snapshot().FrameRevision; got != published {
		t.Fatalf("delayed candidate released more than once: first=%d second=%d", published, got)
	}
}

func TestPhase6DelayedFaultCountAndStagedDisposition(t *testing.T) {
	root := t.TempDir()
	base := "gora: 1\nkind: app\nviewport: {width: 10, height: 10}\nentry: main\nscreens: {main: {type: text, props: {text: base}}}\n"
	if err := os.WriteFile(filepath.Join(root, "app.gora"), []byte(base), 0o600); err != nil {
		t.Fatal(err)
	}
	registry := NewRegistry()
	defer registry.Close()
	p, err := registry.OpenProject(root)
	if err != nil {
		t.Fatal(err)
	}
	v, err := registry.OpenView(p.ID, "app.gora")
	if err != nil {
		t.Fatal(err)
	}
	if err := registry.ConfigureTestFaults(p.ID, v.ID, []TestFaultRule{{Kind: "delayed_candidate", Remaining: 2}}); err != nil {
		t.Fatal(err)
	}
	first, err := registry.ApplyTestOverlay(p.ID, v.ID, overlayBase(registry, p.ID, v.ID), []TestOverlayEntry{{Path: "app.gora", Kind: "source", Text: base}}, true)
	if err != nil || !first.CandidateReload {
		t.Fatalf("first delayed candidate missing: %+v err=%v", first, err)
	}
	runtime, _ := registry.Runtime(p.ID, v.ID)
	frameBeforeRelease := runtime.Snapshot().FrameRevision
	registry.ReleaseDelayedTestFaults(p.ID, v.ID)
	firstReleased, _ := registry.OverlaySnapshot(p.ID, v.ID)
	if firstReleased.CandidateReload || !firstReleased.Installed || runtime.Snapshot().FrameRevision <= frameBeforeRelease {
		t.Fatalf("first delayed release did not install/publish: %+v", firstReleased)
	}
	remaining, _ := registry.OverlaySnapshot(p.ID, v.ID)
	if len(remaining.Faults) != 1 || remaining.Faults[0].Remaining != 1 {
		t.Fatalf("release consumed future delayed occurrence: %+v", remaining.Faults)
	}
	second, err := registry.ApplyTestOverlay(p.ID, v.ID, overlayBase(registry, p.ID, v.ID), []TestOverlayEntry{{Path: "app.gora", Kind: "source", Text: base}}, true)
	if err != nil || !second.CandidateReload {
		t.Fatalf("second delayed occurrence missing: %+v err=%v", second, err)
	}
	registry.ReleaseDelayedTestFaults(p.ID, v.ID)
	if third, err := registry.ApplyTestOverlay(p.ID, v.ID, overlayBase(registry, p.ID, v.ID), []TestOverlayEntry{{Path: "app.gora", Kind: "source", Text: base}}, true); err != nil || third.CandidateReload {
		t.Fatalf("exhausted delayed rule still blocked third candidate: %+v err=%v", third, err)
	}
	if err := registry.ConfigureTestFaults(p.ID, v.ID, []TestFaultRule{{Kind: "delayed_candidate", Remaining: 1}}); err != nil {
		t.Fatal(err)
	}
	stagedText := strings.Replace(base, "base", "staged", 1)
	pendingStage, err := registry.ApplyTestOverlay(p.ID, v.ID, overlayBase(registry, p.ID, v.ID), []TestOverlayEntry{{Path: "app.gora", Kind: "source", Text: stagedText}}, false)
	if err != nil || !pendingStage.CandidateReload {
		t.Fatalf("delayed staged candidate missing: %+v err=%v", pendingStage, err)
	}
	frameBeforeStageRelease := runtime.Snapshot().FrameRevision
	registry.ReleaseDelayedTestFaults(p.ID, v.ID)
	staged, _ := registry.OverlaySnapshot(p.ID, v.ID)
	if staged.CandidateReload || len(staged.Staged) != 1 || runtime.Snapshot().FrameRevision != frameBeforeStageRelease {
		t.Fatalf("install:false delayed release changed publication: %+v", staged)
	}
	installed, err := registry.ApplyTestOverlay(p.ID, v.ID, staged.Revision, nil, true)
	if err != nil || !installed.Installed || findText(runtime.Snapshot().Root) != "staged" {
		t.Fatalf("staged delayed generation did not install: %+v err=%v", installed, err)
	}
}

func TestPhase6ClearDiscardsStagedAndPendingGenerations(t *testing.T) {
	root := t.TempDir()
	base := []byte("gora: 1\nkind: app\nviewport: {width: 10, height: 10}\nentry: main\nscreens: {main: {type: text, props: {text: disk}}}\n")
	if err := os.WriteFile(filepath.Join(root, "app.gora"), base, 0o600); err != nil {
		t.Fatal(err)
	}
	registry := NewRegistry()
	defer registry.Close()
	p, err := registry.OpenProject(root)
	if err != nil {
		t.Fatal(err)
	}
	v, err := registry.OpenView(p.ID, "app.gora")
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := registry.Runtime(p.ID, v.ID)
	if err != nil {
		t.Fatal(err)
	}
	stagedText := strings.Replace(string(base), "disk", "staged", 1)
	staged, err := registry.ApplyTestOverlay(p.ID, v.ID, overlayBase(registry, p.ID, v.ID), []TestOverlayEntry{{Path: "app.gora", Kind: "source", Text: stagedText}}, false)
	if err != nil || len(staged.Staged) != 1 {
		t.Fatalf("stage failed: %+v err=%v", staged, err)
	}
	frameBeforeClear := runtime.Snapshot().FrameRevision
	if got := findText(runtime.Snapshot().Root); got != "disk" {
		t.Fatalf("staged content published before clear: %q", got)
	}
	cleared, err := registry.ClearTestOverlay(p.ID, v.ID, staged.Revision, nil, true)
	if err != nil {
		t.Fatal(err)
	}
	if cleared.CandidateReload || cleared.Installed || len(cleared.Staged) != 0 || len(cleared.Entries) != 0 {
		t.Fatalf("clear left staged/pending state: %+v", cleared)
	}
	if got := findText(runtime.Snapshot().Root); got != "disk" {
		t.Fatalf("clear installed staged content instead of disk: %q", got)
	}
	if runtime.Snapshot().FrameRevision <= frameBeforeClear {
		t.Fatalf("clear did not publish reconciliation frame: before=%d after=%d", frameBeforeClear, runtime.Snapshot().FrameRevision)
	}
	if err := registry.ConfigureTestFaults(p.ID, v.ID, []TestFaultRule{{Kind: "delayed_candidate", Remaining: 1}}); err != nil {
		t.Fatal(err)
	}
	pendingText := strings.Replace(string(base), "disk", "pending", 1)
	pending, err := registry.ApplyTestOverlay(p.ID, v.ID, overlayBase(registry, p.ID, v.ID), []TestOverlayEntry{{Path: "app.gora", Kind: "source", Text: pendingText}}, false)
	if err != nil || !pending.CandidateReload {
		t.Fatalf("pending staged candidate missing: %+v err=%v", pending, err)
	}
	baseBeforePendingClear := overlayBase(registry, p.ID, v.ID)
	clearedPending, err := registry.ClearTestOverlay(p.ID, v.ID, baseBeforePendingClear, nil, true)
	if err != nil {
		t.Fatal(err)
	}
	if clearedPending.CandidateReload || len(clearedPending.Staged) != 0 || len(clearedPending.Entries) != 0 {
		t.Fatalf("clear did not discard pending generation: %+v", clearedPending)
	}
	frameAfterPendingClear := runtime.Snapshot().FrameRevision
	registry.ReleaseDelayedTestFaults(p.ID, v.ID)
	if got := runtime.Snapshot().FrameRevision; got != frameAfterPendingClear {
		t.Fatalf("released cleared pending candidate unexpectedly published: before=%d after=%d", frameAfterPendingClear, got)
	}
}

func TestPhase6NewestPendingCandidateReplacesOlder(t *testing.T) {
	root := t.TempDir()
	base := []byte("gora: 1\nkind: app\nviewport: {width: 10, height: 10}\nentry: main\nscreens: {main: {type: text, props: {text: disk}}}\n")
	if err := os.WriteFile(filepath.Join(root, "app.gora"), base, 0o600); err != nil {
		t.Fatal(err)
	}
	registry := NewRegistry()
	defer registry.Close()
	p, err := registry.OpenProject(root)
	if err != nil {
		t.Fatal(err)
	}
	v, err := registry.OpenView(p.ID, "app.gora")
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := registry.Runtime(p.ID, v.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := registry.ConfigureTestFaults(p.ID, v.ID, []TestFaultRule{{Kind: "delayed_candidate", Remaining: 2}}); err != nil {
		t.Fatal(err)
	}
	firstText := strings.Replace(string(base), "disk", "first", 1)
	first, err := registry.ApplyTestOverlay(p.ID, v.ID, overlayBase(registry, p.ID, v.ID), []TestOverlayEntry{{Path: "app.gora", Kind: "source", Text: firstText}}, true)
	if err != nil || !first.CandidateReload || first.PendingRevision == "" {
		t.Fatalf("first pending candidate missing metadata: %+v err=%v", first, err)
	}
	secondText := strings.Replace(string(base), "disk", "second", 1)
	second, err := registry.ApplyTestOverlay(p.ID, v.ID, overlayBase(registry, p.ID, v.ID), []TestOverlayEntry{{Path: "app.gora", Kind: "source", Text: secondText}}, true)
	if err != nil || !second.CandidateReload || second.PendingRevision == "" || second.PendingRevision == first.PendingRevision {
		t.Fatalf("newer pending candidate did not replace metadata: first=%+v second=%+v err=%v", first, second, err)
	}
	frameBeforeRelease := runtime.Snapshot().FrameRevision
	registry.ReleaseDelayedTestFaults(p.ID, v.ID)
	if snapshot := runtime.Snapshot(); snapshot.FrameRevision != frameBeforeRelease+1 || findText(snapshot.Root) != "second" {
		t.Fatalf("release did not publish only newest candidate: frame before=%d after=%d text=%q", frameBeforeRelease, snapshot.FrameRevision, findText(snapshot.Root))
	}
	released, err := registry.OverlaySnapshot(p.ID, v.ID)
	if err != nil {
		t.Fatal(err)
	}
	if released.CandidateReload || released.PendingRevision != "" || len(released.Faults) != 0 {
		t.Fatalf("pending metadata/fault count not bounded after release: %+v", released)
	}
}

func TestPhase6OverlayBasesAndDiskBackedEvents(t *testing.T) {
	root := t.TempDir()
	base := []byte("gora: 1\nkind: app\nviewport: {width: 10, height: 10}\nentry: main\nscreens: {main: {type: text, props: {text: base}}}\n")
	if err := os.WriteFile(filepath.Join(root, "app.gora"), base, 0o600); err != nil {
		t.Fatal(err)
	}
	registry := NewRegistry()
	defer registry.Close()
	p, err := registry.OpenProject(root)
	if err != nil {
		t.Fatal(err)
	}
	v, err := registry.OpenView(p.ID, "app.gora")
	if err != nil {
		t.Fatal(err)
	}
	installed, err := registry.ApplyTestOverlay(p.ID, v.ID, overlayBase(registry, p.ID, v.ID), []TestOverlayEntry{{Path: "app.gora", Kind: "source", Text: string(bytes.Replace(base, []byte("base"), []byte("first"), 1))}}, true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := registry.ApplyTestOverlay(p.ID, v.ID, "", []TestOverlayEntry{{Path: "app.gora", Kind: "source", Text: string(base)}}, true); err == nil {
		t.Fatal("strict apply accepted omitted base over an installed generation")
	}
	if _, err := registry.ClearTestOverlay(p.ID, v.ID, "", nil, true); err == nil {
		t.Fatal("strict clear accepted omitted base over an installed generation")
	}
	if _, err := registry.InjectReloadEvents(p.ID, v.ID, "", "", []ReloadEvent{{Kind: "remove", Path: "disk-only.gora"}, {Kind: "rename", Path: "disk-only.gora", To: "renamed.gora"}}); err == nil {
		t.Fatal("signal-only inject accepted omitted base")
	}
	if _, err := registry.InjectReloadEvents(p.ID, v.ID, installed.Revision, "", []ReloadEvent{{Kind: "remove", Path: "disk-only.gora"}, {Kind: "rename", Path: "disk-only.gora", To: "renamed.gora"}}); err != nil {
		t.Fatalf("signal-only inject with exact base failed: %v", err)
	}
	result, err := registry.OverlaySnapshot(p.ID, v.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Entries) != len(installed.Entries) {
		t.Fatalf("disk-backed events changed installed provider state: %+v", result)
	}
}

func TestPhase6OverlayProviderImportsCyclesAndAssetBytes(t *testing.T) {
	root := t.TempDir()
	entry := filepath.Join(root, "app.gora")
	diskApp := []byte("gora: 1\nkind: app\nviewport: {width: 10, height: 10}\nentry: main\nscreens: {main: {type: text, props: {text: base}}}\n")
	app := []byte("gora: 1\nkind: app\nviewport: {width: 10, height: 10}\nentry: main\nimports: {components: {a: a.gora}}\nscreens: {main: {type: instance, props: {component: a}}}\n")
	if err := os.WriteFile(entry, diskApp, 0o600); err != nil {
		t.Fatal(err)
	}
	registry := NewRegistry()
	defer registry.Close()
	p, err := registry.OpenProject(root)
	if err != nil {
		t.Fatal(err)
	}
	v, err := registry.OpenView(p.ID, "app.gora")
	if err != nil {
		t.Fatal(err)
	}
	overlay := []TestOverlayEntry{
		{Path: "app.gora", Kind: "source", Text: string(app)},
		{Path: "a.gora", Kind: "source", Text: "gora: 1\nkind: component\nimports: {components: {b: b.gora}}\nviewport: {width: 10, height: 10}\npreviews: {default: {}}\nroot: {type: instance, props: {component: b}}\n"},
		{Path: "b.gora", Kind: "source", Text: "gora: 1\nkind: component\nimports: {components: {a: a.gora}}\nviewport: {width: 10, height: 10}\npreviews: {default: {}}\nroot: {type: instance, props: {component: a}}\n"},
		{Path: "assets/corrupt.png", Kind: "bytes", DataBase64: base64.StdEncoding.EncodeToString([]byte("not png"))},
	}
	result, err := registry.ApplyTestOverlay(p.ID, v.ID, overlayBase(registry, p.ID, v.ID), overlay, true)
	if err != nil {
		t.Fatal(err)
	}
	if result.Valid || len(result.Diagnostics) == 0 {
		t.Fatalf("cycle/invalid overlay did not publish diagnostics: %+v", result)
	}
	if !result.LastGoodAvailable {
		t.Fatal("cycle overlay discarded last-good publication")
	}
}

func TestPhase6OverlayImageBytesReachRendererWithoutDiskCache(t *testing.T) {
	root := t.TempDir()
	entry := filepath.Join(root, "app.gora")
	app := []byte("gora: 1\nkind: app\nviewport: {width: 2, height: 2}\nentry: main\nscreens: {main: {type: image, props: {src: assets/p.png, width: 2, height: 2}}}\n")
	if err := os.WriteFile(entry, app, 0o600); err != nil {
		t.Fatal(err)
	}
	assetDir := filepath.Join(root, "assets")
	if err := os.Mkdir(assetDir, 0o700); err != nil {
		t.Fatal(err)
	}
	encode := func(c color.NRGBA) []byte {
		img := image.NewNRGBA(image.Rect(0, 0, 1, 1))
		img.SetNRGBA(0, 0, c)
		var data bytes.Buffer
		if err := png.Encode(&data, img); err != nil {
			t.Fatal(err)
		}
		return data.Bytes()
	}
	red, blue := encode(color.NRGBA{R: 255, A: 255}), encode(color.NRGBA{B: 255, A: 255})
	if err := os.WriteFile(filepath.Join(assetDir, "p.png"), red, 0o600); err != nil {
		t.Fatal(err)
	}
	registry := NewRegistry()
	defer registry.Close()
	p, err := registry.OpenProject(root)
	if err != nil {
		t.Fatal(err)
	}
	v, err := registry.OpenView(p.ID, "app.gora")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := registry.ApplyTestOverlay(p.ID, v.ID, overlayBase(registry, p.ID, v.ID), []TestOverlayEntry{{Path: "assets/p.png", Kind: "bytes", DataBase64: base64.StdEncoding.EncodeToString(blue)}}, true); err != nil {
		t.Fatal(err)
	}
	runtime, _ := registry.Runtime(p.ID, v.ID)
	snapshot := runtime.Snapshot()
	result := render.Render(snapshot.Root, snapshot.Viewport, render.State{AssetBytes: snapshot.AssetBytes})
	if got := result.Image.RGBAAt(1, 1); got.B != 255 || got.R != 0 {
		t.Fatalf("render used disk image instead of overlay bytes: %#v", got)
	}
	if err := registry.ConfigureTestFaults(p.ID, v.ID, []TestFaultRule{{Kind: "asset_read", Path: "assets/p.png", Remaining: 1}}); err != nil {
		t.Fatal(err)
	}
	readFault, err := registry.ApplyTestOverlay(p.ID, v.ID, overlayBase(registry, p.ID, v.ID), []TestOverlayEntry{{Path: "assets/p.png", Kind: "bytes", DataBase64: base64.StdEncoding.EncodeToString(blue)}}, true)
	if err != nil || readFault.Valid {
		t.Fatalf("asset read fault was not deterministic: %+v err=%v", readFault, err)
	}
	if recovered, recoverErr := registry.ApplyTestOverlay(p.ID, v.ID, overlayBase(registry, p.ID, v.ID), []TestOverlayEntry{{Path: "assets/p.png", Kind: "bytes", DataBase64: base64.StdEncoding.EncodeToString(blue)}}, true); recoverErr != nil || !recovered.Valid {
		t.Fatalf("asset read fault did not exhaust: %+v err=%v", recovered, recoverErr)
	}
	if err := registry.ConfigureTestFaults(p.ID, v.ID, []TestFaultRule{{Kind: "image_decode", Path: "assets/p.png", Remaining: 1}}); err != nil {
		t.Fatal(err)
	}
	corrupt, err := registry.ApplyTestOverlay(p.ID, v.ID, overlayBase(registry, p.ID, v.ID), []TestOverlayEntry{{Path: "assets/p.png", Kind: "bytes", DataBase64: base64.StdEncoding.EncodeToString(blue)}}, true)
	if err != nil {
		t.Fatal(err)
	}
	if corrupt.Valid || len(corrupt.Diagnostics) == 0 {
		t.Fatalf("counted image decode fault did not produce diagnostics: %+v", corrupt)
	}
	recovered, err := registry.ApplyTestOverlay(p.ID, v.ID, overlayBase(registry, p.ID, v.ID), []TestOverlayEntry{{Path: "assets/p.png", Kind: "bytes", DataBase64: base64.StdEncoding.EncodeToString(blue)}}, true)
	if err != nil || !recovered.Valid {
		t.Fatalf("exhausted image decode fault did not recover: %+v err=%v", recovered, err)
	}
}

func TestPhase6OverlayFontBytesAndDecodeFault(t *testing.T) {
	root := t.TempDir()
	entry := filepath.Join(root, "app.gora")
	app := []byte("gora: 1\nkind: app\nviewport: {width: 20, height: 20}\nentry: main\nscreens: {main: {type: text, props: {text: font, font: assets/f.ttf}}}\n")
	if err := os.WriteFile(entry, app, 0o600); err != nil {
		t.Fatal(err)
	}
	registry := NewRegistry()
	defer registry.Close()
	p, err := registry.OpenProject(root)
	if err != nil {
		t.Fatal(err)
	}
	v, err := registry.OpenView(p.ID, "app.gora")
	if err != nil {
		t.Fatal(err)
	}
	valid, err := registry.ApplyTestOverlay(p.ID, v.ID, overlayBase(registry, p.ID, v.ID), []TestOverlayEntry{{Path: "assets/f.ttf", Kind: "bytes", DataBase64: base64.StdEncoding.EncodeToString(goregular.TTF)}}, true)
	if err != nil || !valid.Valid {
		t.Fatalf("valid replacement font rejected: %+v err=%v", valid, err)
	}
	if err := registry.ConfigureTestFaults(p.ID, v.ID, []TestFaultRule{{Kind: "font_decode", Path: "assets/f.ttf", Remaining: 1}}); err != nil {
		t.Fatal(err)
	}
	corrupt, err := registry.ApplyTestOverlay(p.ID, v.ID, overlayBase(registry, p.ID, v.ID), []TestOverlayEntry{{Path: "assets/f.ttf", Kind: "bytes", DataBase64: base64.StdEncoding.EncodeToString(goregular.TTF)}}, true)
	if err != nil {
		t.Fatal(err)
	}
	if corrupt.Valid || len(corrupt.Diagnostics) == 0 {
		t.Fatalf("font decode fault did not produce diagnostics: %+v", corrupt)
	}
}

func TestPhase6MissingProviderAndDelegatedFaults(t *testing.T) {
	root := t.TempDir()
	assetDir := filepath.Join(root, "assets")
	if err := os.Mkdir(assetDir, 0o700); err != nil {
		t.Fatal(err)
	}
	imageBytes := func() []byte {
		img := image.NewNRGBA(image.Rect(0, 0, 1, 1))
		img.SetNRGBA(0, 0, color.NRGBA{R: 255, A: 255})
		var out bytes.Buffer
		if err := png.Encode(&out, img); err != nil {
			t.Fatal(err)
		}
		return out.Bytes()
	}()
	if err := os.WriteFile(filepath.Join(root, "card.gora"), []byte("gora: 1\nkind: component\nviewport: {width: 10, height: 10}\npreviews: {default: {}}\nroot: {type: text, props: {text: card}}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(assetDir, "p.png"), imageBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(assetDir, "f.ttf"), goregular.TTF, 0o600); err != nil {
		t.Fatal(err)
	}
	app := []byte("gora: 1\nkind: app\nviewport: {width: 20, height: 20}\nentry: main\nscreens: {main: {type: image, props: {src: assets/p.png, width: 1, height: 1}}, font: {type: text, props: {text: f, font: assets/f.ttf}}}\n")
	if err := os.WriteFile(filepath.Join(root, "app.gora"), app, 0o600); err != nil {
		t.Fatal(err)
	}
	registry := NewRegistry()
	defer registry.Close()
	p, err := registry.OpenProject(root)
	if err != nil {
		t.Fatal(err)
	}
	v, err := registry.OpenView(p.ID, "app.gora")
	if err != nil {
		t.Fatal(err)
	}
	missing, err := registry.ApplyTestOverlay(p.ID, v.ID, overlayBase(registry, p.ID, v.ID), []TestOverlayEntry{
		{Path: "card.gora", Kind: "missing"},
		{Path: "assets/p.png", Kind: "missing"},
		{Path: "assets/f.ttf", Kind: "missing"},
	}, true)
	if err != nil {
		t.Fatal(err)
	}
	if missing.Valid || len(missing.Diagnostics) < 2 {
		t.Fatalf("missing provider did not shadow disk paths: %+v", missing)
	}
	seenAsset := false
	for _, diagnostic := range missing.Diagnostics {
		if diagnostic.Code == "asset.decode" {
			seenAsset = true
		}
	}
	if !seenAsset {
		t.Fatalf("missing diagnostics lacked exact provider errors: %+v", missing.Diagnostics)
	}
	if _, err := registry.ClearTestOverlay(p.ID, v.ID, overlayBase(registry, p.ID, v.ID), nil, true); err != nil {
		t.Fatal(err)
	}
	missingImport := []byte("gora: 1\nkind: app\nviewport: {width: 20, height: 20}\nentry: main\nimports: {components: {card: card.gora}}\nscreens: {main: {type: text, props: {text: f}}}\n")
	importFailure, importErr := registry.ApplyTestOverlay(p.ID, v.ID, overlayBase(registry, p.ID, v.ID), []TestOverlayEntry{{Path: "app.gora", Kind: "source", Text: string(missingImport)}, {Path: "card.gora", Kind: "missing"}}, true)
	if importErr != nil || importFailure.Valid || !hasDiagnosticCode(importFailure.Diagnostics, "import.read") {
		t.Fatalf("missing import did not report exact read diagnostic: %+v err=%v", importFailure, importErr)
	}
	if _, err := registry.ClearTestOverlay(p.ID, v.ID, overlayBase(registry, p.ID, v.ID), nil, true); err != nil {
		t.Fatal(err)
	}
	for _, rule := range []TestFaultRule{
		{Kind: "source_read", Path: "app.gora", Remaining: 1},
		{Kind: "asset_read", Path: "assets/p.png", Remaining: 1},
		{Kind: "font_decode", Path: "assets/f.ttf", Remaining: 1},
	} {
		if err := registry.ConfigureTestFaults(p.ID, v.ID, []TestFaultRule{rule}); err != nil {
			t.Fatal(err)
		}
		faulted, faultErr := registry.ApplyTestOverlay(p.ID, v.ID, overlayBase(registry, p.ID, v.ID), nil, true)
		if faultErr != nil || faulted.Valid {
			t.Fatalf("delegated %s fault did not fire: %+v err=%v", rule.Kind, faulted, faultErr)
		}
		recovered, recoverErr := registry.ApplyTestOverlay(p.ID, v.ID, overlayBase(registry, p.ID, v.ID), nil, true)
		if recoverErr != nil || !recovered.Valid {
			t.Fatalf("delegated %s fault did not recover after exhaustion: %+v err=%v", rule.Kind, recovered, recoverErr)
		}
	}
}

func TestPhase6FaultKindsDoNotCrossAccessBoundaries(t *testing.T) {
	root := t.TempDir()
	assetDir := filepath.Join(root, "assets")
	if err := os.Mkdir(assetDir, 0o700); err != nil {
		t.Fatal(err)
	}
	img := image.NewNRGBA(image.Rect(0, 0, 1, 1))
	img.SetNRGBA(0, 0, color.NRGBA{R: 255, A: 255})
	var encoded bytes.Buffer
	if err := png.Encode(&encoded, img); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(assetDir, "p.png"), encoded.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(assetDir, "f.ttf"), goregular.TTF, 0o600); err != nil {
		t.Fatal(err)
	}
	app := []byte("gora: 1\nkind: app\nviewport: {width: 20, height: 20}\nentry: main\nscreens: {main: {type: image, props: {src: assets/p.png, width: 1, height: 1}}, font: {type: text, props: {text: f, font: assets/f.ttf}}}\n")
	if err := os.WriteFile(filepath.Join(root, "app.gora"), app, 0o600); err != nil {
		t.Fatal(err)
	}
	registry := NewRegistry()
	defer registry.Close()
	p, err := registry.OpenProject(root)
	if err != nil {
		t.Fatal(err)
	}
	v, err := registry.OpenView(p.ID, "app.gora")
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		kind string
		path string
	}{
		{kind: "source_read", path: "assets/p.png"},
		{kind: "asset_read", path: "app.gora"},
		{kind: "image_decode", path: "assets/f.ttf"},
		{kind: "font_decode", path: "assets/p.png"},
	}
	for _, testCase := range cases {
		if err := registry.ConfigureTestFaults(p.ID, v.ID, []TestFaultRule{{Kind: testCase.kind, Path: testCase.path, Remaining: 1}}); err != nil {
			t.Fatal(err)
		}
		result, err := registry.ApplyTestOverlay(p.ID, v.ID, overlayBase(registry, p.ID, v.ID), nil, true)
		if err != nil || !result.Valid {
			t.Fatalf("wrong-kind %s unexpectedly failed: %+v err=%v", testCase.kind, result, err)
		}
		overlay, _ := registry.OverlaySnapshot(p.ID, v.ID)
		if len(overlay.Faults) != 1 || overlay.Faults[0].Remaining != 1 {
			t.Fatalf("wrong-kind %s was consumed: %+v", testCase.kind, overlay.Faults)
		}
	}
	if err := registry.ConfigureTestFaults(p.ID, v.ID, []TestFaultRule{{Kind: "source_read", Path: "app.gora", Remaining: 1}, {Kind: "asset_read", Path: "app.gora", Remaining: 1}}); err != nil {
		t.Fatal(err)
	}
	faulted, err := registry.ApplyTestOverlay(p.ID, v.ID, overlayBase(registry, p.ID, v.ID), nil, true)
	if err != nil || faulted.Valid {
		t.Fatalf("overlapping source/asset rules did not fail source access: %+v err=%v", faulted, err)
	}
	overlay, _ := registry.OverlaySnapshot(p.ID, v.ID)
	if len(overlay.Faults) != 1 || overlay.Faults[0].Kind != "asset_read" {
		t.Fatalf("overlapping rules consumed the wrong kind: %+v", overlay.Faults)
	}
}

func hasDiagnosticCode(diagnostics []document.Diagnostic, code string) bool {
	for _, diagnostic := range diagnostics {
		if diagnostic.Code == code {
			return true
		}
	}
	return false
}

func TestPhase6InitialInvalidViewRecoversFromStagedOverlay(t *testing.T) {
	root := t.TempDir()
	entry := filepath.Join(root, "app.gora")
	if err := os.WriteFile(entry, []byte("gora: [invalid"), 0o600); err != nil {
		t.Fatal(err)
	}
	valid := "gora: 1\nkind: app\nviewport: {width: 10, height: 10}\nentry: main\nscreens: {main: {type: text, props: {text: recovered}}}\n"
	registry := NewRegistry()
	defer registry.Close()
	p, err := registry.OpenProject(root)
	if err != nil {
		t.Fatal(err)
	}
	v, err := registry.OpenView(p.ID, "app.gora")
	if err != nil {
		t.Fatal(err)
	}
	runtime, _ := registry.Runtime(p.ID, v.ID)
	if !runtime.Snapshot().Invalid {
		t.Fatal("invalid initial view unexpectedly valid")
	}
	staged, err := registry.ApplyTestOverlay(p.ID, v.ID, overlayBase(registry, p.ID, v.ID), []TestOverlayEntry{{Path: "app.gora", Kind: "source", Text: valid}}, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(staged.Staged) != 1 || !runtime.Snapshot().Invalid {
		t.Fatalf("staging invalid initial view mutated runtime: %+v", staged)
	}
	recovered, err := registry.ApplyTestOverlay(p.ID, v.ID, staged.Revision, nil, true)
	if err != nil || !recovered.Valid || findText(runtime.Snapshot().Root) != "recovered" {
		t.Fatalf("staged initial recovery failed: %+v err=%v", recovered, err)
	}
	if err := registry.ConfigureTestFaults(p.ID, v.ID, []TestFaultRule{{Kind: "source_read", Path: "app.gora", Remaining: 1}}); err != nil {
		t.Fatal(err)
	}
	readFault, err := registry.ApplyTestOverlay(p.ID, v.ID, overlayBase(registry, p.ID, v.ID), []TestOverlayEntry{{Path: "app.gora", Kind: "source", Text: valid}}, true)
	if err != nil || readFault.Valid {
		t.Fatalf("source read fault was not injected: %+v err=%v", readFault, err)
	}
	if exhausted, exhaustedErr := registry.ApplyTestOverlay(p.ID, v.ID, overlayBase(registry, p.ID, v.ID), []TestOverlayEntry{{Path: "app.gora", Kind: "source", Text: valid}}, true); exhaustedErr != nil || !exhausted.Valid {
		t.Fatalf("source read fault did not exhaust: %+v err=%v", exhausted, exhaustedErr)
	}
}

func TestPhase6OverlayInvalidTokenReferenceRetainsLastGood(t *testing.T) {
	root := t.TempDir()
	entry := filepath.Join(root, "app.gora")
	disk := []byte("gora: 1\nkind: app\nviewport: {width: 10, height: 10}\nentry: main\nscreens: {main: {type: text, props: {text: base}}}\n")
	if err := os.WriteFile(entry, disk, 0o600); err != nil {
		t.Fatal(err)
	}
	registry := NewRegistry()
	defer registry.Close()
	p, err := registry.OpenProject(root)
	if err != nil {
		t.Fatal(err)
	}
	v, err := registry.OpenView(p.ID, "app.gora")
	if err != nil {
		t.Fatal(err)
	}
	invalid := "gora: 1\nkind: app\nviewport: {width: 10, height: 10}\nentry: main\nimports: {tokens: {theme: theme.gora}}\nscreens: {main: {type: text, props: {text: bad, color: {ref: theme.color.missing}}}}\n"
	result, err := registry.ApplyTestOverlay(p.ID, v.ID, overlayBase(registry, p.ID, v.ID), []TestOverlayEntry{
		{Path: "app.gora", Kind: "source", Text: invalid},
		{Path: "theme.gora", Kind: "source", Text: "gora: 1\nkind: tokens\ntokens: {color: {ink: '#000000'}}\n"},
	}, true)
	if err != nil {
		t.Fatal(err)
	}
	if result.Valid || len(result.Diagnostics) == 0 || !result.LastGoodAvailable {
		t.Fatalf("invalid token reference metadata = %+v", result)
	}
}

func TestPhase6DependencyReplacementPruningAndRecovery(t *testing.T) {
	root := t.TempDir()
	app := []byte("gora: 1\nkind: app\nviewport: {width: 20, height: 20}\nentry: main\nimports: {components: {card: card.gora}}\nscreens: {main: {type: instance, props: {component: card}}}\n")
	card := []byte("gora: 1\nkind: component\nviewport: {width: 20, height: 20}\npreviews: {default: {}}\nroot: {type: text, props: {text: card}}\n")
	if err := os.WriteFile(filepath.Join(root, "app.gora"), app, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "card.gora"), card, 0o600); err != nil {
		t.Fatal(err)
	}
	registry := NewRegistry()
	defer registry.Close()
	p, err := registry.OpenProject(root)
	if err != nil {
		t.Fatal(err)
	}
	v, err := registry.OpenView(p.ID, "app.gora")
	if err != nil {
		t.Fatal(err)
	}
	replaced := string(bytes.Replace(card, []byte("card"), []byte("replaced"), 1))
	if result, err := registry.ApplyTestOverlay(p.ID, v.ID, overlayBase(registry, p.ID, v.ID), []TestOverlayEntry{{Path: "card.gora", Kind: "source", Text: replaced}}, true); err != nil || !result.Valid {
		t.Fatalf("dependency replacement failed: %+v err=%v", result, err)
	}
	if err := os.Remove(filepath.Join(root, "card.gora")); err != nil {
		t.Fatal(err)
	}
	stagedEmpty, err := registry.ApplyTestOverlay(p.ID, v.ID, overlayBase(registry, p.ID, v.ID), nil, false)
	if err != nil {
		t.Fatal(err)
	}
	removed, err := registry.InjectReloadEvents(p.ID, v.ID, stagedEmpty.Revision, stagedEmpty.Revision, []ReloadEvent{{Kind: "remove", Path: "card.gora"}})
	if err != nil || removed.Valid || !removed.LastGoodAvailable {
		t.Fatalf("dependency pruning did not retain last-good: %+v err=%v", removed, err)
	}
	if err := os.WriteFile(filepath.Join(root, "card.gora"), card, 0o600); err != nil {
		t.Fatal(err)
	}
	recovered, err := registry.ClearTestOverlay(p.ID, v.ID, overlayBase(registry, p.ID, v.ID), nil, true)
	if err != nil || !recovered.Valid {
		t.Fatalf("dependency recovery failed: %+v err=%v", recovered, err)
	}
}
