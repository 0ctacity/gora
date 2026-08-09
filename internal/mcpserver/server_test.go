package mcpserver

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"gora/internal/automation"
	"gora/internal/studio"
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

func TestMCPScrollTransportPublishesOneAxisMutation(t *testing.T) {
	root := t.TempDir()
	entry := filepath.Join(root, "app.gora")
	if err := os.WriteFile(entry, []byte(`gora: 1
kind: app
viewport: { width: 100, height: 80 }
entry: main
screens:
  main:
    type: scroll
    name: feed
    props: { axis: vertical }
    children: [{ type: spacer, props: { height: 240 } }]
`), 0o600); err != nil {
		t.Fatal(err)
	}
	service := NewService(NewRegistry())
	defer service.Close()
	httpServer := httptest.NewServer(service.Handler())
	defer httpServer.Close()
	ctx := context.Background()
	client := mcp.NewClient(&mcp.Implementation{Name: "gora-scroll-test", Version: "1"}, nil)
	session, err := client.Connect(ctx, &mcp.StreamableClientTransport{Endpoint: httpServer.URL + "/mcp"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	tools, err := session.ListTools(ctx, nil)
	if err != nil || !hasTool(tools.Tools, "gora_scroll") {
		t.Fatalf("scroll tool unavailable: err=%v tools=%+v", err, tools)
	}
	opened, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "gora_open_project", Arguments: map[string]any{"root": root}})
	if err != nil || opened.IsError {
		t.Fatalf("open project error=%v result=%+v", err, opened)
	}
	projectID := stringField(opened.StructuredContent, "project_id")
	view, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "gora_open_view", Arguments: map[string]any{"project_id": projectID, "file": "app.gora"}})
	if err != nil || view.IsError {
		t.Fatalf("open view error=%v result=%+v", err, view)
	}
	viewID := stringField(view.StructuredContent, "view_id")
	initialRevision := scrollNumberField(view.StructuredContent, "revision")
	initialTree, err := session.ReadResource(ctx, &mcp.ReadResourceParams{URI: "gora://project/" + projectID + "/views/" + viewID + "/tree"})
	if err != nil || len(initialTree.Contents) != 1 {
		t.Fatalf("read initial tree error=%v result=%+v", err, initialTree)
	}
	var initialEnvelope struct {
		Root map[string]any `json:"root"`
	}
	if err := json.Unmarshal([]byte(initialTree.Contents[0].Text), &initialEnvelope); err != nil {
		t.Fatal(err)
	}
	feed := findJSONNodeByName(initialEnvelope.Root, "feed")
	if feed == nil {
		t.Fatalf("initial tree has no feed node: %s", initialTree.Contents[0].Text)
	}
	feedID, _ := feed["id"].(string)
	bar := findJSONNodeByRole(initialEnvelope.Root, "scrollbar")
	if bar == nil {
		t.Fatalf("initial tree has no derived scrollbar node: %s", initialTree.Contents[0].Text)
	}
	barID, _ := bar["id"].(string)
	derivedMutation, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "gora_scroll", Arguments: map[string]any{
		"project_id": projectID, "view_id": viewID, "semantic_id": barID, "mode": "to", "y": 80,
	}})
	if err != nil || derivedMutation.IsError {
		t.Fatalf("derived scrollbar scroll error=%v result=%+v", err, derivedMutation)
	}
	if got := scrollNestedNumberField(derivedMutation.StructuredContent, "view", "revision"); got != initialRevision+1 {
		t.Fatalf("derived scrollbar revision = %v, want exactly %v", got, initialRevision+1)
	}
	mutated, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "gora_scroll", Arguments: map[string]any{
		"project_id": projectID, "view_id": viewID, "semantic_id": feedID, "mode": "to", "y": 600,
	}})
	if err != nil || mutated.IsError {
		t.Fatalf("scroll error=%v result=%+v", err, mutated)
	}
	if got := scrollNestedNumberField(mutated.StructuredContent, "view", "revision"); got != initialRevision+2 {
		t.Fatalf("scroll revision = %v, want exactly %v", got, initialRevision+2)
	}
	tree, err := session.ReadResource(ctx, &mcp.ReadResourceParams{URI: "gora://project/" + projectID + "/views/" + viewID + "/tree"})
	if err != nil || len(tree.Contents) != 1 {
		t.Fatalf("read tree after scroll error=%v result=%+v", err, tree)
	}
	var envelope struct {
		Root map[string]any `json:"root"`
	}
	if err := json.Unmarshal([]byte(tree.Contents[0].Text), &envelope); err != nil {
		t.Fatal(err)
	}
	scrolledFeed := findJSONNodeByName(envelope.Root, "feed")
	if scrolledFeed == nil {
		t.Fatalf("scroll tree has no feed node: %s", tree.Contents[0].Text)
	}
	children, _ := scrolledFeed["children"].([]any)
	var child map[string]any
	for _, raw := range children {
		candidate, _ := raw.(map[string]any)
		if candidate["type"] == "spacer" {
			child = candidate
			break
		}
	}
	if child == nil {
		t.Fatalf("feed children omitted authored content: %#v", scrolledFeed["children"])
	}
	bounds, _ := child["bounds"].(map[string]any)
	if got, _ := bounds["y"].(float64); got != -160 {
		t.Fatalf("MCP tree content y = %v, want clamped -160", got)
	}
}

func TestMCPRuntimeResourcesExposeDerivedScrollbarNodes(t *testing.T) {
	root := t.TempDir()
	entry := filepath.Join(root, "app.gora")
	if err := os.WriteFile(entry, []byte(`gora: 1
kind: app
viewport: { width: 100, height: 80 }
entry: main
screens:
  main:
    type: scroll
    name: feed
    props: { axis: vertical, scrollbar_y: always, scrollbar_x: hidden }
    children: [{ type: spacer, props: { height: 240 } }]
`), 0o600); err != nil {
		t.Fatal(err)
	}
	service := NewService(NewRegistry())
	defer service.Close()
	httpServer := httptest.NewServer(service.Handler())
	defer httpServer.Close()
	ctx := context.Background()
	client := mcp.NewClient(&mcp.Implementation{Name: "gora-scroll-resource-test", Version: "1"}, nil)
	session, err := client.Connect(ctx, &mcp.StreamableClientTransport{Endpoint: httpServer.URL + "/mcp"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	opened, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "gora_open_project", Arguments: map[string]any{"root": root}})
	if err != nil || opened.IsError {
		t.Fatalf("open project error=%v result=%+v", err, opened)
	}
	projectID := stringField(opened.StructuredContent, "project_id")
	view, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "gora_open_view", Arguments: map[string]any{"project_id": projectID, "file": "app.gora"}})
	if err != nil || view.IsError {
		t.Fatalf("open view error=%v result=%+v", err, view)
	}
	viewID := stringField(view.StructuredContent, "view_id")
	treeURI := "gora://project/" + projectID + "/views/" + viewID + "/tree"
	treeResource, err := session.ReadResource(ctx, &mcp.ReadResourceParams{URI: treeURI})
	if err != nil || len(treeResource.Contents) != 1 {
		t.Fatalf("read runtime tree error=%v result=%+v", err, treeResource)
	}
	var envelope struct {
		Root map[string]any `json:"root"`
	}
	if err := json.Unmarshal([]byte(treeResource.Contents[0].Text), &envelope); err != nil {
		t.Fatal(err)
	}
	bar := findJSONNodeByRole(envelope.Root, "scrollbar")
	if bar == nil {
		t.Fatalf("runtime resource omitted derived scrollbar: %s", treeResource.Contents[0].Text)
	}
	semanticID, _ := bar["id"].(string)
	if semanticID == "" {
		t.Fatalf("derived scrollbar has no semantic ID: %#v", bar)
	}
	nodeResource, err := session.ReadResource(ctx, &mcp.ReadResourceParams{URI: "gora://project/" + projectID + "/views/" + viewID + "/nodes/" + url.PathEscape(semanticID)})
	if err != nil || len(nodeResource.Contents) != 1 {
		t.Fatalf("read derived node resource error=%v result=%+v", err, nodeResource)
	}
	var node map[string]any
	if err := json.Unmarshal([]byte(nodeResource.Contents[0].Text), &node); err != nil {
		t.Fatal(err)
	}
	if node["role"] != "scrollbar" || node["focus_order"].(float64) < 0 {
		t.Fatalf("derived node resource = %#v", node)
	}
}

func TestMCPScrollTransportPublishesBothAxisMutationAndRejectsStalePairs(t *testing.T) {
	root := t.TempDir()
	entry := filepath.Join(root, "app.gora")
	source := []byte(`gora: 1
kind: app
viewport: { width: 100, height: 80 }
entry: main
screens:
  main:
    type: scroll
    name: workspace
    props: { axis: both }
    children: [{ type: surface, props: { width: 240, height: 200 } }]
`)
	if err := os.WriteFile(entry, source, 0o600); err != nil {
		t.Fatal(err)
	}
	service := NewService(NewRegistry())
	defer service.Close()
	httpServer := httptest.NewServer(service.Handler())
	defer httpServer.Close()
	ctx := context.Background()
	client := mcp.NewClient(&mcp.Implementation{Name: "gora-scroll-both-test", Version: "1"}, nil)
	session, err := client.Connect(ctx, &mcp.StreamableClientTransport{Endpoint: httpServer.URL + "/mcp"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	opened, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "gora_open_project", Arguments: map[string]any{"root": root}})
	if err != nil || opened.IsError {
		t.Fatalf("open project error=%v result=%+v", err, opened)
	}
	projectID := stringField(opened.StructuredContent, "project_id")
	view, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "gora_open_view", Arguments: map[string]any{"project_id": projectID, "file": "app.gora"}})
	if err != nil || view.IsError {
		t.Fatalf("open view error=%v result=%+v", err, view)
	}
	viewID := stringField(view.StructuredContent, "view_id")
	initialRevision := scrollNumberField(view.StructuredContent, "revision")
	treeURI := "gora://project/" + projectID + "/views/" + viewID + "/tree"
	initialTree, err := session.ReadResource(ctx, &mcp.ReadResourceParams{URI: treeURI})
	if err != nil || len(initialTree.Contents) != 1 {
		t.Fatalf("read initial tree error=%v result=%+v", err, initialTree)
	}
	var initialEnvelope struct {
		Root map[string]any `json:"root"`
	}
	if err := json.Unmarshal([]byte(initialTree.Contents[0].Text), &initialEnvelope); err != nil {
		t.Fatal(err)
	}
	workspace := findJSONNodeByName(initialEnvelope.Root, "workspace")
	if workspace == nil {
		t.Fatalf("initial tree has no workspace node: %s", initialTree.Contents[0].Text)
	}
	semanticID, _ := workspace["id"].(string)
	horizontalBar := findJSONNodeByRoleAndOrientation(initialEnvelope.Root, "scrollbar", "horizontal")
	if horizontalBar == nil {
		t.Fatalf("initial tree has no horizontal derived scrollbar: %s", initialTree.Contents[0].Text)
	}
	horizontalID, _ := horizontalBar["id"].(string)
	derivedMutation, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "gora_scroll", Arguments: map[string]any{
		"project_id": projectID, "view_id": viewID, "semantic_id": horizontalID, "mode": "to", "x": 40,
	}})
	if err != nil || derivedMutation.IsError {
		t.Fatalf("horizontal derived scroll error=%v result=%+v", err, derivedMutation)
	}
	if got := scrollNestedNumberField(derivedMutation.StructuredContent, "view", "revision"); got != initialRevision+1 {
		t.Fatalf("horizontal derived revision = %v, want %v", got, initialRevision+1)
	}
	mutated, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "gora_scroll", Arguments: map[string]any{
		"project_id": projectID, "view_id": viewID, "semantic_id": semanticID, "mode": "to", "x": 999, "y": 999,
	}})
	if err != nil || mutated.IsError {
		t.Fatalf("both-axis scroll error=%v result=%+v", err, mutated)
	}
	if got := scrollNestedNumberField(mutated.StructuredContent, "view", "revision"); got != initialRevision+2 {
		t.Fatalf("both-axis revision = %v, want %v", got, initialRevision+1)
	}
	scrolledTree, err := session.ReadResource(ctx, &mcp.ReadResourceParams{URI: treeURI})
	if err != nil || len(scrolledTree.Contents) != 1 {
		t.Fatalf("read scrolled tree error=%v result=%+v", err, scrolledTree)
	}
	var scrolledEnvelope struct {
		Root map[string]any `json:"root"`
	}
	if err := json.Unmarshal([]byte(scrolledTree.Contents[0].Text), &scrolledEnvelope); err != nil {
		t.Fatal(err)
	}
	scrolledWorkspace := findJSONNodeByName(scrolledEnvelope.Root, "workspace")
	children, _ := scrolledWorkspace["children"].([]any)
	child, _ := children[0].(map[string]any)
	bounds, _ := child["bounds"].(map[string]any)
	if got, _ := bounds["x"].(float64); got != -140 {
		t.Fatalf("both-axis clamped x = %v, want -140", got)
	}
	if got, _ := bounds["y"].(float64); got != -120 {
		t.Fatalf("both-axis clamped y = %v, want -120", got)
	}
	mutated, err = session.CallTool(ctx, &mcp.CallToolParams{Name: "gora_scroll", Arguments: map[string]any{
		"project_id": projectID, "view_id": viewID, "semantic_id": semanticID, "mode": "by", "x": -10, "y": -20,
	}})
	if err != nil || mutated.IsError {
		t.Fatalf("both-axis diagonal scroll error=%v result=%+v", err, mutated)
	}
	if got := scrollNestedNumberField(mutated.StructuredContent, "view", "revision"); got != initialRevision+3 {
		t.Fatalf("diagonal revision = %v, want %v", got, initialRevision+2)
	}
	stale, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "gora_scroll", Arguments: map[string]any{
		"project_id": projectID, "view_id": viewID, "semantic_id": "missing", "mode": "to", "x": 1, "y": 1,
	}})
	if err != nil || !stale.IsError {
		t.Fatalf("stale semantic id result error=%v result=%+v", err, stale)
	}
	viewAfterStale, err := session.ReadResource(ctx, &mcp.ReadResourceParams{URI: "gora://project/" + projectID + "/views/" + viewID})
	if err != nil || len(viewAfterStale.Contents) != 1 {
		t.Fatalf("read view after stale id error=%v result=%+v", err, viewAfterStale)
	}
	var staleSummary struct {
		Revision uint64 `json:"revision"`
	}
	if err := json.Unmarshal([]byte(viewAfterStale.Contents[0].Text), &staleSummary); err != nil {
		t.Fatal(err)
	}
	if staleSummary.Revision != uint64(initialRevision+3) {
		t.Fatalf("stale semantic id changed revision to %d, want %v", staleSummary.Revision, initialRevision+2)
	}
	root2 := t.TempDir()
	if err := os.WriteFile(filepath.Join(root2, "app.gora"), source, 0o600); err != nil {
		t.Fatal(err)
	}
	second, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "gora_open_project", Arguments: map[string]any{"root": root2}})
	if err != nil || second.IsError {
		t.Fatalf("open second project error=%v result=%+v", err, second)
	}
	project2 := stringField(second.StructuredContent, "project_id")
	mismatched, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "gora_scroll", Arguments: map[string]any{
		"project_id": project2, "view_id": viewID, "semantic_id": semanticID, "mode": "to", "x": 1, "y": 1,
	}})
	if err != nil || !mismatched.IsError {
		t.Fatalf("mismatched project/view result error=%v result=%+v", err, mismatched)
	}
	viewAfterMismatch, err := session.ReadResource(ctx, &mcp.ReadResourceParams{URI: "gora://project/" + projectID + "/views/" + viewID})
	if err != nil || len(viewAfterMismatch.Contents) != 1 {
		t.Fatalf("read view after mismatch error=%v result=%+v", err, viewAfterMismatch)
	}
	var mismatchSummary struct {
		Revision uint64 `json:"revision"`
	}
	if err := json.Unmarshal([]byte(viewAfterMismatch.Contents[0].Text), &mismatchSummary); err != nil {
		t.Fatal(err)
	}
	if mismatchSummary.Revision != uint64(initialRevision+3) {
		t.Fatalf("mismatched project/view changed revision to %d, want %v", mismatchSummary.Revision, initialRevision+2)
	}
}

func TestMCPDerivedScrollbarByPreservesOwnerOtherAxis(t *testing.T) {
	root := t.TempDir()
	entry := filepath.Join(root, "app.gora")
	if err := os.WriteFile(entry, []byte(`gora: 1
kind: app
viewport: { width: 100, height: 80 }
entry: main
screens:
  main:
    type: scroll
    name: workspace
    props: { axis: both, scrollbar_x: always, scrollbar_y: always }
    children: [{ type: surface, props: { width: 240, height: 200 } }]
`), 0o600); err != nil {
		t.Fatal(err)
	}
	service := NewService(NewRegistry())
	defer service.Close()
	httpServer := httptest.NewServer(service.Handler())
	defer httpServer.Close()
	ctx := context.Background()
	client := mcp.NewClient(&mcp.Implementation{Name: "gora-scroll-derived-by-test", Version: "1"}, nil)
	session, err := client.Connect(ctx, &mcp.StreamableClientTransport{Endpoint: httpServer.URL + "/mcp"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	opened, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "gora_open_project", Arguments: map[string]any{"root": root}})
	if err != nil || opened.IsError {
		t.Fatalf("open project error=%v result=%+v", err, opened)
	}
	projectID := stringField(opened.StructuredContent, "project_id")
	view, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "gora_open_view", Arguments: map[string]any{"project_id": projectID, "file": "app.gora"}})
	if err != nil || view.IsError {
		t.Fatalf("open view error=%v result=%+v", err, view)
	}
	viewID := stringField(view.StructuredContent, "view_id")
	initialRevision := scrollNumberField(view.StructuredContent, "revision")
	treeResource, err := session.ReadResource(ctx, &mcp.ReadResourceParams{URI: "gora://project/" + projectID + "/views/" + viewID + "/tree"})
	if err != nil || len(treeResource.Contents) != 1 {
		t.Fatalf("read tree error=%v result=%+v", err, treeResource)
	}
	var envelope struct {
		Root map[string]any `json:"root"`
	}
	if err := json.Unmarshal([]byte(treeResource.Contents[0].Text), &envelope); err != nil {
		t.Fatal(err)
	}
	workspace := findJSONNodeByName(envelope.Root, "workspace")
	vertical := findJSONNodeByRoleAndOrientation(envelope.Root, "scrollbar", "vertical")
	horizontal := findJSONNodeByRoleAndOrientation(envelope.Root, "scrollbar", "horizontal")
	if workspace == nil || vertical == nil || horizontal == nil {
		t.Fatalf("tree nodes = workspace:%v vertical:%v horizontal:%v", workspace != nil, vertical != nil, horizontal != nil)
	}
	workspaceID, _ := workspace["id"].(string)
	verticalID, _ := vertical["id"].(string)
	horizontalID, _ := horizontal["id"].(string)
	set, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "gora_scroll", Arguments: map[string]any{
		"project_id": projectID, "view_id": viewID, "semantic_id": workspaceID, "mode": "to", "x": 30, "y": 20,
	}})
	if err != nil || set.IsError {
		t.Fatalf("set owner offset error=%v result=%+v", err, set)
	}
	if got := scrollNestedNumberField(set.StructuredContent, "view", "revision"); got != initialRevision+1 {
		t.Fatalf("owner revision = %v, want %v", got, initialRevision+1)
	}
	verticalBy, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "gora_scroll", Arguments: map[string]any{
		"project_id": projectID, "view_id": viewID, "semantic_id": verticalID, "mode": "by", "x": 0, "y": 10,
	}})
	if err != nil || verticalBy.IsError {
		t.Fatalf("vertical derived by error=%v result=%+v", err, verticalBy)
	}
	if got := scrollNestedNumberField(verticalBy.StructuredContent, "view", "revision"); got != initialRevision+2 {
		t.Fatalf("vertical derived by revision = %v, want %v", got, initialRevision+2)
	}
	horizontalBy, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "gora_scroll", Arguments: map[string]any{
		"project_id": projectID, "view_id": viewID, "semantic_id": horizontalID, "mode": "by", "x": 15, "y": 0,
	}})
	if err != nil || horizontalBy.IsError {
		t.Fatalf("horizontal derived by error=%v result=%+v", err, horizontalBy)
	}
	if got := scrollNestedNumberField(horizontalBy.StructuredContent, "view", "revision"); got != initialRevision+3 {
		t.Fatalf("horizontal derived by revision = %v, want %v", got, initialRevision+3)
	}
	crossAxis, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "gora_scroll", Arguments: map[string]any{
		"project_id": projectID, "view_id": viewID, "semantic_id": verticalID, "mode": "by", "x": 1, "y": 0,
	}})
	if err != nil || !crossAxis.IsError {
		t.Fatalf("vertical cross-axis result error=%v result=%+v", err, crossAxis)
	}
	viewResource, err := session.ReadResource(ctx, &mcp.ReadResourceParams{URI: "gora://project/" + projectID + "/views/" + viewID})
	if err != nil || len(viewResource.Contents) != 1 {
		t.Fatalf("read view after cross-axis error=%v result=%+v", err, viewResource)
	}
	var summary struct {
		Revision uint64 `json:"revision"`
	}
	if err := json.Unmarshal([]byte(viewResource.Contents[0].Text), &summary); err != nil {
		t.Fatal(err)
	}
	if summary.Revision != uint64(initialRevision+3) {
		t.Fatalf("cross-axis rejection changed revision to %d, want %v", summary.Revision, initialRevision+3)
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

func TestMCPAutomationFeatureGate(t *testing.T) {
	ordinary := NewService(NewRegistry())
	defer ordinary.Close()
	if ordinary.AutomationEnabled() {
		t.Fatal("automation unexpectedly enabled without the feature flag")
	}
	automation := NewServiceWithOptions(NewRegistry(), ServiceOptions{Automation: true})
	defer automation.Close()
	if !automation.AutomationEnabled() {
		t.Fatal("automation is not enabled with the feature flag")
	}
}

func TestMCPAutomationToolAndTemplateAreGatedInMemory(t *testing.T) {
	for _, test := range []struct {
		name    string
		enabled bool
	}{
		{name: "ordinary", enabled: false},
		{name: "automation", enabled: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			service := NewServiceWithOptions(NewRegistry(), ServiceOptions{Automation: test.enabled})
			defer service.Close()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			clientTransport, serverTransport := mcp.NewInMemoryTransports()
			if _, err := service.server.Connect(ctx, serverTransport, nil); err != nil {
				t.Fatal(err)
			}
			client := mcp.NewClient(&mcp.Implementation{Name: "automation-gate-test", Version: "1"}, nil)
			session, err := client.Connect(ctx, clientTransport, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer session.Close()
			tools, err := session.ListTools(ctx, nil)
			if err != nil {
				t.Fatal(err)
			}
			resources, err := session.ListResourceTemplates(ctx, nil)
			if err != nil {
				t.Fatal(err)
			}
			if hasTool(tools.Tools, "gora_wait_for_view") != test.enabled {
				t.Fatalf("wait tool gated=%v tools=%+v", test.enabled, tools.Tools)
			}
			foundTemplate := false
			for _, template := range resources.ResourceTemplates {
				if template.URITemplate == "gora://project/{project_id}/views/{view_id}/automation" {
					foundTemplate = true
				}
			}
			if foundTemplate != test.enabled {
				t.Fatalf("automation template gated=%v templates=%+v", test.enabled, resources.ResourceTemplates)
			}
			if !test.enabled {
				root := t.TempDir()
				if err := os.WriteFile(filepath.Join(root, "app.gora"), []byte("gora: 1\nkind: app\nviewport: { width: 40, height: 30 }\nentry: main\nscreens:\n  main: { type: spacer }\n"), 0o600); err != nil {
					t.Fatal(err)
				}
				opened, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "gora_open_project", Arguments: map[string]any{"root": root}})
				if err != nil || opened.IsError {
					t.Fatalf("ordinary project open error=%v result=%+v", err, opened)
				}
				projectID := stringField(opened.StructuredContent, "project_id")
				view, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "gora_open_view", Arguments: map[string]any{"project_id": projectID, "file": "app.gora"}})
				if err != nil || view.IsError {
					t.Fatalf("ordinary view open error=%v result=%+v", err, view)
				}
				viewID := stringField(view.StructuredContent, "view_id")
				if _, err := session.ReadResource(ctx, &mcp.ReadResourceParams{URI: "gora://project/" + projectID + "/views/" + viewID + "/automation"}); err == nil {
					t.Fatal("ordinary MCP exposed the automation resource")
				}
			}
		})
	}
}

func TestMCPAutomationResourceSubscriptionPublishesAfterFrameAndUnsubscribe(t *testing.T) {
	root := t.TempDir()
	entry := filepath.Join(root, "app.gora")
	if err := os.WriteFile(entry, []byte("gora: 1\nkind: app\nviewport: { width: 100, height: 80 }\nentry: main\nscreens:\n  main: { type: spacer }\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	service := NewServiceWithOptions(NewRegistry(), ServiceOptions{Automation: true})
	defer service.Close()
	ctx := context.Background()
	clientTransport, serverTransport := mcp.NewInMemoryTransports()
	if _, err := service.server.Connect(ctx, serverTransport, nil); err != nil {
		t.Fatal(err)
	}
	updates := make(chan string, 8)
	client := mcp.NewClient(&mcp.Implementation{Name: "automation-subscription-test", Version: "1"}, &mcp.ClientOptions{
		ResourceUpdatedHandler: func(_ context.Context, request *mcp.ResourceUpdatedNotificationRequest) {
			if request != nil && request.Params != nil {
				updates <- request.Params.URI
			}
		},
	})
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
		t.Fatalf("open view error=%v result=%+v", err, viewResult)
	}
	viewID := stringField(viewResult.StructuredContent, "view_id")
	automationURI := "gora://project/" + projectID + "/views/" + viewID + "/automation"
	resource, err := session.ReadResource(ctx, &mcp.ReadResourceParams{URI: automationURI})
	if err != nil || len(resource.Contents) != 1 {
		t.Fatalf("automation resource error=%v result=%+v", err, resource)
	}
	var initial struct {
		FrameRevision uint64 `json:"frame_revision"`
	}
	if err := json.Unmarshal([]byte(resource.Contents[0].Text), &initial); err != nil {
		t.Fatal(err)
	}
	if err := session.Subscribe(ctx, &mcp.SubscribeParams{URI: automationURI}); err != nil {
		t.Fatalf("subscribe automation resource: %v", err)
	}
	if _, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "gora_set_viewport", Arguments: map[string]any{
		"project_id": projectID, "view_id": viewID, "width": 120, "height": 80,
	}}); err != nil {
		t.Fatal(err)
	}
	select {
	case uri := <-updates:
		if uri != automationURI {
			t.Fatalf("first notification URI = %q, want %q", uri, automationURI)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for automation resource notification")
	}
	updatedResource, err := session.ReadResource(ctx, &mcp.ReadResourceParams{URI: automationURI})
	if err != nil || len(updatedResource.Contents) != 1 {
		t.Fatalf("updated automation resource error=%v result=%+v", err, updatedResource)
	}
	var updated struct {
		FrameRevision uint64 `json:"frame_revision"`
		Agreement     bool   `json:"agreement"`
	}
	if err := json.Unmarshal([]byte(updatedResource.Contents[0].Text), &updated); err != nil {
		t.Fatal(err)
	}
	if updated.FrameRevision != initial.FrameRevision+1 || !updated.Agreement {
		t.Fatalf("notification was not after one matching publication: initial=%+v updated=%+v", initial, updated)
	}
	select {
	case uri := <-updates:
		t.Fatalf("duplicate automation notification for one mutation: %q", uri)
	case <-time.After(100 * time.Millisecond):
	}
	if err := session.Unsubscribe(ctx, &mcp.UnsubscribeParams{URI: automationURI}); err != nil {
		t.Fatalf("unsubscribe automation resource: %v", err)
	}
	if _, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "gora_set_viewport", Arguments: map[string]any{
		"project_id": projectID, "view_id": viewID, "width": 140, "height": 80,
	}}); err != nil {
		t.Fatal(err)
	}
	select {
	case uri := <-updates:
		t.Fatalf("notification delivered after unsubscribe: %q", uri)
	case <-time.After(100 * time.Millisecond):
	}
}

func TestMCPAutomationTraceConfigureDispatchReadAndClear(t *testing.T) {
	root := t.TempDir()
	entry := filepath.Join(root, "app.gora")
	if err := os.WriteFile(entry, []byte("gora: 1\nkind: app\nviewport: { width: 100, height: 80 }\nentry: main\nscreens:\n  main: { type: spacer }\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	service := NewServiceWithOptions(NewRegistry(), ServiceOptions{Automation: true})
	defer service.Close()
	ctx := context.Background()
	clientTransport, serverTransport := mcp.NewInMemoryTransports()
	if _, err := service.server.Connect(ctx, serverTransport, nil); err != nil {
		t.Fatal(err)
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "trace-test", Version: "1"}, nil)
	session, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	opened, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "gora_open_project", Arguments: map[string]any{"root": root}})
	if err != nil || opened.IsError {
		t.Fatalf("open project: %v %+v", err, opened)
	}
	projectID := stringField(opened.StructuredContent, "project_id")
	viewResult, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "gora_open_view", Arguments: map[string]any{"project_id": projectID, "file": "app.gora"}})
	if err != nil || viewResult.IsError {
		t.Fatalf("open view: %v %+v", err, viewResult)
	}
	viewID := stringField(viewResult.StructuredContent, "view_id")
	configured, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "gora_configure_event_trace", Arguments: map[string]any{"project_id": projectID, "view_id": viewID, "enabled": true, "capacity": 2}})
	if err != nil || configured.IsError {
		t.Fatalf("configure trace: %v %+v", err, configured)
	}
	dispatched, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "gora_dispatch_input", Arguments: map[string]any{"project_id": projectID, "view_id": viewID, "wait": "none", "events": []any{map[string]any{"type": "scroll", "source": "wheel", "x": 1, "y": 1, "delta_y": 0, "units": "logical", "phase": "begin", "momentum": "none", "time_ms": 1}}}})
	if err != nil || dispatched.IsError {
		for _, content := range dispatched.Content {
			if text, ok := content.(*mcp.TextContent); ok {
				t.Logf("dispatch error text: %s", text.Text)
			}
		}
		t.Fatalf("dispatch: %v %+v", err, dispatched)
	}
	traceURI := "gora://project/" + projectID + "/views/" + viewID + "/automation/trace"
	resource, err := session.ReadResource(ctx, &mcp.ReadResourceParams{URI: traceURI})
	if err != nil || len(resource.Contents) != 1 {
		t.Fatalf("read trace: %v %+v", err, resource)
	}
	var trace struct {
		Generation uint64 `json:"generation"`
		Capacity   int    `json:"capacity"`
		Entries    []struct {
			Stage   string `json:"stage"`
			Outcome string `json:"outcome"`
		} `json:"entries"`
	}
	if err := json.Unmarshal([]byte(resource.Contents[0].Text), &trace); err != nil {
		t.Fatal(err)
	}
	if trace.Generation == 0 || trace.Capacity != 2 || len(trace.Entries) != 2 {
		t.Fatalf("trace=%+v", trace)
	}
	if trace.Entries[len(trace.Entries)-1].Stage != "publication" || trace.Entries[len(trace.Entries)-1].Outcome != "phase_only" {
		t.Fatalf("trace publication=%+v", trace.Entries)
	}
	cleared, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "gora_clear_event_trace", Arguments: map[string]any{"project_id": projectID, "view_id": viewID}})
	if err != nil || cleared.IsError {
		t.Fatalf("clear trace: %v %+v", err, cleared)
	}
	if got := stringField(cleared.StructuredContent, "view_id"); got != viewID {
		t.Fatalf("clear output=%+v", cleared.StructuredContent)
	}
}

func TestMCPAutomationClipboardAndClockToolsInMemory(t *testing.T) {
	root := t.TempDir()
	entry := filepath.Join(root, "app.gora")
	if err := os.WriteFile(entry, []byte("gora: 1\nkind: app\nviewport: { width: 100, height: 80 }\nentry: main\nscreens:\n  main: { type: spacer }\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	service := NewServiceWithOptions(NewRegistry(), ServiceOptions{Automation: true})
	defer service.Close()
	ctx := context.Background()
	clientTransport, serverTransport := mcp.NewInMemoryTransports()
	if _, err := service.server.Connect(ctx, serverTransport, nil); err != nil {
		t.Fatal(err)
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "phase4-test", Version: "1"}, nil)
	session, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	opened, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "gora_open_project", Arguments: map[string]any{"root": root}})
	if err != nil || opened.IsError {
		t.Fatalf("open project: %v %+v", err, opened)
	}
	projectID := stringField(opened.StructuredContent, "project_id")
	view, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "gora_open_view", Arguments: map[string]any{"project_id": projectID, "file": "app.gora"}})
	if err != nil || view.IsError {
		t.Fatalf("open view: %v %+v", err, view)
	}
	viewID := stringField(view.StructuredContent, "view_id")
	if _, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "gora_configure_event_trace", Arguments: map[string]any{"project_id": projectID, "view_id": viewID, "enabled": true, "capacity": 32}}); err != nil {
		t.Fatal(err)
	}
	if _, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "gora_set_automation_clipboard", Arguments: map[string]any{"project_id": projectID, "view_id": viewID, "text": "secret"}}); err != nil {
		t.Fatal(err)
	}
	read, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "gora_read_automation_clipboard", Arguments: map[string]any{"project_id": projectID, "view_id": viewID}})
	if err != nil || read.IsError || stringField(read.StructuredContent, "text") != "secret" {
		t.Fatalf("read clipboard: %v %+v", err, read)
	}
	setClock, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "gora_set_view_clock", Arguments: map[string]any{"project_id": projectID, "view_id": viewID, "mode": "frozen"}})
	if err != nil || setClock.IsError {
		t.Fatalf("freeze clock: %v %+v", err, setClock)
	}
	advanced, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "gora_advance_view_clock", Arguments: map[string]any{"project_id": projectID, "view_id": viewID, "delta_ms": 500}})
	if err != nil || advanced.IsError {
		t.Fatalf("advance clock: %v %+v", err, advanced)
	}
	if advanced.StructuredContent == nil {
		t.Fatalf("clock output=%#v", advanced.StructuredContent)
	}
	automationURI := "gora://project/" + projectID + "/views/" + viewID + "/automation"
	resource, err := session.ReadResource(ctx, &mcp.ReadResourceParams{URI: automationURI})
	if err != nil || len(resource.Contents) != 1 {
		t.Fatalf("automation resource: %v %+v", err, resource)
	}
	if strings.Contains(resource.Contents[0].Text, "secret") {
		t.Fatal("automation resource leaked clipboard contents")
	}
	var snapshot struct {
		ClockMode       string `json:"clock_mode"`
		ClockTimeMS     int64  `json:"clock_time_ms"`
		ClipboardLength int    `json:"clipboard_length"`
		BlinkVisible    bool   `json:"blink_visible"`
		EditingHistory  any    `json:"editing_history"`
	}
	if err := json.Unmarshal([]byte(resource.Contents[0].Text), &snapshot); err != nil {
		t.Fatal(err)
	}
	if snapshot.ClockMode != "frozen" || snapshot.ClockTimeMS == 0 || snapshot.ClipboardLength != len("secret") || snapshot.EditingHistory == nil {
		t.Fatalf("automation snapshot=%+v", snapshot)
	}
	traceResource, err := session.ReadResource(ctx, &mcp.ReadResourceParams{URI: automationURI + "/trace"})
	if err != nil || len(traceResource.Contents) != 1 || !strings.Contains(traceResource.Contents[0].Text, `"stage":"clock"`) {
		t.Fatalf("clock trace resource: %v %+v", err, traceResource)
	}
}

func TestMCPTraceSubscriptionCoalescesBatchAndSuppressesDisabledDispatch(t *testing.T) {
	root := t.TempDir()
	entry := filepath.Join(root, "app.gora")
	if err := os.WriteFile(entry, []byte("gora: 1\nkind: app\nviewport: { width: 100, height: 80 }\nentry: main\nscreens:\n  main: { type: spacer }\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	service := NewServiceWithOptions(NewRegistry(), ServiceOptions{Automation: true})
	defer service.Close()
	ctx := context.Background()
	clientTransport, serverTransport := mcp.NewInMemoryTransports()
	if _, err := service.server.Connect(ctx, serverTransport, nil); err != nil {
		t.Fatal(err)
	}
	updates := make(chan string, 32)
	client := mcp.NewClient(&mcp.Implementation{Name: "trace-subscription-test", Version: "1"}, &mcp.ClientOptions{ResourceUpdatedHandler: func(_ context.Context, request *mcp.ResourceUpdatedNotificationRequest) {
		if request != nil && request.Params != nil {
			updates <- request.Params.URI
		}
	}})
	session, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	opened, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "gora_open_project", Arguments: map[string]any{"root": root}})
	if err != nil || opened.IsError {
		t.Fatalf("open project: %v %+v", err, opened)
	}
	projectID := stringField(opened.StructuredContent, "project_id")
	viewResult, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "gora_open_view", Arguments: map[string]any{"project_id": projectID, "file": "app.gora"}})
	if err != nil || viewResult.IsError {
		t.Fatalf("open view: %v %+v", err, viewResult)
	}
	viewID := stringField(viewResult.StructuredContent, "view_id")
	traceURI := "gora://project/" + projectID + "/views/" + viewID + "/automation/trace"
	if err := session.Subscribe(ctx, &mcp.SubscribeParams{URI: traceURI}); err != nil {
		t.Fatalf("subscribe trace: %v", err)
	}
	configured, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "gora_configure_event_trace", Arguments: map[string]any{"project_id": projectID, "view_id": viewID, "enabled": true, "capacity": 4}})
	if err != nil || configured.IsError {
		t.Fatalf("configure trace: %v %+v", err, configured)
	}
	events := make([]any, 0, 1000)
	for index := 0; index < 1000; index++ {
		events = append(events, map[string]any{"type": "scroll", "source": "wheel", "x": 1, "y": 1, "units": "logical", "phase": "update", "momentum": "none", "time_ms": index + 1})
	}
	dispatched, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "gora_dispatch_input", Arguments: map[string]any{"project_id": projectID, "view_id": viewID, "wait": "none", "events": events}})
	if err != nil || dispatched.IsError {
		t.Fatalf("dispatch trace batch: %v %+v", err, dispatched)
	}
	for len(updates) > 0 {
		<-updates
	}
	resource, err := session.ReadResource(ctx, &mcp.ReadResourceParams{URI: traceURI})
	if err != nil || len(resource.Contents) != 1 {
		t.Fatalf("read trace: %v %+v", err, resource)
	}
	var trace automation.TraceSnapshot
	if err := json.Unmarshal([]byte(resource.Contents[0].Text), &trace); err != nil {
		t.Fatal(err)
	}
	if len(trace.Entries) != 4 || trace.Entries[len(trace.Entries)-1].EventIndex != 999 {
		t.Fatalf("trace rollover/coalescing=%+v", trace)
	}
	if _, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "gora_configure_event_trace", Arguments: map[string]any{"project_id": projectID, "view_id": viewID, "enabled": false, "capacity": 4}}); err != nil {
		t.Fatal(err)
	}
	for len(updates) > 0 {
		<-updates
	}
	if _, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "gora_dispatch_input", Arguments: map[string]any{"project_id": projectID, "view_id": viewID, "wait": "none", "events": []any{map[string]any{"type": "scroll", "source": "wheel", "units": "logical", "phase": "update", "momentum": "none", "time_ms": 1001}}}}); err != nil {
		t.Fatal(err)
	}
	select {
	case uri := <-updates:
		t.Fatalf("disabled dispatch emitted trace update %q", uri)
	case <-time.After(100 * time.Millisecond):
	}
}

func TestMCPResourceSubscriptionValidationAndAutomationGate(t *testing.T) {
	writeProject := func(t *testing.T) string {
		t.Helper()
		root := t.TempDir()
		if err := os.WriteFile(filepath.Join(root, "app.gora"), []byte("gora: 1\nkind: app\nviewport: { width: 100, height: 80 }\nentry: main\nscreens:\n  main: { type: spacer }\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, "theme.gora"), []byte("gora: 1\nkind: tokens\ntokens: {}\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		return root
	}
	openSession := func(t *testing.T, automation bool) (*Service, *mcp.ClientSession, string, string, string) {
		t.Helper()
		service := NewServiceWithOptions(NewRegistry(), ServiceOptions{Automation: automation})
		ctx := context.Background()
		clientTransport, serverTransport := mcp.NewInMemoryTransports()
		if _, err := service.server.Connect(ctx, serverTransport, nil); err != nil {
			service.Close()
			t.Fatal(err)
		}
		client := mcp.NewClient(&mcp.Implementation{Name: "subscription-validation-test", Version: "1"}, &mcp.ClientOptions{
			ResourceUpdatedHandler: func(context.Context, *mcp.ResourceUpdatedNotificationRequest) {},
		})
		session, err := client.Connect(ctx, clientTransport, nil)
		if err != nil {
			service.Close()
			t.Fatal(err)
		}
		root := writeProject(t)
		opened, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "gora_open_project", Arguments: map[string]any{"root": root}})
		if err != nil || opened.IsError {
			session.Close()
			service.Close()
			t.Fatalf("open project error=%v result=%+v", err, opened)
		}
		projectID := stringField(opened.StructuredContent, "project_id")
		viewResult, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "gora_open_view", Arguments: map[string]any{"project_id": projectID, "file": "app.gora"}})
		if err != nil || viewResult.IsError {
			session.Close()
			service.Close()
			t.Fatalf("open view error=%v result=%+v", err, viewResult)
		}
		viewID := stringField(viewResult.StructuredContent, "view_id")
		tokenResult, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "gora_open_view", Arguments: map[string]any{"project_id": projectID, "file": "theme.gora"}})
		if err != nil || tokenResult.IsError {
			session.Close()
			service.Close()
			t.Fatalf("open token view error=%v result=%+v", err, tokenResult)
		}
		return service, session, projectID, viewID, stringField(tokenResult.StructuredContent, "view_id")
	}

	t.Run("ordinary resources remain subscribable", func(t *testing.T) {
		service, session, projectID, viewID, _ := openSession(t, false)
		defer session.Close()
		defer service.Close()
		ctx := context.Background()
		viewURI := "gora://project/" + projectID + "/views/" + viewID
		if err := session.Subscribe(ctx, &mcp.SubscribeParams{URI: viewURI}); err != nil {
			t.Fatalf("ordinary view resource subscription rejected: %v", err)
		}
		if err := session.Unsubscribe(ctx, &mcp.UnsubscribeParams{URI: viewURI}); err != nil {
			t.Fatalf("ordinary view resource unsubscribe failed: %v", err)
		}
		automationURI := viewURI + "/automation"
		if err := service.validateSubscription(ctx, &mcp.SubscribeRequest{Params: &mcp.SubscribeParams{URI: automationURI}}); err == nil {
			t.Fatal("automation subscription was accepted without --automation")
		}
	})

	t.Run("automation URI validation", func(t *testing.T) {
		service, session, projectID, viewID, tokenViewID := openSession(t, true)
		defer session.Close()
		defer service.Close()
		ctx := context.Background()
		for _, uri := range []string{
			"gora://project/missing/views/missing/automation",
			"gora://project/" + projectID + "/views/" + viewID + "/automation/extra",
			"gora://project/" + projectID + "/views/" + tokenViewID + "/automation",
			"not-a-gora-resource",
		} {
			if err := service.validateSubscription(ctx, &mcp.SubscribeRequest{Params: &mcp.SubscribeParams{URI: uri}}); err == nil {
				t.Fatalf("invalid subscription accepted: %q", uri)
			}
		}
	})
}

func TestMCPAutomationWaitResourceAndStructuredErrorsInMemory(t *testing.T) {
	root := t.TempDir()
	appPath := filepath.Join(root, "app.gora")
	tokenPath := filepath.Join(root, "theme.gora")
	if err := os.WriteFile(appPath, []byte("gora: 1\nkind: app\nviewport: { width: 100, height: 80 }\nentry: main\nscreens:\n  main: { type: spacer }\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(tokenPath, []byte("gora: 1\nkind: tokens\ntokens: {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	service := NewServiceWithOptions(NewRegistry(), ServiceOptions{Automation: true})
	defer service.Close()
	ctx := context.Background()
	clientTransport, serverTransport := mcp.NewInMemoryTransports()
	if _, err := service.server.Connect(ctx, serverTransport, nil); err != nil {
		t.Fatal(err)
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "automation-wait-test", Version: "1"}, nil)
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
		t.Fatalf("open view error=%v result=%+v", err, viewResult)
	}
	viewID := stringField(viewResult.StructuredContent, "view_id")
	automationURI := "gora://project/" + projectID + "/views/" + viewID + "/automation"
	resource, err := session.ReadResource(ctx, &mcp.ReadResourceParams{URI: automationURI})
	if err != nil || len(resource.Contents) != 1 || resource.Contents[0].Text == "" {
		t.Fatalf("automation resource error=%v result=%+v", err, resource)
	}
	wait, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "gora_wait_for_view", Arguments: map[string]any{
		"project_id": projectID, "view_id": viewID, "after_frame_revision": 0, "timeout_ms": 100,
	}})
	if err != nil || wait.IsError || wait.StructuredContent == nil {
		t.Fatalf("wait success error=%v result=%+v", err, wait)
	}
	waitMap, _ := wait.StructuredContent.(map[string]any)
	if _, ok := waitMap["snapshot"]; !ok {
		t.Fatalf("wait result has no snapshot: %+v", wait.StructuredContent)
	}
	negativeStable, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "gora_wait_for_view", Arguments: map[string]any{
		"project_id": projectID, "view_id": viewID, "stable_frames": -1, "timeout_ms": 1,
	}})
	if err != nil || !negativeStable.IsError {
		t.Fatalf("negative stable_frames error=%v result=%+v", err, negativeStable)
	}
	var current struct {
		FrameRevision uint64 `json:"frame_revision"`
	}
	if err := json.Unmarshal([]byte(resource.Contents[0].Text), &current); err != nil {
		t.Fatal(err)
	}
	resized, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "gora_set_viewport", Arguments: map[string]any{
		"project_id": projectID, "view_id": viewID, "width": 120, "height": 80,
	}})
	if err != nil || resized.IsError {
		t.Fatalf("viewport mutation error=%v result=%+v", err, resized)
	}
	updatedResource, err := session.ReadResource(ctx, &mcp.ReadResourceParams{URI: automationURI})
	if err != nil || len(updatedResource.Contents) != 1 {
		t.Fatalf("updated automation resource error=%v result=%+v", err, updatedResource)
	}
	var updated struct {
		FrameRevision uint64 `json:"frame_revision"`
		Agreement     bool   `json:"agreement"`
	}
	if err := json.Unmarshal([]byte(updatedResource.Contents[0].Text), &updated); err != nil {
		t.Fatal(err)
	}
	if updated.FrameRevision != current.FrameRevision+1 || !updated.Agreement {
		t.Fatalf("viewport did not publish exactly once: before=%+v after=%+v", current, updated)
	}
	current.FrameRevision = updated.FrameRevision
	timeout, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "gora_wait_for_view", Arguments: map[string]any{
		"project_id": projectID, "view_id": viewID, "after_frame_revision": current.FrameRevision + 1, "timeout_ms": 1,
	}})
	if err != nil || !timeout.IsError || timeout.StructuredContent == nil {
		t.Fatalf("wait timeout error=%v result=%+v", err, timeout)
	}
	timeoutMap, _ := timeout.StructuredContent.(map[string]any)
	if _, ok := timeoutMap["snapshot"]; !ok {
		t.Fatalf("timeout result has no latest snapshot: %+v", timeout.StructuredContent)
	}
	mismatch, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "gora_wait_for_view", Arguments: map[string]any{
		"project_id": "missing", "view_id": viewID, "after_frame_revision": 0, "timeout_ms": 1,
	}})
	if err != nil || !mismatch.IsError {
		t.Fatalf("mismatched wait error=%v result=%+v", err, mismatch)
	}
	tokenResult, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "gora_open_view", Arguments: map[string]any{"project_id": projectID, "file": "theme.gora"}})
	if err != nil || tokenResult.IsError {
		t.Fatalf("open token view error=%v result=%+v", err, tokenResult)
	}
	tokenWait, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "gora_wait_for_view", Arguments: map[string]any{
		"project_id": projectID, "view_id": stringField(tokenResult.StructuredContent, "view_id"), "after_frame_revision": 0, "timeout_ms": 1,
	}})
	if err != nil || !tokenWait.IsError {
		t.Fatalf("token wait error=%v result=%+v", err, tokenWait)
	}
	tokenURI := "gora://project/" + projectID + "/views/" + stringField(tokenResult.StructuredContent, "view_id") + "/automation"
	if tokenResource, tokenErr := session.ReadResource(ctx, &mcp.ReadResourceParams{URI: tokenURI}); tokenErr == nil && len(tokenResource.Contents) != 0 {
		t.Fatalf("token automation resource unexpectedly available: %+v", tokenResource)
	}
}

func TestMCPServiceCloseWakesAutomationWaiters(t *testing.T) {
	root := t.TempDir()
	entry := filepath.Join(root, "app.gora")
	if err := os.WriteFile(entry, []byte("gora: 1\nkind: app\nviewport: { width: 100, height: 80 }\nentry: main\nscreens:\n  main: { type: spacer }\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	registry := NewRegistry()
	service := NewServiceWithOptions(registry, ServiceOptions{Automation: true})
	project, err := registry.OpenProject(root)
	if err != nil {
		service.Close()
		t.Fatal(err)
	}
	view, err := registry.OpenView(project.ID, "app.gora")
	if err != nil {
		service.Close()
		t.Fatal(err)
	}
	runtime, err := registry.Runtime(project.ID, view.ID)
	if err != nil {
		service.Close()
		t.Fatal(err)
	}
	initial := runtime.AutomationSnapshot()
	done := make(chan error, 1)
	go func() {
		_, waitErr := runtime.WaitForView(context.Background(), studio.WaitForViewRequest{AfterFrameRevision: initial.FrameRevision, AfterFrameSet: true, Timeout: time.Second})
		done <- waitErr
	}()
	service.Close()
	select {
	case waitErr := <-done:
		if !errors.Is(waitErr, studio.ErrRuntimeClosed) {
			t.Fatalf("service close wait error = %v", waitErr)
		}
	case <-time.After(time.Second):
		t.Fatal("service close did not wake waiter")
	}
}
func scrollNestedNumberField(value any, outer, inner string) float64 {
	mapping, _ := value.(map[string]any)
	nested, _ := mapping[outer].(map[string]any)
	result, _ := nested[inner].(float64)
	return result
}

func scrollNumberField(value any, name string) float64 {
	mapping, _ := value.(map[string]any)
	result, _ := mapping[name].(float64)
	return result
}

func findJSONNodeByName(node map[string]any, name string) map[string]any {
	if node == nil {
		return nil
	}
	if got, _ := node["name"].(string); got == name {
		return node
	}
	children, _ := node["children"].([]any)
	for _, child := range children {
		mapping, _ := child.(map[string]any)
		if found := findJSONNodeByName(mapping, name); found != nil {
			return found
		}
	}
	return nil
}

func findJSONNodeByRole(node map[string]any, role string) map[string]any {
	if node == nil {
		return nil
	}
	if got, _ := node["role"].(string); got == role {
		return node
	}
	children, _ := node["children"].([]any)
	for _, child := range children {
		mapping, _ := child.(map[string]any)
		if found := findJSONNodeByRole(mapping, role); found != nil {
			return found
		}
	}
	return nil
}

func findJSONNodeByRoleAndOrientation(node map[string]any, role, orientation string) map[string]any {
	if node == nil {
		return nil
	}
	if gotRole, _ := node["role"].(string); gotRole == role {
		if gotOrientation, _ := node["orientation"].(string); gotOrientation == orientation {
			return node
		}
	}
	children, _ := node["children"].([]any)
	for _, child := range children {
		mapping, _ := child.(map[string]any)
		if found := findJSONNodeByRoleAndOrientation(mapping, role, orientation); found != nil {
			return found
		}
	}
	return nil
}
