package semantic

import (
	"image"
	"testing"

	"gora/internal/document"
	"gora/internal/project"
)

func TestBuildRetainsHiddenNodesAndExposesStableInteractiveSemantics(t *testing.T) {
	hidden := &project.Node{
		Handle: "hidden", Type: "button", Name: "hidden-button", Hidden: true, Scope: "screen:home",
		Props: map[string]any{"label": "Hidden"},
	}
	reports := &project.Node{
		Handle: "reports", Type: "link", Name: "reports-link", Scope: "screen:home",
		Props:      map[string]any{"label": "Reports", "to": "reports"},
		Source:     document.Source{File: "/project/nav.gora", Line: 12, Column: 5},
		Breadcrumb: []string{"sidebar"},
		On:         document.Events{Activate: []document.Action{{Action: "set", State: "open", Value: false}}},
	}
	current := &project.Node{
		Handle: "home", Type: "link", Name: "home-link", Scope: "screen:home",
		Props: map[string]any{"label": "Home", "to": "home"},
	}
	root := &project.Node{Handle: "root", Type: "stack", Name: "root", Children: []*project.Node{hidden, reports, current}}
	geometry := map[string]Geometry{
		"root":    {Bounds: image.Rect(0, 0, 300, 200), Clip: image.Rect(0, 0, 300, 200), PaintOrder: 0},
		"reports": {Bounds: image.Rect(0, 220, 120, 260), Clip: image.Rect(0, 0, 300, 200), PaintOrder: 1},
		"home":    {Bounds: image.Rect(0, 20, 120, 60), Clip: image.Rect(0, 0, 300, 200), PaintOrder: 2},
	}

	tree := Build(root, geometry, Context{
		Screen: "home", Values: map[string]map[string]any{"screen:home": {"open": true}},
		Hovered: "reports", Focused: "home",
	})
	if tree == nil || len(tree.Children) != 3 {
		t.Fatalf("tree = %+v", tree)
	}
	if tree.Children[0].Visible || tree.Children[0].Bounds != nil {
		t.Fatalf("hidden node = %+v", tree.Children[0])
	}
	gotReports := tree.Children[1]
	if gotReports.ID != "screen/home/component/sidebar/node/reports-link" || gotReports.Role != "link" || gotReports.Label != "Reports" || gotReports.Current || gotReports.InViewport {
		t.Fatalf("reports semantics = %+v", gotReports)
	}
	if gotReports.FocusOrder != 0 || !gotReports.Hovered || len(gotReports.Operations) != 2 {
		t.Fatalf("reports interaction = %+v", gotReports)
	}
	if len(gotReports.Effects) != 2 || gotReports.Effects[1].Action != "navigate" || gotReports.Effects[1].To != "reports" {
		t.Fatalf("reports effects = %+v", gotReports.Effects)
	}
	gotHome := tree.Children[2]
	if !gotHome.Current || !gotHome.InViewport || !gotHome.Focused || gotHome.FocusOrder != 1 {
		t.Fatalf("home semantics = %+v", gotHome)
	}
}

func TestInteractiveIDPercentEncodesContextBreadcrumbAndName(t *testing.T) {
	root := &project.Node{
		Handle: "save", Type: "button", Name: "save item", Scope: "screen:main",
		Breadcrumb: []string{"card/list"}, Props: map[string]any{"label": "Save"},
	}
	tree := Build(root, map[string]Geometry{"save": {Bounds: image.Rect(0, 0, 20, 20), Clip: image.Rect(0, 0, 20, 20)}}, Context{Screen: "main view"})
	if tree.ID != "screen/main%20view/component/card%2Flist/node/save%20item" {
		t.Fatalf("ID = %q", tree.ID)
	}
}

func TestBuildExposesSemanticControlRolesValuesAndCompositeFocus(t *testing.T) {
	minimum, maximum, step := 0.0, 100.0, 5.0
	root := &project.Node{Handle: "root", Type: "stack", Children: []*project.Node{
		{Handle: "toggle", Type: "toggle", Name: "alerts-toggle", Scope: "screen:main", Binding: "alerts", Props: map[string]any{"label": "Alerts", "checked": true, "value": true}},
		{Handle: "group", Type: "radio_group", Name: "plan-group", Scope: "screen:main", Binding: "plan", Props: map[string]any{"label": "Plan", "value": "annual"}, Children: []*project.Node{
			{Handle: "monthly", Type: "radio", Name: "monthly-radio", Scope: "screen:main", Binding: "plan", Props: map[string]any{"label": "Monthly", "value": "monthly", "selected": false}},
			{Handle: "annual", Type: "radio", Name: "annual-radio", Scope: "screen:main", Binding: "plan", Props: map[string]any{"label": "Annual", "value": "annual", "selected": true}},
		}},
		{Handle: "slider", Type: "slider", Name: "volume-slider", Scope: "screen:main", Binding: "volume", BindingState: &document.StateDeclaration{Type: "number", Min: &minimum, Max: &maximum, Step: &step}, Props: map[string]any{"label": "Volume", "value": float64(40), "orientation": "horizontal"}},
	}}
	geometry := map[string]Geometry{}
	for index, handle := range []string{"root", "toggle", "group", "monthly", "annual", "slider"} {
		geometry[handle] = Geometry{Bounds: image.Rect(0, index*20, 100, index*20+18), Clip: image.Rect(0, 0, 200, 200), PaintOrder: index}
	}
	tree := Build(root, geometry, Context{Screen: "main"})
	flat := Flatten(tree)
	byHandle := map[string]*Node{}
	for _, node := range flat {
		byHandle[node.Handle] = node
	}
	if got := byHandle["toggle"]; got.Role != "switch" || got.Checked == nil || !*got.Checked || got.FocusOrder != 0 {
		t.Fatalf("toggle semantics = %+v", got)
	} else if len(got.Actions) != 1 || got.Actions[0].Action != "toggle" || got.Actions[0].State != "alerts" {
		t.Fatalf("toggle actions = %+v", got.Actions)
	}
	if got := byHandle["group"]; got.Role != "radiogroup" || got.Value != "annual" || got.FocusOrder != -1 {
		t.Fatalf("group semantics = %+v", got)
	}
	if got := byHandle["annual"]; got.Role != "radio" || got.Selected == nil || !*got.Selected || got.FocusOrder != 1 {
		t.Fatalf("annual semantics = %+v", got)
	} else if len(got.Actions) != 1 || got.Actions[0].Action != "set" || got.Actions[0].State != "plan" || got.Actions[0].Value != "annual" {
		t.Fatalf("annual actions = %+v", got.Actions)
	}
	if got := byHandle["monthly"]; got.FocusOrder != -1 {
		t.Fatalf("monthly focus = %+v", got)
	}
	if got := byHandle["slider"]; got.Role != "slider" || got.Value != float64(40) || got.Min == nil || *got.Min != 0 || got.Max == nil || *got.Max != 100 || got.Step == nil || *got.Step != 5 || got.FocusOrder != 2 {
		t.Fatalf("slider semantics = %+v", got)
	}
}
