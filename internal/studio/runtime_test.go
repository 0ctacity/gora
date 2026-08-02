package studio

import (
	"encoding/json"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"testing"

	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/unit"
	"gioui.org/widget/material"

	"gora/internal/document"
	"gora/internal/interaction"
	"gora/internal/render"
	"gora/internal/semantic"
)

func TestRuntimeInspectPublishesCanonicalTreeAndLastGoodDiagnostics(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "app.gora")
	valid := []byte(`
gora: 1
kind: app
viewport: { width: 200, height: 80 }
entry: home
screens:
  home:
    type: link
    name: reports-link
    props: { label: Reports, to: reports }
    children:
      - { type: text, props: { text: Reports } }
  reports: { type: spacer }
`)
	if err := os.WriteFile(path, valid, 0o600); err != nil {
		t.Fatal(err)
	}
	runtime, err := NewRuntime(dir, path)
	if err != nil {
		t.Fatal(err)
	}
	data, warning, err := runtime.Inspect("headless")
	if err != nil || warning != "" {
		t.Fatalf("inspect error=%v warning=%q", err, warning)
	}
	var envelope semantic.Envelope
	if err := json.Unmarshal(data, &envelope); err != nil {
		t.Fatal(err)
	}
	if !envelope.Valid || envelope.HostMode != "headless" || envelope.CurrentScreen != "home" || envelope.Root == nil || envelope.Root.Role != "link" {
		t.Fatalf("envelope = %+v", envelope)
	}
	if err := os.WriteFile(path, []byte("gora: 1\nkind: app\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runtime.Reload()
	data, warning, err = runtime.Inspect("headless")
	if err != nil || warning == "" {
		t.Fatalf("last-good inspect error=%v warning=%q", err, warning)
	}
	if err := json.Unmarshal(data, &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.Valid || envelope.Root == nil || len(envelope.Diagnostics) == 0 {
		t.Fatalf("last-good envelope = %+v", envelope)
	}
}

func TestRuntimeSemanticControlMethodsActivateSetResetAndScroll(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "app.gora")
	source := []byte(`
gora: 1
kind: app
viewport: { width: 120, height: 80 }
state:
  count: { type: number, default: 1 }
entry: main
screens:
  main:
    type: stack
    props: { direction: vertical }
    children:
      - type: button
        name: increment
        props: { label: Increment }
        on:
          activate: [{ action: increment, state: count }]
        children: [{ type: text, props: { text: Add } }]
      - type: scroll
        name: feed
        props: { axis: vertical }
        children: [{ type: spacer, props: { height: 200 } }]
`)
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
	button := namedSemanticNode(tree, "increment")
	if button == nil {
		t.Fatal("missing button")
	}
	if err := runtime.ActivateSemanticID(button.ID); err != nil {
		t.Fatal(err)
	}
	if got := runtime.Snapshot().StateValues["screen:main"]["count"]; got != float64(2) {
		t.Fatalf("count after activation = %#v", got)
	}
	if err := runtime.SetStateValues("screen:main", map[string]any{"count": float64(7)}); err != nil {
		t.Fatal(err)
	}
	if err := runtime.ResetStateScope("screen:main"); err != nil {
		t.Fatal(err)
	}
	if got := runtime.Snapshot().StateValues["screen:main"]["count"]; got != float64(1) {
		t.Fatalf("count after reset = %#v", got)
	}
	tree, err = runtime.RuntimeTree()
	if err != nil {
		t.Fatal(err)
	}
	var scroll *semantic.Node
	for _, node := range semantic.Flatten(tree) {
		if node.Type == "scroll" {
			scroll = node
			break
		}
	}
	if scroll == nil {
		t.Fatal("missing scroll")
	}
	if err := runtime.ScrollSemanticID(scroll.ID, "to", 0, 500); err != nil {
		t.Fatal(err)
	}
	if got := runtime.Snapshot().Scroll["feed"].Y; got != 120 {
		t.Fatalf("clamped scroll = %d, want 120", got)
	}
}

func TestRuntimeActivatesAndSetsBoundSemanticControls(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "controls.gora")
	source := []byte(`
gora: 1
kind: app
viewport: { width: 240, height: 140 }
state:
  enabled: { type: boolean, default: false }
  plan: { type: enum, values: [monthly, annual], default: monthly }
  volume: { type: number, default: 20, min: 0, max: 100, step: 10 }
entry: main
screens:
  main:
    type: stack
    children:
      - type: toggle
        name: enabled-toggle
        props: { label: Enabled, bind: enabled, height: 30 }
        children: [{ type: text, props: { text: Enabled } }]
      - type: radio_group
        name: plan-group
        props: { label: Plan, bind: plan }
        children:
          - type: radio
            name: monthly-radio
            props: { label: Monthly, value: monthly }
            children: [{ type: text, props: { text: Monthly } }]
          - type: radio
            name: annual-radio
            props: { label: Annual, value: annual }
            children: [{ type: text, props: { text: Annual } }]
      - type: slider
        name: volume-slider
        props: { label: Volume, bind: volume, height: 20 }
        children:
          - { type: slider_track, props: { height: 4 } }
          - { type: slider_thumb, props: { width: 12, height: 12 } }
`)
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
	if err := runtime.ActivateSemanticID(namedSemanticNode(tree, "enabled-toggle").ID); err != nil {
		t.Fatal(err)
	}
	if got := runtime.Snapshot().StateValues["screen:main"]["enabled"]; got != true {
		t.Fatalf("toggle value = %#v", got)
	}
	tree, _ = runtime.RuntimeTree()
	annual := namedSemanticNode(tree, "annual-radio")
	if annual == nil || len(annual.Actions) != 1 || annual.Actions[0].State != "plan" || annual.Actions[0].Value != "annual" {
		t.Fatalf("annual semantics = %+v", annual)
	}
	if err := runtime.ActivateSemanticID(annual.ID); err != nil {
		t.Fatal(err)
	}
	if got := runtime.Snapshot().StateValues["screen:main"]["plan"]; got != "annual" {
		t.Fatalf("radio value = %#v, annual=%+v", got, annual)
	}
	tree, _ = runtime.RuntimeTree()
	value, err := runtime.SetControlValue(namedSemanticNode(tree, "volume-slider").ID, float64(56))
	if err != nil || value != float64(60) {
		t.Fatalf("set control value = %#v, %v", value, err)
	}
	if _, err := runtime.SetControlValue(namedSemanticNode(tree, "volume-slider").ID, float64(100)); err != nil {
		t.Fatal(err)
	}
	before := runtime.Snapshot().RuntimeRevision
	if err := runtime.Activate(interaction.Activation{Scope: "screen:main", Actions: []document.Action{{Action: "increment", State: "volume", By: float64(10)}}}); err != nil {
		t.Fatal(err)
	}
	if after := runtime.Snapshot().RuntimeRevision; after != before {
		t.Fatalf("bound no-op changed runtime revision: before=%d after=%d", before, after)
	}
}

func TestNorthstarPointerKeyboardHistoryAndScrollRestoration(t *testing.T) {
	repositoryRoot, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := NewRuntime(repositoryRoot, filepath.Join(repositoryRoot, "examples", "dashboard", "app.gora"))
	if err != nil {
		t.Fatal(err)
	}
	router := interaction.NewRouter()
	renderFrame := func() (Snapshot, *semantic.Node) {
		snapshot := runtime.Snapshot()
		tree := render.Render(snapshot.Root, snapshot.Viewport, renderState(snapshot)).Tree
		router.Update(tree)
		return snapshot, tree
	}
	snapshot, _ := renderFrame()
	if snapshot.Screen != "overview" {
		t.Fatalf("initial screen = %q", snapshot.Screen)
	}
	if got := router.FocusNext(false); got == "" {
		t.Fatal("overview link did not receive keyboard focus")
	}
	router.FocusNext(false)
	activation, ok := router.KeyDown("Enter")
	if !ok {
		t.Fatal("Enter did not activate revenue link")
	}
	if err := runtime.Activate(activation); err != nil {
		t.Fatal(err)
	}
	snapshot, tree := renderFrame()
	if snapshot.Screen != "revenue" || !snapshot.CanBack {
		t.Fatalf("after keyboard navigation = %+v", snapshot)
	}
	current := namedSemanticNode(tree, "revenue-link")
	if current == nil || !current.Current || current.Props["background"] != "#635BFF" {
		t.Fatalf("current revenue link = %+v", current)
	}
	runtime.SetScrollOffset("revenue-feed", "vertical", 90)
	_, tree = renderFrame()
	reports := namedSemanticNode(tree, "reports-link")
	if reports == nil || reports.Bounds == nil || reports.Clip == nil {
		t.Fatalf("reports link = %+v", reports)
	}
	point := reports.Bounds.ImageRectangle().Intersect(reports.Clip.ImageRectangle()).Min.Add(image.Pt(4, 4))
	if !router.Press(7, point) {
		t.Fatal("reports pointer press was rejected")
	}
	activation, ok = router.Release(7, point)
	if !ok {
		t.Fatal("reports pointer release did not activate")
	}
	if err := runtime.Activate(activation); err != nil {
		t.Fatal(err)
	}
	snapshot, tree = renderFrame()
	if snapshot.Screen != "reports" || namedSemanticNode(tree, "reports-link") == nil || !namedSemanticNode(tree, "reports-link").Current {
		t.Fatalf("after reports navigation = %+v", snapshot)
	}
	back := namedSemanticNode(tree, "history-back")
	if back == nil {
		t.Fatal("missing history back button")
	}
	if err := runtime.Activate(interaction.Activation{Scope: back.Scope, Actions: back.Actions}); err != nil {
		t.Fatal(err)
	}
	snapshot, _ = renderFrame()
	if snapshot.Screen != "revenue" || snapshot.Scroll["revenue-feed"].Y != 90 || !snapshot.CanForward {
		t.Fatalf("restored revenue entry = %+v", snapshot)
	}
}

func TestRuntimeRepeatedNavigationAndReloadRetentionIsBounded(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "app.gora")
	source := []byte(`
gora: 1
kind: app
viewport: { width: 120, height: 80 }
entry: first
screens:
  first:
    type: scroll
    name: first-feed
    props: { axis: vertical }
    children: [{ type: spacer, props: { height: 200 } }]
  second:
    type: scroll
    name: second-feed
    props: { axis: vertical }
    children: [{ type: spacer, props: { height: 200 } }]
`)
	if err := os.WriteFile(path, source, 0o600); err != nil {
		t.Fatal(err)
	}
	runtime, err := NewRuntime(dir, path)
	if err != nil {
		t.Fatal(err)
	}
	for index := 0; index < 300; index++ {
		target := "second"
		if runtime.Snapshot().Screen == "second" {
			target = "first"
		}
		runtime.SetScrollOffset(runtime.Snapshot().Screen+"-feed", "vertical", index)
		if err := runtime.Activate(interaction.Activation{Actions: []document.Action{{Action: "navigate", To: target}}}); err != nil {
			t.Fatal(err)
		}
		if index%25 == 0 {
			runtime.Reload()
		}
	}
	if runtime.navigation == nil || runtime.navigation.Len() != 100 {
		t.Fatalf("history length = %d", runtime.navigation.Len())
	}
	if len(runtime.scroll) > 1 || len(runtime.state.AllValues()) != 0 {
		t.Fatalf("unbounded runtime state: scroll=%v state=%v", runtime.scroll, runtime.state.AllValues())
	}
}

func namedSemanticNode(root *semantic.Node, name string) *semantic.Node {
	for _, node := range semantic.Flatten(root) {
		if node.Name == name {
			return node
		}
	}
	return nil
}

func TestRuntimeNavigationCommitsStateRestoresScrollAndResetsStudioSelection(t *testing.T) {
	root := t.TempDir()
	entry := filepath.Join(root, "app.gora")
	if err := os.WriteFile(entry, []byte(`
gora: 1
kind: app
viewport: { width: 400, height: 300 }
state:
  visits: { type: number, default: 0 }
entry: home
screens:
  home:
    type: scroll
    name: home-feed
    props: { axis: vertical }
    children:
      - { type: spacer, props: { height: 800 } }
  reports:
    type: scroll
    name: reports-feed
    props: { axis: vertical }
    children:
      - { type: spacer, props: { height: 800 } }
`), 0o600); err != nil {
		t.Fatal(err)
	}
	runtime, err := NewRuntime(root, entry)
	if err != nil {
		t.Fatal(err)
	}
	runtime.SetScrollOffset("home-feed", "vertical", 40)
	if err := runtime.Activate(interaction.Activation{Scope: "screen:home", Actions: []document.Action{
		{Action: "increment", State: "visits", By: float64(1)},
		{Action: "navigate", To: "reports"},
	}}); err != nil {
		t.Fatal(err)
	}
	snapshot := runtime.Snapshot()
	if snapshot.Screen != "reports" || !snapshot.CanBack || snapshot.CanForward || len(snapshot.Scroll) != 0 {
		t.Fatalf("after navigate = %+v", snapshot)
	}
	if snapshot.StateValues["screen:home"]["visits"] != float64(1) {
		t.Fatalf("home state = %+v", snapshot.StateValues["screen:home"])
	}
	runtime.SetScrollOffset("reports-feed", "vertical", 80)
	if err := runtime.Activate(interaction.Activation{Scope: "screen:reports", Actions: []document.Action{{Action: "back"}}}); err != nil {
		t.Fatal(err)
	}
	snapshot = runtime.Snapshot()
	if snapshot.Screen != "home" || snapshot.Scroll["home-feed"].Y != 40 || snapshot.CanBack || !snapshot.CanForward {
		t.Fatalf("after back = %+v", snapshot)
	}

	if !runtime.SelectScreen("reports") {
		t.Fatal("SelectScreen reports failed")
	}
	snapshot = runtime.Snapshot()
	if snapshot.Screen != "reports" || snapshot.CanBack || snapshot.CanForward || len(snapshot.Scroll) != 0 {
		t.Fatalf("Studio selection did not reset navigation = %+v", snapshot)
	}
}

func TestRepeatedButtonActivationAcrossPersistentRebuilds(t *testing.T) {
	repositoryRoot, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := NewRuntime(repositoryRoot, filepath.Join(repositoryRoot, "examples", "interactivity", "app.gora"))
	if err != nil {
		t.Fatal(err)
	}
	var cache render.GioCache
	theme := material.NewTheme()
	router := interaction.NewRouter()
	nextResult := func() (Snapshot, render.GioResult) {
		snapshot := runtime.Snapshot()
		var operations op.Ops
		result := cache.Layout(layout.Context{
			Ops: &operations, Metric: unit.Metric{PxPerDp: 1, PxPerSp: 1}, Constraints: layout.Exact(snapshot.Viewport),
		}, theme, snapshot.Root, snapshot.Viewport, renderState(snapshot))
		router.Update(result.Tree)
		return snapshot, result
	}
	activate := func(name string, pointerID int) {
		t.Helper()
		_, result := nextResult()
		var region *semantic.Node
		for _, node := range semantic.Flatten(result.Tree) {
			if node.Name == name && (node.Role == "button" || node.Role == "link") {
				region = node
				break
			}
		}
		if region == nil {
			t.Fatalf("missing interaction region %q", name)
		}
		visible := region.Bounds.ImageRectangle().Intersect(region.Clip.ImageRectangle())
		point := image.Pt((visible.Min.X+visible.Max.X)/2, (visible.Min.Y+visible.Max.Y)/2)
		if !router.Press(pointerID, point) {
			t.Fatalf("press %q was rejected", name)
		}
		activation, ok := router.Release(pointerID, point)
		if !ok {
			t.Fatalf("release %q did not activate", name)
		}
		if err := runtime.Activate(activation); err != nil {
			t.Fatal(err)
		}
	}

	activate("annual-plan", 1)
	if got := runtime.Snapshot().StateValues["screen:main"]["plan"]; got != "annual" {
		t.Fatalf("annual plan = %#v", got)
	}
	activate("monthly-plan", 2)
	if got := runtime.Snapshot().StateValues["screen:main"]["plan"]; got != "monthly" {
		t.Fatalf("monthly plan = %#v", got)
	}
	activate("increment", 3)
	activate("decrement", 4)
	if got := runtime.Snapshot().StateValues["screen:main/team-seats"]["count"]; got != float64(3) {
		t.Fatalf("stepper count = %#v", got)
	}
	activate("toggle-details", 5)
	activate("toggle-details", 6)
	if got := runtime.Snapshot().StateValues["screen:main"]["details"]; got != false {
		t.Fatalf("details = %#v", got)
	}
}

func TestRuntimeOwnsStateAcrossActivationReloadAndReset(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "app.gora")
	source := []byte(`
gora: 1
kind: app
viewport: { width: 200, height: 100 }
state:
  count: { type: number, default: 1 }
entry: main
screens:
  main:
    type: text
    props: { content: { ref: state.count } }
`)
	if err := os.WriteFile(path, source, 0o600); err != nil {
		t.Fatal(err)
	}
	runtime, err := NewRuntime(dir, path)
	if err != nil {
		t.Fatal(err)
	}
	if got := runtime.Snapshot().Root.Props["content"]; got != "1" {
		t.Fatalf("initial content = %#v", got)
	}
	if err := runtime.Activate(interaction.Activation{Scope: "screen:main", Actions: []document.Action{{Action: "increment", State: "count", By: float64(2)}}}); err != nil {
		t.Fatal(err)
	}
	if got := runtime.Snapshot().Root.Props["content"]; got != "3" {
		t.Fatalf("mutated content = %#v", got)
	}
	persistentRoot := runtime.Snapshot().Root
	runtime.SetTransient(interaction.Transient{Focused: "button"})
	if transientRoot := runtime.Snapshot().Root; transientRoot != persistentRoot {
		t.Fatal("transient interaction rebuilt persistent geometry")
	}
	runtime.Reload()
	if got := runtime.Snapshot().Root.Props["content"]; got != "3" {
		t.Fatalf("reloaded content = %#v", got)
	}
	runtime.ResetState()
	snapshot := runtime.Snapshot()
	if got := snapshot.Root.Props["content"]; got != "1" || snapshot.Transient != (interaction.Transient{}) {
		t.Fatalf("reset snapshot = %#v", snapshot)
	}
}

func TestReloadPreservesLastGoodFrame(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "app.gora")
	valid := []byte(`
gora: 1
kind: app
viewport: { width: 320, height: 200 }
entry: main
screens:
  main:
    type: surface
    props: { background: "#112233" }
`)
	if err := os.WriteFile(path, valid, 0o600); err != nil {
		t.Fatal(err)
	}
	runtime, err := NewRuntime(dir, path)
	if err != nil {
		t.Fatal(err)
	}
	before := runtime.Snapshot()
	if before.Root == nil || before.Invalid {
		t.Fatalf("initial snapshot = %#v", before)
	}

	if err := os.WriteFile(path, []byte("gora: 1\nkind: app\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runtime.Reload()
	after := runtime.Snapshot()
	if after.Root != before.Root || !after.Invalid || len(after.Diagnostics) == 0 {
		t.Fatalf("last-good state not preserved: %#v", after)
	}
}

func TestNamedScrollPersistsAndUnnamedScrollResets(t *testing.T) {
	tests := []struct {
		name      string
		nodeName  string
		preserved bool
	}{
		{name: "named", nodeName: "feed", preserved: true},
		{name: "unnamed", preserved: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "app.gora")
			nameLine := ""
			if test.nodeName != "" {
				nameLine = "\n    name: " + test.nodeName
			}
			source := `gora: 1
kind: app
viewport: { width: 200, height: 100 }
entry: main
screens:
  main:
    type: scroll` + nameLine + `
    props: { axis: vertical }
    children:
      - type: spacer
        props: { height: 400 }
`
			if err := os.WriteFile(path, []byte(source), 0o600); err != nil {
				t.Fatal(err)
			}
			runtime, err := NewRuntime(dir, path)
			if err != nil {
				t.Fatal(err)
			}
			runtime.Scroll(30)
			if len(runtime.Snapshot().Scroll) != 1 {
				t.Fatal("scroll offset was not recorded")
			}
			runtime.Reload()
			got := len(runtime.Snapshot().Scroll) == 1
			if got != test.preserved {
				t.Fatalf("preserved=%v, want %v", got, test.preserved)
			}
		})
	}
}

func TestScrollAxisTargetsHorizontalScrollAlongsideVerticalScroll(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "app.gora")
	source := []byte(`
gora: 1
kind: app
viewport: { width: 200, height: 100 }
entry: main
screens:
  main:
    type: overlay
    children:
      - type: scroll
        name: feed
        props: { axis: vertical }
        children:
          - type: spacer
            props: { height: 400 }
      - type: scroll
        name: rail
        props: { axis: horizontal }
        children:
          - type: spacer
            props: { width: 400 }
`)
	if err := os.WriteFile(path, source, 0o600); err != nil {
		t.Fatal(err)
	}
	runtime, err := NewRuntime(dir, path)
	if err != nil {
		t.Fatal(err)
	}

	runtime.ScrollAxis("horizontal", 30)

	scroll := runtime.Snapshot().Scroll
	if got := scroll["rail"].X; got != 30 {
		t.Fatalf("horizontal offset = %d", got)
	}
	if _, ok := scroll["feed"]; ok {
		t.Fatal("horizontal gesture moved the vertical scroll node")
	}
}

func TestSetScrollOffsetSupportsDraggableScrollbar(t *testing.T) {
	runtime := &Runtime{scroll: make(map[string]image.Point)}
	runtime.SetScrollOffset("feed", "vertical", 95)
	runtime.SetScrollOffset("gallery", "horizontal", 42)
	snapshot := runtime.Snapshot()
	if got := snapshot.Scroll["feed"].Y; got != 95 {
		t.Fatalf("vertical offset = %d", got)
	}
	if got := snapshot.Scroll["gallery"].X; got != 42 {
		t.Fatalf("horizontal offset = %d", got)
	}
}

func TestCaptureUsesViewportScaleAndWarnsForLastGood(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "app.gora")
	valid := []byte(`
gora: 1
kind: app
viewport: { width: 20, height: 10 }
entry: main
screens:
  main: { type: surface, props: { background: "#112233" } }
`)
	if err := os.WriteFile(path, valid, 0o600); err != nil {
		t.Fatal(err)
	}
	runtime, err := NewRuntime(dir, path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("gora: 1\nkind: app\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runtime.Reload()
	output := filepath.Join(dir, "capture.png")
	warning, err := runtime.Capture(output, 2)
	if err != nil {
		t.Fatal(err)
	}
	if warning == "" {
		t.Fatal("missing last-good warning")
	}
	file, err := os.Open(output)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	captured, err := png.DecodeConfig(file)
	if err != nil {
		t.Fatal(err)
	}
	if captured.Width != 40 || captured.Height != 20 {
		t.Fatalf("capture size = %dx%d", captured.Width, captured.Height)
	}
}
