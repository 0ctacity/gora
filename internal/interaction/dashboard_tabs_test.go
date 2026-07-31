package interaction

import (
	"image"
	"path/filepath"
	"testing"

	"gora/internal/project"
	"gora/internal/render"
)

func TestNorthstarSidebarButtonsSwitchDashboardPanels(t *testing.T) {
	repositoryRoot, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	loaded, diagnostics := project.Load(repositoryRoot, filepath.Join(repositoryRoot, "examples", "dashboard", "app.gora"), 1280)
	if len(diagnostics) != 0 {
		t.Fatalf("load diagnostics: %+v", diagnostics)
	}
	store := NewStore()
	specs := make([]ScopeSpec, len(loaded.StateScopes))
	for index, scope := range loaded.StateScopes {
		specs[index] = ScopeSpec{ID: scope.ID, Context: scope.Context, State: scope.State, Initial: scope.Initial}
	}
	store.Reconcile(specs)
	initialRoot := ResolvePersistentTree(loaded.Screens[loaded.Selected], store.AllValues())
	if findNamedNode(initialRoot, "overview-panel") == nil {
		t.Fatal("dashboard did not start on the overview tab")
	}

	tabs := []struct {
		button string
		panel  string
	}{
		{button: "overview-tab", panel: "overview-panel"},
		{button: "revenue-tab", panel: "revenue-panel"},
		{button: "customers-tab", panel: "customers-panel"},
		{button: "reports-tab", panel: "reports-panel"},
	}
	for _, tab := range tabs {
		root := ResolvePersistentTree(loaded.Screens[loaded.Selected], store.AllValues())
		result := render.Render(root, image.Pt(1280, 800), render.State{Values: store.AllValues()})
		var target *render.InteractionRegion
		for index := range result.Interactions {
			if nodeName(root, result.Interactions[index].Handle) == tab.button {
				target = &result.Interactions[index]
				break
			}
		}
		if target == nil {
			t.Fatalf("missing sidebar button %q", tab.button)
		}
		if err := store.Apply(target.Scope, target.Actions); err != nil {
			t.Fatal(err)
		}
		root = ResolvePersistentTree(loaded.Screens[loaded.Selected], store.AllValues())
		if findNamedNode(root, tab.panel) == nil {
			t.Fatalf("activating %q did not show %q", tab.button, tab.panel)
		}
		for _, other := range tabs {
			if other.panel != tab.panel && findNamedNode(root, other.panel) != nil {
				t.Fatalf("activating %q left %q visible", tab.button, other.panel)
			}
		}
	}
}

func nodeName(root *project.Node, handle string) string {
	if root == nil {
		return ""
	}
	if root.Handle == handle {
		return root.Name
	}
	for _, child := range root.Children {
		if name := nodeName(child, handle); name != "" {
			return name
		}
	}
	return ""
}

func findNamedNode(root *project.Node, name string) *project.Node {
	if root == nil {
		return nil
	}
	if root.Name == name {
		return root
	}
	for _, child := range root.Children {
		if found := findNamedNode(child, name); found != nil {
			return found
		}
	}
	return nil
}
