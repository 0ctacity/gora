package automation

import (
	"encoding/json"
	"fmt"
	"image"
	"reflect"
	"sort"
	"strings"

	"gora/internal/document"
	"gora/internal/interaction"
	"gora/internal/render"
	"gora/internal/semantic"
)

// ViewSnapshot is the renderer-neutral, immutable state used by the finite
// assertion evaluator. Hosts construct it from one published runtime read;
// the evaluator never calls back into a host or mutates the tree.
type ViewSnapshot struct {
	Valid                     bool
	LastGoodAvailable         bool
	Agreement                 bool
	RuntimePublished          bool
	GeometryPublished         bool
	Idle                      bool
	IdleReasons               []string
	Selection                 string
	Selections                []string
	Viewport                  image.Point
	CanBack                   bool
	CanForward                bool
	RuntimeRevision           uint64
	FrameRevision             uint64
	GeometryRevision          uint64
	PublishedRuntimeRevision  uint64
	PublishedGeometryRevision uint64
	ReloadRevision            uint64
	AutomationInputRevision   uint64
	Diagnostics               []document.Diagnostic
	Transient                 interaction.Transient
	Router                    interaction.RouterSnapshot
	Editing                   interaction.EditingStoreSnapshot
	StateValues               map[string]map[string]any
	VisibleScopes             map[string]bool
	Scroll                    map[string]render.ScrollMetrics
	ScrollOffsets             map[string]image.Point
	Clock                     map[string]any
	Trace                     TraceSnapshot
	Capture                   CaptureIdentity
	Tree                      *semantic.Node
}

type CaptureIdentity struct {
	Selection                 string `json:"selection,omitempty"`
	ViewportWidth             int    `json:"viewport_width"`
	ViewportHeight            int    `json:"viewport_height"`
	RuntimeRevision           uint64 `json:"runtime_revision"`
	FrameRevision             uint64 `json:"frame_revision"`
	GeometryRevision          uint64 `json:"geometry_revision"`
	PublishedRuntimeRevision  uint64 `json:"published_runtime_revision"`
	PublishedGeometryRevision uint64 `json:"published_geometry_revision"`
	Width                     int    `json:"width"`
	Height                    int    `json:"height"`
	Valid                     bool   `json:"valid"`
}

// Assertion is the finite v1 assertion union. Expected is intentionally an
// ordinary JSON value; no expression language, path query, or scripting is
// interpreted by the evaluator.
type Assertion struct {
	Kind       string   `json:"kind"`
	SemanticID string   `json:"semantic_id,omitempty"`
	ID         string   `json:"id,omitempty"`
	Field      string   `json:"field,omitempty"`
	Expected   any      `json:"expected,omitempty"`
	Tolerance  float64  `json:"tolerance,omitempty"`
	Relation   string   `json:"relation,omitempty"`
	OtherID    string   `json:"other_id,omitempty"`
	X          *int     `json:"x,omitempty"`
	Y          *int     `json:"y,omitempty"`
	Stage      string   `json:"stage,omitempty"`
	Owner      string   `json:"owner,omitempty"`
	Axis       string   `json:"axis,omitempty"`
	Outcome    string   `json:"outcome,omitempty"`
	Consumed   *float64 `json:"consumed,omitempty"`
	Residual   *float64 `json:"residual,omitempty"`
	Stages     []string `json:"stages,omitempty"`
	Owners     []string `json:"owners,omitempty"`
	Generation *uint64  `json:"generation,omitempty"`
	// Kind-specific finite fields. These are deliberately explicit so unknown
	// keys cannot become an open-ended extension map.
	Value              any      `json:"value,omitempty"`
	Bounds             any      `json:"bounds,omitempty"`
	Clip               any      `json:"clip,omitempty"`
	Null               *bool    `json:"null,omitempty"`
	Offset             any      `json:"offset,omitempty"`
	Maximum            any      `json:"maximum,omitempty"`
	ViewportSize       any      `json:"viewport,omitempty"`
	ContentSize        any      `json:"content,omitempty"`
	EnabledX           *bool    `json:"enabled_x,omitempty"`
	EnabledY           *bool    `json:"enabled_y,omitempty"`
	DiagnosticIndex    *int     `json:"diagnostic_index,omitempty"`
	DiagnosticCode     string   `json:"diagnostic_code,omitempty"`
	DiagnosticSeverity string   `json:"diagnostic_severity,omitempty"`
	DiagnosticCodes    []string `json:"diagnostic_codes,omitempty"`
	UnknownFields      []string `json:"-"`
	MalformedReason    string   `json:"-"`
	ProvidedFields     []string `json:"-"`
}

// UnmarshalJSON retains finite kind-specific fields not represented by the
// compact common struct (for example bounds or enabled_x) without enabling an
// expression language. Evaluators only consult a fixed allowlist per kind.
func (assertion *Assertion) UnmarshalJSON(data []byte) error {
	type plain Assertion
	var decoded plain
	if err := json.Unmarshal(data, &decoded); err != nil {
		// Preserve the assertion's kind when one was supplied, turning a type
		// error into an ordered failed assertion instead of aborting its batch.
		var raw map[string]json.RawMessage
		if rawErr := json.Unmarshal(data, &raw); rawErr != nil {
			if json.Valid(data) {
				*assertion = Assertion{MalformedReason: rawErr.Error()}
				return nil
			}
			return rawErr
		}
		var kind string
		_ = json.Unmarshal(raw["kind"], &kind)
		*assertion = Assertion{Kind: kind, MalformedReason: err.Error()}
		return nil
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	known := map[string]bool{"kind": true, "semantic_id": true, "id": true, "field": true, "expected": true, "tolerance": true, "relation": true, "other_id": true, "x": true, "y": true, "stage": true, "owner": true, "axis": true, "outcome": true, "consumed": true, "residual": true, "stages": true, "owners": true, "generation": true, "value": true, "bounds": true, "clip": true, "null": true, "offset": true, "maximum": true, "viewport": true, "content": true, "enabled_x": true, "enabled_y": true, "diagnostic_index": true, "diagnostic_code": true, "diagnostic_severity": true, "diagnostic_codes": true}
	decoded.UnknownFields = make([]string, 0)
	decoded.ProvidedFields = make([]string, 0, len(raw))
	for key := range raw {
		decoded.ProvidedFields = append(decoded.ProvidedFields, key)
		if known[key] {
			continue
		}
		decoded.UnknownFields = append(decoded.UnknownFields, key)
	}
	sort.Strings(decoded.UnknownFields)
	sort.Strings(decoded.ProvidedFields)
	*assertion = Assertion(decoded)
	return nil
}

type AssertionResult struct {
	Index    int    `json:"index"`
	Kind     string `json:"kind"`
	Passed   bool   `json:"passed"`
	Expected any    `json:"expected,omitempty"`
	Actual   any    `json:"actual,omitempty"`
	Reason   string `json:"reason,omitempty"`
}

type AssertionReport struct {
	Passed                    bool              `json:"passed"`
	Results                   []AssertionResult `json:"results"`
	RuntimeRevision           uint64            `json:"runtime_revision"`
	FrameRevision             uint64            `json:"frame_revision"`
	GeometryRevision          uint64            `json:"geometry_revision"`
	PublishedRuntimeRevision  uint64            `json:"published_runtime_revision"`
	PublishedGeometryRevision uint64            `json:"published_geometry_revision"`
	ReloadRevision            uint64            `json:"reload_revision"`
	Resources                 []string          `json:"resources,omitempty"`
}

// EvaluateAssertions evaluates every assertion against the supplied snapshot
// in source order. Mismatches, unsupported kinds, and malformed/unknown fields
// are represented as ordered failed results so later assertions still run.
func EvaluateAssertions(snapshot AssertionSnapshot, assertions []Assertion) (AssertionReport, error) {
	if snapshot.View.Tree == nil {
		snapshot.View.Tree = snapshot.Tree
	}
	if snapshot.View.Scroll == nil {
		snapshot.View.Scroll = snapshot.Scroll
	}
	if snapshot.View.ScrollOffsets == nil {
		snapshot.View.ScrollOffsets = snapshot.ScrollOffsets
	}
	report := AssertionReport{
		Passed: true, Results: make([]AssertionResult, 0, len(assertions)),
		RuntimeRevision: snapshot.View.RuntimeRevision, FrameRevision: snapshot.View.FrameRevision,
		GeometryRevision: snapshot.View.GeometryRevision, PublishedRuntimeRevision: snapshot.View.PublishedRuntimeRevision,
		PublishedGeometryRevision: snapshot.View.PublishedGeometryRevision, ReloadRevision: snapshot.View.ReloadRevision,
	}
	for index, assertion := range assertions {
		result := evaluateAssertion(snapshot, assertion)
		result.Index, result.Kind = index, assertion.Kind
		report.Results = append(report.Results, result)
		if !result.Passed {
			report.Passed = false
		}
	}
	return report, nil
}

type AssertionSnapshot struct {
	Tree          *semantic.Node
	View          ViewSnapshot
	Router        interaction.RouterSnapshot
	Editing       interaction.EditingStoreSnapshot
	StateValues   map[string]map[string]any
	Scroll        map[string]render.ScrollMetrics
	ScrollOffsets map[string]image.Point
	Trace         TraceSnapshot
}

func validateAssertion(assertion Assertion) error {
	switch assertion.Kind {
	case "view", "trace":
	case "node_exists", "node_absent", "node_state", "node_geometry", "node_relation", "scroll", "transient":
		if assertion.SemanticID == "" && assertion.ID == "" && assertion.Kind != "transient" {
			return fmt.Errorf("semantic_id is required")
		}
	case "state_scope":
		if assertion.ID == "" && assertion.SemanticID == "" {
			return fmt.Errorf("scope id is required")
		}
	default:
		return fmt.Errorf("unsupported assertion kind %q", assertion.Kind)
	}
	if assertion.Tolerance < 0 {
		return fmt.Errorf("tolerance must be non-negative")
	}
	return nil
}

func evaluateAssertion(snapshot AssertionSnapshot, assertion Assertion) AssertionResult {
	result := AssertionResult{Expected: assertion.Expected}
	if assertion.MalformedReason != "" {
		result.Passed = false
		result.Reason = "malformed assertion: " + assertion.MalformedReason
		return result
	}
	if len(assertion.UnknownFields) > 0 {
		result.Passed = false
		result.Reason = "unknown assertion field(s): " + strings.Join(assertion.UnknownFields, ", ")
		return result
	}
	if !assertionFieldsAllowed(assertion) {
		result.Passed = false
		result.Reason = "assertion field is not allowed for kind"
		return result
	}
	if err := validateAssertion(assertion); err != nil {
		result.Passed = false
		result.Reason = err.Error()
		return result
	}
	assertion.Field = inferredAssertionField(assertion)
	expected := assertionExpected(assertion)
	result.Expected = expected
	switch assertion.Kind {
	case "view":
		actual, ok := viewField(snapshot.View, assertion.Field)
		if assertion.DiagnosticIndex != nil {
			actual, ok = diagnosticField(snapshot.View.Diagnostics, assertion.Field, *assertion.DiagnosticIndex)
		}
		if assertion.Field == "" {
			actual = viewMap(snapshot.View)
			ok = true
		}
		result.Actual, result.Passed = actual, ok && expectedEqual(expected, actual, assertion.Tolerance)
		if !ok {
			result.Reason = "unknown view field"
		}
	case "node_exists", "node_absent":
		node := findNode(snapshot.View.Tree, nodeID(assertion))
		present := node != nil
		result.Actual = present
		result.Passed = (assertion.Kind == "node_exists" && present) || (assertion.Kind == "node_absent" && !present)
		if !result.Passed {
			result.Reason = "semantic node presence did not match expectation"
		}
	case "node_state":
		node := findNode(snapshot.View.Tree, nodeID(assertion))
		if node == nil {
			result.Actual, result.Reason = nil, "semantic node was not found"
			break
		}
		actual, ok := nodeStateField(node, snapshot, assertion.Field)
		result.Actual, result.Passed = actual, ok && expectedEqual(expected, actual, assertion.Tolerance)
		if !ok {
			result.Reason = "unknown node state field"
		}
	case "node_geometry":
		node := findNode(snapshot.View.Tree, nodeID(assertion))
		if node == nil {
			result.Reason = "semantic node was not found"
			break
		}
		actual, ok := nodeGeometryField(node, assertion.Field)
		result.Actual, result.Passed = actual, ok && expectedGeometry(expected, actual, assertion.Tolerance)
		if !ok {
			result.Reason = "unknown geometry field"
		}
	case "node_relation":
		result.Actual, result.Passed, result.Reason = evaluateRelation(snapshot.View.Tree, assertion)
	case "scroll":
		result.Actual, result.Passed, result.Reason = evaluateScroll(snapshot, assertion)
	case "transient":
		actual, ok := transientField(snapshot, assertion.Field)
		result.Actual, result.Passed = actual, ok && expectedEqual(expected, actual, assertion.Tolerance)
		if !ok {
			result.Reason = "unknown transient field"
		}
	case "state_scope":
		id := assertion.ID
		if id == "" {
			id = assertion.SemanticID
		}
		if snapshot.View.VisibleScopes != nil && !snapshot.View.VisibleScopes[id] {
			result.Actual, result.Passed, result.Reason = nil, false, "state scope is not visible"
			break
		}
		actual, ok := snapshot.StateValues[id]
		result.Actual, result.Passed = actual, ok && expectedEqual(expected, actual, assertion.Tolerance)
		if !ok {
			result.Reason = "state scope was not found"
		}
	case "trace":
		result.Actual, result.Passed, result.Reason = evaluateTrace(snapshot.Trace, assertion)
	}
	if !result.Passed && result.Reason == "" {
		result.Reason = "expected value did not match actual value"
	}
	return result
}

func inferredAssertionField(assertion Assertion) string {
	if assertion.Field != "" {
		return assertion.Field
	}
	switch assertion.Kind {
	case "node_state":
		if assertion.Value != nil {
			return "value"
		}
	case "node_geometry":
		if assertion.Bounds != nil {
			return "bounds"
		}
		if assertion.Clip != nil {
			return "clip"
		}
		if assertion.Null != nil {
			return "null"
		}
	case "scroll":
		if assertion.Offset != nil {
			return "offset"
		}
		if assertion.Maximum != nil {
			return "maximum"
		}
		if assertion.ViewportSize != nil {
			return "viewport"
		}
		if assertion.ContentSize != nil {
			return "content"
		}
		if assertion.EnabledX != nil {
			return "enabled_x"
		}
		if assertion.EnabledY != nil {
			return "enabled_y"
		}
	case "view":
		if assertion.DiagnosticCode != "" {
			return "diagnostic_code"
		}
		if assertion.DiagnosticSeverity != "" {
			return "diagnostic_severity"
		}
		if assertion.DiagnosticCodes != nil {
			return "diagnostic_codes"
		}
	}
	return assertion.Field
}

func assertionFieldsAllowed(assertion Assertion) bool {
	if len(assertion.ProvidedFields) == 0 {
		return true
	}
	allowed := map[string]bool{"kind": true, "expected": true, "tolerance": true}
	sets := map[string][]string{
		"view":          {"field", "diagnostic_index", "diagnostic_code", "diagnostic_severity", "diagnostic_codes"},
		"node_exists":   {"semantic_id", "id"},
		"node_absent":   {"semantic_id", "id"},
		"node_state":    {"semantic_id", "id", "field", "value"},
		"node_geometry": {"semantic_id", "id", "field", "bounds", "clip", "null"},
		"node_relation": {"semantic_id", "id", "relation", "other_id", "x", "y"},
		"scroll":        {"semantic_id", "id", "field", "offset", "maximum", "viewport", "content", "enabled_x", "enabled_y"},
		"transient":     {"field"},
		"state_scope":   {"semantic_id", "id"},
		"trace":         {"stage", "owner", "axis", "outcome", "consumed", "residual", "stages", "owners", "generation"},
	}
	for _, field := range sets[assertion.Kind] {
		allowed[field] = true
	}
	for _, field := range assertion.ProvidedFields {
		if !allowed[field] {
			return false
		}
	}
	return true
}

func nodeID(assertion Assertion) string {
	if assertion.SemanticID != "" {
		return assertion.SemanticID
	}
	return assertion.ID
}

func assertionExpected(assertion Assertion) any {
	if assertion.Expected != nil {
		return assertion.Expected
	}
	for _, value := range []any{assertion.Value, assertion.Bounds, assertion.Clip, assertion.Offset, assertion.Maximum, assertion.ViewportSize, assertion.ContentSize} {
		if value != nil {
			return value
		}
	}
	if assertion.EnabledX != nil {
		return *assertion.EnabledX
	}
	if assertion.EnabledY != nil {
		return *assertion.EnabledY
	}
	if assertion.Null != nil {
		return *assertion.Null
	}
	if assertion.DiagnosticCode != "" {
		return assertion.DiagnosticCode
	}
	if assertion.DiagnosticSeverity != "" {
		return assertion.DiagnosticSeverity
	}
	if assertion.DiagnosticCodes != nil {
		return assertion.DiagnosticCodes
	}
	return nil
}

func findNode(root *semantic.Node, id string) *semantic.Node {
	if root == nil || id == "" {
		return nil
	}
	for _, node := range semantic.Flatten(root) {
		if node != nil && node.ID == id {
			return node
		}
	}
	return nil
}

func viewMap(view ViewSnapshot) map[string]any {
	return map[string]any{
		"valid": view.Valid, "last_good": view.LastGoodAvailable, "last_good_available": view.LastGoodAvailable, "selection": view.Selection,
		"selections": view.Selections, "viewport": map[string]any{"width": view.Viewport.X, "height": view.Viewport.Y},
		"viewport_width": view.Viewport.X, "viewport_height": view.Viewport.Y,
		"runtime_revision": view.RuntimeRevision, "frame_revision": view.FrameRevision, "geometry_revision": view.GeometryRevision,
		"published_runtime_revision": view.PublishedRuntimeRevision, "published_geometry_revision": view.PublishedGeometryRevision,
		"reload_revision": view.ReloadRevision, "automation_input_revision": view.AutomationInputRevision,
		"can_back": view.CanBack, "can_forward": view.CanForward, "idle": view.Idle,
		"diagnostic_count": len(view.Diagnostics), "diagnostics": view.Diagnostics,
	}
}

func viewField(view ViewSnapshot, field string) (any, bool) {
	if field == "" {
		return viewMap(view), true
	}
	if value, ok := viewMap(view)[strings.ToLower(field)]; ok {
		return value, true
	}
	switch strings.ToLower(field) {
	case "diagnostic_code":
		return diagnosticField(view.Diagnostics, field, 0)
	case "diagnostic_severity":
		return diagnosticField(view.Diagnostics, field, 0)
	case "diagnostic_codes":
		diagnostics := sortedDiagnostics(view.Diagnostics)
		codes := make([]string, len(diagnostics))
		for i := range diagnostics {
			codes[i] = diagnostics[i].Code
		}
		return codes, true
	case "diagnostic_severities":
		diagnostics := sortedDiagnostics(view.Diagnostics)
		severities := make([]string, len(diagnostics))
		for i := range diagnostics {
			severities[i] = diagnostics[i].Severity
		}
		return severities, true
	}
	return nil, false
}

func sortedDiagnostics(values []document.Diagnostic) []document.Diagnostic {
	result := append([]document.Diagnostic(nil), values...)
	sort.SliceStable(result, func(i, j int) bool {
		left, right := result[i], result[j]
		if left.File != right.File {
			return left.File < right.File
		}
		if left.Line != right.Line {
			return left.Line < right.Line
		}
		if left.Column != right.Column {
			return left.Column < right.Column
		}
		return left.Code < right.Code
	})
	return result
}

func diagnosticField(values []document.Diagnostic, field string, index int) (any, bool) {
	diagnostics := sortedDiagnostics(values)
	if index < 0 || index >= len(diagnostics) {
		return nil, true
	}
	diagnostic := diagnostics[index]
	switch strings.ToLower(field) {
	case "diagnostic_code":
		return diagnostic.Code, true
	case "diagnostic_severity":
		return diagnostic.Severity, true
	default:
		return nil, false
	}
}

func nodeStateField(node *semantic.Node, snapshot AssertionSnapshot, field string) (any, bool) {
	if node == nil {
		return nil, false
	}
	if field == "" {
		return node, true
	}
	switch strings.ToLower(field) {
	case "type":
		return node.Type, true
	case "role":
		return node.Role, true
	case "label":
		return node.Label, true
	case "id", "semantic_id":
		return node.ID, true
	case "name":
		return node.Name, true
	case "breadcrumb", "breadcrumbs":
		return node.Breadcrumb, true
	case "scope":
		return node.Scope, true
	case "binding":
		return node.Binding, true
	case "form":
		return node.Form, true
	case "source":
		return node.Source, true
	case "value":
		return node.Value, true
	case "committed", "committed_value":
		return node.CommittedValue, true
	case "enabled":
		return node.Enabled, true
	case "visible":
		return node.Visible, true
	case "in_viewport", "inviewport":
		return node.InViewport, true
	case "checked":
		return pointerBoolValue(node.Checked), true
	case "selected":
		return pointerBoolValue(node.Selected), true
	case "expanded":
		return pointerBoolValue(node.Expanded), true
	case "valid":
		return pointerBoolValue(node.Valid), true
	case "dirty":
		return node.Dirty, true
	case "touched":
		return node.Touched, true
	case "focused":
		return node.Focused || snapshot.Router.FocusedID == node.ID, true
	case "hovered":
		return node.Hovered, true
	case "pressed":
		return node.Pressed, true
	case "active":
		for _, id := range snapshot.Router.ActiveIDs {
			if id == node.ID {
				return true, true
			}
		}
		return false, true
	case "focus_order":
		return node.FocusOrder, true
	case "paint_order":
		return node.PaintOrder, true
	case "state":
		return node.State, true
	case "selection_start":
		return node.SelectionStart, true
	case "selection_end":
		return node.SelectionEnd, true
	case "composition":
		return node.Composition, true
	case "composing":
		return node.Composing, true
	case "read_only":
		return node.ReadOnly, true
	}
	return nil, false
}

func pointerBoolValue(value *bool) any {
	if value == nil {
		return nil
	}
	return *value
}

func nodeGeometryField(node *semantic.Node, field string) (any, bool) {
	if field == "" {
		field = "bounds"
	}
	switch strings.ToLower(field) {
	case "bounds":
		return rectMap(node.Bounds), true
	case "clip":
		return rectMap(node.Clip), true
	case "null", "is_null":
		return node.Bounds == nil, true
	}
	return nil, false
}

func rectMap(value *semantic.Rect) any {
	if value == nil {
		return nil
	}
	return map[string]any{"x": value.X, "y": value.Y, "width": value.Width, "height": value.Height}
}

func expectedGeometry(expected, actual any, tolerance float64) bool {
	if expected == nil || actual == nil {
		return expected == nil && actual == nil
	}
	if expectedMap, ok := expected.(map[string]any); ok {
		actualMap, ok := actual.(map[string]any)
		if !ok {
			return false
		}
		for key, value := range expectedMap {
			if !expectedEqual(value, actualMap[key], tolerance) {
				return false
			}
		}
		return true
	}
	return expectedEqual(expected, actual, tolerance)
}

func evaluateRelation(root *semantic.Node, assertion Assertion) (any, bool, string) {
	node := findNode(root, nodeID(assertion))
	if node == nil {
		return nil, false, "semantic node was not found"
	}
	other := findNode(root, assertion.OtherID)
	relation := strings.ToLower(assertion.Relation)
	switch relation {
	case "contains_point":
		if assertion.X == nil || assertion.Y == nil {
			return nil, false, "x and y are required"
		}
		actual := node.Bounds != nil && image.Pt(assertion.XValue(), assertion.YValue()).In(node.Bounds.ImageRectangle())
		// keep relation semantics deterministic for omitted expectations
		return relationResult(actual, assertionExpected(assertion))
	case "parent", "child":
		if other == nil {
			return nil, false, "other node was not found"
		}
		parentOf := relation == "parent"
		actual := false
		if parentOf {
			actual = containsChild(node, other)
		} else {
			actual = containsChild(other, node)
		}
		return relationResult(actual, assertionExpected(assertion))
	case "paint_above", "paint_below":
		if other == nil {
			return nil, false, "other node was not found"
		}
		actual := node.PaintOrder > other.PaintOrder
		if relation == "paint_below" {
			actual = node.PaintOrder < other.PaintOrder
		}
		return relationResult(actual, assertionExpected(assertion))
	case "focus_before", "focus_after":
		if other == nil {
			return nil, false, "other node was not found"
		}
		actual := node.FocusOrder < other.FocusOrder
		if relation == "focus_after" {
			actual = node.FocusOrder > other.FocusOrder
		}
		return relationResult(actual, assertionExpected(assertion))
	case "clipped_by":
		if other == nil || node.Clip == nil || other.Clip == nil {
			return false, false, "clip geometry is unavailable"
		}
		actual := other.Clip.ImageRectangle().Intersect(node.Clip.ImageRectangle()) == node.Clip.ImageRectangle()
		return relationResult(actual, assertionExpected(assertion))
	default:
		return nil, false, "unknown node relation"
	}
}

func relationResult(actual bool, expected any) (any, bool, string) {
	if expected == nil {
		return actual, actual, ""
	}
	return actual, expectedEqual(expected, actual, 0), ""
}

func containsChild(parent, child *semantic.Node) bool {
	for _, candidate := range parent.Children {
		if candidate == child || candidate.ID == child.ID {
			return true
		}
	}
	return false
}

func (assertion Assertion) XValue() int {
	if assertion.X == nil {
		return 0
	}
	return *assertion.X
}
func (assertion Assertion) YValue() int {
	if assertion.Y == nil {
		return 0
	}
	return *assertion.Y
}

func evaluateScroll(snapshot AssertionSnapshot, assertion Assertion) (any, bool, string) {
	node := findNode(snapshot.View.Tree, nodeID(assertion))
	key := nodeID(assertion)
	if node != nil && node.Handle != "" {
		key = node.Handle
	}
	metrics, ok := snapshot.View.Scroll[key]
	if !ok {
		metrics, ok = snapshot.Scroll[key]
	}
	if !ok && node != nil && node.Group != "" {
		key = node.Group
		metrics, ok = snapshot.View.Scroll[key]
		if !ok {
			metrics, ok = snapshot.Scroll[key]
		}
	}
	if !ok && node != nil && node.Name != "" {
		metrics, ok = snapshot.View.Scroll[node.Name]
		if !ok {
			metrics, ok = snapshot.Scroll[node.Name]
		}
	}
	if !ok {
		return nil, false, "scroll metrics were not found"
	}
	actual := map[string]any{"offset": map[string]any{"x": offsetForNode(snapshot.View, node, key, 0), "y": offsetForNode(snapshot.View, node, key, 1)}, "maximum": map[string]any{"x": metrics.Maximum.X, "y": metrics.Maximum.Y}, "viewport": map[string]any{"width": metrics.Viewport.Dx(), "height": metrics.Viewport.Dy()}, "content": map[string]any{"width": metrics.ContentSize.X, "height": metrics.ContentSize.Y}, "enabled_x": metrics.EnabledX, "enabled_y": metrics.EnabledY}
	if assertion.Field != "" {
		field := strings.ToLower(assertion.Field)
		if value, exists := actual[field]; exists {
			return value, expectedEqual(assertionExpected(assertion), value, assertion.Tolerance), ""
		}
		if field == "offset_x" || field == "offset_y" {
			offset := actual["offset"].(map[string]any)
			key := "x"
			if field == "offset_y" {
				key = "y"
			}
			return offset[key], expectedEqual(assertionExpected(assertion), offset[key], assertion.Tolerance), ""
		}
		if field == "maximum_x" || field == "maximum_y" {
			maximum := actual["maximum"].(map[string]any)
			key := "x"
			if field == "maximum_y" {
				key = "y"
			}
			return maximum[key], expectedEqual(assertionExpected(assertion), maximum[key], assertion.Tolerance), ""
		}
	}
	return actual, expectedEqual(assertionExpected(assertion), actual, assertion.Tolerance), ""
}

func offsetFor(view ViewSnapshot, key string, axis int) int {
	point, ok := view.ScrollOffsets[key]
	if !ok {
		return 0
	}
	if axis == 0 {
		return point.X
	}
	return point.Y
}

func offsetForNode(view ViewSnapshot, node *semantic.Node, key string, axis int) int {
	candidates := []string{key, nodeIDFromNode(node), node.Name, node.Group}
	if node != nil && node.Group != "" {
		for _, owner := range semantic.Flatten(view.Tree) {
			if owner != nil && owner.Handle == node.Group {
				candidates = append(candidates, owner.ID, owner.Name)
				break
			}
		}
	}
	for _, candidate := range candidates {
		if candidate == "" {
			continue
		}
		if point, ok := view.ScrollOffsets[candidate]; ok {
			if axis == 0 {
				return point.X
			}
			return point.Y
		}
	}
	return offsetFor(view, key, axis)
}

func nodeIDFromNode(node *semantic.Node) string {
	if node == nil {
		return ""
	}
	return node.ID
}

func transientField(snapshot AssertionSnapshot, field string) (any, bool) {
	switch strings.ToLower(field) {
	case "focus", "focused":
		return snapshot.Router.FocusedID, true
	case "hovered":
		if len(snapshot.Router.HoveredIDs) > 0 {
			return snapshot.Router.HoveredIDs[0], true
		}
		return "", true
	case "pressed":
		if len(snapshot.Router.PressedIDs) > 0 {
			return snapshot.Router.PressedIDs[0], true
		}
		return "", true
	case "pointer_capture":
		if snapshot.Router.PointerCapture == nil {
			return nil, true
		}
		capture := snapshot.Router.PointerCapture
		return map[string]any{"owner_id": capture.OwnerID, "pointer_id": capture.PointerID, "source": capture.Source, "buttons": capture.Buttons, "point": map[string]any{"x": capture.Point.X, "y": capture.Point.Y}}, true
	case "pointer_capture_owner", "capture_owner":
		if snapshot.Router.PointerCapture == nil {
			return "", true
		}
		return snapshot.Router.PointerCapture.OwnerID, true
	case "gesture_capture", "scrollbar_gesture_owner":
		return snapshot.Router.ScrollbarGestureOwner, true
	case "slider_gesture_owner":
		return snapshot.Router.SliderGestureOwner, true
	case "popup", "open_select":
		return snapshot.Router.OpenSelectID, true
	case "active_option":
		if len(snapshot.Router.ActiveIDs) > 0 {
			return snapshot.Router.ActiveIDs[0], true
		}
		return "", true
	case "keyboard_owner":
		if snapshot.Router.KeyboardPress == nil {
			return "", true
		}
		return snapshot.Router.KeyboardPress.OwnerID, true
	case "editing_target":
		field, ok := focusedEditingField(snapshot)
		if !ok {
			return "", true
		}
		return field.ID, true
	case "queue_cardinality":
		return snapshot.Router.QueueSizes.ValueChanges + snapshot.Router.QueueSizes.ScrollChanges, true
	case "clock":
		return snapshot.View.Clock, true
	case "caret":
		if fieldSnapshot, ok := focusedEditingField(snapshot); ok {
			return map[string]any{"selection_start": fieldSnapshot.SelectionStart, "selection_end": fieldSnapshot.SelectionEnd}, true
		}
		return nil, true
	case "selection":
		if fieldSnapshot, ok := focusedEditingField(snapshot); ok {
			return map[string]any{"start": fieldSnapshot.SelectionStart, "end": fieldSnapshot.SelectionEnd}, true
		}
		return nil, true
	case "composition":
		if fieldSnapshot, ok := focusedEditingField(snapshot); ok {
			return map[string]any{"composing": fieldSnapshot.Composing, "text": fieldSnapshot.Composition, "start": fieldSnapshot.CompositionStart, "end": fieldSnapshot.CompositionEnd}, true
		}
		return map[string]any{"composing": false, "text": "", "start": 0, "end": 0}, true
	}
	return nil, false
}

func focusedEditingField(snapshot AssertionSnapshot) (interaction.FieldSnapshot, bool) {
	if snapshot.Editing.Fields == nil {
		return interaction.FieldSnapshot{}, false
	}
	focused := snapshot.Router.FocusedID
	if field, ok := snapshot.Editing.Fields[focused]; ok {
		return field, true
	}
	// Router IDs are normally semantic IDs, but native hosts may retain the
	// editing-store ID (handle/name) while the published tree has been rebuilt.
	if node := findNode(snapshot.View.Tree, focused); node != nil {
		for _, candidate := range []string{node.Handle, node.Name} {
			if candidate != "" {
				if field, ok := snapshot.Editing.Fields[candidate]; ok {
					return field, true
				}
			}
		}
	}
	for _, field := range snapshot.Editing.Fields {
		if field.ID == focused {
			return field, true
		}
	}
	return interaction.FieldSnapshot{}, false
}

func evaluateTrace(trace TraceSnapshot, assertion Assertion) (any, bool, string) {
	if assertion.Generation != nil && trace.Generation != *assertion.Generation {
		return trace.Generation, false, "trace generation did not match"
	}
	entries := trace.Entries
	actual := make([]string, 0, len(entries))
	for _, entry := range entries {
		actual = append(actual, entry.Stage)
	}
	if len(assertion.Stages) == 0 && assertion.Stage == "" {
		return actual, true, ""
	}
	stages := append([]string(nil), assertion.Stages...)
	if assertion.Stage != "" {
		stages = []string{assertion.Stage}
	}
	position := 0
	for stageIndex, wanted := range stages {
		found := false
		owner := assertion.Owner
		if owner == "" && stageIndex < len(assertion.Owners) {
			owner = assertion.Owners[stageIndex]
		}
		for position < len(entries) {
			entry := entries[position]
			position++
			if entry.Stage == wanted && (owner == "" || entry.TargetID == owner || entry.SemanticID == owner) && (assertion.Axis == "" || entry.Axis == assertion.Axis) && (assertion.Outcome == "" || entry.Outcome == assertion.Outcome) && (assertion.Consumed == nil || absFloat(entry.Consumed-*assertion.Consumed) <= 1e-9) && (assertion.Residual == nil || absFloat(entry.Residual-*assertion.Residual) <= 1e-9) {
				found = true
				break
			}
		}
		if !found {
			return actual, false, "trace subsequence did not match"
		}
	}
	return actual, true, ""
}

func expectedEqual(expected, actual any, tolerance float64) bool {
	if expected == nil {
		return actual == nil
	}
	if numberExpected(expected) && numberExpected(actual) {
		e, _ := numberFloat(expected)
		a, _ := numberFloat(actual)
		return absFloat(e-a) <= tolerance
	}
	if expectedMap, ok := expected.(map[string]any); ok {
		actualMap, ok := actual.(map[string]any)
		if !ok || len(expectedMap) != len(actualMap) {
			return false
		}
		for key, value := range expectedMap {
			if !expectedEqual(value, actualMap[key], tolerance) {
				return false
			}
		}
		return true
	}
	if expectedValue := reflect.ValueOf(expected); expectedValue.IsValid() && expectedValue.Kind() == reflect.Slice {
		actualValue := reflect.ValueOf(actual)
		if !actualValue.IsValid() || actualValue.Kind() != reflect.Slice || expectedValue.Len() != actualValue.Len() {
			return false
		}
		for index := 0; index < expectedValue.Len(); index++ {
			if !expectedEqual(expectedValue.Index(index).Interface(), actualValue.Index(index).Interface(), tolerance) {
				return false
			}
		}
		return true
	}
	return reflect.DeepEqual(expected, actual)
}

func numberExpected(value any) bool {
	switch value.(type) {
	case int, int32, int64, uint, uint32, uint64, float32, float64:
		return true
	}
	return false
}
func numberFloat(value any) (float64, bool) {
	switch value := value.(type) {
	case int:
		return float64(value), true
	case int32:
		return float64(value), true
	case int64:
		return float64(value), true
	case uint:
		return float64(value), true
	case uint32:
		return float64(value), true
	case uint64:
		return float64(value), true
	case float32:
		return float64(value), true
	case float64:
		return value, true
	}
	return 0, false
}
func absFloat(value float64) float64 {
	if value < 0 {
		return -value
	}
	return value
}

// Keep imports and JSON output deterministic for callers inspecting maps.
func sortedKeys(values map[string]any) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
