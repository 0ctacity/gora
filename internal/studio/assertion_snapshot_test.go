package studio

import (
	"image"
	"os"
	"path/filepath"
	"testing"

	"gora/internal/automation"
	"gora/internal/semantic"
)

func TestAutomationAssertionSnapshotReadDoesNotAdvanceRevisions(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "app.gora")
	if err := os.WriteFile(path, []byte("gora: 1\nkind: app\nviewport: { width: 100, height: 80 }\nentry: main\nscreens:\n  main:\n    type: text\n    props: { text: Hello }\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runtime, err := NewRuntime(dir, path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.RuntimeTree(); err != nil {
		t.Fatal(err)
	}
	before := runtime.AutomationSnapshot()
	_ = runtime.AutomationAssertionSnapshot()
	after := runtime.AutomationSnapshot()
	if before.RuntimeRevision != after.RuntimeRevision || before.GeometryRevision != after.GeometryRevision || before.FrameRevision != after.FrameRevision {
		t.Fatalf("assertion snapshot advanced revisions: before=%+v after=%+v", before, after)
	}
}

func TestAutomationAssertionSnapshotResolvesAuthoredAndDerivedScrollOwners(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "scroll.gora")
	source := []byte("gora: 1\nkind: app\nviewport: { width: 100, height: 80 }\nentry: main\nscreens:\n  main:\n    type: scroll\n    name: workspace\n    props: { axis: both, scrollbar_x: always, scrollbar_y: always }\n    children: [{ type: surface, props: { width: 240, height: 200 } }]\n")
	if err := os.WriteFile(path, source, 0o600); err != nil {
		t.Fatal(err)
	}
	runtime, err := NewRuntime(dir, path)
	if err != nil {
		t.Fatal(err)
	}
	tree, err := runtime.RuntimeTree()
	if err != nil {
		t.Fatal(err)
	}
	workspace := namedSemanticNode(tree, "workspace")
	if workspace == nil {
		t.Fatal("missing workspace")
	}
	if err := runtime.ScrollSemanticID(workspace.ID, "to", 30, 20); err != nil {
		t.Fatal(err)
	}
	var vertical *semantic.Node
	for _, node := range semantic.Flatten(tree) {
		if node.Role == "scrollbar" && node.Orientation == "vertical" {
			vertical = node
			break
		}
	}
	if vertical == nil {
		t.Fatal("missing derived vertical scrollbar")
	}
	snapshot := runtime.AutomationAssertionSnapshot()
	var derivedIDs []string
	for _, node := range semantic.Flatten(tree) {
		if node.Role == "scrollbar" || node.Type == "scrollbar_track" || node.Type == "scrollbar_thumb" {
			derivedIDs = append(derivedIDs, node.ID)
		}
	}
	ids := append([]string{workspace.ID}, derivedIDs...)
	for _, id := range ids {
		for _, assertion := range []automation.Assertion{
			{Kind: "scroll", SemanticID: id, Field: "offset", Expected: map[string]any{"x": 30, "y": 20}},
			{Kind: "scroll", SemanticID: id, Field: "offset_x", Expected: 30},
			{Kind: "scroll", SemanticID: id, Field: "offset_y", Expected: 20},
			{Kind: "scroll", SemanticID: id, Field: "maximum", Expected: map[string]any{"x": 140, "y": 120}},
			{Kind: "scroll", SemanticID: id, Field: "maximum_x", Expected: 140},
			{Kind: "scroll", SemanticID: id, Field: "maximum_y", Expected: 120},
			{Kind: "scroll", SemanticID: id, Field: "viewport", Expected: map[string]any{"width": 100, "height": 80}},
			{Kind: "scroll", SemanticID: id, Field: "content", Expected: map[string]any{"width": 240, "height": 200}},
			{Kind: "scroll", SemanticID: id, Field: "enabled_x", Expected: true},
			{Kind: "scroll", SemanticID: id, Field: "enabled_y", Expected: true},
		} {
			report, evalErr := automation.EvaluateAssertions(snapshot, []automation.Assertion{assertion})
			if evalErr != nil || !report.Passed {
				t.Fatalf("scroll assertion %q/%q report=%+v err=%v offsets=%v metrics=%v", id, assertion.Field, report, evalErr, snapshot.ScrollOffsets, snapshot.Scroll)
			}
		}
	}
	if snapshot.ScrollOffsets[workspace.ID] != image.Pt(30, 20) || snapshot.ScrollOffsets[vertical.ID] != image.Pt(30, 20) {
		t.Fatalf("canonical scroll offsets = %+v", snapshot.ScrollOffsets)
	}
}

func TestAutomationAssertionSnapshotVisibleScopeFilteringUsesPublishedRuntime(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "scope.gora")
	source := []byte("gora: 1\nkind: app\nviewport: { width: 100, height: 80 }\nstate:\n  count: { type: number, default: 1 }\nentry: main\nscreens:\n  main:\n    type: text\n    props: { text: Count }\n  other:\n    type: text\n    props: { text: Other }\n")
	if err := os.WriteFile(path, source, 0o600); err != nil {
		t.Fatal(err)
	}
	runtime, err := NewRuntime(dir, path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.RuntimeTree(); err != nil {
		t.Fatal(err)
	}
	snapshot := runtime.AutomationAssertionSnapshot()
	if !snapshot.View.VisibleScopes["screen:main"] {
		t.Fatalf("published visible scope missing: %+v", snapshot.View.VisibleScopes)
	}
	if snapshot.View.VisibleScopes["hidden:retained"] {
		t.Fatal("hidden retained scope reported visible")
	}
	if _, retained := snapshot.StateValues["screen:other"]; retained && snapshot.View.VisibleScopes["screen:other"] {
		t.Fatal("retained non-selected scope reported visible")
	}
	report, evalErr := automation.EvaluateAssertions(snapshot, []automation.Assertion{{Kind: "state_scope", ID: "screen:main", Expected: map[string]any{"count": 1}}})
	if evalErr != nil || !report.Passed {
		t.Fatalf("visible runtime scope assertion = %+v err=%v", report, evalErr)
	}
}

func TestAutomationAssertionSnapshotConcurrentPublicationIsSelfConsistent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "concurrent.gora")
	source := []byte("gora: 1\nkind: app\nviewport: { width: 100, height: 80 }\nstate:\n  count: { type: number, default: 0 }\nentry: main\nscreens:\n  main:\n    type: text\n    props: { text: Count }\n")
	if err := os.WriteFile(path, source, 0o600); err != nil {
		t.Fatal(err)
	}
	runtime, err := NewRuntime(dir, path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.RuntimeTree(); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 50; i++ {
			_ = runtime.SetStateValues("screen:main", map[string]any{"count": i})
		}
	}()
	for i := 0; i < 50; i++ {
		snapshot := runtime.AutomationAssertionSnapshot()
		if snapshot.View.PublishedRuntimeRevision > snapshot.View.RuntimeRevision || snapshot.View.PublishedGeometryRevision > snapshot.View.GeometryRevision {
			t.Fatalf("inconsistent publication tuple: %+v", snapshot.View)
		}
		if _, evalErr := automation.EvaluateAssertions(snapshot, []automation.Assertion{{Kind: "view", Field: "runtime_revision", Expected: snapshot.View.RuntimeRevision}}); evalErr != nil {
			t.Fatal(evalErr)
		}
	}
	<-done
}

func TestCapturePNGReadOnlyIdentityIsCapturedWithImmutableSnapshot(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "capture.gora")
	if err := os.WriteFile(path, []byte("gora: 1\nkind: app\nviewport: { width: 32, height: 24 }\nentry: main\nscreens:\n  main:\n    type: text\n    props: { text: Capture }\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runtime, err := NewRuntime(dir, path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.RuntimeTree(); err != nil {
		t.Fatal(err)
	}
	before := runtime.AutomationSnapshot()
	data, _, identity, err := runtime.CapturePNGReadOnly(1)
	if err != nil {
		t.Skipf("live capture unavailable in this environment: %v", err)
	}
	if len(data) == 0 || identity.Selection != before.Selection || identity.RuntimeRevision != before.RuntimeRevision || identity.FrameRevision != before.FrameRevision || identity.GeometryRevision != before.GeometryRevision || identity.Width != before.Viewport.X || identity.Height != before.Viewport.Y {
		t.Fatalf("capture identity=%+v snapshot=%+v bytes=%d", identity, before, len(data))
	}
	after := runtime.AutomationSnapshot()
	if before.RuntimeRevision != after.RuntimeRevision || before.FrameRevision != after.FrameRevision || before.GeometryRevision != after.GeometryRevision {
		t.Fatalf("read-only capture advanced revisions: before=%+v after=%+v", before, after)
	}
}
