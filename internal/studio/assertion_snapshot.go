package studio

import (
	"image"
	"sort"

	"gora/internal/automation"
	"gora/internal/document"
	"gora/internal/interaction"
	"gora/internal/project"
	"gora/internal/render"
	"gora/internal/semantic"
)

// AutomationAssertionSnapshot joins the published semantic tree, runtime
// envelope, router/edit state, scroll metrics, and trace ring for one
// renderer-neutral assertion read. The returned values are copies or immutable
// published objects; evaluating assertions never calls back into Runtime.
func (runtime *Runtime) AutomationAssertionSnapshot() automation.AssertionSnapshot {
	runtime.mu.RLock()
	defer runtime.mu.RUnlock()
	tree := runtime.publishedTree
	metrics := cloneScrollMetrics(runtime.publishedScroll)
	offsets := cloneScroll(runtime.scroll)
	router := cloneRouterSnapshot(runtime.routerSnapshot)
	if !runtime.routerSnapshotSet && runtime.router != nil {
		router = cloneRouterSnapshot(runtime.router.Snapshot())
	}
	trace := runtime.trace.Snapshot()
	editing := interaction.EditingStoreSnapshot{Fields: map[string]interaction.FieldSnapshot{}}
	if runtime.editing != nil {
		editing = runtime.editing.Snapshot()
	}
	stateValues := map[string]map[string]any{}
	if runtime.state != nil {
		stateValues = cloneStateValues(runtime.state.AllValues())
	}
	visibleScopes := make(map[string]bool)
	if tree != nil {
		for _, node := range semantic.Flatten(tree) {
			if node != nil && node.Visible && node.Scope != "" {
				visibleScopes[node.Scope] = true
			}
		}
	}
	selections := []string{}
	canBack, canForward := false, false
	if runtime.loaded != nil {
		if runtime.loaded.Document.Kind == document.KindApp {
			for name := range runtime.loaded.Screens {
				selections = append(selections, name)
			}
		} else {
			selections = append(selections, runtime.loaded.Previews...)
		}
	}
	var sourceRoot *project.Node
	if runtime.loaded != nil {
		sourceRoot = runtime.loaded.Root
		if runtime.loaded.Document.Kind == document.KindApp {
			sourceRoot = runtime.loaded.Screens[runtime.selected]
		}
	}
	canonicalizeScrollAliases(tree, sourceRoot, offsets, metrics, runtime.scroll, runtime.publishedScroll)
	sort.Strings(selections)
	if runtime.navigation != nil {
		canBack, canForward = runtime.navigation.CanBack(), runtime.navigation.CanForward()
	}
	runtimePublished := tree != nil && runtime.publishedValid && !runtime.invalid && !runtime.candidateReload && runtime.publishedRuntimeRevision == runtime.runtimeRevision
	geometryPublished := runtimePublished && runtime.publishedGeometryRevision == runtime.geometryRevision
	idle := runtimePublished && geometryPublished && !runtime.closed && !runtime.candidateReload && router.QueueSizes.ValueChanges == 0 && router.QueueSizes.ScrollChanges == 0
	idleReasons := []string{}
	if runtime.closed {
		idleReasons = append(idleReasons, "closed")
	}
	if runtime.candidateReload {
		idleReasons = append(idleReasons, "candidate_reload")
	}
	if !runtimePublished || !geometryPublished {
		idleReasons = append(idleReasons, "unpublished_frame")
	}
	if runtime.invalid {
		idleReasons = append(idleReasons, "invalid_source")
	}
	if router.QueueSizes.ValueChanges != 0 || router.QueueSizes.ScrollChanges != 0 {
		idleReasons = append(idleReasons, "pending_automation_input")
	}
	clock := map[string]any{
		"mode":    runtime.clockMode,
		"time_ms": runtime.clockTimeMS,
	}
	if runtime.nextTimer != nil {
		clock["next_timer_ms"] = runtime.nextTimer.dueMS
	}
	return automation.AssertionSnapshot{
		Tree: tree,
		View: automation.ViewSnapshot{
			Tree:  tree,
			Valid: !runtime.invalid, LastGoodAvailable: tree != nil,
			Agreement: runtimePublished && geometryPublished, RuntimePublished: runtimePublished,
			GeometryPublished: geometryPublished, Idle: idle,
			IdleReasons: idleReasons, Selection: runtime.selected,
			Selections: selections, Viewport: runtime.viewport,
			CanBack: canBack, CanForward: canForward,
			RuntimeRevision: runtime.runtimeRevision, FrameRevision: runtime.frameRevision,
			GeometryRevision: runtime.geometryRevision, PublishedRuntimeRevision: runtime.publishedRuntimeRevision,
			PublishedGeometryRevision: runtime.publishedGeometryRevision, ReloadRevision: runtime.reloadRevision,
			AutomationInputRevision: runtime.automationInputRevision, Diagnostics: append([]document.Diagnostic(nil), runtime.diagnostics...),
			Transient: interaction.Transient{Focused: router.FocusedID, OpenSelect: router.OpenSelectID}, Router: router, Editing: editing,
			StateValues: stateValues, Scroll: metrics,
			VisibleScopes: visibleScopes,
			ScrollOffsets: offsets, Clock: clock, Trace: trace,
			Capture: automation.CaptureIdentity{Selection: runtime.selected, ViewportWidth: runtime.viewport.X, ViewportHeight: runtime.viewport.Y, RuntimeRevision: runtime.runtimeRevision, FrameRevision: runtime.frameRevision, GeometryRevision: runtime.geometryRevision, PublishedRuntimeRevision: runtime.publishedRuntimeRevision, PublishedGeometryRevision: runtime.publishedGeometryRevision, Width: runtime.viewport.X, Height: runtime.viewport.Y, Valid: tree != nil},
		},
		Router: router, Editing: editing, StateValues: stateValues,
		Scroll: metrics, ScrollOffsets: offsets, Trace: trace,
	}
}

func canonicalizeScrollAliases(tree *semantic.Node, sourceRoot *project.Node, offsets map[string]image.Point, metrics map[string]render.ScrollMetrics, rawOffsets map[string]image.Point, rawMetrics map[string]render.ScrollMetrics) {
	if tree == nil {
		return
	}
	// Runtime scroll maps intentionally use renderer/source handles while the
	// public assertion surface uses semantic IDs. Resolve both authored and
	// renderer-derived nodes through their canonical owner identity, then copy
	// the owner's value to every alias. Do not depend on the source tree being
	// present (last-good/reloaded trees can be semantic-only).
	_ = sourceRoot
	nodes := semantic.Flatten(tree)
	byHandle := make(map[string]*semantic.Node, len(nodes))
	for _, node := range nodes {
		if node != nil && node.Handle != "" {
			byHandle[node.Handle] = node
		}
	}
	for _, node := range semantic.Flatten(tree) {
		if node == nil {
			continue
		}
		owner := node
		if node.Group != "" {
			if candidate := byHandle[node.Group]; candidate != nil {
				owner = candidate
			}
		}
		if owner != nil && (owner.Role == "scrollbar" || owner.Role == "scrollbar_track" || owner.Role == "scrollbar_thumb" || owner.Role == "scrollbar_corner") && owner.Group != "" {
			if candidate := byHandle[owner.Group]; candidate != nil {
				owner = candidate
			}
		}
		candidates := []string{node.Name, owner.Name, semanticScrollKey(owner), owner.Handle, node.Handle, node.ID}
		var offset image.Point
		var ok bool
		for _, candidate := range candidates {
			if candidate != "" {
				if offset, ok = rawOffsets[candidate]; ok {
					break
				}
			}
		}
		if ok {
			offsets[node.ID] = offset
			if node.Handle != "" {
				offsets[node.Handle] = offset
			}
			if node.Group != "" {
				offsets[node.Group] = offset
			}
		}
		var metric render.ScrollMetrics
		for _, candidate := range []string{owner.Handle, owner.Name, semanticScrollKey(owner), node.Handle, node.ID} {
			if candidate != "" {
				if metric, ok = rawMetrics[candidate]; ok {
					break
				}
			}
		}
		if ok {
			metrics[node.ID] = metric
			if node.Handle != "" {
				metrics[node.Handle] = metric
			}
			if node.Group != "" {
				metrics[node.Group] = metric
			}
		}
	}
}
