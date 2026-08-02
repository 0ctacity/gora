package interaction

import (
	"image"
	"path/filepath"
	"testing"

	"gora/internal/project"
	"gora/internal/render"
	"gora/internal/semantic"
)

func TestNorthstarSidebarLinksExposeRealScreenNavigation(t *testing.T) {
	repositoryRoot, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	loaded, diagnostics := project.Load(repositoryRoot, filepath.Join(repositoryRoot, "examples", "dashboard", "app.gora"), 1280)
	if len(diagnostics) != 0 {
		t.Fatalf("load diagnostics: %+v", diagnostics)
	}
	if len(loaded.Screens) != 4 {
		t.Fatalf("screens = %v", loaded.Screens)
	}
	root := ResolvePersistentTree(loaded.Screens["overview"], nil)
	result := render.Render(root, image.Pt(1280, 800), render.State{Screen: "overview"})
	links := make(map[string]*semantic.Node)
	for _, node := range semantic.Flatten(result.Tree) {
		if node.Role == "link" {
			links[node.Name] = node
		}
	}
	store := NewStore()
	for _, target := range []string{"overview", "revenue", "customers", "reports"} {
		link := links[target+"-link"]
		if link == nil {
			t.Fatalf("missing %s link", target)
		}
		if link.Current != (target == "overview") {
			t.Fatalf("%s current = %t", target, link.Current)
		}
		navigation, err := store.ApplyActivation(link.Scope, link.Actions)
		if err != nil {
			t.Fatal(err)
		}
		if navigation == nil || navigation.Action != "navigate" || navigation.To != target {
			t.Fatalf("%s navigation = %+v", target, navigation)
		}
	}
}
