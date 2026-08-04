package mcpserver

import (
	"context"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestMCPProjectAndViewLifecycleOverStreamableHTTP(t *testing.T) {
	root := t.TempDir()
	entry := filepath.Join(root, "app.gora")
	if err := os.WriteFile(entry, []byte(`gora: 1
kind: app
viewport: { width: 100, height: 80 }
state:
  volume: { type: number, default: 40, min: 0, max: 100, step: 5 }
entry: main
screens:
  main:
    type: slider
    name: volume-slider
    props: { label: Volume, bind: volume, width: 100, height: 40 }
    children:
      - { type: slider_track, props: { height: 8 } }
      - { type: slider_thumb, props: { width: 16, height: 16 } }
`), 0o600); err != nil {
		t.Fatal(err)
	}
	service := NewService(NewRegistry())
	defer service.Close()
	httpServer := httptest.NewServer(service.Handler())
	defer httpServer.Close()

	ctx := context.Background()
	client := mcp.NewClient(&mcp.Implementation{Name: "gora-test", Version: "1"}, nil)
	session, err := client.Connect(ctx, &mcp.StreamableClientTransport{Endpoint: httpServer.URL + "/mcp"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	if initialized := session.InitializeResult(); initialized == nil || initialized.ProtocolVersion != "2026-07-28" {
		t.Fatalf("protocol = %+v", initialized)
	}

	tools, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !hasTool(tools.Tools, "gora_open_project") || !hasTool(tools.Tools, "gora_open_view") || !hasTool(tools.Tools, "gora_capture") || !hasTool(tools.Tools, "gora_set_control_value") || !hasTool(tools.Tools, "gora_apply_document_changes") {
		t.Fatalf("tools = %+v", tools.Tools)
	}
	opened, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "gora_open_project", Arguments: map[string]any{"root": root}})
	if err != nil || opened.IsError {
		t.Fatalf("open project error=%v result=%+v", err, opened)
	}
	projectID := stringField(opened.StructuredContent, "project_id")
	if projectID == "" {
		t.Fatalf("open result = %#v", opened.StructuredContent)
	}
	view, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "gora_open_view", Arguments: map[string]any{"project_id": projectID, "file": "app.gora"}})
	if err != nil || view.IsError || stringField(view.StructuredContent, "view_id") == "" {
		t.Fatalf("open view error=%v result=%+v", err, view)
	}
	viewID := stringField(view.StructuredContent, "view_id")
	control, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "gora_set_control_value", Arguments: map[string]any{
		"project_id": projectID, "view_id": viewID, "semantic_id": "screen/main/node/volume-slider", "value": 43,
	}})
	if err != nil || control.IsError || numberField(control.StructuredContent, "value") != 45 {
		t.Fatalf("set control error=%v result=%+v", err, control)
	}
	resized, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "gora_set_viewport", Arguments: map[string]any{
		"project_id": projectID, "view_id": viewID, "width": 320, "height": 240,
	}})
	if err != nil || resized.IsError {
		t.Fatalf("resize error=%v result=%+v", err, resized)
	}
	treeURI := "gora://project/" + projectID + "/views/" + viewID + "/tree"
	tree, err := session.ReadResource(ctx, &mcp.ReadResourceParams{URI: treeURI})
	if err != nil || len(tree.Contents) != 1 || tree.Contents[0].Text == "" {
		t.Fatalf("read tree error=%v result=%+v", err, tree)
	}
	captured, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "gora_capture", Arguments: map[string]any{
		"project_id": projectID, "view_id": viewID, "scale": 1,
	}})
	if err != nil || captured.IsError || len(captured.Content) == 0 {
		t.Fatalf("capture error=%v result=%+v", err, captured)
	}
	sourceURI := "gora://project/" + projectID + "/sources/app.gora"
	source, err := session.ReadResource(ctx, &mcp.ReadResourceParams{URI: sourceURI})
	if err != nil || len(source.Contents) != 1 || source.Contents[0].Text == "" {
		t.Fatalf("read source error=%v result=%+v", err, source)
	}
	resources, err := session.ListResources(ctx, nil)
	if err != nil || !hasResource(resources.Resources, "gora://projects") {
		t.Fatalf("resources error=%v result=%+v", err, resources)
	}
	read, err := session.ReadResource(ctx, &mcp.ReadResourceParams{URI: "gora://projects"})
	if err != nil || len(read.Contents) != 1 || read.Contents[0].Text == "" {
		t.Fatalf("read projects error=%v result=%+v", err, read)
	}
}

func TestMCPFieldDraftSubmitAndResetTools(t *testing.T) {
	root := t.TempDir()
	entry := filepath.Join(root, "forms.gora")
	if err := os.WriteFile(entry, []byte(`gora: 1
kind: app
viewport: { width: 240, height: 180 }
state:
  name: { type: text, default: Ada }
  disabled_name: { type: text, default: Disabled }
  hidden_name: { type: text, default: Hidden }
  show_hidden: { type: boolean, default: false }
entry: main
screens:
  main:
    type: form
    name: profile-form
    children:
      - type: stack
        props: { direction: vertical }
        children:
          - type: text_field
            name: name-field
            props: { label: Name, bind: name, required: true }
            children:
              - { type: field_box, props: { width: 200, height: 40 } }
          - type: text_field
            name: disabled-field
            props: { label: Disabled, bind: disabled_name, disabled: true }
            children:
              - { type: field_box, props: { width: 200, height: 40 } }
          - type: text_field
            name: hidden-field
            props: { label: Hidden, bind: hidden_name }
            variants:
              - when: { state: show_hidden, equals: false }
                visible: false
            children:
              - { type: field_box, props: { width: 200, height: 40 } }
`), 0o600); err != nil {
		t.Fatal(err)
	}
	service := NewService(NewRegistry())
	defer service.Close()
	httpServer := httptest.NewServer(service.Handler())
	defer httpServer.Close()
	ctx := context.Background()
	client := mcp.NewClient(&mcp.Implementation{Name: "gora-test", Version: "1"}, nil)
	session, err := client.Connect(ctx, &mcp.StreamableClientTransport{Endpoint: httpServer.URL + "/mcp"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	tools, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"gora_set_field_draft", "gora_submit_form", "gora_reset_form"} {
		if !hasTool(tools.Tools, name) {
			t.Fatalf("missing tool %s", name)
		}
	}
	opened, _ := session.CallTool(ctx, &mcp.CallToolParams{Name: "gora_open_project", Arguments: map[string]any{"root": root}})
	projectID := stringField(opened.StructuredContent, "project_id")
	view, _ := session.CallTool(ctx, &mcp.CallToolParams{Name: "gora_open_view", Arguments: map[string]any{"project_id": projectID, "file": "forms.gora"}})
	viewID := stringField(view.StructuredContent, "view_id")
	base := map[string]any{"project_id": projectID, "view_id": viewID}
	for _, test := range []struct {
		name      string
		projectID string
		viewID    string
		fieldID   string
	}{
		{name: "stale semantic ID", projectID: projectID, viewID: viewID, fieldID: "screen/main/node/missing-field"},
		{name: "disabled field", projectID: projectID, viewID: viewID, fieldID: "screen/main/node/disabled-field"},
		{name: "hidden field", projectID: projectID, viewID: viewID, fieldID: "screen/main/node/hidden-field"},
		{name: "project/view mismatch", projectID: "missing-project", viewID: viewID, fieldID: "screen/main/node/name-field"},
	} {
		t.Run(test.name, func(t *testing.T) {
			result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "gora_set_field_draft", Arguments: map[string]any{
				"project_id": test.projectID, "view_id": test.viewID, "semantic_id": test.fieldID, "draft": "Changed",
			}})
			if err != nil || !result.IsError {
				t.Fatalf("rejected draft error=%v result=%+v", err, result)
			}
		})
	}
	draft := mapsClone(base)
	draft["semantic_id"] = "screen/main/node/name-field"
	draft["draft"] = ""
	result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "gora_set_field_draft", Arguments: draft})
	if err != nil || result.IsError {
		t.Fatalf("invalid draft should remain an ordinary tool result: error=%v result=%+v", err, result)
	}
	if boolField(result.StructuredContent, "valid") || stringField(result.StructuredContent, "draft") != "" || stringField(result.StructuredContent, "value") != "Ada" {
		t.Fatalf("invalid draft output = %+v", result.StructuredContent)
	}
	submit := mapsClone(base)
	submit["semantic_id"] = "screen/main/node/profile-form"
	result, err = session.CallTool(ctx, &mcp.CallToolParams{Name: "gora_submit_form", Arguments: submit})
	if err != nil || !result.IsError {
		t.Fatalf("invalid form submit error=%v result=%+v", err, result)
	}

	draft["draft"] = "Grace"
	result, err = session.CallTool(ctx, &mcp.CallToolParams{Name: "gora_set_field_draft", Arguments: draft})
	if err != nil || result.IsError {
		t.Fatalf("set draft error=%v result=%+v", err, result)
	}
	if got := stringField(result.StructuredContent, "draft"); got != "Grace" {
		t.Fatalf("draft output = %q, want Grace", got)
	}
	if valid := boolField(result.StructuredContent, "valid"); !valid {
		t.Fatalf("draft output should be valid: %+v", result.StructuredContent)
	}
	if got := stringField(result.StructuredContent, "value"); got != "Grace" {
		t.Fatalf("valid draft published value = %q, want Grace", got)
	}
	result, err = session.CallTool(ctx, &mcp.CallToolParams{Name: "gora_set_control_value", Arguments: map[string]any{
		"project_id": projectID, "view_id": viewID, "semantic_id": "screen/main/node/name-field", "value": "",
	}})
	if err != nil || !result.IsError {
		t.Fatalf("invalid typed field value error=%v result=%+v", err, result)
	}
	result, err = session.CallTool(ctx, &mcp.CallToolParams{Name: "gora_submit_form", Arguments: submit})
	if err != nil || result.IsError {
		t.Fatalf("submit error=%v result=%+v", err, result)
	}
	result, err = session.CallTool(ctx, &mcp.CallToolParams{Name: "gora_reset_form", Arguments: submit})
	if err != nil || result.IsError {
		t.Fatalf("reset error=%v result=%+v", err, result)
	}
}

func mapsClone(source map[string]any) map[string]any {
	result := make(map[string]any, len(source))
	for key, value := range source {
		result[key] = value
	}
	return result
}

func hasTool(tools []*mcp.Tool, name string) bool {
	for _, tool := range tools {
		if tool.Name == name {
			return true
		}
	}
	return false
}

func hasResource(resources []*mcp.Resource, uri string) bool {
	for _, resource := range resources {
		if resource.URI == uri {
			return true
		}
	}
	return false
}

func stringField(value any, name string) string {
	mapping, _ := value.(map[string]any)
	result, _ := mapping[name].(string)
	return result
}

func numberField(value any, name string) float64 {
	mapping, _ := value.(map[string]any)
	result, _ := mapping[name].(float64)
	return result
}

func boolField(value any, name string) bool {
	mapping, _ := value.(map[string]any)
	result, _ := mapping[name].(bool)
	return result
}
