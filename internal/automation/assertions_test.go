package automation

import (
	"encoding/json"
	"image"
	"testing"

	"gora/internal/document"
	"gora/internal/interaction"
	"gora/internal/render"
	"gora/internal/semantic"
)

func TestEvaluateAssertionsCollectsEveryResultFromOneSnapshot(t *testing.T) {
	checked := true
	tree := &semantic.Node{
		ID: "screen/main", Type: "screen", Role: "region", Visible: true, InViewport: true,
		Bounds: &semantic.Rect{X: 0, Y: 0, Width: 100, Height: 80}, Clip: &semantic.Rect{X: 0, Y: 0, Width: 100, Height: 80}, PaintOrder: 1,
		Children: []*semantic.Node{{
			ID: "screen/main/button", Type: "button", Role: "button", Label: "Save", Enabled: true, Visible: true, InViewport: true,
			Checked: &checked, Focused: true, FocusOrder: 1, PaintOrder: 2,
			Bounds: &semantic.Rect{X: 10, Y: 10, Width: 40, Height: 20}, Clip: &semantic.Rect{X: 0, Y: 0, Width: 100, Height: 80},
		}},
	}
	snapshot := AssertionSnapshot{
		Tree:   tree,
		View:   ViewSnapshot{Valid: true, LastGoodAvailable: true, Selection: "main", Viewport: image.Pt(100, 80), FrameRevision: 4, RuntimeRevision: 3, GeometryRevision: 2, Idle: true},
		Router: interaction.RouterSnapshot{FocusedID: "screen/main/button"},
		Trace:  TraceSnapshot{Enabled: true, Generation: 2, Revision: 3, Entries: []TraceEntry{{Stage: "accepted", TargetID: "screen/main/button"}, {Stage: "mutation", TargetID: "screen/main/button"}}},
	}
	result, err := EvaluateAssertions(snapshot, []Assertion{
		{Kind: "view", Field: "valid", Expected: true},
		{Kind: "node_exists", SemanticID: "screen/main/button"},
		{Kind: "node_state", SemanticID: "screen/main/button", Field: "focused", Expected: true},
		{Kind: "node_geometry", SemanticID: "screen/main/button", Field: "bounds", Expected: map[string]any{"x": 10, "y": 10, "width": 40, "height": 20}},
		{Kind: "node_relation", SemanticID: "screen/main", Relation: "contains_point", X: intPointer(20), Y: intPointer(20)},
		{Kind: "trace", Stages: []string{"accepted", "mutation"}, Generation: uint64Pointer(2)},
		{Kind: "node_absent", SemanticID: "screen/missing"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Passed || len(result.Results) != 7 {
		t.Fatalf("assertion report = %+v", result)
	}
	for index, assertion := range result.Results {
		if assertion.Index != index || !assertion.Passed {
			t.Fatalf("result[%d] = %+v", index, assertion)
		}
	}
}

func TestEvaluateAssertionsReportsMismatchesWithoutMutatingSnapshot(t *testing.T) {
	snapshot := AssertionSnapshot{View: ViewSnapshot{Valid: true, FrameRevision: 8}, Trace: TraceSnapshot{Generation: 1, Revision: 1}}
	result, err := EvaluateAssertions(snapshot, []Assertion{
		{Kind: "view", Field: "valid", Expected: false},
		{Kind: "node_exists", SemanticID: "missing"},
		{Kind: "trace", Generation: uint64Pointer(4), Stages: []string{"publication"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Passed || len(result.Results) != 3 {
		t.Fatalf("mismatch report = %+v", result)
	}
	if snapshot.View.FrameRevision != 8 || snapshot.Trace.Generation != 1 {
		t.Fatalf("snapshot mutated: %+v", snapshot)
	}
}

func TestEvaluateAssertionsJoinsHostAndStudioFields(t *testing.T) {
	snapshot := AssertionSnapshot{HostValues: map[string]any{
		"connection_state": "connected",
		"frame_revision":   float64(9),
		"studio": map[string]any{
			"inspect": true,
		},
	}}
	report, err := EvaluateAssertions(snapshot, []Assertion{
		{Kind: "host", Field: "connection_state", Expected: "connected"},
		{Kind: "host", Field: "frame_revision", Expected: float64(9)},
		{Kind: "studio", Field: "studio", Expected: map[string]any{"inspect": true}},
		{Kind: "studio", Field: "inspect", Expected: true},
	})
	if err != nil || !report.Passed {
		t.Fatalf("host/studio assertion report=%+v err=%v", report, err)
	}
	failed, err := EvaluateAssertions(snapshot, []Assertion{{Kind: "host", Field: "unknown", Expected: true}})
	if err != nil || failed.Passed || failed.Results[0].Reason != "unknown host or Studio field" {
		t.Fatalf("unknown host field report=%+v err=%v", failed, err)
	}
}

func TestEvaluateAssertionsSupportsScrollTransientScopeAndTraceFields(t *testing.T) {
	root := &semantic.Node{ID: "root", Bounds: &semantic.Rect{X: 0, Y: 0, Width: 100, Height: 100}, Clip: &semantic.Rect{X: 0, Y: 0, Width: 100, Height: 100}, Children: []*semantic.Node{{ID: "scroll", Handle: "scroll-handle", Role: "scroll", Bounds: &semantic.Rect{X: 0, Y: 0, Width: 80, Height: 80}, Clip: &semantic.Rect{X: 0, Y: 0, Width: 80, Height: 80}}}}
	checked := AssertionSnapshot{
		Tree:        root,
		View:        ViewSnapshot{Tree: root, Scroll: map[string]render.ScrollMetrics{"scroll-handle": {Viewport: image.Rect(0, 0, 80, 80), Maximum: image.Pt(10, 20), ContentSize: image.Pt(90, 100), EnabledX: true, EnabledY: true}}, ScrollOffsets: map[string]image.Point{"scroll-handle": image.Pt(3, 4)}},
		Router:      interaction.RouterSnapshot{FocusedID: "scroll", QueueSizes: interaction.RouterQueueSizes{ValueChanges: 1, ScrollChanges: 2}},
		StateValues: map[string]map[string]any{"screen/main": {"count": 2}},
		Trace:       TraceSnapshot{Generation: 7, Entries: []TraceEntry{{Stage: "accepted", TargetID: "scroll", Axis: "x", Consumed: 3, Residual: 1}, {Stage: "mutation", TargetID: "scroll"}}},
	}
	consumed, residual := 3.0, 1.0
	report, err := EvaluateAssertions(checked, []Assertion{
		{Kind: "scroll", SemanticID: "scroll", Field: "offset_x", Expected: 3},
		{Kind: "scroll", SemanticID: "scroll", Field: "maximum", Expected: map[string]any{"x": 10, "y": 20}},
		{Kind: "transient", Field: "focused", Expected: "scroll"},
		{Kind: "transient", Field: "queue_cardinality", Expected: 3},
		{Kind: "state_scope", ID: "screen/main", Expected: map[string]any{"count": 2}},
		{Kind: "trace", Generation: uint64Pointer(7), Stages: []string{"accepted", "mutation"}},
		{Kind: "trace", Stage: "accepted", Consumed: &consumed, Residual: &residual},
	})
	if err != nil || !report.Passed {
		t.Fatalf("extended assertion report = %+v err=%v", report, err)
	}
}

func TestAssertionUnmarshalRetainsFiniteKindFields(t *testing.T) {
	var assertion Assertion
	if err := json.Unmarshal([]byte(`{"kind":"node_geometry","semantic_id":"node","bounds":{"x":1,"y":2,"width":3,"height":4}}`), &assertion); err != nil {
		t.Fatal(err)
	}
	if assertion.Bounds == nil {
		t.Fatalf("finite geometry field was discarded: %+v", assertion)
	}
	root := &semantic.Node{ID: "node", Visible: true, Bounds: &semantic.Rect{X: 1, Y: 2, Width: 3, Height: 4}}
	assertion.SemanticID = "node"
	report, evalErr := EvaluateAssertions(AssertionSnapshot{Tree: root, View: ViewSnapshot{Tree: root}}, []Assertion{assertion})
	if evalErr != nil || !report.Passed {
		t.Fatalf("inferred bounds assertion = %+v err=%v", report, evalErr)
	}
}

func TestFiniteKindFieldsInferTheirFieldWhenOmitted(t *testing.T) {
	root := &semantic.Node{ID: "scroll", Handle: "scroll", Role: "scroll", Visible: true, Bounds: &semantic.Rect{X: 0, Y: 0, Width: 10, Height: 10}}
	snapshot := AssertionSnapshot{Tree: root, View: ViewSnapshot{Tree: root, Scroll: map[string]render.ScrollMetrics{"scroll": {Viewport: image.Rect(0, 0, 10, 10), Maximum: image.Pt(2, 3), ContentSize: image.Pt(12, 13), EnabledX: true, EnabledY: true}}, ScrollOffsets: map[string]image.Point{"scroll": image.Pt(1, 2)}, Diagnostics: []document.Diagnostic{{Code: "parse", Severity: "error"}}}}
	assertions := []Assertion{{Kind: "node_geometry", SemanticID: "scroll", Bounds: map[string]any{"x": 0, "y": 0, "width": 10, "height": 10}}, {Kind: "scroll", SemanticID: "scroll", EnabledX: boolPointer(true)}, {Kind: "scroll", SemanticID: "scroll", Maximum: map[string]any{"x": 2, "y": 3}}, {Kind: "view", DiagnosticCode: "parse"}}
	report, err := EvaluateAssertions(snapshot, assertions)
	if err != nil || !report.Passed {
		t.Fatalf("inferred finite fields = %+v err=%v", report, err)
	}
}

func TestNodeRelationsAndActiveStateUseContractDirections(t *testing.T) {
	parent := &semantic.Node{ID: "parent", Visible: true, Children: []*semantic.Node{{ID: "child", Visible: true}}}
	snapshot := AssertionSnapshot{Tree: parent, View: ViewSnapshot{Tree: parent}}
	report, err := EvaluateAssertions(snapshot, []Assertion{
		{Kind: "node_relation", SemanticID: "parent", Relation: "parent", OtherID: "child", Expected: true},
		{Kind: "node_relation", SemanticID: "child", Relation: "child", OtherID: "parent", Expected: true},
		{Kind: "node_relation", SemanticID: "parent", Relation: "parent", OtherID: "child", Expected: false},
	})
	if err != nil {
		t.Fatal(err)
	}
	if report.Results[0].Passed != true || report.Results[1].Passed != true || report.Results[2].Passed != false {
		t.Fatalf("parent/child relation results = %+v", report.Results)
	}
	current := true
	active := &semantic.Node{ID: "link", Current: current, Visible: true}
	root := &semantic.Node{ID: "root", Visible: true, Children: []*semantic.Node{active}}
	report, err = EvaluateAssertions(AssertionSnapshot{Tree: root, View: ViewSnapshot{Tree: root}, Router: interaction.RouterSnapshot{}}, []Assertion{{Kind: "node_state", SemanticID: "link", Field: "active", Expected: false}})
	if err != nil || !report.Passed {
		t.Fatalf("link current incorrectly reported active: %+v err=%v", report, err)
	}
}

func TestEveryNodeRelationAndExpectedFalse(t *testing.T) {
	parent := &semantic.Node{ID: "parent", Visible: true, FocusOrder: 1, PaintOrder: 2, Bounds: &semantic.Rect{X: 0, Y: 0, Width: 100, Height: 100}, Clip: &semantic.Rect{X: 0, Y: 0, Width: 100, Height: 100}}
	child := &semantic.Node{ID: "child", Visible: true, FocusOrder: 2, PaintOrder: 3, Bounds: &semantic.Rect{X: 10, Y: 10, Width: 20, Height: 20}, Clip: &semantic.Rect{X: 0, Y: 0, Width: 50, Height: 50}}
	outside := &semantic.Node{ID: "outside", Visible: true, FocusOrder: 3, PaintOrder: 1, Bounds: &semantic.Rect{X: 70, Y: 70, Width: 20, Height: 20}, Clip: &semantic.Rect{X: 0, Y: 0, Width: 90, Height: 90}}
	parent.Children = []*semantic.Node{child, outside}
	snapshot := AssertionSnapshot{Tree: parent}
	tests := []struct {
		name      string
		assertion Assertion
	}{
		{"parent", Assertion{Kind: "node_relation", SemanticID: "parent", Relation: "parent", OtherID: "child", Expected: true}},
		{"child", Assertion{Kind: "node_relation", SemanticID: "child", Relation: "child", OtherID: "parent", Expected: true}},
		{"focus_before", Assertion{Kind: "node_relation", SemanticID: "parent", Relation: "focus_before", OtherID: "child", Expected: true}},
		{"focus_after", Assertion{Kind: "node_relation", SemanticID: "child", Relation: "focus_after", OtherID: "parent", Expected: true}},
		{"paint_above", Assertion{Kind: "node_relation", SemanticID: "child", Relation: "paint_above", OtherID: "parent", Expected: true}},
		{"paint_below", Assertion{Kind: "node_relation", SemanticID: "parent", Relation: "paint_below", OtherID: "child", Expected: true}},
		{"contains_inside", Assertion{Kind: "node_relation", SemanticID: "parent", Relation: "contains_point", X: intPointer(20), Y: intPointer(20), Expected: true}},
		{"contains_outside", Assertion{Kind: "node_relation", SemanticID: "parent", Relation: "contains_point", X: intPointer(120), Y: intPointer(120), Expected: false}},
		{"clipped_by", Assertion{Kind: "node_relation", SemanticID: "child", Relation: "clipped_by", OtherID: "parent", Expected: true}},
		{"not_clipped_by", Assertion{Kind: "node_relation", SemanticID: "outside", Relation: "clipped_by", OtherID: "child", Expected: false}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			report, err := EvaluateAssertions(snapshot, []Assertion{test.assertion})
			if err != nil || !report.Passed {
				t.Fatalf("relation report = %+v err=%v", report, err)
			}
		})
	}
	// Omitted expected values use the relation's actual truth value.
	report, err := EvaluateAssertions(snapshot, []Assertion{{Kind: "node_relation", SemanticID: "parent", Relation: "parent", OtherID: "child"}})
	if err != nil || !report.Passed {
		t.Fatalf("omitted relation expected = %+v err=%v", report, err)
	}
}

func TestTransientEditingAndVisibleScopes(t *testing.T) {
	root := &semantic.Node{ID: "root", Visible: true, Scope: "visible", Children: []*semantic.Node{{ID: "button", Role: "button", Visible: true}, {ID: "field", Role: "textbox", Visible: true}}}
	snapshot := AssertionSnapshot{
		Tree:        root,
		View:        ViewSnapshot{Tree: root, VisibleScopes: map[string]bool{"visible": true}},
		Router:      interaction.RouterSnapshot{FocusedID: "button"},
		Editing:     interaction.EditingStoreSnapshot{Fields: map[string]interaction.FieldSnapshot{"field": {ID: "field", Draft: "draft", SelectionStart: 1, SelectionEnd: 2, Composing: true, Composition: "ime", CompositionStart: 1, CompositionEnd: 2}}},
		StateValues: map[string]map[string]any{"visible": {"count": 1}, "hidden": {"count": 2}},
	}
	report, err := EvaluateAssertions(snapshot, []Assertion{
		{Kind: "transient", Field: "editing_target", Expected: ""},
		{Kind: "state_scope", ID: "visible", Expected: map[string]any{"count": 1}},
		{Kind: "state_scope", ID: "hidden", Expected: map[string]any{"count": 2}},
	})
	if err != nil || report.Passed || report.Results[0].Actual != "" || report.Results[2].Passed {
		t.Fatalf("transient/visible scope results = %+v err=%v", report, err)
	}
	// The focused field's identity can differ from a semantic node ID; editing
	// metadata remains authoritative for caret, selection, and composition.
	snapshot.Router.FocusedID = "field"
	report, err = EvaluateAssertions(snapshot, []Assertion{
		{Kind: "transient", Field: "caret", Expected: map[string]any{"selection_start": 1, "selection_end": 2}},
		{Kind: "transient", Field: "composition", Expected: map[string]any{"composing": true, "text": "ime", "start": 1, "end": 2}},
	})
	if err != nil || !report.Passed {
		t.Fatalf("editing metadata results = %+v err=%v", report, err)
	}
}

func TestEveryTransientFieldAndAlias(t *testing.T) {
	root := &semantic.Node{ID: "field-semantic", Handle: "field-handle", Role: "textbox", Visible: true}
	snapshot := AssertionSnapshot{
		Tree: root,
		View: ViewSnapshot{Tree: root, Clock: map[string]any{"mode": "frozen", "time_ms": int64(7)}},
		Router: interaction.RouterSnapshot{
			FocusedID: "field-semantic", HoveredIDs: []string{"hover"}, PressedIDs: []string{"press"}, ActiveIDs: []string{"option"}, OpenSelectID: "popup",
			PointerCapture: &interaction.PointerCaptureSnapshot{OwnerID: "capture", PointerID: 1, Source: "mouse"}, KeyboardPress: &interaction.KeyboardPressSnapshot{OwnerID: "keyboard", Key: "Space"},
			ScrollbarGestureOwner: "scrollbar", SliderGestureOwner: "slider", QueueSizes: interaction.RouterQueueSizes{ValueChanges: 2, ScrollChanges: 3},
		},
		Editing: interaction.EditingStoreSnapshot{Fields: map[string]interaction.FieldSnapshot{"field-handle": {ID: "field-store", SelectionStart: 2, SelectionEnd: 4, Composing: true, Composition: "ime", CompositionStart: 2, CompositionEnd: 4}}},
	}
	fields := []struct {
		field    string
		expected any
	}{
		{"focus", "field-semantic"}, {"focused", "field-semantic"}, {"hovered", "hover"}, {"pressed", "press"}, {"pointer_capture_owner", "capture"}, {"capture_owner", "capture"}, {"pointer_capture", map[string]any{"owner_id": "capture", "pointer_id": 1, "source": "mouse", "buttons": 0, "point": map[string]any{"x": 0, "y": 0}}}, {"gesture_capture", "scrollbar"}, {"scrollbar_gesture_owner", "scrollbar"}, {"slider_gesture_owner", "slider"}, {"popup", "popup"}, {"open_select", "popup"}, {"active_option", "option"}, {"keyboard_owner", "keyboard"}, {"editing_target", "field-store"}, {"caret", map[string]any{"selection_start": 2, "selection_end": 4}}, {"selection", map[string]any{"start": 2, "end": 4}}, {"composition", map[string]any{"composing": true, "text": "ime", "start": 2, "end": 4}}, {"queue_cardinality", 5}, {"clock", map[string]any{"mode": "frozen", "time_ms": int64(7)}},
	}
	for _, test := range fields {
		t.Run(test.field, func(t *testing.T) {
			report, err := EvaluateAssertions(snapshot, []Assertion{{Kind: "transient", Field: test.field, Expected: test.expected}})
			if err != nil || !report.Passed {
				t.Fatalf("transient %s report=%+v err=%v", test.field, report, err)
			}
		})
	}
	// A focused non-field never becomes an editing target.
	snapshot.Router.FocusedID = "capture"
	report, err := EvaluateAssertions(snapshot, []Assertion{{Kind: "transient", Field: "editing_target", Expected: ""}})
	if err != nil || !report.Passed {
		t.Fatalf("non-field editing target = %+v err=%v", report, err)
	}
}

func TestMalformedAssertionsAreOrderedFailures(t *testing.T) {
	var assertion Assertion
	if err := json.Unmarshal([]byte(`{"kind":"view","field":"valid","unknown":1}`), &assertion); err != nil {
		t.Fatal(err)
	}
	var malformed Assertion
	if err := json.Unmarshal([]byte(`1`), &malformed); err != nil {
		t.Fatal(err)
	}
	report, err := EvaluateAssertions(AssertionSnapshot{View: ViewSnapshot{Valid: true}}, []Assertion{assertion, malformed, {Kind: "view", Field: "valid", Expected: true}})
	if err != nil || len(report.Results) != 3 || report.Results[0].Passed || report.Results[1].Passed || !report.Results[2].Passed {
		t.Fatalf("malformed ordered report = %+v err=%v", report, err)
	}
}

func TestDiagnosticsAreAssertableByDeterministicPosition(t *testing.T) {
	snapshot := AssertionSnapshot{View: ViewSnapshot{Diagnostics: []document.Diagnostic{
		{File: "z.gora", Line: 4, Column: 1, Code: "late", Severity: "warning"},
		{File: "a.gora", Line: 2, Column: 1, Code: "early", Severity: "error"},
	}}}
	report, err := EvaluateAssertions(snapshot, []Assertion{
		{Kind: "view", Field: "diagnostic_code", DiagnosticIndex: intPointer(0), Expected: "early"},
		{Kind: "view", Field: "diagnostic_severity", DiagnosticIndex: intPointer(1), Expected: "warning"},
		{Kind: "view", Field: "diagnostic_codes", Expected: []any{"early", "late"}},
	})
	if err != nil || !report.Passed {
		t.Fatalf("diagnostic report = %+v err=%v", report, err)
	}
}

func TestFiniteAssertionFieldsTable(t *testing.T) {
	checked, selected, expanded, valid := true, true, false, true
	child := &semantic.Node{ID: "child", Type: "text", Role: "textbox", Label: "Name", Value: "draft", CommittedValue: "saved", Enabled: true, Visible: true, InViewport: false, Checked: &checked, Selected: &selected, Expanded: &expanded, Valid: &valid, Dirty: true, Touched: true, Focused: true, Hovered: true, Pressed: true, FocusOrder: 2, PaintOrder: 3, Bounds: &semantic.Rect{X: 4, Y: 5, Width: 6, Height: 7}, Clip: &semantic.Rect{X: 0, Y: 0, Width: 10, Height: 10}}
	root := &semantic.Node{ID: "root", Visible: true, Scope: "scope", Bounds: &semantic.Rect{X: 0, Y: 0, Width: 20, Height: 20}, Clip: &semantic.Rect{X: 0, Y: 0, Width: 20, Height: 20}, Children: []*semantic.Node{child}}
	view := ViewSnapshot{Tree: root, Valid: true, LastGoodAvailable: true, Agreement: true, RuntimePublished: true, GeometryPublished: true, Selection: "main", Selections: []string{"main", "other"}, Viewport: image.Pt(20, 20), CanBack: true, CanForward: false, RuntimeRevision: 1, FrameRevision: 2, GeometryRevision: 3, PublishedRuntimeRevision: 1, PublishedGeometryRevision: 3, ReloadRevision: 4, AutomationInputRevision: 5, Idle: true, Diagnostics: []document.Diagnostic{{Code: "code", Severity: "warning"}}}
	fields := []struct {
		kind, field string
		expected    any
	}{
		{"view", "valid", true}, {"view", "last_good", true}, {"view", "selection", "main"}, {"view", "viewport_width", 20}, {"view", "runtime_revision", uint64(1)}, {"view", "frame_revision", uint64(2)}, {"view", "geometry_revision", uint64(3)}, {"view", "published_runtime_revision", uint64(1)}, {"view", "published_geometry_revision", uint64(3)}, {"view", "reload_revision", uint64(4)}, {"view", "automation_input_revision", uint64(5)}, {"view", "can_back", true}, {"view", "can_forward", false}, {"view", "idle", true}, {"view", "diagnostic_count", 1}, {"view", "diagnostic_code", "code"}, {"view", "diagnostic_severity", "warning"},
		{"node_state", "type", "text"}, {"node_state", "role", "textbox"}, {"node_state", "label", "Name"}, {"node_state", "value", "draft"}, {"node_state", "committed", "saved"}, {"node_state", "enabled", true}, {"node_state", "visible", true}, {"node_state", "in_viewport", false}, {"node_state", "checked", true}, {"node_state", "selected", true}, {"node_state", "expanded", false}, {"node_state", "valid", true}, {"node_state", "dirty", true}, {"node_state", "touched", true}, {"node_state", "focused", true}, {"node_state", "hovered", true}, {"node_state", "pressed", true}, {"node_state", "active", false}, {"node_state", "focus_order", 2}, {"node_state", "paint_order", 3},
		{"node_geometry", "bounds", map[string]any{"x": 4, "y": 5, "width": 6, "height": 7}}, {"node_geometry", "clip", map[string]any{"x": 0, "y": 0, "width": 10, "height": 10}}, {"node_geometry", "null", false},
	}
	assertions := make([]Assertion, 0, len(fields))
	for _, field := range fields {
		assertion := Assertion{Kind: field.kind, Field: field.field, Expected: field.expected}
		if field.kind == "node_state" || field.kind == "node_geometry" {
			assertion.SemanticID = "child"
		}
		assertions = append(assertions, assertion)
	}
	report, err := EvaluateAssertions(AssertionSnapshot{Tree: root, View: view}, assertions)
	if err != nil || !report.Passed {
		t.Fatalf("finite field table report = %+v err=%v", report, err)
	}
}

func TestScrollStaleSemanticIDDoesNotBindSoleScrollport(t *testing.T) {
	root := &semantic.Node{ID: "root", Visible: true, Children: []*semantic.Node{{ID: "scroll", Handle: "handle", Name: "authored", Role: "scroll", Visible: true}}}
	snapshot := AssertionSnapshot{Tree: root, View: ViewSnapshot{Tree: root, Scroll: map[string]render.ScrollMetrics{"handle": {Viewport: image.Rect(0, 0, 10, 10), Maximum: image.Pt(5, 5), EnabledX: true, EnabledY: true}}, ScrollOffsets: map[string]image.Point{"handle": image.Pt(2, 3)}}}
	report, err := EvaluateAssertions(snapshot, []Assertion{{Kind: "scroll", SemanticID: "stale", Field: "offset", Expected: map[string]any{"x": 2, "y": 3}}})
	if err != nil || report.Passed || report.Results[0].Reason != "scroll metrics were not found" {
		t.Fatalf("stale scroll unexpectedly bound sole metric: %+v err=%v", report, err)
	}
}

func TestComponentBreadcrumbHiddenOffscreenAndDerivedNodeAssertions(t *testing.T) {
	hidden := &semantic.Node{ID: "hidden", Type: "button", Visible: false, InViewport: false, Scope: "hidden-scope", Bounds: nil, Clip: nil}
	offscreen := &semantic.Node{ID: "offscreen", Type: "text", Visible: true, InViewport: false, Scope: "component:card", Breadcrumb: []string{"card", "item"}, Binding: "name", Form: "form", Source: semantic.Source{File: "app.gora", Line: 4, Column: 2}, State: map[string]any{"count": 2}, Bounds: &semantic.Rect{X: 1000, Y: 1000, Width: 10, Height: 10}, Clip: &semantic.Rect{X: 1000, Y: 1000, Width: 10, Height: 10}}
	derived := &semantic.Node{ID: "scroll/scrollbar/vertical", Type: "scrollbar", Role: "scrollbar", Visible: true, Orientation: "vertical", Group: "scroll", InViewport: true, Bounds: &semantic.Rect{X: 9, Y: 0, Width: 1, Height: 10}, Clip: &semantic.Rect{X: 0, Y: 0, Width: 10, Height: 10}}
	root := &semantic.Node{ID: "root", Visible: true, Children: []*semantic.Node{hidden, offscreen, derived}}
	report, err := EvaluateAssertions(AssertionSnapshot{Tree: root, View: ViewSnapshot{Tree: root}}, []Assertion{
		{Kind: "node_exists", SemanticID: "hidden"}, {Kind: "node_state", SemanticID: "hidden", Field: "visible", Expected: false}, {Kind: "node_geometry", SemanticID: "hidden", Field: "null", Expected: true},
		{Kind: "node_exists", SemanticID: "offscreen"}, {Kind: "node_state", SemanticID: "offscreen", Field: "in_viewport", Expected: false}, {Kind: "node_state", SemanticID: "offscreen", Field: "breadcrumb", Expected: []any{"card", "item"}}, {Kind: "node_state", SemanticID: "offscreen", Field: "scope", Expected: "component:card"}, {Kind: "node_state", SemanticID: "offscreen", Field: "binding", Expected: "name"}, {Kind: "node_state", SemanticID: "offscreen", Field: "form", Expected: "form"}, {Kind: "node_state", SemanticID: "offscreen", Field: "source", Expected: offscreen.Source}, {Kind: "node_state", SemanticID: "offscreen", Field: "state", Expected: map[string]any{"count": 2}},
		{Kind: "node_state", SemanticID: derived.ID, Field: "role", Expected: "scrollbar"}, {Kind: "node_state", SemanticID: derived.ID, Field: "visible", Expected: true},
	})
	if err != nil || !report.Passed {
		t.Fatalf("component/hidden/offscreen/derived report = %+v err=%v", report, err)
	}
}

func TestTraceSubsequenceFiltersEveryPromisedField(t *testing.T) {
	trace := TraceSnapshot{Generation: 9, Entries: []TraceEntry{
		{Stage: "accepted", TargetID: "button", SemanticID: "button", Axis: "x", Outcome: "consumed", Consumed: 2, Residual: 1},
		{Stage: "accepted", TargetID: "button", SemanticID: "button", Axis: "y", Outcome: "residual", Consumed: 0, Residual: 3},
		{Stage: "mutation", TargetID: "button", SemanticID: "button"},
	}}
	consumed, residual := 2.0, 1.0
	report, err := EvaluateAssertions(AssertionSnapshot{Trace: trace}, []Assertion{
		{Kind: "trace", Generation: uint64Pointer(9), Stages: []string{"accepted", "mutation"}, Owners: []string{"button", "button"}},
		{Kind: "trace", Stage: "accepted", Owner: "button", Axis: "x", Outcome: "consumed", Consumed: &consumed, Residual: &residual},
		{Kind: "trace", Generation: uint64Pointer(8), Stages: []string{"accepted"}},
		{Kind: "trace", Stages: []string{"accepted", "missing"}},
	})
	if err != nil || report.Passed || !report.Results[0].Passed || !report.Results[1].Passed || report.Results[2].Passed || report.Results[3].Passed {
		t.Fatalf("trace field report = %+v err=%v", report, err)
	}
}

func intPointer(value int) *int          { return &value }
func uint64Pointer(value uint64) *uint64 { return &value }
func boolPointer(value bool) *bool       { return &value }
