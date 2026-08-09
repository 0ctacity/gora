package mcpserver

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"gora/internal/automation"
	"gora/internal/semantic"
	"gora/internal/session"
	"gora/internal/studio"
)

type Service struct {
	registry   *Registry
	server     *mcp.Server
	handler    http.Handler
	automation bool
	// capturePNG is nil in production and permits renderer-independent MCP
	// comparison tests to supply an immutable PNG/identity pair.
	capturePNG func(*studio.Runtime, int) ([]byte, string, automation.CaptureIdentity, error)
}

type ServiceOptions struct {
	Automation bool
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
	HostMode  string `json:"host_mode,omitempty" jsonschema:"headless, app, or studio; defaults to headless"`
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

type SetFieldDraftInput struct {
	ProjectID  string `json:"project_id"`
	ViewID     string `json:"view_id"`
	SemanticID string `json:"semantic_id"`
	Draft      string `json:"draft"`
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

type FieldDraftOutput struct {
	ProjectID string      `json:"project_id"`
	View      ViewSummary `json:"view"`
	Draft     string      `json:"draft"`
	Value     any         `json:"value"`
	Valid     bool        `json:"valid"`
	Issues    any         `json:"issues,omitempty"`
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

type AssertViewInput struct {
	ProjectID          string                 `json:"project_id"`
	ViewID             string                 `json:"view_id"`
	AfterFrameRevision *uint64                `json:"after_frame_revision,omitempty"`
	TimeoutMS          int                    `json:"timeout_ms,omitempty"`
	Assertions         []automation.Assertion `json:"assertions"`
}

type AssertViewOutput struct {
	ProjectID                 string                       `json:"project_id"`
	ViewID                    string                       `json:"view_id"`
	Passed                    bool                         `json:"passed"`
	Results                   []automation.AssertionResult `json:"results"`
	RuntimeRevision           uint64                       `json:"runtime_revision"`
	FrameRevision             uint64                       `json:"frame_revision"`
	GeometryRevision          uint64                       `json:"geometry_revision"`
	PublishedRuntimeRevision  uint64                       `json:"published_runtime_revision"`
	PublishedGeometryRevision uint64                       `json:"published_geometry_revision"`
	Snapshot                  studio.AutomationSnapshot    `json:"snapshot"`
	Resources                 []string                     `json:"resources"`
}

type CaptureMask struct {
	X      int `json:"x"`
	Y      int `json:"y"`
	Width  int `json:"width"`
	Height int `json:"height"`
}

type CompareCaptureInput struct {
	ProjectID        string        `json:"project_id"`
	ViewID           string        `json:"view_id"`
	Reference        string        `json:"reference"`
	Scale            int           `json:"scale"`
	ChannelTolerance int           `json:"channel_tolerance,omitempty"`
	MaxChangedPixels int           `json:"max_changed_pixels,omitempty"`
	Masks            []CaptureMask `json:"masks,omitempty"`
	SaveDiff         string        `json:"save_diff,omitempty"`
}

type CompareCaptureOutput struct {
	ProjectID                 string       `json:"project_id"`
	ViewID                    string       `json:"view_id"`
	Passed                    bool         `json:"passed"`
	Width                     int          `json:"width"`
	Height                    int          `json:"height"`
	ReferenceWidth            int          `json:"reference_width"`
	ReferenceHeight           int          `json:"reference_height"`
	DimensionMismatch         bool         `json:"dimension_mismatch"`
	ChangedPixels             int          `json:"changed_pixels"`
	ChangedRatio              float64      `json:"changed_ratio"`
	MaximumDelta              int          `json:"maximum_delta"`
	ChangedBounds             *CaptureMask `json:"changed_bounds,omitempty"`
	Scale                     int          `json:"scale"`
	Warning                   string       `json:"warning,omitempty"`
	Reference                 string       `json:"reference"`
	SavedDiff                 string       `json:"saved_diff,omitempty"`
	RuntimeRevision           uint64       `json:"runtime_revision"`
	FrameRevision             uint64       `json:"frame_revision"`
	GeometryRevision          uint64       `json:"geometry_revision"`
	Selection                 string       `json:"selection,omitempty"`
	CaptureFrameRevision      uint64       `json:"capture_frame_revision"`
	CaptureGeometryRevision   uint64       `json:"capture_geometry_revision"`
	PublishedRuntimeRevision  uint64       `json:"published_runtime_revision"`
	PublishedGeometryRevision uint64       `json:"published_geometry_revision"`
	CaptureValid              bool         `json:"capture_valid"`
	Resources                 []string     `json:"resources,omitempty"`
}

type WaitForViewInput struct {
	ProjectID            string  `json:"project_id"`
	ViewID               string  `json:"view_id"`
	AfterFrameRevision   *uint64 `json:"after_frame_revision,omitempty"`
	AfterRuntimeRevision *uint64 `json:"after_runtime_revision,omitempty"`
	Condition            string  `json:"condition,omitempty" jsonschema:"published or idle"`
	StableFrames         int     `json:"stable_frames,omitempty"`
	TimeoutMS            int     `json:"timeout_ms,omitempty"`
}

type WaitForViewOutput struct {
	ProjectID string                    `json:"project_id"`
	ViewID    string                    `json:"view_id"`
	Snapshot  studio.AutomationSnapshot `json:"snapshot"`
	Resources []string                  `json:"resources"`
}

type DispatchInput struct {
	ProjectID string             `json:"project_id"`
	ViewID    string             `json:"view_id"`
	Events    []automation.Event `json:"events"`
	Wait      string             `json:"wait,omitempty" jsonschema:"none, published, or idle"`
	TimeoutMS int                `json:"timeout_ms,omitempty"`
}

type DispatchOutput struct {
	ProjectID string                    `json:"project_id"`
	ViewID    string                    `json:"view_id"`
	Results   []automation.Result       `json:"results"`
	Snapshot  studio.AutomationSnapshot `json:"snapshot"`
	Resources []string                  `json:"resources"`
}

type ConfigureEventTraceInput struct {
	ProjectID string `json:"project_id"`
	ViewID    string `json:"view_id"`
	Enabled   bool   `json:"enabled"`
	Capacity  int    `json:"capacity,omitempty"`
}

type ClearEventTraceInput struct {
	ProjectID string `json:"project_id"`
	ViewID    string `json:"view_id"`
}

type EventTraceOutput struct {
	ProjectID string                   `json:"project_id"`
	ViewID    string                   `json:"view_id"`
	Trace     automation.TraceSnapshot `json:"trace"`
}

type SetAutomationClipboardInput struct {
	ProjectID string `json:"project_id"`
	ViewID    string `json:"view_id"`
	Text      string `json:"text"`
}

type ReadAutomationClipboardInput struct {
	ProjectID string `json:"project_id"`
	ViewID    string `json:"view_id"`
}

type AutomationClipboardOutput struct {
	ProjectID string `json:"project_id"`
	ViewID    string `json:"view_id"`
	Text      string `json:"text,omitempty"`
}

type SetViewClockInput struct {
	ProjectID string `json:"project_id"`
	ViewID    string `json:"view_id"`
	Mode      string `json:"mode" jsonschema:"real or frozen"`
}

type AdvanceViewClockInput struct {
	ProjectID    string `json:"project_id"`
	ViewID       string `json:"view_id"`
	DeltaMS      int64  `json:"delta_ms"`
	RunUntilIdle bool   `json:"run_until_idle,omitempty"`
}

type ViewClockOutput struct {
	ProjectID string                    `json:"project_id"`
	ViewID    string                    `json:"view_id"`
	Clock     studio.ViewClockSnapshot  `json:"clock"`
	Snapshot  studio.AutomationSnapshot `json:"snapshot"`
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
	return NewServiceWithOptions(registry, ServiceOptions{})
}

func NewServiceWithOptions(registry *Registry, options ServiceOptions) *Service {
	if registry == nil {
		registry = NewRegistry()
	}
	var service *Service
	server := mcp.NewServer(&mcp.Implementation{
		Name: "gora", Title: "Gora", Version: "1", Description: "Project-oriented Gora design runtime",
	}, &mcp.ServerOptions{
		Instructions: "Open a project root, open one or more Gora views, then inspect or control them using their project_id and view_id.",
		Capabilities: &mcp.ServerCapabilities{},
		SubscribeHandler: func(ctx context.Context, request *mcp.SubscribeRequest) error {
			if service == nil {
				return fmt.Errorf("MCP service is not initialized")
			}
			return service.validateSubscription(ctx, request)
		},
		UnsubscribeHandler: func(_ context.Context, request *mcp.UnsubscribeRequest) error {
			if request == nil || request.Params == nil || request.Params.URI == "" {
				return fmt.Errorf("resource URI is required")
			}
			// The SDK owns subscription bookkeeping. Unsubscribe must remain
			// valid even when a project/view was closed after subscribing so the
			// SDK can release its session-side stream cleanly.
			return nil
		},
	})
	service = &Service{registry: registry, server: server, automation: options.Automation}
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
	if options.Automation {
		service.registerAutomationTools()
		service.registerAutomationResources()
		service.registerPhase6Tools()
		service.registerPhase6Resources()
	}
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

func (s *Service) AutomationEnabled() bool { return s != nil && s.automation }

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
		result, err := s.registry.OpenView(input.ProjectID, input.File, input.HostMode)
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
			resources := []string{base, base + "/tree"}
			if s.automation {
				resources = append(resources, base+"/automation", base+"/automation/trace", base+"/automation/overlay")
			}
			s.server.RemoveResources(resources...)
			s.notifyProject(input.ProjectID)
		}
		return nil, ClosedOutput{Closed: err == nil, ID: input.ViewID}, err
	})
}

func (s *Service) registerRuntimeTools() {
	mutation := &mcp.ToolAnnotations{ReadOnlyHint: false, IdempotentHint: false, OpenWorldHint: boolPointer(false), DestructiveHint: boolPointer(false)}
	mcp.AddTool(s.server, &mcp.Tool{Name: "gora_set_viewport", Description: "Set a view's logical viewport.", Annotations: mutation}, func(ctx context.Context, _ *mcp.CallToolRequest, input ViewportInput) (*mcp.CallToolResult, RuntimeMutationOutput, error) {
		if input.Width <= 0 || input.Height <= 0 {
			return nil, RuntimeMutationOutput{}, fmt.Errorf("viewport dimensions must be positive")
		}
		backend, err := s.registry.Backend(input.ProjectID, input.ViewID)
		if err != nil {
			return nil, RuntimeMutationOutput{}, err
		}
		if err := backend.SetViewport(ctx, input.Width, input.Height); err != nil {
			return nil, RuntimeMutationOutput{}, err
		}
		return s.runtimeMutation(input.ProjectID, input.ViewID)
	})
	mcp.AddTool(s.server, &mcp.Tool{Name: "gora_select", Description: "Select an app screen or component fixture.", Annotations: mutation}, func(ctx context.Context, _ *mcp.CallToolRequest, input SelectInput) (*mcp.CallToolResult, RuntimeMutationOutput, error) {
		backend, err := s.registry.Backend(input.ProjectID, input.ViewID)
		if err != nil {
			return nil, RuntimeMutationOutput{}, err
		}
		if err := backend.Select(ctx, input.Name); err != nil {
			return nil, RuntimeMutationOutput{}, err
		}
		return s.runtimeMutation(input.ProjectID, input.ViewID)
	})
	mcp.AddTool(s.server, &mcp.Tool{Name: "gora_activate", Description: "Activate one visible enabled semantic control.", Annotations: mutation}, func(ctx context.Context, _ *mcp.CallToolRequest, input ActivateInput) (*mcp.CallToolResult, RuntimeMutationOutput, error) {
		backend, err := s.registry.Backend(input.ProjectID, input.ViewID)
		if err == nil {
			err = backend.Activate(ctx, input.SemanticID)
		}
		if err != nil {
			return nil, RuntimeMutationOutput{}, err
		}
		return s.runtimeMutation(input.ProjectID, input.ViewID)
	})
	mcp.AddTool(s.server, &mcp.Tool{Name: "gora_scroll", Description: "Scroll one visible scroll node by delta or to an absolute offset.", Annotations: mutation}, func(ctx context.Context, _ *mcp.CallToolRequest, input ScrollInput) (*mcp.CallToolResult, RuntimeMutationOutput, error) {
		backend, err := s.registry.Backend(input.ProjectID, input.ViewID)
		if err == nil {
			err = backend.Scroll(ctx, input.SemanticID, input.Mode, input.X, input.Y)
		}
		if err != nil {
			return nil, RuntimeMutationOutput{}, err
		}
		return s.runtimeMutation(input.ProjectID, input.ViewID)
	})
	mcp.AddTool(s.server, &mcp.Tool{Name: "gora_set_state", Description: "Atomically set typed values in one visible lexical state scope.", Annotations: mutation}, func(ctx context.Context, _ *mcp.CallToolRequest, input SetStateInput) (*mcp.CallToolResult, RuntimeMutationOutput, error) {
		backend, err := s.registry.Backend(input.ProjectID, input.ViewID)
		if err == nil {
			err = backend.SetState(ctx, input.ScopeID, input.Values)
		}
		if err != nil {
			return nil, RuntimeMutationOutput{}, err
		}
		return s.runtimeMutation(input.ProjectID, input.ViewID)
	})
	mcp.AddTool(s.server, &mcp.Tool{Name: "gora_set_control_value", Description: "Set one visible enabled semantic control's bound value.", Annotations: mutation}, func(ctx context.Context, _ *mcp.CallToolRequest, input SetControlValueInput) (*mcp.CallToolResult, ControlValueOutput, error) {
		backend, backendErr := s.registry.Backend(input.ProjectID, input.ViewID)
		if backendErr != nil {
			return nil, ControlValueOutput{}, backendErr
		}
		if backend.Mode() != session.HostModeHeadless {
			data, commandErr := s.registry.HostCommandResult(ctx, input.ProjectID, input.ViewID, "set_control_value", map[string]any{"semantic_id": input.SemanticID, "value": input.Value})
			if commandErr != nil {
				return nil, ControlValueOutput{}, commandErr
			}
			var result struct {
				Value any `json:"value"`
			}
			_ = json.Unmarshal(data, &result)
			view, summaryErr := s.registry.ViewSummary(input.ProjectID, input.ViewID)
			if summaryErr != nil {
				return nil, ControlValueOutput{}, summaryErr
			}
			s.notifyView(input.ProjectID, input.ViewID)
			return nil, ControlValueOutput{ProjectID: input.ProjectID, View: view, Value: result.Value}, nil
		}
		runtime, err := s.registry.Runtime(input.ProjectID, input.ViewID)
		if err != nil {
			return nil, ControlValueOutput{}, err
		}
		value, err := runtime.SetControlValue(input.SemanticID, input.Value)
		if err != nil {
			return nil, ControlValueOutput{}, err
		}
		if _, err := runtime.RuntimeTree(); err != nil {
			return nil, ControlValueOutput{}, err
		}
		if s.automation {
			if err := s.registry.RefreshAutomationDriver(input.ProjectID, input.ViewID); err != nil {
				return nil, ControlValueOutput{}, err
			}
		}
		view, err := s.registry.ViewSummary(input.ProjectID, input.ViewID)
		if err != nil {
			return nil, ControlValueOutput{}, err
		}
		s.notifyView(input.ProjectID, input.ViewID)
		return nil, ControlValueOutput{ProjectID: input.ProjectID, View: view, Value: value}, nil
	})
	mcp.AddTool(s.server, &mcp.Tool{Name: "gora_set_field_draft", Description: "Set one visible editable field's draft; valid typed values publish immediately.", Annotations: mutation}, func(ctx context.Context, _ *mcp.CallToolRequest, input SetFieldDraftInput) (*mcp.CallToolResult, FieldDraftOutput, error) {
		backend, backendErr := s.registry.Backend(input.ProjectID, input.ViewID)
		if backendErr != nil {
			return nil, FieldDraftOutput{}, backendErr
		}
		if backend.Mode() != session.HostModeHeadless {
			data, commandErr := s.registry.HostCommandResult(ctx, input.ProjectID, input.ViewID, "set_field_draft", map[string]any{"semantic_id": input.SemanticID, "draft": input.Draft})
			if commandErr != nil {
				return nil, FieldDraftOutput{}, commandErr
			}
			var result struct {
				Draft string `json:"draft"`
				Value any    `json:"value"`
				Valid bool   `json:"valid"`
			}
			_ = json.Unmarshal(data, &result)
			view, summaryErr := s.registry.ViewSummary(input.ProjectID, input.ViewID)
			if summaryErr != nil {
				return nil, FieldDraftOutput{}, summaryErr
			}
			s.notifyView(input.ProjectID, input.ViewID)
			return nil, FieldDraftOutput{ProjectID: input.ProjectID, View: view, Draft: result.Draft, Value: result.Value, Valid: result.Valid}, nil
		}
		runtime, err := s.registry.Runtime(input.ProjectID, input.ViewID)
		if err != nil {
			return nil, FieldDraftOutput{}, err
		}
		err = runtime.SetFieldDraft(input.SemanticID, input.Draft)
		if err != nil {
			return nil, FieldDraftOutput{}, err
		}
		view, err := s.registry.ViewSummary(input.ProjectID, input.ViewID)
		if err != nil {
			return nil, FieldDraftOutput{}, err
		}
		tree, err := runtime.RuntimeTree()
		if err != nil {
			return nil, FieldDraftOutput{}, err
		}
		if s.automation {
			if err := s.registry.RefreshAutomationDriver(input.ProjectID, input.ViewID); err != nil {
				return nil, FieldDraftOutput{}, err
			}
		}
		var field *semantic.Node
		for _, node := range semantic.Flatten(tree) {
			if node.ID == input.SemanticID {
				field = node
				break
			}
		}
		if field == nil || field.Role != "textbox" {
			return nil, FieldDraftOutput{}, fmt.Errorf("unknown semantic field %q", input.SemanticID)
		}
		s.notifyView(input.ProjectID, input.ViewID)
		valid := field.Valid != nil && *field.Valid
		return nil, FieldDraftOutput{ProjectID: input.ProjectID, View: view, Draft: fmt.Sprint(field.Value), Value: field.CommittedValue, Valid: valid, Issues: field.Issues}, nil
	})
	mcp.AddTool(s.server, &mcp.Tool{Name: "gora_submit_form", Description: "Validate and submit one local form by semantic ID.", Annotations: mutation}, func(ctx context.Context, _ *mcp.CallToolRequest, input ActivateInput) (*mcp.CallToolResult, RuntimeMutationOutput, error) {
		backend, err := s.registry.Backend(input.ProjectID, input.ViewID)
		if err != nil {
			return nil, RuntimeMutationOutput{}, err
		}
		if backend.Mode() != session.HostModeHeadless {
			err = backend.SubmitForm(ctx, input.SemanticID)
		} else {
			runtime, runtimeErr := s.registry.Runtime(input.ProjectID, input.ViewID)
			if runtimeErr != nil {
				return nil, RuntimeMutationOutput{}, runtimeErr
			}
			err = runtime.SubmitForm(input.SemanticID)
		}
		if err != nil {
			return nil, RuntimeMutationOutput{}, err
		}
		return s.runtimeMutation(input.ProjectID, input.ViewID)
	})
	mcp.AddTool(s.server, &mcp.Tool{Name: "gora_reset_form", Description: "Reset only the states bound to fields in one local form.", Annotations: mutation}, func(ctx context.Context, _ *mcp.CallToolRequest, input ActivateInput) (*mcp.CallToolResult, RuntimeMutationOutput, error) {
		backend, err := s.registry.Backend(input.ProjectID, input.ViewID)
		if err != nil {
			return nil, RuntimeMutationOutput{}, err
		}
		if backend.Mode() != session.HostModeHeadless {
			err = backend.ResetForm(ctx, input.SemanticID)
		} else {
			runtime, runtimeErr := s.registry.Runtime(input.ProjectID, input.ViewID)
			if runtimeErr != nil {
				return nil, RuntimeMutationOutput{}, runtimeErr
			}
			err = runtime.ResetForm(input.SemanticID)
		}
		if err != nil {
			return nil, RuntimeMutationOutput{}, err
		}
		return s.runtimeMutation(input.ProjectID, input.ViewID)
	})
	mcp.AddTool(s.server, &mcp.Tool{Name: "gora_reset_state", Description: "Reset one state scope or the selected view context.", Annotations: mutation}, func(ctx context.Context, _ *mcp.CallToolRequest, input ResetStateInput) (*mcp.CallToolResult, RuntimeMutationOutput, error) {
		backend, err := s.registry.Backend(input.ProjectID, input.ViewID)
		if err != nil {
			return nil, RuntimeMutationOutput{}, err
		}
		if backend.Mode() != session.HostModeHeadless {
			err = backend.ResetState(ctx, input.ScopeID)
		} else {
			runtime, runtimeErr := s.registry.Runtime(input.ProjectID, input.ViewID)
			if runtimeErr != nil {
				return nil, RuntimeMutationOutput{}, runtimeErr
			}
			err = runtime.ResetStateScope(input.ScopeID)
		}
		if err != nil {
			return nil, RuntimeMutationOutput{}, err
		}
		return s.runtimeMutation(input.ProjectID, input.ViewID)
	})
	mcp.AddTool(s.server, &mcp.Tool{Name: "gora_capture", Description: "Capture the current view as inline PNG content and optionally a new project-contained file.", Annotations: &mcp.ToolAnnotations{ReadOnlyHint: false, IdempotentHint: false, OpenWorldHint: boolPointer(false), DestructiveHint: boolPointer(false)}}, func(ctx context.Context, _ *mcp.CallToolRequest, input CaptureInput) (*mcp.CallToolResult, CaptureOutput, error) {
		if input.Scale <= 0 {
			return nil, CaptureOutput{}, fmt.Errorf("scale must be a positive integer")
		}
		backend, err := s.registry.Backend(input.ProjectID, input.ViewID)
		if err != nil {
			return nil, CaptureOutput{}, err
		}
		if backend.Mode() != session.HostModeHeadless {
			data, commandErr := s.registry.HostCommandResult(ctx, input.ProjectID, input.ViewID, "capture", map[string]any{"scale": input.Scale})
			if commandErr != nil {
				return nil, CaptureOutput{}, commandErr
			}
			var result struct {
				PNGBase64 string `json:"png_base64"`
				Warning   string `json:"warning"`
			}
			if unmarshalErr := json.Unmarshal(data, &result); unmarshalErr != nil {
				return nil, CaptureOutput{}, unmarshalErr
			}
			png, decodeErr := base64.StdEncoding.DecodeString(result.PNGBase64)
			if decodeErr != nil {
				return nil, CaptureOutput{}, decodeErr
			}
			output := ""
			if input.Output != "" {
				root, rootErr := s.registry.ProjectRoot(input.ProjectID)
				if rootErr != nil {
					return nil, CaptureOutput{}, rootErr
				}
				output, err = containedCapturePath(root, input.Output)
				if err == nil {
					err = writeNewFile(output, png)
				}
				if err != nil {
					return nil, CaptureOutput{}, err
				}
			}
			view, summaryErr := s.registry.ViewSummary(input.ProjectID, input.ViewID)
			if summaryErr != nil {
				return nil, CaptureOutput{}, summaryErr
			}
			resultOutput := CaptureOutput{ProjectID: input.ProjectID, View: view, Width: view.Viewport.Width * input.Scale, Height: view.Viewport.Height * input.Scale, Warning: result.Warning, Output: output}
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.ImageContent{Data: png, MIMEType: "image/png"}}}, resultOutput, nil
		}
		runtime, err := s.registry.Runtime(input.ProjectID, input.ViewID)
		if err != nil {
			return nil, CaptureOutput{}, err
		}
		if s.automation && s.registry.ConsumeTestFault(input.ProjectID, input.ViewID, "capture_failure") {
			return nil, CaptureOutput{}, fmt.Errorf("injected capture failure")
		}
		before := runtime.AutomationSnapshot()
		data, warning, err := runtime.CapturePNG(input.Scale)
		if err != nil {
			return nil, CaptureOutput{}, err
		}
		if s.automation && warning == "" {
			if err := s.registry.RefreshAutomationDriver(input.ProjectID, input.ViewID); err != nil {
				return nil, CaptureOutput{}, err
			}
		}
		if after := runtime.AutomationSnapshot(); after.FrameRevision != before.FrameRevision {
			s.notifyView(input.ProjectID, input.ViewID)
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

func (s *Service) registerAutomationTools() {
	readOnly := &mcp.ToolAnnotations{ReadOnlyHint: true, IdempotentHint: true, OpenWorldHint: boolPointer(false)}
	mcp.AddTool(s.server, &mcp.Tool{Name: "gora_assert_view", Description: "Evaluate finite deterministic assertions against one immutable published view snapshot.", Annotations: readOnly}, func(ctx context.Context, _ *mcp.CallToolRequest, input AssertViewInput) (*mcp.CallToolResult, AssertViewOutput, error) {
		backend, backendErr := s.registry.Backend(input.ProjectID, input.ViewID)
		if backendErr != nil {
			return nil, AssertViewOutput{}, backendErr
		}
		if backend.Mode() != session.HostModeHeadless {
			data, commandErr := s.registry.HostCommandResult(ctx, input.ProjectID, input.ViewID, "assert", map[string]any{"assertions": input.Assertions})
			if commandErr != nil {
				return nil, AssertViewOutput{}, commandErr
			}
			var report automation.AssertionReport
			if err := json.Unmarshal(data, &report); err != nil {
				return nil, AssertViewOutput{}, err
			}
			snapshot, snapshotErr := s.registry.AutomationSnapshot(input.ProjectID, input.ViewID)
			if snapshotErr != nil && snapshotErr.Error() != "" {
				return nil, AssertViewOutput{}, snapshotErr
			}
			output := AssertViewOutput{ProjectID: input.ProjectID, ViewID: input.ViewID, Passed: report.Passed, Results: report.Results, RuntimeRevision: snapshot.RuntimeRevision, FrameRevision: snapshot.FrameRevision, GeometryRevision: snapshot.GeometryRevision, PublishedRuntimeRevision: snapshot.PublishedRuntimeRevision, PublishedGeometryRevision: snapshot.PublishedGeometryRevision, Snapshot: snapshot, Resources: automationResources(input.ProjectID, input.ViewID)}
			return nil, output, nil
		}
		runtime, err := s.registry.Runtime(input.ProjectID, input.ViewID)
		if err != nil {
			return nil, AssertViewOutput{}, err
		}
		timeout := 5 * time.Second
		if input.TimeoutMS != 0 {
			if input.TimeoutMS < 1 || input.TimeoutMS > 60000 {
				return nil, AssertViewOutput{}, fmt.Errorf("timeout_ms must be between 1 and 60000")
			}
			timeout = time.Duration(input.TimeoutMS) * time.Millisecond
		}
		if input.AfterFrameRevision != nil {
			request := studio.WaitForViewRequest{Condition: "published", StableFrames: 1, Timeout: timeout, AfterFrameSet: true, AfterFrameRevision: *input.AfterFrameRevision, AllowAlreadySatisfied: true}
			if _, waitErr := runtime.WaitForView(ctx, request); waitErr != nil {
				output := AssertViewOutput{ProjectID: input.ProjectID, ViewID: input.ViewID, Snapshot: runtime.AutomationSnapshot(), Resources: automationResources(input.ProjectID, input.ViewID)}
				var timeoutErr *studio.WaitTimeoutError
				if errors.As(waitErr, &timeoutErr) {
					return &mcp.CallToolResult{IsError: true, StructuredContent: output, Content: []mcp.Content{&mcp.TextContent{Text: waitErr.Error()}}}, output, nil
				}
				return nil, output, waitErr
			}
		}
		assertionSnapshot := runtime.AutomationAssertionSnapshot()
		report, err := automation.EvaluateAssertions(assertionSnapshot, input.Assertions)
		output := AssertViewOutput{ProjectID: input.ProjectID, ViewID: input.ViewID, Passed: report.Passed, Results: report.Results, RuntimeRevision: report.RuntimeRevision, FrameRevision: report.FrameRevision, GeometryRevision: report.GeometryRevision, PublishedRuntimeRevision: report.PublishedRuntimeRevision, PublishedGeometryRevision: report.PublishedGeometryRevision, Snapshot: runtime.AutomationSnapshot(), Resources: automationResources(input.ProjectID, input.ViewID)}
		if err != nil {
			return nil, AssertViewOutput{}, err
		}
		return nil, output, nil
	})
	captureMutation := &mcp.ToolAnnotations{ReadOnlyHint: false, IdempotentHint: false, OpenWorldHint: boolPointer(false), DestructiveHint: boolPointer(false)}
	mcp.AddTool(s.server, &mcp.Tool{Name: "gora_compare_capture", Description: "Compare an overlay-free current view PNG with a contained reference image using deterministic NRGBA pixels.", Annotations: captureMutation}, func(ctx context.Context, _ *mcp.CallToolRequest, input CompareCaptureInput) (*mcp.CallToolResult, CompareCaptureOutput, error) {
		if input.Scale <= 0 {
			return nil, CompareCaptureOutput{}, fmt.Errorf("scale must be a positive integer")
		}
		if input.ChannelTolerance < 0 || input.ChannelTolerance > 255 {
			return nil, CompareCaptureOutput{}, fmt.Errorf("channel_tolerance must be between 0 and 255")
		}
		if input.MaxChangedPixels < 0 {
			return nil, CompareCaptureOutput{}, fmt.Errorf("max_changed_pixels must be non-negative")
		}
		for _, mask := range input.Masks {
			if mask.X < 0 || mask.Y < 0 || mask.Width < 0 || mask.Height < 0 {
				return nil, CompareCaptureOutput{}, fmt.Errorf("mask coordinates and dimensions must be non-negative")
			}
		}
		backend, err := s.registry.Backend(input.ProjectID, input.ViewID)
		if err != nil {
			return nil, CompareCaptureOutput{}, err
		}
		var runtime *studio.Runtime
		if backend.Mode() == session.HostModeHeadless {
			runtime, err = s.registry.Runtime(input.ProjectID, input.ViewID)
			if err != nil {
				return nil, CompareCaptureOutput{}, err
			}
			if s.automation && s.registry.ConsumeTestFault(input.ProjectID, input.ViewID, "capture_failure") {
				return nil, CompareCaptureOutput{}, fmt.Errorf("injected capture failure")
			}
		}
		root, err := s.registry.ProjectRoot(input.ProjectID)
		if err != nil {
			return nil, CompareCaptureOutput{}, err
		}
		referencePath, err := containedExistingCapturePath(root, input.Reference)
		if err != nil {
			return nil, CompareCaptureOutput{}, err
		}
		reference, err := os.ReadFile(referencePath)
		if err != nil {
			return nil, CompareCaptureOutput{}, err
		}
		var savePath string
		if input.SaveDiff != "" {
			savePath, err = containedCapturePath(root, input.SaveDiff)
			if err != nil {
				return nil, CompareCaptureOutput{}, err
			}
			if _, statErr := os.Lstat(savePath); statErr == nil {
				return nil, CompareCaptureOutput{}, fmt.Errorf("diff output already exists")
			} else if !os.IsNotExist(statErr) {
				return nil, CompareCaptureOutput{}, statErr
			}
		}
		var current []byte
		var warning string
		var captureIdentity automation.CaptureIdentity
		if runtime == nil {
			backend, backendErr := s.registry.Backend(input.ProjectID, input.ViewID)
			if backendErr != nil {
				return nil, CompareCaptureOutput{}, backendErr
			}
			current, warning, captureIdentity, err = backend.Capture(ctx, input.Scale)
		} else {
			capture := s.capturePNG
			if capture == nil {
				capture = func(runtime *studio.Runtime, scale int) ([]byte, string, automation.CaptureIdentity, error) {
					return runtime.CapturePNGReadOnly(scale)
				}
			}
			current, warning, captureIdentity, err = capture(runtime, input.Scale)
		}
		if err != nil {
			return nil, CompareCaptureOutput{}, err
		}
		masks := make([]image.Rectangle, 0, len(input.Masks))
		for _, mask := range input.Masks {
			masks = append(masks, image.Rect(mask.X*input.Scale, mask.Y*input.Scale, (mask.X+mask.Width)*input.Scale, (mask.Y+mask.Height)*input.Scale))
		}
		comparison, err := automation.ComparePNG(reference, current, automation.CompareOptions{ChannelTolerance: input.ChannelTolerance, MaxChangedPixels: input.MaxChangedPixels, Masks: masks})
		if err != nil {
			return nil, CompareCaptureOutput{}, err
		}
		if savePath != "" && !comparison.Passed {
			if err := writeNewFile(savePath, comparison.DiffPNG); err != nil {
				return nil, CompareCaptureOutput{}, err
			}
		}
		output := CompareCaptureOutput{ProjectID: input.ProjectID, ViewID: input.ViewID, Passed: comparison.Passed, Width: comparison.Width, Height: comparison.Height, ReferenceWidth: comparison.ReferenceWidth, ReferenceHeight: comparison.ReferenceHeight, DimensionMismatch: comparison.DimensionMismatch, ChangedPixels: comparison.ChangedPixels, ChangedRatio: comparison.ChangedRatio, MaximumDelta: comparison.MaximumDelta, Scale: input.Scale, Warning: warning, Reference: input.Reference, SavedDiff: func() string {
			if comparison.Passed {
				return ""
			}
			return input.SaveDiff
		}(), RuntimeRevision: captureIdentity.RuntimeRevision, FrameRevision: captureIdentity.FrameRevision, GeometryRevision: captureIdentity.GeometryRevision, Selection: captureIdentity.Selection, CaptureFrameRevision: captureIdentity.FrameRevision, CaptureGeometryRevision: captureIdentity.GeometryRevision, PublishedRuntimeRevision: captureIdentity.PublishedRuntimeRevision, PublishedGeometryRevision: captureIdentity.PublishedGeometryRevision, CaptureValid: captureIdentity.Valid, Resources: automationResources(input.ProjectID, input.ViewID)}
		if !comparison.ChangedBounds.Empty() {
			bounds := comparison.ChangedBounds
			output.ChangedBounds = &CaptureMask{X: bounds.Min.X, Y: bounds.Min.Y, Width: bounds.Dx(), Height: bounds.Dy()}
		}
		if comparison.Passed {
			return nil, output, nil
		}
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.ImageContent{Data: comparison.CurrentPNG, MIMEType: "image/png"}, &mcp.ImageContent{Data: comparison.DiffPNG, MIMEType: "image/png"}}, StructuredContent: output}, output, nil
	})
	mcp.AddTool(s.server, &mcp.Tool{Name: "gora_wait_for_view", Description: "Wait for a deterministic published or idle view frame and return its automation snapshot.", Annotations: readOnly}, func(ctx context.Context, _ *mcp.CallToolRequest, input WaitForViewInput) (*mcp.CallToolResult, WaitForViewOutput, error) {
		runtime, runtimeErr := s.registry.Runtime(input.ProjectID, input.ViewID)
		if runtimeErr != nil {
			// Attached views use their host-owned long-poll session bridge.
			runtime = nil
		}
		request := studio.WaitForViewRequest{Condition: input.Condition, StableFrames: input.StableFrames}
		if input.AfterFrameRevision != nil {
			request.AfterFrameRevision = *input.AfterFrameRevision
			request.AfterFrameSet = true
			request.AllowAlreadySatisfied = true
		}
		if input.AfterRuntimeRevision != nil {
			request.AfterRuntimeRevision = *input.AfterRuntimeRevision
			request.AfterRuntimeSet = true
			request.AllowAlreadySatisfied = true
		}
		if input.TimeoutMS == 0 {
			request.Timeout = 5 * time.Second
		} else if input.TimeoutMS < 1 || input.TimeoutMS > 60000 {
			return nil, WaitForViewOutput{}, fmt.Errorf("timeout_ms must be between 1 and 60000")
		} else {
			request.Timeout = time.Duration(input.TimeoutMS) * time.Millisecond
		}
		var snapshot studio.AutomationSnapshot
		var waitErr error
		if runtime != nil {
			snapshot, waitErr = runtime.WaitForView(ctx, request)
		} else {
			snapshot, waitErr = s.registry.WaitForView(ctx, input.ProjectID, input.ViewID, request)
		}
		output := WaitForViewOutput{
			ProjectID: input.ProjectID,
			ViewID:    input.ViewID,
			Snapshot:  snapshot,
			Resources: []string{
				"gora://project/" + input.ProjectID + "/views/" + input.ViewID + "/tree",
				"gora://project/" + input.ProjectID + "/views/" + input.ViewID + "/automation",
			},
		}
		if waitErr == nil {
			return nil, output, nil
		}
		var timeout *studio.WaitTimeoutError
		if errors.As(waitErr, &timeout) {
			return &mcp.CallToolResult{IsError: true, StructuredContent: output, Content: []mcp.Content{&mcp.TextContent{Text: waitErr.Error()}}}, output, nil
		}
		return nil, output, waitErr
	})
	mutation := &mcp.ToolAnnotations{ReadOnlyHint: false, IdempotentHint: false, OpenWorldHint: boolPointer(false), DestructiveHint: boolPointer(false)}
	mcp.AddTool(s.server, &mcp.Tool{Name: "gora_dispatch_input", Description: "Dispatch an ordered, renderer-neutral pointer, keyboard, scroll, or editing batch to one MCP view.", Annotations: mutation}, func(ctx context.Context, _ *mcp.CallToolRequest, input DispatchInput) (*mcp.CallToolResult, DispatchOutput, error) {
		if input.Wait == "" {
			input.Wait = "published"
		}
		if input.Wait != "none" && input.Wait != "published" && input.Wait != "idle" {
			return nil, DispatchOutput{}, fmt.Errorf("wait must be none, published, or idle")
		}
		timeout := 5 * time.Second
		if input.TimeoutMS != 0 {
			if input.TimeoutMS < 1 || input.TimeoutMS > 60000 {
				return nil, DispatchOutput{}, fmt.Errorf("timeout_ms must be between 1 and 60000")
			}
			timeout = time.Duration(input.TimeoutMS) * time.Millisecond
		}
		runtime, err := s.registry.Runtime(input.ProjectID, input.ViewID)
		if err != nil {
			data, commandErr := s.registry.HostCommandResult(ctx, input.ProjectID, input.ViewID, "dispatch", map[string]any{"events": input.Events})
			if commandErr != nil {
				return nil, DispatchOutput{}, commandErr
			}
			var results []automation.Result
			_ = json.Unmarshal(data, &results)
			snapshot, snapshotErr := s.registry.AutomationSnapshot(input.ProjectID, input.ViewID)
			if snapshotErr != nil && snapshotErr.Error() != "" {
				return nil, DispatchOutput{}, snapshotErr
			}
			return nil, DispatchOutput{ProjectID: input.ProjectID, ViewID: input.ViewID, Results: results, Snapshot: snapshot, Resources: automationResources(input.ProjectID, input.ViewID)}, nil
		}
		driver, err := s.registry.AutomationDriver(input.ProjectID, input.ViewID)
		if err != nil {
			return nil, DispatchOutput{}, err
		}
		results, dispatchErr := driver.Dispatch(input.Events)
		snapshot := runtime.AutomationSnapshot()
		output := DispatchOutput{ProjectID: input.ProjectID, ViewID: input.ViewID, Results: results, Snapshot: snapshot, Resources: automationResources(input.ProjectID, input.ViewID)}
		if dispatchErr != nil {
			if len(results) != 0 {
				// A runtime error after an earlier valid event may leave that
				// prefix committed; publish its resulting snapshot.
				s.notifyView(input.ProjectID, input.ViewID)
			}
			return &mcp.CallToolResult{IsError: true, StructuredContent: output, Content: []mcp.Content{&mcp.TextContent{Text: dispatchErr.Error()}}}, output, nil
		}
		// Dispatch publishes each changed event synchronously through Runtime;
		// notify subscribers after the final batch frame is installed.
		s.notifyView(input.ProjectID, input.ViewID)
		if input.Wait != "none" {
			request := studio.WaitForViewRequest{Condition: input.Wait, StableFrames: 1, Timeout: timeout, AfterFrameSet: true, AfterFrameRevision: snapshot.FrameRevision, AllowAlreadySatisfied: true}
			waited, waitErr := runtime.WaitForView(ctx, request)
			output.Snapshot = waited
			if waitErr != nil {
				var timeoutErr *studio.WaitTimeoutError
				if errors.As(waitErr, &timeoutErr) {
					return &mcp.CallToolResult{IsError: true, StructuredContent: output, Content: []mcp.Content{&mcp.TextContent{Text: waitErr.Error()}}}, output, nil
				}
				return nil, output, waitErr
			}
		}
		return nil, output, nil
	})
	mcp.AddTool(s.server, &mcp.Tool{Name: "gora_configure_event_trace", Description: "Configure the bounded per-view automation event trace.", Annotations: mutation}, func(ctx context.Context, _ *mcp.CallToolRequest, input ConfigureEventTraceInput) (*mcp.CallToolResult, EventTraceOutput, error) {
		runtime, err := s.registry.Runtime(input.ProjectID, input.ViewID)
		if err != nil {
			data, commandErr := s.registry.HostCommandResult(ctx, input.ProjectID, input.ViewID, "configure_trace", map[string]any{"enabled": input.Enabled, "capacity": input.Capacity})
			if commandErr != nil {
				return nil, EventTraceOutput{}, commandErr
			}
			var trace automation.TraceSnapshot
			_ = json.Unmarshal(data, &trace)
			return nil, EventTraceOutput{ProjectID: input.ProjectID, ViewID: input.ViewID, Trace: trace}, nil
		}
		if err := runtime.ConfigureEventTrace(input.Enabled, input.Capacity); err != nil {
			return nil, EventTraceOutput{}, err
		}
		output := EventTraceOutput{ProjectID: input.ProjectID, ViewID: input.ViewID, Trace: runtime.EventTrace()}
		s.notifyView(input.ProjectID, input.ViewID)
		if !output.Trace.Enabled {
			s.notifyTrace(input.ProjectID, input.ViewID)
		}
		return nil, output, nil
	})
	mcp.AddTool(s.server, &mcp.Tool{Name: "gora_clear_event_trace", Description: "Clear the per-view automation event trace while preserving its generation.", Annotations: mutation}, func(ctx context.Context, _ *mcp.CallToolRequest, input ClearEventTraceInput) (*mcp.CallToolResult, EventTraceOutput, error) {
		runtime, err := s.registry.Runtime(input.ProjectID, input.ViewID)
		if err != nil {
			data, commandErr := s.registry.HostCommandResult(ctx, input.ProjectID, input.ViewID, "clear_trace", nil)
			if commandErr != nil {
				return nil, EventTraceOutput{}, commandErr
			}
			var trace automation.TraceSnapshot
			_ = json.Unmarshal(data, &trace)
			return nil, EventTraceOutput{ProjectID: input.ProjectID, ViewID: input.ViewID, Trace: trace}, nil
		}
		runtime.ClearEventTrace()
		output := EventTraceOutput{ProjectID: input.ProjectID, ViewID: input.ViewID, Trace: runtime.EventTrace()}
		s.notifyView(input.ProjectID, input.ViewID)
		if !output.Trace.Enabled {
			s.notifyTrace(input.ProjectID, input.ViewID)
		}
		return nil, output, nil
	})
	mcp.AddTool(s.server, &mcp.Tool{Name: "gora_set_automation_clipboard", Description: "Set the isolated clipboard for one automation view.", Annotations: mutation}, func(ctx context.Context, _ *mcp.CallToolRequest, input SetAutomationClipboardInput) (*mcp.CallToolResult, AutomationClipboardOutput, error) {
		runtime, err := s.registry.Runtime(input.ProjectID, input.ViewID)
		if err != nil {
			if err := s.registry.HostCommand(ctx, input.ProjectID, input.ViewID, "set_clipboard", map[string]any{"text": input.Text}); err != nil {
				return nil, AutomationClipboardOutput{}, err
			}
			s.notifyView(input.ProjectID, input.ViewID)
			return nil, AutomationClipboardOutput{ProjectID: input.ProjectID, ViewID: input.ViewID}, nil
		}
		runtime.SetAutomationClipboard(input.Text)
		output := AutomationClipboardOutput{ProjectID: input.ProjectID, ViewID: input.ViewID}
		s.notifyView(input.ProjectID, input.ViewID)
		return nil, output, nil
	})
	mcp.AddTool(s.server, &mcp.Tool{Name: "gora_read_automation_clipboard", Description: "Read the isolated clipboard for one automation view.", Annotations: readOnly}, func(ctx context.Context, _ *mcp.CallToolRequest, input ReadAutomationClipboardInput) (*mcp.CallToolResult, AutomationClipboardOutput, error) {
		runtime, err := s.registry.Runtime(input.ProjectID, input.ViewID)
		if err != nil {
			data, commandErr := s.registry.HostCommandResult(ctx, input.ProjectID, input.ViewID, "get_clipboard", nil)
			if commandErr != nil {
				return nil, AutomationClipboardOutput{}, commandErr
			}
			var result struct {
				Text string `json:"text"`
			}
			_ = json.Unmarshal(data, &result)
			return nil, AutomationClipboardOutput{ProjectID: input.ProjectID, ViewID: input.ViewID, Text: result.Text}, nil
		}
		return nil, AutomationClipboardOutput{ProjectID: input.ProjectID, ViewID: input.ViewID, Text: runtime.AutomationClipboard()}, nil
	})
	mcp.AddTool(s.server, &mcp.Tool{Name: "gora_set_view_clock", Description: "Set one automation view's interaction clock to real or frozen mode.", Annotations: mutation}, func(ctx context.Context, _ *mcp.CallToolRequest, input SetViewClockInput) (*mcp.CallToolResult, ViewClockOutput, error) {
		runtime, err := s.registry.Runtime(input.ProjectID, input.ViewID)
		if err != nil {
			data, commandErr := s.registry.HostCommandResult(ctx, input.ProjectID, input.ViewID, "set_clock", map[string]any{"mode": input.Mode})
			if commandErr != nil {
				return nil, ViewClockOutput{}, commandErr
			}
			var result struct {
				Clock    studio.ViewClockSnapshot  `json:"clock"`
				Snapshot studio.AutomationSnapshot `json:"snapshot"`
			}
			_ = json.Unmarshal(data, &result)
			return nil, ViewClockOutput{ProjectID: input.ProjectID, ViewID: input.ViewID, Clock: result.Clock, Snapshot: result.Snapshot}, nil
		}
		before := runtime.AutomationSnapshot()
		beforeTrace := runtime.EventTrace().Revision
		clock, err := runtime.SetViewClock(input.Mode)
		if err != nil {
			return nil, ViewClockOutput{}, err
		}
		output := ViewClockOutput{ProjectID: input.ProjectID, ViewID: input.ViewID, Clock: clock, Snapshot: runtime.AutomationSnapshot()}
		after := output.Snapshot
		runtime.RecordEventTrace(automation.TraceEntry{Stage: "clock", Type: "clock", Outcome: input.Mode, RuntimeBefore: before.RuntimeRevision, RuntimeAfter: after.RuntimeRevision, GeometryBefore: before.GeometryRevision, GeometryAfter: after.GeometryRevision, FrameBefore: before.FrameRevision, FrameAfter: after.FrameRevision, TraceBefore: beforeTrace, TraceAfter: beforeTrace + 1})
		s.notifyView(input.ProjectID, input.ViewID)
		return nil, output, nil
	})
	mcp.AddTool(s.server, &mcp.Tool{Name: "gora_advance_view_clock", Description: "Advance a frozen automation view clock by a positive duration.", Annotations: mutation}, func(ctx context.Context, _ *mcp.CallToolRequest, input AdvanceViewClockInput) (*mcp.CallToolResult, ViewClockOutput, error) {
		runtime, err := s.registry.Runtime(input.ProjectID, input.ViewID)
		if err != nil {
			data, commandErr := s.registry.HostCommandResult(ctx, input.ProjectID, input.ViewID, "advance_clock", map[string]any{"delta_ms": input.DeltaMS, "run_until_idle": input.RunUntilIdle})
			if commandErr != nil {
				return nil, ViewClockOutput{}, commandErr
			}
			var result struct {
				Clock    studio.ViewClockSnapshot  `json:"clock"`
				Snapshot studio.AutomationSnapshot `json:"snapshot"`
			}
			_ = json.Unmarshal(data, &result)
			return nil, ViewClockOutput{ProjectID: input.ProjectID, ViewID: input.ViewID, Clock: result.Clock, Snapshot: result.Snapshot}, nil
		}
		before := runtime.AutomationSnapshot()
		beforeTrace := runtime.EventTrace().Revision
		clock, err := runtime.AdvanceViewClock(input.DeltaMS, input.RunUntilIdle)
		if err != nil {
			return nil, ViewClockOutput{}, err
		}
		if s.automation {
			s.registry.ReleaseDelayedTestFaults(input.ProjectID, input.ViewID)
		}
		output := ViewClockOutput{ProjectID: input.ProjectID, ViewID: input.ViewID, Clock: clock, Snapshot: runtime.AutomationSnapshot()}
		after := output.Snapshot
		runtime.RecordEventTrace(automation.TraceEntry{Stage: "clock", Type: "clock", Outcome: fmt.Sprintf("advance:%d", input.DeltaMS), RuntimeBefore: before.RuntimeRevision, RuntimeAfter: after.RuntimeRevision, GeometryBefore: before.GeometryRevision, GeometryAfter: after.GeometryRevision, FrameBefore: before.FrameRevision, FrameAfter: after.FrameRevision, TraceBefore: beforeTrace, TraceAfter: beforeTrace + 1})
		s.notifyView(input.ProjectID, input.ViewID)
		return nil, output, nil
	})
	mcp.AddTool(s.server, &mcp.Tool{Name: "gora_run_until_idle", Description: "Drain due timers for a frozen automation view.", Annotations: mutation}, func(ctx context.Context, _ *mcp.CallToolRequest, input ReadAutomationClipboardInput) (*mcp.CallToolResult, ViewClockOutput, error) {
		runtime, err := s.registry.Runtime(input.ProjectID, input.ViewID)
		if err != nil {
			data, commandErr := s.registry.HostCommandResult(ctx, input.ProjectID, input.ViewID, "run_until_idle", nil)
			if commandErr != nil {
				return nil, ViewClockOutput{}, commandErr
			}
			var result struct {
				Clock    studio.ViewClockSnapshot  `json:"clock"`
				Snapshot studio.AutomationSnapshot `json:"snapshot"`
			}
			_ = json.Unmarshal(data, &result)
			return nil, ViewClockOutput{ProjectID: input.ProjectID, ViewID: input.ViewID, Clock: result.Clock, Snapshot: result.Snapshot}, nil
		}
		before := runtime.AutomationSnapshot()
		beforeTrace := runtime.EventTrace().Revision
		clock, err := runtime.RunUntilIdle()
		if err != nil {
			return nil, ViewClockOutput{}, err
		}
		output := ViewClockOutput{ProjectID: input.ProjectID, ViewID: input.ViewID, Clock: clock, Snapshot: runtime.AutomationSnapshot()}
		after := output.Snapshot
		runtime.RecordEventTrace(automation.TraceEntry{Stage: "clock", Type: "clock", Outcome: "run_until_idle", RuntimeBefore: before.RuntimeRevision, RuntimeAfter: after.RuntimeRevision, GeometryBefore: before.GeometryRevision, GeometryAfter: after.GeometryRevision, FrameBefore: before.FrameRevision, FrameAfter: after.FrameRevision, TraceBefore: beforeTrace, TraceAfter: beforeTrace + 1})
		s.notifyView(input.ProjectID, input.ViewID)
		return nil, output, nil
	})
}

func automationResources(projectID, viewID string) []string {
	base := "gora://project/" + projectID + "/views/" + viewID
	return []string{base + "/tree", base + "/automation", base + "/automation/trace", base + "/automation/overlay"}
}

func (s *Service) registerAutomationResources() {
	s.server.AddResourceTemplate(&mcp.ResourceTemplate{
		URITemplate: "gora://project/{project_id}/views/{view_id}/automation",
		Name:        "gora-view-automation", MIMEType: "application/json",
	}, func(ctx context.Context, request *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
		projectID, viewID, ok := parseAutomationURI(request.Params.URI)
		if !ok {
			return nil, mcp.ResourceNotFoundError(request.Params.URI)
		}
		snapshot, err := s.registry.AutomationSnapshot(projectID, viewID)
		if err != nil && snapshot.SchemaVersion == 0 {
			return nil, mcp.ResourceNotFoundError(request.Params.URI)
		}
		return jsonResource(request.Params.URI, snapshot)
	})
	s.server.AddResourceTemplate(&mcp.ResourceTemplate{
		URITemplate: "gora://project/{project_id}/views/{view_id}/automation/trace",
		Name:        "gora-view-automation-trace", MIMEType: "application/json",
	}, func(ctx context.Context, request *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
		projectID, viewID, ok := parseTraceURI(request.Params.URI)
		if !ok {
			return nil, mcp.ResourceNotFoundError(request.Params.URI)
		}
		trace, err := s.registry.AutomationTrace(ctx, projectID, viewID)
		if err != nil {
			return nil, mcp.ResourceNotFoundError(request.Params.URI)
		}
		return jsonResource(request.Params.URI, trace)
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
	runtime, err := s.registry.Runtime(projectID, viewID)
	if err != nil {
		view, summaryErr := s.registry.ViewSummary(projectID, viewID)
		if summaryErr != nil {
			return nil, RuntimeMutationOutput{}, summaryErr
		}
		if view.HostMode == "headless" {
			return nil, RuntimeMutationOutput{}, err
		}
		if view.ConnectionState != "connected" {
			return nil, RuntimeMutationOutput{}, fmt.Errorf("attached host disconnected: %s", view.DisconnectReason)
		}
		s.notifyView(projectID, viewID)
		return nil, RuntimeMutationOutput{ProjectID: projectID, View: view}, nil
	}
	if _, err := runtime.RuntimeTree(); err != nil {
		return nil, RuntimeMutationOutput{}, err
	}
	if s.automation {
		if err := s.registry.RefreshAutomationDriver(projectID, viewID); err != nil {
			return nil, RuntimeMutationOutput{}, err
		}
	}
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

// validateSubscription applies the same project/view containment and runtime
// availability rules used by resource reads before the SDK records a
// subscription. The SDK owns the per-session subscription set; this method
// only validates the requested resource URI.
func (s *Service) validateSubscription(_ context.Context, request *mcp.SubscribeRequest) error {
	if request == nil || request.Params == nil || request.Params.URI == "" {
		return fmt.Errorf("resource URI is required")
	}
	uri := request.Params.URI
	if uri == "gora://projects" {
		return nil
	}
	if projectID, viewID, ok := parseTraceURI(uri); ok {
		if !s.automation {
			return fmt.Errorf("automation resources are disabled")
		}
		view, err := s.registry.ViewSummary(projectID, viewID)
		if err != nil {
			return fmt.Errorf("unknown project or view: %w", err)
		}
		if !view.RuntimeAvailable {
			return fmt.Errorf("automation is unavailable for token views")
		}
		return nil
	}
	if projectID, viewID, ok := parseOverlayURI(uri); ok {
		if !s.automation {
			return fmt.Errorf("automation resources are disabled")
		}
		view, err := s.registry.ViewSummary(projectID, viewID)
		if err != nil {
			return fmt.Errorf("unknown project or view: %w", err)
		}
		if !view.RuntimeAvailable {
			return fmt.Errorf("automation is unavailable for token views")
		}
		return nil
	}
	if projectID, viewID, ok := parseAutomationURI(uri); ok {
		if !s.automation {
			return fmt.Errorf("automation resources are disabled")
		}
		view, err := s.registry.ViewSummary(projectID, viewID)
		if err != nil {
			return fmt.Errorf("unknown project or view: %w", err)
		}
		if !view.RuntimeAvailable {
			return fmt.Errorf("automation is unavailable for token views")
		}
		return nil
	}
	if projectID, viewID, semanticID, ok := parseNodeURI(uri); ok {
		runtime, err := s.registry.Runtime(projectID, viewID)
		if err != nil {
			return fmt.Errorf("unknown project or runtime view: %w", err)
		}
		tree, err := runtime.RuntimeTree()
		if err != nil {
			return err
		}
		for _, node := range semantic.Flatten(tree) {
			if node.ID == semanticID {
				return nil
			}
		}
		return fmt.Errorf("unknown runtime node %q", semanticID)
	}
	const prefix = "gora://project/"
	if !strings.HasPrefix(uri, prefix) {
		return fmt.Errorf("unknown resource URI %q", uri)
	}
	parts := strings.Split(strings.TrimPrefix(uri, prefix), "/")
	if len(parts) < 2 || parts[0] == "" {
		return fmt.Errorf("malformed project resource URI %q", uri)
	}
	projectID := parts[0]
	if len(parts) == 2 && (parts[1] == "manifest" || parts[1] == "diagnostics") {
		if _, err := s.registry.ProjectRoot(projectID); err != nil {
			return fmt.Errorf("unknown project: %w", err)
		}
		return nil
	}
	if len(parts) == 3 && (parts[1] == "sources" || parts[1] == "documents") && parts[2] != "" {
		if _, err := s.registry.DocumentResource(projectID, parts[2]); err != nil {
			return fmt.Errorf("unknown project source: %w", err)
		}
		return nil
	}
	if len(parts) >= 3 && parts[1] == "views" && parts[2] != "" {
		viewID := parts[2]
		view, err := s.registry.ViewSummary(projectID, viewID)
		if err != nil {
			return fmt.Errorf("unknown project or view: %w", err)
		}
		if len(parts) == 3 {
			return nil
		}
		if len(parts) == 4 && parts[3] == "tree" {
			if !view.RuntimeAvailable {
				return fmt.Errorf("runtime tree is unavailable for token views")
			}
			if _, err := s.registry.InspectView(projectID, viewID); err != nil {
				return err
			}
			return nil
		}
	}
	return fmt.Errorf("unknown resource URI %q", uri)
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
	if s.automation {
		automationURI := viewURI + "/automation"
		s.server.AddResource(&mcp.Resource{URI: automationURI, Name: "gora-view-automation", MIMEType: "application/json"}, func(_ context.Context, request *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
			snapshot, err := s.registry.AutomationSnapshot(projectID, viewID)
			if err != nil && snapshot.SchemaVersion == 0 {
				return nil, mcp.ResourceNotFoundError(request.Params.URI)
			}
			return jsonResource(request.Params.URI, snapshot)
		})
		traceURI := viewURI + "/automation/trace"
		s.server.AddResource(&mcp.Resource{URI: traceURI, Name: "gora-view-automation-trace", MIMEType: "application/json"}, func(_ context.Context, request *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
			trace, err := s.registry.AutomationTrace(context.Background(), projectID, viewID)
			if err != nil {
				return nil, mcp.ResourceNotFoundError(request.Params.URI)
			}
			return jsonResource(request.Params.URI, trace)
		})
		overlayURI := viewURI + "/automation/overlay"
		s.server.AddResource(&mcp.Resource{URI: overlayURI, Name: "gora-view-automation-overlay", MIMEType: "application/json"}, func(_ context.Context, request *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
			overlay, err := s.registry.OverlaySnapshot(projectID, viewID)
			if err != nil {
				return nil, mcp.ResourceNotFoundError(request.Params.URI)
			}
			return jsonResource(request.Params.URI, overlay)
		})
	}
	s.server.AddResource(&mcp.Resource{URI: treeURI, Name: "gora-runtime-tree", MIMEType: "application/json"}, func(_ context.Context, request *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
		data, err := s.registry.InspectView(projectID, viewID)
		if err != nil {
			return nil, mcp.ResourceNotFoundError(request.Params.URI)
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
	if s.automation {
		_ = s.server.ResourceUpdated(ctx, &mcp.ResourceUpdatedNotificationParams{URI: base + "/automation"})
		_ = s.server.ResourceUpdated(ctx, &mcp.ResourceUpdatedNotificationParams{URI: base + "/automation/overlay"})
		runtime, err := s.registry.Runtime(projectID, viewID)
		if err == nil && runtime.EventTrace().Enabled {
			_ = s.server.ResourceUpdated(ctx, &mcp.ResourceUpdatedNotificationParams{URI: base + "/automation/trace"})
		}
	}
}

func (s *Service) notifyTrace(projectID, viewID string) {
	if !s.automation {
		return
	}
	base := "gora://project/" + projectID + "/views/" + viewID
	_ = s.server.ResourceUpdated(context.Background(), &mcp.ResourceUpdatedNotificationParams{URI: base + "/automation/trace"})
}

func (s *Service) removeProjectResources(projectID string, views []ViewSummary, sources []SourceSummary) {
	base := "gora://project/" + projectID
	uris := []string{base + "/manifest", base + "/diagnostics"}
	for _, view := range views {
		viewBase := base + "/views/" + view.ID
		uris = append(uris, viewBase, viewBase+"/tree")
		if s.automation {
			uris = append(uris, viewBase+"/automation", viewBase+"/automation/trace", viewBase+"/automation/overlay")
		}
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

func parseAutomationURI(uri string) (string, string, bool) {
	const prefix = "gora://project/"
	if !strings.HasPrefix(uri, prefix) || !strings.HasSuffix(uri, "/automation") {
		return "", "", false
	}
	rest := strings.TrimSuffix(strings.TrimPrefix(uri, prefix), "/automation")
	parts := strings.SplitN(rest, "/views/", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" || strings.Contains(parts[1], "/") {
		return "", "", false
	}
	return parts[0], parts[1], true
}

func parseTraceURI(uri string) (string, string, bool) {
	const prefix = "gora://project/"
	const suffix = "/automation/trace"
	if !strings.HasPrefix(uri, prefix) || !strings.HasSuffix(uri, suffix) {
		return "", "", false
	}
	rest := strings.TrimSuffix(strings.TrimPrefix(uri, prefix), suffix)
	parts := strings.SplitN(rest, "/views/", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" || strings.Contains(parts[1], "/") {
		return "", "", false
	}
	return parts[0], parts[1], true
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

func containedExistingCapturePath(root, reference string) (string, error) {
	if !filepath.IsAbs(reference) {
		reference = filepath.Join(root, reference)
	}
	if !strings.EqualFold(filepath.Ext(reference), ".png") {
		return "", fmt.Errorf("reference must use the .png extension")
	}
	canonical, err := filepath.EvalSymlinks(reference)
	if err != nil {
		return "", err
	}
	canonical, err = filepath.Abs(canonical)
	if err != nil {
		return "", err
	}
	relative, err := filepath.Rel(root, canonical)
	if err != nil || relative == ".." || filepath.IsAbs(relative) || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("reference is outside project root")
	}
	return canonical, nil
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
	return RunWithOptions(ctx, listen, stderr, ServiceOptions{})
}

func RunWithOptions(ctx context.Context, listen string, stderr io.Writer, options ServiceOptions) error {
	listener, err := net.Listen("tcp", listen)
	if err != nil {
		return err
	}
	service := NewServiceWithOptions(nil, options)
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
