package mcpserver

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestMCPDispatchInputToolIsGatedAndDispatchesOrderedEvents(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "app.gora"), []byte(`gora: 1
kind: app
viewport: { width: 120, height: 80 }
state:
  count: { type: number, default: 0 }
entry: main
screens:
  main:
    type: button
    name: save
    props: { label: Save }
    on:
      activate:
        - action: increment
          state: count
    children: [{ type: text, props: { text: Save } }]
`), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, enabled := range []bool{false, true} {
		service := NewServiceWithOptions(NewRegistry(), ServiceOptions{Automation: enabled})
		ctx := context.Background()
		clientTransport, serverTransport := mcp.NewInMemoryTransports()
		if _, err := service.server.Connect(ctx, serverTransport, nil); err != nil {
			t.Fatal(err)
		}
		client := mcp.NewClient(&mcp.Implementation{Name: "dispatch-test", Version: "1"}, nil)
		session, err := client.Connect(ctx, clientTransport, nil)
		if err != nil {
			t.Fatal(err)
		}
		tools, err := session.ListTools(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		if hasTool(tools.Tools, "gora_dispatch_input") != enabled {
			t.Fatalf("automation=%v tools=%+v", enabled, tools.Tools)
		}
		if enabled {
			opened, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "gora_open_project", Arguments: map[string]any{"root": root}})
			if err != nil || opened.IsError {
				t.Fatalf("open project error=%v result=%+v", err, opened)
			}
			projectID := stringField(opened.StructuredContent, "project_id")
			openedView, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "gora_open_view", Arguments: map[string]any{"project_id": projectID, "file": "app.gora"}})
			if err != nil || openedView.IsError {
				t.Fatalf("open view error=%v result=%+v", err, openedView)
			}
			viewID := stringField(openedView.StructuredContent, "view_id")
			result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "gora_dispatch_input", Arguments: map[string]any{
				"project_id": projectID, "view_id": viewID, "wait": "published", "events": []any{
					map[string]any{"type": "pointer", "kind": "press", "pointer_id": 1, "source": "mouse", "x": 10, "y": 10, "button": "primary", "time_ms": 1},
					map[string]any{"type": "pointer", "kind": "release", "pointer_id": 1, "source": "mouse", "x": 10, "y": 10, "button": "primary", "time_ms": 2},
				},
			}})
			if err != nil || result.IsError {
				t.Fatalf("dispatch error=%v result=%+v", err, result)
			}
			var output struct {
				Results []struct {
					Index         int    `json:"index"`
					Consumed      bool   `json:"consumed"`
					TargetID      string `json:"target_id"`
					FrameRevision uint64 `json:"frame_revision"`
				} `json:"results"`
				Snapshot struct {
					StateValues map[string]map[string]any `json:"state_values"`
				} `json:"snapshot"`
			}
			data, err := json.Marshal(result.StructuredContent)
			if err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal(data, &output); err != nil {
				t.Fatal(err)
			}
			if len(output.Results) != 2 || output.Results[0].Index != 0 || output.Results[1].Index != 1 || output.Results[0].TargetID == "" || !output.Results[1].Consumed || output.Results[1].FrameRevision <= output.Results[0].FrameRevision {
				t.Fatalf("ordered event results = %+v", output.Results)
			}
			if got := output.Snapshot.StateValues["screen:main"]["count"]; got != float64(1) {
				t.Fatalf("dispatch did not commit ordered activation: state=%+v", output.Snapshot.StateValues)
			}
			invalid, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "gora_dispatch_input", Arguments: map[string]any{
				"project_id": projectID, "view_id": viewID, "wait": "none", "events": []any{
					map[string]any{"type": "pointer", "kind": "press", "pointer_id": 9, "source": "mouse", "x": 10, "y": 10, "button": "primary", "time_ms": 3},
					map[string]any{"type": "key", "kind": "down", "name": "NotAKey", "time_ms": 4},
				},
			}})
			if err != nil || !invalid.IsError {
				t.Fatalf("invalid batch error=%v result=%+v", err, invalid)
			}
			resource, err := session.ReadResource(ctx, &mcp.ReadResourceParams{URI: "gora://project/" + projectID + "/views/" + viewID + "/automation"})
			if err != nil || len(resource.Contents) != 1 {
				t.Fatalf("read automation after invalid batch error=%v result=%+v", err, resource)
			}
			var unchanged struct {
				StateValues map[string]map[string]any `json:"state_values"`
			}
			if err := json.Unmarshal([]byte(resource.Contents[0].Text), &unchanged); err != nil {
				t.Fatal(err)
			}
			if got := unchanged.StateValues["screen:main"]["count"]; got != float64(1) {
				t.Fatalf("invalid batch partially mutated state: %+v", unchanged.StateValues)
			}
		}
		_ = session.Close()
		service.Close()
	}
}

func TestMCPDispatchInputRejectsProjectViewAndTokenMismatchesAndSupportsWaitModes(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "app.gora"), []byte(`gora: 1
kind: app
viewport: { width: 120, height: 80 }
entry: main
screens:
  main:
    type: button
    name: action
    props: { label: Action }
    children: [{ type: text, props: { text: Action } }]
`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "theme.gora"), []byte("gora: 1\nkind: tokens\ntokens: {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	service := NewServiceWithOptions(NewRegistry(), ServiceOptions{Automation: true})
	defer service.Close()
	ctx := context.Background()
	clientTransport, serverTransport := mcp.NewInMemoryTransports()
	if _, err := service.server.Connect(ctx, serverTransport, nil); err != nil {
		t.Fatal(err)
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "dispatch-boundary-test", Version: "1"}, nil)
	session, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	opened, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "gora_open_project", Arguments: map[string]any{"root": root}})
	if err != nil || opened.IsError {
		t.Fatalf("open project error=%v result=%+v", err, opened)
	}
	projectID := stringField(opened.StructuredContent, "project_id")
	viewResult, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "gora_open_view", Arguments: map[string]any{"project_id": projectID, "file": "app.gora"}})
	if err != nil || viewResult.IsError {
		t.Fatalf("open app view error=%v result=%+v", err, viewResult)
	}
	viewID := stringField(viewResult.StructuredContent, "view_id")
	tokenResult, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "gora_open_view", Arguments: map[string]any{"project_id": projectID, "file": "theme.gora"}})
	if err != nil || tokenResult.IsError {
		t.Fatalf("open token view error=%v result=%+v", err, tokenResult)
	}
	tokenID := stringField(tokenResult.StructuredContent, "view_id")
	call := func(project, view, wait string, events []any) *mcp.CallToolResult {
		t.Helper()
		result, callErr := session.CallTool(ctx, &mcp.CallToolParams{Name: "gora_dispatch_input", Arguments: map[string]any{"project_id": project, "view_id": view, "wait": wait, "events": events}})
		if callErr != nil {
			t.Fatalf("dispatch call error=%v result=%+v", callErr, result)
		}
		return result
	}
	moveInside := func(kind string) []any {
		return []any{map[string]any{"type": "pointer", "kind": kind, "pointer_id": 1, "source": "mouse", "x": 10, "y": 10, "time_ms": 1}}
	}
	for _, wait := range []string{"none", "published", "idle"} {
		result := call(projectID, viewID, wait, moveInside("move"))
		if result.IsError {
			t.Fatalf("wait=%s rejected: %+v", wait, result)
		}
	}
	if result := call("missing-project", viewID, "none", moveInside("move")); !result.IsError {
		t.Fatal("mismatched project dispatch succeeded")
	}
	if result := call(projectID, "missing-view", "none", moveInside("move")); !result.IsError {
		t.Fatal("mismatched view dispatch succeeded")
	}
	if result := call(projectID, tokenID, "none", moveInside("move")); !result.IsError {
		t.Fatal("token dispatch succeeded")
	}
	if result := call(projectID, viewID, "none", []any{map[string]any{"type": "pointer", "kind": "release", "pointer_id": 99, "source": "mouse", "x": 10, "y": 10, "button": "primary", "time_ms": 2}}); !result.IsError {
		t.Fatal("invalid pointer sequence succeeded")
	}
	if result := call(projectID, viewID, "bogus", moveInside("move")); !result.IsError {
		t.Fatal("invalid wait mode succeeded")
	}
}
