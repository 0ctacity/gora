package mcpserver

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"gora/internal/semantic"
	"gora/internal/studio"
)

func TestProjectWatcherReloadsAnAffectedView(t *testing.T) {
	root := t.TempDir()
	entry := filepath.Join(root, "app.gora")
	write := func(width int) {
		t.Helper()
		source := fmt.Sprintf("gora: 1\nkind: app\nviewport: { width: %d, height: 80 }\nentry: main\nscreens:\n  main: { type: spacer }\n", width)
		if err := os.WriteFile(entry, []byte(source), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write(100)
	registry := NewRegistry()
	defer registry.Close()
	project, err := registry.OpenProject(root)
	if err != nil {
		t.Fatal(err)
	}
	view, err := registry.OpenView(project.ID, "app.gora")
	if err != nil {
		t.Fatal(err)
	}
	write(220)
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		current, summaryErr := registry.ViewSummary(project.ID, view.ID)
		if summaryErr == nil && current.Viewport.Width == 220 {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	current, _ := registry.ViewSummary(project.ID, view.ID)
	t.Fatalf("watcher did not reload view: %+v", current)
}

func TestProjectWatcherReloadsOnlyAffectedViewsAndKeepsWatchSetBounded(t *testing.T) {
	root := t.TempDir()
	shared := filepath.Join(root, "shared.gora")
	affectedEntry := filepath.Join(root, "affected.gora")
	unrelatedEntry := filepath.Join(root, "unrelated.gora")
	writeShared := func(width int) {
		t.Helper()
		source := fmt.Sprintf(`gora: 1
kind: component
viewport: { width: 100, height: 80 }
previews:
  default: {}
root:
  type: surface
  name: shared-root
  props: { width: %d, height: 20, background: "#112233" }
`, width)
		if err := os.WriteFile(shared, []byte(source), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(affectedEntry, []byte(`gora: 1
kind: app
imports:
  components:
    shared: ./shared.gora
viewport: { width: 160, height: 80 }
entry: main
screens:
  main:
    type: instance
    name: shared-instance
    props: { component: shared }
`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(unrelatedEntry, []byte(`gora: 1
kind: app
viewport: { width: 160, height: 80 }
entry: main
screens:
  main: { type: surface, props: { width: 48, height: 24, background: "#445566" } }
`), 0o600); err != nil {
		t.Fatal(err)
	}
	writeShared(40)

	registry := NewRegistry()
	defer registry.Close()
	project, err := registry.OpenProject(root)
	if err != nil {
		t.Fatal(err)
	}
	affected, err := registry.OpenView(project.ID, "affected.gora")
	if err != nil {
		t.Fatal(err)
	}
	unrelated, err := registry.OpenView(project.ID, "unrelated.gora")
	if err != nil {
		t.Fatal(err)
	}
	affectedRuntime, err := registry.Runtime(project.ID, affected.ID)
	if err != nil {
		t.Fatal(err)
	}
	unrelatedRuntime, err := registry.Runtime(project.ID, unrelated.ID)
	if err != nil {
		t.Fatal(err)
	}
	_, err = affectedRuntime.RuntimeTree()
	if err != nil {
		t.Fatalf("affected initial tree: %v diagnostics=%+v", err, affectedRuntime.Snapshot().Diagnostics)
	}
	unrelatedTree, err := unrelatedRuntime.RuntimeTree()
	if err != nil {
		t.Fatalf("unrelated initial tree: %v diagnostics=%+v", err, unrelatedRuntime.Snapshot().Diagnostics)
	}
	affectedBefore := affectedRuntime.Snapshot()
	unrelatedBefore := unrelatedRuntime.Snapshot()
	projectBefore := registry.ListProjects()[0].Revision
	projectRef, err := registry.project(project.ID)
	if err != nil {
		t.Fatal(err)
	}
	projectRef.mu.RLock()
	knownBefore := len(projectRef.sources)
	projectRef.mu.RUnlock()
	projectRef.watch.mu.Lock()
	watchedBefore := len(projectRef.watch.files)
	projectRef.watch.mu.Unlock()

	// A burst should coalesce to the latest source while keeping one known
	// dependency/watch entry per path.
	for width := 60; width < 72; width++ {
		writeShared(width)
	}
	hasSharedWidth := func(tree *semantic.Node, width int) bool {
		for _, node := range semantic.Flatten(tree) {
			if node.Name == "shared-root" {
				return node.Bounds != nil && node.Bounds.Width == width
			}
		}
		return false
	}
	deadline := time.Now().Add(4 * time.Second)
	var currentAffected *semantic.Node
	for time.Now().Before(deadline) {
		candidate, treeErr := affectedRuntime.RuntimeTree()
		if treeErr == nil {
			currentAffected = candidate
			if hasSharedWidth(candidate, 71) {
				break
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	if currentAffected == nil || !hasSharedWidth(currentAffected, 71) {
		t.Fatal("affected view did not publish the final coalesced shared width 71")
	}
	// The watcher debounces file events for 120ms. Wait one full debounce
	// window after the final candidate before asserting exactly one install.
	time.Sleep(250 * time.Millisecond)
	affectedAfter := affectedRuntime.Snapshot()
	if affectedAfter.RuntimeRevision <= affectedBefore.RuntimeRevision {
		t.Fatalf("affected runtime revision = %d, want > %d", affectedAfter.RuntimeRevision, affectedBefore.RuntimeRevision)
	}
	projectAfter := registry.ListProjects()[0].Revision
	if projectAfter != projectBefore+1 {
		t.Fatalf("project revision = %d, want exactly %d for one coalesced candidate", projectAfter, projectBefore+1)
	}
	unchangedTree, err := unrelatedRuntime.RuntimeTree()
	if err != nil {
		t.Fatal(err)
	}
	unrelatedAfter := unrelatedRuntime.Snapshot()
	if unrelatedAfter.RuntimeRevision != unrelatedBefore.RuntimeRevision || unrelatedAfter.Viewport != unrelatedBefore.Viewport || !reflect.DeepEqual(unrelatedAfter.Scroll, unrelatedBefore.Scroll) || !reflect.DeepEqual(unrelatedAfter.StateValues, unrelatedBefore.StateValues) || !reflect.DeepEqual(unchangedTree, unrelatedTree) {
		t.Fatalf("unrelated view changed after shared dependency edit: before revision/tree=%d/%+v after=%d/%+v", unrelatedBefore.RuntimeRevision, unrelatedTree, unrelatedAfter.RuntimeRevision, unchangedTree)
	}
	projectRef.mu.RLock()
	knownAfter := len(projectRef.sources)
	projectRef.mu.RUnlock()
	projectRef.watch.mu.Lock()
	watchedAfter, pendingAfter := len(projectRef.watch.files), len(projectRef.watch.pending)
	projectRef.watch.mu.Unlock()
	if knownAfter != knownBefore || watchedAfter != watchedBefore || pendingAfter != 0 {
		t.Fatalf("watch set grew or retained pending events: known %d->%d watched %d->%d pending=%d", knownBefore, knownAfter, watchedBefore, watchedAfter, pendingAfter)
	}
}

func TestRegistryCloseCompletesProjectWatcherAndReleasesViews(t *testing.T) {
	root := t.TempDir()
	entry := filepath.Join(root, "app.gora")
	if err := os.WriteFile(entry, []byte("gora: 1\nkind: app\nviewport: { width: 100, height: 80 }\nentry: main\nscreens:\n  main: { type: spacer }\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	registry := NewRegistry()
	project, err := registry.OpenProject(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := registry.OpenView(project.ID, "app.gora"); err != nil {
		t.Fatal(err)
	}
	projectRef, err := registry.project(project.ID)
	if err != nil {
		t.Fatal(err)
	}
	done := projectRef.watch.done
	registry.Close()
	select {
	case <-done:
	default:
		t.Fatal("project watcher did not finish during registry shutdown")
	}
	if projects := registry.ListProjects(); len(projects) != 0 {
		t.Fatalf("projects retained after close: %+v", projects)
	}
	projectRef.mu.RLock()
	views := len(projectRef.views)
	projectRef.mu.RUnlock()
	if views != 0 {
		t.Fatalf("views retained after project close: %d", views)
	}
}

func TestRegistryReusesCanonicalProjectsAndViews(t *testing.T) {
	root := t.TempDir()
	entry := filepath.Join(root, "app.gora")
	if err := os.WriteFile(entry, []byte("gora: 1\nkind: app\nviewport: { width: 100, height: 80 }\nentry: main\nscreens:\n  main: { type: spacer }\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	registry := NewRegistry()
	defer registry.Close()

	first, err := registry.OpenProject(root)
	if err != nil {
		t.Fatal(err)
	}
	second, err := registry.OpenProject(filepath.Join(root, "."))
	if err != nil {
		t.Fatal(err)
	}
	if first.ID == "" || first.ID != second.ID || len(registry.ListProjects()) != 1 {
		t.Fatalf("projects: first=%+v second=%+v all=%+v", first, second, registry.ListProjects())
	}

	view1, err := registry.OpenView(first.ID, "app.gora")
	if err != nil {
		t.Fatal(err)
	}
	view2, err := registry.OpenView(first.ID, entry)
	if err != nil {
		t.Fatal(err)
	}
	if view1.ID == "" || view1.ID != view2.ID || !view1.Valid || len(registry.ListViews(first.ID)) != 1 {
		t.Fatalf("views: first=%+v second=%+v", view1, view2)
	}
}

func TestRegistryOpensInvalidAndTokenDocuments(t *testing.T) {
	root := t.TempDir()
	invalid := filepath.Join(root, "bad.gora")
	tokens := filepath.Join(root, "theme.gora")
	if err := os.WriteFile(invalid, []byte("gora: 1\nkind: app\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(tokens, []byte("gora: 1\nkind: tokens\ntokens: {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	registry := NewRegistry()
	defer registry.Close()
	project, err := registry.OpenProject(root)
	if err != nil {
		t.Fatal(err)
	}
	bad, err := registry.OpenView(project.ID, "bad.gora")
	if err != nil {
		t.Fatal(err)
	}
	if bad.Valid || len(bad.Diagnostics) == 0 {
		t.Fatalf("invalid view = %+v", bad)
	}
	theme, err := registry.OpenView(project.ID, "theme.gora")
	if err != nil {
		t.Fatal(err)
	}
	if !theme.Valid || theme.Kind != "tokens" || theme.RuntimeAvailable {
		t.Fatalf("token view = %+v", theme)
	}
}

func TestRegistryRejectsViewOutsideProject(t *testing.T) {
	registry := NewRegistry()
	defer registry.Close()
	project, err := registry.OpenProject(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "app.gora")
	if err := os.WriteFile(outside, []byte("gora: 1\nkind: tokens\ntokens: {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := registry.OpenView(project.ID, outside); err == nil {
		t.Fatal("outside view was accepted")
	}
}

func TestRegistryOpenViewPublishesInitialValidFrameForAutomation(t *testing.T) {
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
	view, err := registry.OpenView(project.ID, "app.gora")
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := registry.Runtime(project.ID, view.ID)
	if err != nil {
		t.Fatal(err)
	}
	snapshot := runtime.AutomationSnapshot()
	if snapshot.FrameRevision == 0 || !snapshot.Agreement || !snapshot.Idle {
		t.Fatalf("initial automation publication = %+v", snapshot)
	}
}

func TestProjectReloadPublishesMatchingAutomationFrame(t *testing.T) {
	root := t.TempDir()
	entry := filepath.Join(root, "app.gora")
	write := func(width int) {
		t.Helper()
		source := fmt.Sprintf("gora: 1\nkind: app\nviewport: { width: %d, height: 80 }\nentry: main\nscreens:\n  main: { type: spacer }\n", width)
		if err := os.WriteFile(entry, []byte(source), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write(100)
	registry := NewRegistry()
	defer registry.Close()
	project, err := registry.OpenProject(root)
	if err != nil {
		t.Fatal(err)
	}
	view, err := registry.OpenView(project.ID, "app.gora")
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := registry.Runtime(project.ID, view.ID)
	if err != nil {
		t.Fatal(err)
	}
	initial := runtime.AutomationSnapshot()
	write(140)
	projectRef, err := registry.project(project.ID)
	if err != nil {
		t.Fatal(err)
	}
	projectRef.mu.Lock()
	projectRef.reloadAffectedViewsLocked(map[string]bool{projectRef.views[view.ID].entry: true})
	projectRef.mu.Unlock()
	updated := runtime.AutomationSnapshot()
	if updated.FrameRevision != initial.FrameRevision+1 || !updated.Agreement || updated.Viewport.X != 140 {
		t.Fatalf("reloaded automation frame = %+v initial=%+v", updated, initial)
	}
}

func TestProjectReloadInvalidRetainsLastGoodAutomationFrame(t *testing.T) {
	root := t.TempDir()
	entry := filepath.Join(root, "app.gora")
	valid := []byte("gora: 1\nkind: app\nviewport: { width: 100, height: 80 }\nentry: main\nscreens:\n  main: { type: spacer }\n")
	if err := os.WriteFile(entry, valid, 0o600); err != nil {
		t.Fatal(err)
	}
	registry := NewRegistry()
	defer registry.Close()
	project, err := registry.OpenProject(root)
	if err != nil {
		t.Fatal(err)
	}
	view, err := registry.OpenView(project.ID, "app.gora")
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := registry.Runtime(project.ID, view.ID)
	if err != nil {
		t.Fatal(err)
	}
	initial := runtime.AutomationSnapshot()
	if err := os.WriteFile(entry, []byte("gora: 1\nkind: app\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	projectRef, err := registry.project(project.ID)
	if err != nil {
		t.Fatal(err)
	}
	projectRef.mu.Lock()
	projectRef.reloadAffectedViewsLocked(map[string]bool{projectRef.views[view.ID].entry: true})
	projectRef.mu.Unlock()
	invalid := runtime.AutomationSnapshot()
	if invalid.FrameRevision != initial.FrameRevision || invalid.Agreement || invalid.Valid || !invalid.LastGoodAvailable || len(invalid.Diagnostics) == 0 {
		t.Fatalf("invalid reload automation snapshot = %+v initial=%+v", invalid, initial)
	}
}

func TestRegistryCloseWakesAutomationWaiters(t *testing.T) {
	root := t.TempDir()
	entry := filepath.Join(root, "app.gora")
	if err := os.WriteFile(entry, []byte("gora: 1\nkind: app\nviewport: { width: 100, height: 80 }\nentry: main\nscreens:\n  main: { type: spacer }\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	registry := NewRegistry()
	project, err := registry.OpenProject(root)
	if err != nil {
		t.Fatal(err)
	}
	view, err := registry.OpenView(project.ID, "app.gora")
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := registry.Runtime(project.ID, view.ID)
	if err != nil {
		t.Fatal(err)
	}
	initial := runtime.AutomationSnapshot()
	done := make(chan error, 1)
	go func() {
		_, waitErr := runtime.WaitForView(context.Background(), studio.WaitForViewRequest{AfterFrameRevision: initial.FrameRevision, AfterFrameSet: true, Timeout: time.Second})
		done <- waitErr
	}()
	registry.Close()
	select {
	case waitErr := <-done:
		if !errors.Is(waitErr, studio.ErrRuntimeClosed) {
			t.Fatalf("registry close wait error = %v", waitErr)
		}
	case <-time.After(time.Second):
		t.Fatal("registry close did not wake waiter")
	}
}

func TestRegistryProjectAndViewCloseWakeAutomationWaiters(t *testing.T) {
	for _, mode := range []string{"view", "project"} {
		t.Run(mode, func(t *testing.T) {
			root := t.TempDir()
			entry := filepath.Join(root, "app.gora")
			if err := os.WriteFile(entry, []byte("gora: 1\nkind: app\nviewport: { width: 100, height: 80 }\nentry: main\nscreens:\n  main: { type: spacer }\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			registry := NewRegistry()
			project, err := registry.OpenProject(root)
			if err != nil {
				t.Fatal(err)
			}
			view, err := registry.OpenView(project.ID, "app.gora")
			if err != nil {
				t.Fatal(err)
			}
			runtime, err := registry.Runtime(project.ID, view.ID)
			if err != nil {
				t.Fatal(err)
			}
			initial := runtime.AutomationSnapshot()
			done := make(chan error, 1)
			go func() {
				_, waitErr := runtime.WaitForView(context.Background(), studio.WaitForViewRequest{AfterFrameRevision: initial.FrameRevision, AfterFrameSet: true, Timeout: time.Second})
				done <- waitErr
			}()
			if mode == "view" {
				err = registry.CloseView(project.ID, view.ID)
			} else {
				err = registry.CloseProject(project.ID)
			}
			if err != nil {
				t.Fatal(err)
			}
			select {
			case waitErr := <-done:
				if !errors.Is(waitErr, studio.ErrRuntimeClosed) {
					t.Fatalf("%s close wait error = %v", mode, waitErr)
				}
			case <-time.After(time.Second):
				t.Fatalf("%s close did not wake waiter", mode)
			}
			registry.Close()
		})
	}
}
