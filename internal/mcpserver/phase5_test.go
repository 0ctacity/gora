package mcpserver

import (
	"bytes"
	"context"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"gora/internal/automation"
	"gora/internal/studio"
)

func TestMCPPhase5AssertionsAndCaptureComparison(t *testing.T) {
	root := t.TempDir()
	entry := filepath.Join(root, "app.gora")
	if err := os.WriteFile(entry, []byte(`gora: 1
kind: app
viewport: { width: 100, height: 80 }
entry: main
screens:
  main:
    type: button
    name: save
    props: { label: Save }
    children: [{ type: text, props: { text: Save } }]
`), 0o600); err != nil {
		t.Fatal(err)
	}
	service := NewServiceWithOptions(NewRegistry(), ServiceOptions{Automation: true})
	defer service.Close()
	ctx := context.Background()
	clientTransport, serverTransport := mcp.NewInMemoryTransports()
	if _, err := service.server.Connect(ctx, serverTransport, nil); err != nil {
		t.Fatal(err)
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "phase5-test", Version: "1"}, nil)
	session, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	tools, err := session.ListTools(ctx, nil)
	if err != nil || !hasTool(tools.Tools, "gora_assert_view") || !hasTool(tools.Tools, "gora_compare_capture") {
		t.Fatalf("phase5 tools unavailable: err=%v tools=%+v", err, tools)
	}
	for _, tool := range tools.Tools {
		if tool.Name == "gora_assert_view" {
			if tool.Annotations == nil || !tool.Annotations.ReadOnlyHint || !tool.Annotations.IdempotentHint {
				t.Fatalf("assert tool annotations = %+v", tool.Annotations)
			}
		}
		if tool.Name == "gora_compare_capture" {
			if tool.Annotations == nil || tool.Annotations.ReadOnlyHint || tool.Annotations.IdempotentHint {
				t.Fatalf("compare tool annotations = %+v", tool.Annotations)
			}
		}
		if tool.Name == "gora_assert_view" {
			schemaBytes, _ := json.Marshal(tool.InputSchema)
			schema := string(schemaBytes)
			if !strings.Contains(schema, "semantic_id") || !strings.Contains(schema, "\"items\":{\"additionalProperties\":false") {
				t.Fatalf("assert tool schema is not finite/discoverable: %s", schema)
			}
		}
	}
	opened, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "gora_open_project", Arguments: map[string]any{"root": root}})
	if err != nil || opened.IsError {
		t.Fatalf("open project: err=%v result=%+v", err, opened)
	}
	projectID := stringField(opened.StructuredContent, "project_id")
	view, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "gora_open_view", Arguments: map[string]any{"project_id": projectID, "file": "app.gora"}})
	if err != nil || view.IsError {
		t.Fatalf("open view: err=%v result=%+v", err, view)
	}
	viewID := stringField(view.StructuredContent, "view_id")
	dispatched, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "gora_dispatch_input", Arguments: map[string]any{
		"project_id": projectID, "view_id": viewID, "wait": "idle", "events": []any{
			map[string]any{"type": "pointer", "kind": "press", "pointer_id": 1, "source": "mouse", "x": 10, "y": 10, "button": "primary", "time_ms": 1},
			map[string]any{"type": "pointer", "kind": "release", "pointer_id": 1, "source": "mouse", "x": 10, "y": 10, "button": "primary", "time_ms": 2},
		},
	}})
	if err != nil || dispatched.IsError {
		t.Fatalf("dispatch before assertion: err=%v result=%+v", err, dispatched)
	}
	dispatchSnapshot, _ := dispatched.StructuredContent.(map[string]any)["snapshot"].(map[string]any)
	dispatchFrame, _ := dispatchSnapshot["frame_revision"].(float64)
	waited, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "gora_wait_for_view", Arguments: map[string]any{
		"project_id": projectID, "view_id": viewID, "condition": "idle", "after_frame_revision": uint64(dispatchFrame), "timeout_ms": 1000,
	}})
	if err != nil || waited.IsError {
		t.Fatalf("explicit wait after dispatch: err=%v result=%+v", err, waited)
	}
	asserted, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "gora_assert_view", Arguments: map[string]any{
		"project_id": projectID, "view_id": viewID, "assertions": []any{
			map[string]any{"kind": "view", "field": "valid", "expected": true},
			map[string]any{"kind": "node_exists", "semantic_id": "screen/main/node/save"},
			map[string]any{"kind": "node_state", "semantic_id": "screen/main/node/save", "field": "label", "expected": "Save"},
			map[string]any{"kind": "transient", "field": "focused", "expected": "screen/main/node/save"},
		},
	}})
	if err != nil || asserted.IsError || !boolField(asserted.StructuredContent, "passed") {
		t.Fatalf("assert view: err=%v result=%+v", err, asserted)
	}
	frameRevision := uint64(numberField(asserted.StructuredContent, "frame_revision"))
	timedOut, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "gora_assert_view", Arguments: map[string]any{
		"project_id": projectID, "view_id": viewID, "after_frame_revision": frameRevision + 1000, "timeout_ms": 1, "assertions": []any{},
	}})
	if err != nil || !timedOut.IsError {
		t.Fatalf("after-frame timeout was not reported: err=%v result=%+v", err, timedOut)
	}
	outsideCapture := filepath.Join(t.TempDir(), "outside.png")
	if err := os.WriteFile(outsideCapture, []byte("outside"), 0o600); err != nil {
		t.Fatal(err)
	}
	outsideTraversal := filepath.Join(root, "..", "phase5-live-outside.png")
	if err := os.WriteFile(outsideTraversal, []byte("outside"), 0o600); err != nil {
		t.Fatal(err)
	}
	defer os.Remove(outsideTraversal)
	refLink := filepath.Join(root, "reference-escape.png")
	if err := os.Symlink(outsideCapture, refLink); err != nil {
		t.Fatal(err)
	}
	diffParent := filepath.Join(root, "diff-escape")
	if err := os.Symlink(filepath.Dir(outsideCapture), diffParent); err != nil {
		t.Fatal(err)
	}
	for _, args := range []map[string]any{
		{"project_id": projectID, "view_id": viewID, "reference": "missing.png", "scale": 1},
		{"project_id": projectID, "view_id": viewID, "reference": "reference.txt", "scale": 1},
		{"project_id": projectID, "view_id": viewID, "reference": outsideCapture, "scale": 1},
		{"project_id": projectID, "view_id": viewID, "reference": "../phase5-live-outside.png", "scale": 1},
		{"project_id": projectID, "view_id": viewID, "reference": "reference-escape.png", "scale": 1},
		{"project_id": projectID, "view_id": viewID, "reference": "missing.png", "scale": 1, "save_diff": "diff-escape/out.png"},
	} {
		result, callErr := session.CallTool(ctx, &mcp.CallToolParams{Name: "gora_compare_capture", Arguments: args})
		if callErr != nil || !result.IsError {
			t.Fatalf("filesystem compare validation accepted: args=%v err=%v result=%+v", args, callErr, result)
		}
	}
	captured, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "gora_capture", Arguments: map[string]any{
		"project_id": projectID, "view_id": viewID, "scale": 1, "output": "reference.png",
	}})
	if err != nil || captured.IsError {
		t.Logf("live capture segment unavailable in this environment: err=%v result=%+v", err, captured)
		return
	}
	compared, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "gora_compare_capture", Arguments: map[string]any{
		"project_id": projectID, "view_id": viewID, "reference": "reference.png", "scale": 1,
	}})
	if err != nil || compared.IsError || !boolField(compared.StructuredContent, "passed") {
		t.Fatalf("compare capture: err=%v result=%+v", err, compared)
	}
	// Exercise the MCP 2x logical-mask path and the intentional actionable
	// mismatch. The failure remains a normal passed:false result with both
	// current and diff PNG image content.
	captured2, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "gora_capture", Arguments: map[string]any{
		"project_id": projectID, "view_id": viewID, "scale": 2, "output": "reference-2x.png",
	}})
	if err != nil || captured2.IsError {
		t.Fatalf("capture 2x: err=%v result=%+v", err, captured2)
	}
	compared2, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "gora_compare_capture", Arguments: map[string]any{
		"project_id": projectID, "view_id": viewID, "reference": "reference-2x.png", "scale": 2,
		"masks": []any{map[string]any{"x": 0, "y": 0, "width": 1, "height": 1}},
	}})
	if err != nil || compared2.IsError || !boolField(compared2.StructuredContent, "passed") {
		t.Fatalf("compare 2x mask: err=%v result=%+v", err, compared2)
	}
	referenceBytes, err := os.ReadFile(filepath.Join(root, "reference.png"))
	if err != nil {
		t.Fatal(err)
	}
	referenceImage, err := png.Decode(bytes.NewReader(referenceBytes))
	if err != nil {
		t.Fatal(err)
	}
	changed := image.NewNRGBA(referenceImage.Bounds())
	for y := referenceImage.Bounds().Min.Y; y < referenceImage.Bounds().Max.Y; y++ {
		for x := referenceImage.Bounds().Min.X; x < referenceImage.Bounds().Max.X; x++ {
			changed.Set(x, y, referenceImage.At(x, y))
		}
	}
	changed.SetNRGBA(changed.Bounds().Min.X, changed.Bounds().Min.Y, color.NRGBA{R: 255, A: 255})
	var mismatch bytes.Buffer
	if err := png.Encode(&mismatch, changed); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "mismatch.png"), mismatch.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	failed, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "gora_compare_capture", Arguments: map[string]any{
		"project_id": projectID, "view_id": viewID, "reference": "mismatch.png", "scale": 1,
	}})
	if err != nil || failed.IsError || boolField(failed.StructuredContent, "passed") || len(failed.Content) != 2 {
		t.Fatalf("intentional mismatch: err=%v result=%+v", err, failed)
	}
}

func TestPhase5CapturePathsRequireContainedReferenceAndNewDiff(t *testing.T) {
	root := t.TempDir()
	canonicalRoot, err := canonicalDirectory(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(canonicalRoot, "reference.png"), []byte("png"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := containedExistingCapturePath(canonicalRoot, "reference.png"); err != nil {
		t.Fatal(err)
	}
	if _, err := containedExistingCapturePath(canonicalRoot, "missing.png"); err == nil {
		t.Fatal("missing reference accepted")
	}
	if _, err := containedExistingCapturePath(canonicalRoot, "reference.txt"); err == nil {
		t.Fatal("wrong reference extension accepted")
	}
	if _, err := containedExistingCapturePath(canonicalRoot, filepath.Join("..", filepath.Base(canonicalRoot), "reference.png")); err != nil {
		t.Fatalf("canonical in-root traversal rejected unexpectedly: %v", err)
	}
	escapeOutside := filepath.Join(filepath.Dir(canonicalRoot), "phase5-outside.png")
	if err := os.WriteFile(escapeOutside, []byte("outside"), 0o600); err != nil {
		t.Fatal(err)
	}
	defer os.Remove(escapeOutside)
	if _, err := containedExistingCapturePath(canonicalRoot, filepath.Join("..", "phase5-outside.png")); err == nil {
		t.Fatal("true parent traversal escaped root")
	}
	if _, err := containedExistingCapturePath(canonicalRoot, outsidePath(t)); err == nil {
		t.Fatal("absolute outside reference accepted")
	}
	outside := filepath.Join(t.TempDir(), "outside.png")
	if err := os.WriteFile(outside, []byte("png"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(canonicalRoot, "escape.png")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}
	if _, err := containedExistingCapturePath(canonicalRoot, "escape.png"); err == nil {
		t.Fatal("symlink reference escaped root")
	}
	if _, err := containedCapturePath(canonicalRoot, "diff.png"); err != nil {
		t.Fatalf("contained diff rejected: %v", err)
	}
	diffOutside := filepath.Join(t.TempDir(), "diff")
	if err := os.MkdirAll(diffOutside, 0o700); err != nil {
		t.Fatal(err)
	}
	diffLink := filepath.Join(canonicalRoot, "diff-link")
	if err := os.Symlink(diffOutside, diffLink); err != nil {
		t.Fatal(err)
	}
	if _, err := containedCapturePath(canonicalRoot, filepath.Join("diff-link", "out.png")); err == nil {
		t.Fatal("symlink diff parent escaped root")
	}
	if err := os.WriteFile(filepath.Join(canonicalRoot, "existing-diff.png"), []byte("png"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := containedCapturePath(canonicalRoot, "existing-diff.png"); err != nil {
		t.Fatal("existing diff path should be contained before refusal check")
	}
	if err := writeNewFile(filepath.Join(canonicalRoot, "existing-diff.png"), []byte("replacement")); err == nil {
		t.Fatal("existing diff was overwritten")
	}
}

func outsidePath(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "outside.png")
	if err := os.WriteFile(path, []byte("outside"), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestMCPCompareCaptureRejectsInvalidBoundsBeforeRuntimeAccess(t *testing.T) {
	service := NewServiceWithOptions(NewRegistry(), ServiceOptions{Automation: true})
	defer service.Close()
	ctx := context.Background()
	clientTransport, serverTransport := mcp.NewInMemoryTransports()
	if _, err := service.server.Connect(ctx, serverTransport, nil); err != nil {
		t.Fatal(err)
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "phase5-validation", Version: "1"}, nil)
	session, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	for _, args := range []map[string]any{
		{"project_id": "missing", "view_id": "missing", "reference": "ref.png", "scale": 0},
		{"project_id": "missing", "view_id": "missing", "reference": "ref.png", "scale": -1},
		{"project_id": "missing", "view_id": "missing", "reference": "ref.png", "scale": 1, "channel_tolerance": -1},
		{"project_id": "missing", "view_id": "missing", "reference": "ref.png", "scale": 1, "channel_tolerance": 256},
		{"project_id": "missing", "view_id": "missing", "reference": "ref.png", "scale": 1, "max_changed_pixels": -1},
		{"project_id": "missing", "view_id": "missing", "reference": "ref.png", "scale": 1, "masks": []any{map[string]any{"x": -1, "y": 0, "width": 1, "height": 1}}},
	} {
		result, callErr := session.CallTool(ctx, &mcp.CallToolParams{Name: "gora_compare_capture", Arguments: args})
		if callErr != nil || !result.IsError {
			t.Fatalf("invalid compare args accepted: args=%v err=%v result=%+v", args, callErr, result)
		}
	}
}

func TestMCPCompareCaptureRendererIndependentOutputMatrix(t *testing.T) {
	root := t.TempDir()
	entry := filepath.Join(root, "app.gora")
	if err := os.WriteFile(entry, []byte("gora: 1\nkind: app\nviewport: { width: 2, height: 2 }\nentry: main\nscreens:\n  main:\n    type: text\n    props: { text: Matrix }\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	currentImage := image.NewNRGBA(image.Rect(0, 0, 2, 2))
	currentImage.SetNRGBA(0, 0, color.NRGBA{R: 255, A: 255})
	currentPNG := encodePhase5PNG(t, currentImage)
	service := NewServiceWithOptions(NewRegistry(), ServiceOptions{Automation: true})
	service.capturePNG = func(runtime *studio.Runtime, scale int) ([]byte, string, automation.CaptureIdentity, error) {
		identity := runtime.AutomationAssertionSnapshot().View.Capture
		return currentPNG, "", identity, nil
	}
	defer service.Close()
	ctx := context.Background()
	clientTransport, serverTransport := mcp.NewInMemoryTransports()
	if _, err := service.server.Connect(ctx, serverTransport, nil); err != nil {
		t.Fatal(err)
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "phase5-matrix", Version: "1"}, nil)
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
	view, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "gora_open_view", Arguments: map[string]any{"project_id": projectID, "file": "app.gora"}})
	if err != nil || view.IsError {
		t.Fatalf("open view: %v %+v", err, view)
	}
	viewID := stringField(view.StructuredContent, "view_id")
	referencePath := filepath.Join(root, "reference.png")
	if err := os.WriteFile(referencePath, currentPNG, 0o600); err != nil {
		t.Fatal(err)
	}
	referenceBefore, _ := os.ReadFile(referencePath)
	pass, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "gora_compare_capture", Arguments: map[string]any{"project_id": projectID, "view_id": viewID, "reference": "reference.png", "scale": 1, "save_diff": "pass-diff.png"}})
	if err != nil || pass.IsError || !boolField(pass.StructuredContent, "passed") {
		t.Fatalf("exact compare failed: %v %+v", err, pass)
	}
	if _, err := os.Stat(filepath.Join(root, "pass-diff.png")); !os.IsNotExist(err) {
		t.Fatalf("pass compare wrote diff: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "existing-diff.png"), []byte("existing"), 0o600); err != nil {
		t.Fatal(err)
	}
	existing, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "gora_compare_capture", Arguments: map[string]any{"project_id": projectID, "view_id": viewID, "reference": "reference.png", "scale": 1, "save_diff": "existing-diff.png"}})
	if err != nil || !existing.IsError {
		t.Fatalf("existing diff accepted: %v %+v", err, existing)
	}
	diffEscape := filepath.Join(root, "diff-escape")
	if err := os.Symlink(filepath.Dir(filepath.Join(t.TempDir(), "outside.png")), diffEscape); err != nil {
		t.Fatal(err)
	}
	escapeResult, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "gora_compare_capture", Arguments: map[string]any{"project_id": projectID, "view_id": viewID, "reference": "reference.png", "scale": 1, "save_diff": "diff-escape/out.png"}})
	if err != nil || !escapeResult.IsError {
		t.Fatalf("symlink diff parent accepted: %v %+v", err, escapeResult)
	}
	if after, _ := os.ReadFile(referencePath); !bytes.Equal(referenceBefore, after) {
		t.Fatal("pass compare modified reference")
	}
	mismatchImage := image.NewNRGBA(image.Rect(0, 0, 2, 2))
	if err := os.WriteFile(referencePath, encodePhase5PNG(t, mismatchImage), 0o600); err != nil {
		t.Fatal(err)
	}
	mismatchReference, _ := os.ReadFile(referencePath)
	failure, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "gora_compare_capture", Arguments: map[string]any{"project_id": projectID, "view_id": viewID, "reference": "reference.png", "scale": 1, "save_diff": "fail-diff.png"}})
	if err != nil || failure.IsError || boolField(failure.StructuredContent, "passed") || len(failure.Content) != 2 {
		t.Fatalf("mismatch compare response: %v %+v", err, failure)
	}
	for index, content := range failure.Content {
		imageContent, ok := content.(*mcp.ImageContent)
		if !ok || imageContent.MIMEType != "image/png" {
			t.Fatalf("failure content[%d] = %#v", index, content)
		}
		if index == 0 && !bytes.Equal(imageContent.Data, currentPNG) {
			t.Fatal("first mismatch image is not current PNG")
		}
	}
	diffBytes, err := os.ReadFile(filepath.Join(root, "fail-diff.png"))
	if err != nil {
		t.Fatal(err)
	}
	if diffContent, ok := failure.Content[1].(*mcp.ImageContent); !ok || !bytes.Equal(diffBytes, diffContent.Data) {
		t.Fatal("saved diff bytes differ from returned diff")
	}
	if after, _ := os.ReadFile(referencePath); !bytes.Equal(mismatchReference, after) {
		t.Fatal("failed compare modified reference")
	}
	if stringField(failure.StructuredContent, "selection") == "" || numberField(failure.StructuredContent, "capture_frame_revision") == 0 || numberField(failure.StructuredContent, "changed_pixels") != 1 || numberField(failure.StructuredContent, "scale") != 1 {
		t.Fatalf("failure metadata incomplete: %+v", failure.StructuredContent)
	}
	if err := os.WriteFile(referencePath, encodePhase5PNG(t, image.NewNRGBA(image.Rect(0, 0, 1, 1))), 0o600); err != nil {
		t.Fatal(err)
	}
	dimension, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "gora_compare_capture", Arguments: map[string]any{"project_id": projectID, "view_id": viewID, "reference": "reference.png", "scale": 1}})
	if err != nil || dimension.IsError || boolField(dimension.StructuredContent, "passed") || !boolField(dimension.StructuredContent, "dimension_mismatch") || len(dimension.Content) != 2 {
		t.Fatalf("dimension mismatch response: %v %+v", err, dimension)
	}
}

func encodePhase5PNG(t *testing.T, value image.Image) []byte {
	t.Helper()
	var buffer bytes.Buffer
	if err := png.Encode(&buffer, value); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}
