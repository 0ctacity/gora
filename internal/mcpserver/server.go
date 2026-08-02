package mcpserver

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"gora/internal/semantic"
)

type Service struct {
	registry *Registry
	server   *mcp.Server
	handler  http.Handler
}

type OpenProjectInput struct {
	Root string `json:"root" jsonschema:"absolute existing directory used as the Gora project containment root"`
}

type ProjectInput struct {
	ProjectID string `json:"project_id" jsonschema:"opaque project identifier returned by gora_open_project"`
}

type OpenViewInput struct {
	ProjectID string `json:"project_id"`
	File      string `json:"file" jsonschema:"root-relative or absolute .gora entry file"`
}

type ViewInput struct {
	ProjectID string `json:"project_id"`
	ViewID    string `json:"view_id"`
}

type ProjectsOutput struct {
	Projects []ProjectSummary `json:"projects"`
}

type ViewsOutput struct {
	ProjectID string        `json:"project_id"`
	Views     []ViewSummary `json:"views"`
}

type ClosedOutput struct {
	Closed bool   `json:"closed"`
	ID     string `json:"id"`
}

type ViewportInput struct {
	ProjectID string `json:"project_id"`
	ViewID    string `json:"view_id"`
	Width     int    `json:"width"`
	Height    int    `json:"height"`
}

type SelectInput struct {
	ProjectID string `json:"project_id"`
	ViewID    string `json:"view_id"`
	Name      string `json:"name"`
}

type ActivateInput struct {
	ProjectID  string `json:"project_id"`
	ViewID     string `json:"view_id"`
	SemanticID string `json:"semantic_id"`
}

type ScrollInput struct {
	ProjectID  string `json:"project_id"`
	ViewID     string `json:"view_id"`
	SemanticID string `json:"semantic_id"`
	Mode       string `json:"mode" jsonschema:"by or to"`
	X          int    `json:"x,omitempty"`
	Y          int    `json:"y,omitempty"`
}

type SetStateInput struct {
	ProjectID string         `json:"project_id"`
	ViewID    string         `json:"view_id"`
	ScopeID   string         `json:"scope_id"`
	Values    map[string]any `json:"values"`
}

type SetControlValueInput struct {
	ProjectID  string `json:"project_id"`
	ViewID     string `json:"view_id"`
	SemanticID string `json:"semantic_id"`
	Value      any    `json:"value"`
}

type ResetStateInput struct {
	ProjectID string `json:"project_id"`
	ViewID    string `json:"view_id"`
	ScopeID   string `json:"scope_id,omitempty"`
}

type RuntimeMutationOutput struct {
	ProjectID string      `json:"project_id"`
	View      ViewSummary `json:"view"`
}

type ControlValueOutput struct {
	ProjectID string      `json:"project_id"`
	View      ViewSummary `json:"view"`
	Value     any         `json:"value"`
}

type CaptureInput struct {
	ProjectID string `json:"project_id"`
	ViewID    string `json:"view_id"`
	Scale     int    `json:"scale"`
	Output    string `json:"output,omitempty"`
}

type CaptureOutput struct {
	ProjectID string      `json:"project_id"`
	View      ViewSummary `json:"view"`
	Width     int         `json:"width"`
	Height    int         `json:"height"`
	Warning   string      `json:"warning,omitempty"`
	Output    string      `json:"output,omitempty"`
}

type ApplyChangesInput struct {
	ProjectID string           `json:"project_id"`
	Changes   []DocumentChange `json:"changes"`
}

type ProjectManifest struct {
	Project   ProjectSummary    `json:"project"`
	Sources   []SourceSummary   `json:"sources"`
	Documents []DocumentSummary `json:"documents"`
	Assets    []string          `json:"assets"`
}

func NewService(registry *Registry) *Service {
	if registry == nil {
		registry = NewRegistry()
	}
	server := mcp.NewServer(&mcp.Implementation{
		Name: "gora", Title: "Gora", Version: "1", Description: "Project-oriented Gora design runtime",
	}, &mcp.ServerOptions{
		Instructions: "Open a project root, open one or more Gora views, then inspect or control them using their project_id and view_id.",
		Capabilities: &mcp.ServerCapabilities{},
	})
	service := &Service{registry: registry, server: server}
	registry.SetChangeHandler(func(projectID string, viewIDs []string) {
		if sources, err := registry.KnownSources(projectID); err == nil {
			for _, source := range sources {
				service.addSourceResources(projectID, source.SourceID)
				base := "gora://project/" + projectID
				_ = service.server.ResourceUpdated(context.Background(), &mcp.ResourceUpdatedNotificationParams{URI: base + "/sources/" + source.SourceID})
				_ = service.server.ResourceUpdated(context.Background(), &mcp.ResourceUpdatedNotificationParams{URI: base + "/documents/" + source.SourceID})
			}
		}
		service.notifyProject(projectID)
		for _, viewID := range viewIDs {
			service.notifyView(projectID, viewID)
		}
	})
	service.registerLifecycleTools()
	service.registerRuntimeTools()
	service.registerEditingTools()
	service.registerResources()
	for _, project := range registry.ListProjects() {
		service.addProjectResources(project.ID)
		for _, view := range project.Views {
			service.addViewResources(project.ID, view.ID)
		}
	}
	streamable := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, &mcp.StreamableHTTPOptions{
		Stateless:                    true,
		PropagateRequestCancellation: true,
	})
	protection := http.NewCrossOriginProtection()
	mux := http.NewServeMux()
	mux.Handle("/mcp", protection.Handler(streamable))
	service.handler = mux
	return service
}

func (s *Service) Handler() http.Handler { return s.handler }

func (s *Service) Close() { s.registry.Close() }

func (s *Service) registerLifecycleTools() {
	readOnly := &mcp.ToolAnnotations{ReadOnlyHint: true, IdempotentHint: true, OpenWorldHint: boolPointer(false)}
	closedWrite := &mcp.ToolAnnotations{ReadOnlyHint: false, IdempotentHint: true, OpenWorldHint: boolPointer(false), DestructiveHint: boolPointer(false)}
	destructive := &mcp.ToolAnnotations{ReadOnlyHint: false, IdempotentHint: true, OpenWorldHint: boolPointer(false), DestructiveHint: boolPointer(true)}
	mcp.AddTool(s.server, &mcp.Tool{Name: "gora_open_project", Description: "Open or reuse a Gora project by canonical root.", Annotations: closedWrite}, func(_ context.Context, _ *mcp.CallToolRequest, input OpenProjectInput) (*mcp.CallToolResult, ProjectSummary, error) {
		result, err := s.registry.OpenProject(input.Root)
		if err == nil {
			s.addProjectResources(result.ID)
			s.notifyProject(result.ID)
		}
		return nil, result, err
	})
	mcp.AddTool(s.server, &mcp.Tool{Name: "gora_list_projects", Description: "List open Gora projects.", Annotations: readOnly}, func(_ context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, ProjectsOutput, error) {
		return nil, ProjectsOutput{Projects: s.registry.ListProjects()}, nil
	})
	mcp.AddTool(s.server, &mcp.Tool{Name: "gora_close_project", Description: "Close a project and all of its views.", Annotations: destructive}, func(_ context.Context, _ *mcp.CallToolRequest, input ProjectInput) (*mcp.CallToolResult, ClosedOutput, error) {
		views := s.registry.ListViews(input.ProjectID)
		sources, _ := s.registry.KnownSources(input.ProjectID)
		err := s.registry.CloseProject(input.ProjectID)
		if err == nil {
			s.removeProjectResources(input.ProjectID, views, sources)
			_ = s.server.ResourceUpdated(context.Background(), &mcp.ResourceUpdatedNotificationParams{URI: "gora://projects"})
		}
		return nil, ClosedOutput{Closed: err == nil, ID: input.ProjectID}, err
	})
	mcp.AddTool(s.server, &mcp.Tool{Name: "gora_open_view", Description: "Open or reuse a live app, component, or token view within a project.", Annotations: closedWrite}, func(_ context.Context, _ *mcp.CallToolRequest, input OpenViewInput) (*mcp.CallToolResult, ViewSummary, error) {
		result, err := s.registry.OpenView(input.ProjectID, input.File)
		if err == nil {
			s.addViewResources(input.ProjectID, result.ID)
			if sources, sourceErr := s.registry.KnownSources(input.ProjectID); sourceErr == nil {
				for _, source := range sources {
					s.addSourceResources(input.ProjectID, source.SourceID)
				}
			}
			s.notifyProject(input.ProjectID)
		}
		return nil, result, err
	})
	mcp.AddTool(s.server, &mcp.Tool{Name: "gora_list_views", Description: "List a project's open views.", Annotations: readOnly}, func(_ context.Context, _ *mcp.CallToolRequest, input ProjectInput) (*mcp.CallToolResult, ViewsOutput, error) {
		if _, err := s.registry.ProjectRoot(input.ProjectID); err != nil {
			return nil, ViewsOutput{}, err
		}
		return nil, ViewsOutput{ProjectID: input.ProjectID, Views: s.registry.ListViews(input.ProjectID)}, nil
	})
	mcp.AddTool(s.server, &mcp.Tool{Name: "gora_close_view", Description: "Close one project view while retaining shared project resources.", Annotations: destructive}, func(_ context.Context, _ *mcp.CallToolRequest, input ViewInput) (*mcp.CallToolResult, ClosedOutput, error) {
		err := s.registry.CloseView(input.ProjectID, input.ViewID)
		if err == nil {
			base := "gora://project/" + input.ProjectID + "/views/" + input.ViewID
			s.server.RemoveResources(base, base+"/tree")
			s.notifyProject(input.ProjectID)
		}
		return nil, ClosedOutput{Closed: err == nil, ID: input.ViewID}, err
	})
}

func (s *Service) registerRuntimeTools() {
	mutation := &mcp.ToolAnnotations{ReadOnlyHint: false, IdempotentHint: false, OpenWorldHint: boolPointer(false), DestructiveHint: boolPointer(false)}
	mcp.AddTool(s.server, &mcp.Tool{Name: "gora_set_viewport", Description: "Set a view's logical viewport.", Annotations: mutation}, func(_ context.Context, _ *mcp.CallToolRequest, input ViewportInput) (*mcp.CallToolResult, RuntimeMutationOutput, error) {
		if input.Width <= 0 || input.Height <= 0 {
			return nil, RuntimeMutationOutput{}, fmt.Errorf("viewport dimensions must be positive")
		}
		runtime, err := s.registry.Runtime(input.ProjectID, input.ViewID)
		if err != nil {
			return nil, RuntimeMutationOutput{}, err
		}
		runtime.SetViewport(input.Width, input.Height)
		return s.runtimeMutation(input.ProjectID, input.ViewID)
	})
	mcp.AddTool(s.server, &mcp.Tool{Name: "gora_select", Description: "Select an app screen or component fixture.", Annotations: mutation}, func(_ context.Context, _ *mcp.CallToolRequest, input SelectInput) (*mcp.CallToolResult, RuntimeMutationOutput, error) {
		runtime, err := s.registry.Runtime(input.ProjectID, input.ViewID)
		if err != nil {
			return nil, RuntimeMutationOutput{}, err
		}
		if !runtime.SelectScreen(input.Name) {
			return nil, RuntimeMutationOutput{}, fmt.Errorf("unknown selection %q", input.Name)
		}
		return s.runtimeMutation(input.ProjectID, input.ViewID)
	})
	mcp.AddTool(s.server, &mcp.Tool{Name: "gora_activate", Description: "Activate one visible enabled semantic control.", Annotations: mutation}, func(_ context.Context, _ *mcp.CallToolRequest, input ActivateInput) (*mcp.CallToolResult, RuntimeMutationOutput, error) {
		runtime, err := s.registry.Runtime(input.ProjectID, input.ViewID)
		if err == nil {
			err = runtime.ActivateSemanticID(input.SemanticID)
		}
		if err != nil {
			return nil, RuntimeMutationOutput{}, err
		}
		return s.runtimeMutation(input.ProjectID, input.ViewID)
	})
	mcp.AddTool(s.server, &mcp.Tool{Name: "gora_scroll", Description: "Scroll one visible scroll node by delta or to an absolute offset.", Annotations: mutation}, func(_ context.Context, _ *mcp.CallToolRequest, input ScrollInput) (*mcp.CallToolResult, RuntimeMutationOutput, error) {
		runtime, err := s.registry.Runtime(input.ProjectID, input.ViewID)
		if err == nil {
			err = runtime.ScrollSemanticID(input.SemanticID, input.Mode, input.X, input.Y)
		}
		if err != nil {
			return nil, RuntimeMutationOutput{}, err
		}
		return s.runtimeMutation(input.ProjectID, input.ViewID)
	})
	mcp.AddTool(s.server, &mcp.Tool{Name: "gora_set_state", Description: "Atomically set typed values in one visible lexical state scope.", Annotations: mutation}, func(_ context.Context, _ *mcp.CallToolRequest, input SetStateInput) (*mcp.CallToolResult, RuntimeMutationOutput, error) {
		runtime, err := s.registry.Runtime(input.ProjectID, input.ViewID)
		if err == nil {
			err = runtime.SetStateValues(input.ScopeID, input.Values)
		}
		if err != nil {
			return nil, RuntimeMutationOutput{}, err
		}
		return s.runtimeMutation(input.ProjectID, input.ViewID)
	})
	mcp.AddTool(s.server, &mcp.Tool{Name: "gora_set_control_value", Description: "Set one visible enabled semantic control's bound value.", Annotations: mutation}, func(_ context.Context, _ *mcp.CallToolRequest, input SetControlValueInput) (*mcp.CallToolResult, ControlValueOutput, error) {
		runtime, err := s.registry.Runtime(input.ProjectID, input.ViewID)
		if err != nil {
			return nil, ControlValueOutput{}, err
		}
		value, err := runtime.SetControlValue(input.SemanticID, input.Value)
		if err != nil {
			return nil, ControlValueOutput{}, err
		}
		view, err := s.registry.ViewSummary(input.ProjectID, input.ViewID)
		if err != nil {
			return nil, ControlValueOutput{}, err
		}
		return nil, ControlValueOutput{ProjectID: input.ProjectID, View: view, Value: value}, nil
	})
	mcp.AddTool(s.server, &mcp.Tool{Name: "gora_reset_state", Description: "Reset one state scope or the selected view context.", Annotations: mutation}, func(_ context.Context, _ *mcp.CallToolRequest, input ResetStateInput) (*mcp.CallToolResult, RuntimeMutationOutput, error) {
		runtime, err := s.registry.Runtime(input.ProjectID, input.ViewID)
		if err == nil {
			err = runtime.ResetStateScope(input.ScopeID)
		}
		if err != nil {
			return nil, RuntimeMutationOutput{}, err
		}
		return s.runtimeMutation(input.ProjectID, input.ViewID)
	})
	mcp.AddTool(s.server, &mcp.Tool{Name: "gora_capture", Description: "Capture the current view as inline PNG content and optionally a new project-contained file.", Annotations: &mcp.ToolAnnotations{ReadOnlyHint: false, IdempotentHint: false, OpenWorldHint: boolPointer(false), DestructiveHint: boolPointer(false)}}, func(_ context.Context, _ *mcp.CallToolRequest, input CaptureInput) (*mcp.CallToolResult, CaptureOutput, error) {
		if input.Scale <= 0 {
			return nil, CaptureOutput{}, fmt.Errorf("scale must be a positive integer")
		}
		runtime, err := s.registry.Runtime(input.ProjectID, input.ViewID)
		if err != nil {
			return nil, CaptureOutput{}, err
		}
		data, warning, err := runtime.CapturePNG(input.Scale)
		if err != nil {
			return nil, CaptureOutput{}, err
		}
		output := ""
		if input.Output != "" {
			root, rootErr := s.registry.ProjectRoot(input.ProjectID)
			if rootErr != nil {
				return nil, CaptureOutput{}, rootErr
			}
			output, err = containedCapturePath(root, input.Output)
			if err == nil {
				err = writeNewFile(output, data)
			}
			if err != nil {
				return nil, CaptureOutput{}, err
			}
		}
		view, err := s.registry.ViewSummary(input.ProjectID, input.ViewID)
		if err != nil {
			return nil, CaptureOutput{}, err
		}
		result := CaptureOutput{ProjectID: input.ProjectID, View: view, Width: view.Viewport.Width * input.Scale, Height: view.Viewport.Height * input.Scale, Warning: warning, Output: output}
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.ImageContent{Data: data, MIMEType: "image/png"}}}, result, nil
	})
}

func (s *Service) registerEditingTools() {
	annotations := &mcp.ToolAnnotations{ReadOnlyHint: false, IdempotentHint: false, OpenWorldHint: boolPointer(false), DestructiveHint: boolPointer(true)}
	mcp.AddTool(s.server, &mcp.Tool{Name: "gora_apply_document_changes", Description: "Atomically create, replace, or patch structured Gora documents.", Annotations: annotations}, func(_ context.Context, _ *mcp.CallToolRequest, input ApplyChangesInput) (*mcp.CallToolResult, ChangeResult, error) {
		result, err := s.registry.ApplyDocumentChanges(input.ProjectID, input.Changes)
		if err != nil {
			var validation *CandidateValidationError
			if errors.As(err, &validation) {
				return &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: err.Error()}}}, ChangeResult{ProjectID: input.ProjectID, Diagnostics: validation.Diagnostics}, nil
			}
			return nil, ChangeResult{}, err
		}
		for _, document := range result.Documents {
			s.addSourceResources(input.ProjectID, document.SourceID)
			base := "gora://project/" + input.ProjectID
			_ = s.server.ResourceUpdated(context.Background(), &mcp.ResourceUpdatedNotificationParams{URI: base + "/sources/" + document.SourceID})
			_ = s.server.ResourceUpdated(context.Background(), &mcp.ResourceUpdatedNotificationParams{URI: base + "/documents/" + document.SourceID})
		}
		s.notifyProject(input.ProjectID)
		for _, view := range s.registry.ListViews(input.ProjectID) {
			s.notifyView(input.ProjectID, view.ID)
		}
		return nil, result, nil
	})
}

func (s *Service) runtimeMutation(projectID, viewID string) (*mcp.CallToolResult, RuntimeMutationOutput, error) {
	view, err := s.registry.ViewSummary(projectID, viewID)
	if err == nil {
		s.notifyView(projectID, viewID)
	}
	return nil, RuntimeMutationOutput{ProjectID: projectID, View: view}, err
}

func (s *Service) registerResources() {
	s.server.AddResource(&mcp.Resource{
		URI: "gora://projects", Name: "gora-projects", Title: "Open Gora projects",
		Description: "Project sessions currently owned by this Gora MCP server.", MIMEType: "application/json",
	}, func(_ context.Context, request *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
		return jsonResource(request.Params.URI, ProjectsOutput{Projects: s.registry.ListProjects()})
	})
	s.server.AddResourceTemplate(&mcp.ResourceTemplate{
		URITemplate: "gora://project/{project_id}/views/{view_id}/nodes/{semantic_id}", Name: "gora-runtime-node", MIMEType: "application/json",
	}, func(_ context.Context, request *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
		projectID, viewID, semanticID, ok := parseNodeURI(request.Params.URI)
		if !ok {
			return nil, mcp.ResourceNotFoundError(request.Params.URI)
		}
		runtime, err := s.registry.Runtime(projectID, viewID)
		if err != nil {
			return nil, mcp.ResourceNotFoundError(request.Params.URI)
		}
		tree, err := runtime.RuntimeTree()
		if err != nil {
			return nil, err
		}
		for _, node := range semantic.Flatten(tree) {
			if node.ID == semanticID {
				return jsonResource(request.Params.URI, node)
			}
		}
		return nil, mcp.ResourceNotFoundError(request.Params.URI)
	})
}

func (s *Service) addProjectResources(projectID string) {
	manifestURI := "gora://project/" + projectID + "/manifest"
	s.server.AddResource(&mcp.Resource{URI: manifestURI, Name: "gora-project-manifest", MIMEType: "application/json"}, func(_ context.Context, request *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
		for _, project := range s.registry.ListProjects() {
			if project.ID == projectID {
				sources, _ := s.registry.KnownSources(projectID)
				documents, assets, _ := s.registry.DocumentSummaries(projectID)
				return jsonResource(request.Params.URI, ProjectManifest{Project: project, Sources: sources, Documents: documents, Assets: assets})
			}
		}
		return nil, mcp.ResourceNotFoundError(request.Params.URI)
	})
	diagnosticsURI := "gora://project/" + projectID + "/diagnostics"
	s.server.AddResource(&mcp.Resource{URI: diagnosticsURI, Name: "gora-project-diagnostics", MIMEType: "application/json"}, func(_ context.Context, request *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
		views := s.registry.ListViews(projectID)
		if _, err := s.registry.ProjectRoot(projectID); err != nil {
			return nil, mcp.ResourceNotFoundError(request.Params.URI)
		}
		return jsonResource(request.Params.URI, ViewsOutput{ProjectID: projectID, Views: views})
	})
	if sources, err := s.registry.KnownSources(projectID); err == nil {
		for _, source := range sources {
			s.addSourceResources(projectID, source.SourceID)
		}
	}
}

func (s *Service) addViewResources(projectID, viewID string) {
	viewURI := "gora://project/" + projectID + "/views/" + viewID
	treeURI := viewURI + "/tree"
	s.server.AddResource(&mcp.Resource{URI: viewURI, Name: "gora-view", MIMEType: "application/json"}, func(_ context.Context, request *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
		view, err := s.registry.ViewSummary(projectID, viewID)
		if err != nil {
			return nil, mcp.ResourceNotFoundError(request.Params.URI)
		}
		return jsonResource(request.Params.URI, view)
	})
	view, err := s.registry.ViewSummary(projectID, viewID)
	if err != nil || !view.RuntimeAvailable {
		return
	}
	s.server.AddResource(&mcp.Resource{URI: treeURI, Name: "gora-runtime-tree", MIMEType: "application/json"}, func(_ context.Context, request *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
		runtime, err := s.registry.Runtime(projectID, viewID)
		if err != nil {
			return nil, mcp.ResourceNotFoundError(request.Params.URI)
		}
		data, _, err := runtime.Inspect("headless")
		if err != nil {
			return nil, err
		}
		return &mcp.ReadResourceResult{Contents: []*mcp.ResourceContents{{URI: request.Params.URI, MIMEType: "application/json", Text: string(data)}}}, nil
	})
}

func (s *Service) addSourceResources(projectID, sourceID string) {
	base := "gora://project/" + projectID
	sourceURI := base + "/sources/" + sourceID
	documentURI := base + "/documents/" + sourceID
	s.server.AddResource(&mcp.Resource{URI: sourceURI, Name: "gora-source", MIMEType: "application/yaml"}, func(_ context.Context, request *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
		resource, err := s.registry.DocumentResource(projectID, sourceID)
		if err != nil {
			return nil, mcp.ResourceNotFoundError(request.Params.URI)
		}
		return &mcp.ReadResourceResult{Contents: []*mcp.ResourceContents{{URI: request.Params.URI, MIMEType: "application/yaml", Text: resource.Source}}}, nil
	})
	s.server.AddResource(&mcp.Resource{URI: documentURI, Name: "gora-document", MIMEType: "application/json"}, func(_ context.Context, request *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
		resource, err := s.registry.DocumentResource(projectID, sourceID)
		if err != nil {
			return nil, mcp.ResourceNotFoundError(request.Params.URI)
		}
		return jsonResource(request.Params.URI, resource)
	})
}

func (s *Service) notifyProject(projectID string) {
	ctx := context.Background()
	_ = s.server.ResourceUpdated(ctx, &mcp.ResourceUpdatedNotificationParams{URI: "gora://projects"})
	_ = s.server.ResourceUpdated(ctx, &mcp.ResourceUpdatedNotificationParams{URI: "gora://project/" + projectID + "/manifest"})
	_ = s.server.ResourceUpdated(ctx, &mcp.ResourceUpdatedNotificationParams{URI: "gora://project/" + projectID + "/diagnostics"})
}

func (s *Service) notifyView(projectID, viewID string) {
	ctx := context.Background()
	base := "gora://project/" + projectID + "/views/" + viewID
	_ = s.server.ResourceUpdated(ctx, &mcp.ResourceUpdatedNotificationParams{URI: base})
	_ = s.server.ResourceUpdated(ctx, &mcp.ResourceUpdatedNotificationParams{URI: base + "/tree"})
}

func (s *Service) removeProjectResources(projectID string, views []ViewSummary, sources []SourceSummary) {
	base := "gora://project/" + projectID
	uris := []string{base + "/manifest", base + "/diagnostics"}
	for _, view := range views {
		viewBase := base + "/views/" + view.ID
		uris = append(uris, viewBase, viewBase+"/tree")
	}
	for _, source := range sources {
		uris = append(uris, base+"/sources/"+source.SourceID, base+"/documents/"+source.SourceID)
	}
	s.server.RemoveResources(uris...)
}

func jsonResource(uri string, value any) (*mcp.ReadResourceResult, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	return &mcp.ReadResourceResult{Contents: []*mcp.ResourceContents{{URI: uri, MIMEType: "application/json", Text: string(data)}}}, nil
}

func boolPointer(value bool) *bool { return &value }

func parseNodeURI(uri string) (string, string, string, bool) {
	const prefix = "gora://project/"
	if !strings.HasPrefix(uri, prefix) {
		return "", "", "", false
	}
	projectAndRest := strings.SplitN(strings.TrimPrefix(uri, prefix), "/views/", 2)
	if len(projectAndRest) != 2 {
		return "", "", "", false
	}
	viewAndNode := strings.SplitN(projectAndRest[1], "/nodes/", 2)
	if len(viewAndNode) != 2 {
		return "", "", "", false
	}
	semanticID, err := url.PathUnescape(viewAndNode[1])
	if err != nil {
		return "", "", "", false
	}
	return projectAndRest[0], viewAndNode[0], semanticID, true
}

func containedCapturePath(root, output string) (string, error) {
	if !filepath.IsAbs(output) {
		output = filepath.Join(root, output)
	}
	if !strings.EqualFold(filepath.Ext(output), ".png") {
		return "", fmt.Errorf("capture output must use the .png extension")
	}
	parent, err := filepath.EvalSymlinks(filepath.Dir(output))
	if err != nil {
		return "", err
	}
	output = filepath.Join(parent, filepath.Base(output))
	relative, err := filepath.Rel(root, output)
	if err != nil || relative == ".." || filepath.IsAbs(relative) || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("capture output is outside project root")
	}
	return output, nil
}

func writeNewFile(path string, data []byte) error {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	ok := false
	defer func() {
		_ = file.Close()
		if !ok {
			_ = os.Remove(path)
		}
	}()
	if _, err := io.Copy(file, bytes.NewReader(data)); err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	ok = true
	return nil
}

func Run(ctx context.Context, listen string, stderr io.Writer) error {
	listener, err := net.Listen("tcp", listen)
	if err != nil {
		return err
	}
	service := NewService(nil)
	defer service.Close()
	httpServer := &http.Server{Handler: service.Handler(), ReadHeaderTimeout: 10 * time.Second}
	serveDone := make(chan error, 1)
	go func() { serveDone <- httpServer.Serve(listener) }()
	fmt.Fprintf(stderr, "Gora MCP listening at http://%s/mcp\n", listener.Addr())
	select {
	case <-ctx.Done():
		shutdownContext, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return httpServer.Shutdown(shutdownContext)
	case err := <-serveDone:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	}
}
