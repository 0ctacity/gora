package mcpserver

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"gora/internal/automation"
	"gora/internal/document"
	"gora/internal/project"
	"gora/internal/session"
	"gora/internal/studio"
)

type ProjectSummary struct {
	ID       string        `json:"project_id"`
	Root     string        `json:"root"`
	Revision uint64        `json:"revision"`
	Valid    bool          `json:"valid"`
	Views    []ViewSummary `json:"views"`
}

type ViewSummary struct {
	ID               string                      `json:"view_id"`
	File             string                      `json:"file"`
	Kind             document.Kind               `json:"kind,omitempty"`
	Valid            bool                        `json:"valid"`
	RuntimeAvailable bool                        `json:"runtime_available"`
	Selection        string                      `json:"selection,omitempty"`
	Selections       []string                    `json:"selections"`
	Viewport         struct{ Width, Height int } `json:"viewport"`
	Revision         uint64                      `json:"revision"`
	Diagnostics      []document.Diagnostic       `json:"diagnostics"`
	HostMode         session.HostMode            `json:"host_mode"`
	ConnectionState  string                      `json:"connection_state"`
	HostInstanceID   string                      `json:"host_instance_id,omitempty"`
	ProtocolVersion  int                         `json:"protocol_version,omitempty"`
	Capabilities     []string                    `json:"capabilities,omitempty"`
	HostPID          int                         `json:"host_pid,omitempty"`
	DisconnectReason string                      `json:"disconnect_reason,omitempty"`
}

type Registry struct {
	mu       sync.RWMutex
	projects map[string]*Project
	byRoot   map[string]string
	onChange func(string, []string)
}

type Project struct {
	mu       sync.RWMutex
	id       string
	root     string
	revision uint64
	views    map[string]*View
	byEntry  map[string]string
	sources  map[string]bool
	watch    *projectWatcher
	notify   func(string, []string)
}

type View struct {
	id          string
	entry       string
	kind        document.Kind
	diagnostics []document.Diagnostic
	runtime     *studio.Runtime
	driver      *automation.Driver
	overlay     *viewOverlay
	faults      map[string]*testFaultRule
	hostMode    session.HostMode
	host        *hostBackend
	backend     ViewBackend
}

type hostBackend struct {
	mu         sync.Mutex
	socketPath string
	identity   session.HostIdentity
	protocol   int
	last       studio.AutomationSnapshot
	lastHost   studio.HostSnapshot
	connected  bool
	reason     string
	stop       chan struct{}
	done       chan struct{}
	stopOnce   sync.Once
	notify     func()
}

func NewRegistry() *Registry {
	return &Registry{projects: make(map[string]*Project), byRoot: make(map[string]string)}
}

func (r *Registry) SetChangeHandler(handler func(string, []string)) {
	r.mu.Lock()
	r.onChange = handler
	for _, project := range r.projects {
		project.notify = handler
	}
	r.mu.Unlock()
}

func (r *Registry) OpenProject(root string) (ProjectSummary, error) {
	canonical, err := canonicalDirectory(root)
	if err != nil {
		return ProjectSummary{}, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if id := r.byRoot[canonical]; id != "" {
		return r.projects[id].summary(), nil
	}
	project := &Project{id: opaqueID(), root: canonical, views: make(map[string]*View), byEntry: make(map[string]string), sources: make(map[string]bool), revision: 1, notify: r.onChange}
	project.watch, err = newProjectWatcher(project)
	if err != nil {
		return ProjectSummary{}, err
	}
	r.projects[project.id] = project
	r.byRoot[canonical] = project.id
	return project.summary(), nil
}

func (r *Registry) ListProjects() []ProjectSummary {
	r.mu.RLock()
	projects := make([]*Project, 0, len(r.projects))
	for _, project := range r.projects {
		projects = append(projects, project)
	}
	r.mu.RUnlock()
	result := make([]ProjectSummary, 0, len(projects))
	for _, project := range projects {
		result = append(result, project.summary())
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Root < result[j].Root })
	return result
}

func (r *Registry) CloseProject(id string) error {
	r.mu.Lock()
	project := r.projects[id]
	if project == nil {
		r.mu.Unlock()
		return fmt.Errorf("unknown project %q", id)
	}
	delete(r.projects, id)
	delete(r.byRoot, project.root)
	r.mu.Unlock()
	project.close()
	return nil
}

func (r *Registry) OpenView(projectID, file string, modes ...string) (ViewSummary, error) {
	project, err := r.project(projectID)
	if err != nil {
		return ViewSummary{}, err
	}
	entry, err := containedFile(project.root, file)
	if err != nil {
		return ViewSummary{}, err
	}
	hostMode := session.HostModeHeadless
	if len(modes) > 0 && modes[0] != "" {
		hostMode = session.HostMode(modes[0])
	}
	if hostMode != session.HostModeHeadless && hostMode != session.HostModeApp && hostMode != session.HostModeStudio {
		return ViewSummary{}, fmt.Errorf("unsupported host_mode %q", hostMode)
	}
	project.mu.Lock()
	defer project.mu.Unlock()
	key := viewKey(entry, hostMode)
	if id := project.byEntry[key]; id != "" {
		return project.views[id].summary(), nil
	}
	source, err := os.ReadFile(entry)
	if err != nil {
		return ViewSummary{}, err
	}
	doc, parseDiagnostics := document.Parse(entry, source)
	diagnostics := projectpkgValidate(project.root, entry)
	if len(diagnostics) == 0 && len(parseDiagnostics) != 0 {
		diagnostics = parseDiagnostics
	}
	var kind document.Kind
	if doc != nil {
		kind = doc.Kind
	}
	view := &View{id: opaqueID(), entry: entry, kind: kind, hostMode: hostMode, diagnostics: diagnostics, overlay: newViewOverlay(), faults: make(map[string]*testFaultRule)}
	if hostMode != session.HostModeHeadless {
		if kind == document.KindTokens {
			return ViewSummary{}, fmt.Errorf("token views cannot attach to %s host", hostMode)
		}
		host, err := attachHost(project.root, entry, hostMode)
		if err != nil {
			return ViewSummary{}, err
		}
		view.host = host
		view.backend = host
		host.startObserver(func() {
			if project.notify != nil {
				project.notify(project.id, []string{view.id})
			}
		})
		view.kind = kind
	} else if kind != document.KindTokens {
		view.runtime = studio.NewRuntimeAllowInvalid(project.root, entry)
		view.driver = newAutomationDriver(view.runtime)
		view.backend = &headlessBackend{runtime: view.runtime, driver: view.driver}
	}
	project.views[view.id] = view
	project.byEntry[key] = view.id
	project.sources[entry] = true
	if view.runtime != nil {
		for _, dependency := range view.runtime.Dependencies() {
			if filepath.Ext(dependency) == ".gora" {
				project.sources[filepath.Clean(dependency)] = true
			}
		}
		// Publish the initial valid reference frame eagerly so automation clients
		// can wait immediately after opening a view. Invalid views remain open
		// with diagnostics and simply have no last-good frame yet.
		if tree, frameErr := view.runtime.RuntimeTree(); frameErr == nil && !view.runtime.Snapshot().Invalid {
			view.driver.Update(tree)
		} else {
			view.driver.Update(nil)
		}
		view.runtime.PublishRouterSnapshot(view.driver.Router().Snapshot())
	}
	project.revision++
	project.refreshWatchLocked()
	return view.summary(), nil
}

func newAutomationDriver(runtime *studio.Runtime) *automation.Driver {
	return automation.NewDriverWithSnapshot(runtime, func() automation.RevisionSnapshot {
		snapshot := runtime.AutomationSnapshot()
		return automation.RevisionSnapshot{
			RuntimeRevision: snapshot.RuntimeRevision, FrameRevision: snapshot.FrameRevision,
			GeometryRevision: snapshot.GeometryRevision, PublishedRuntimeRevision: snapshot.PublishedRuntimeRevision,
			PublishedGeometryRevision: snapshot.PublishedGeometryRevision, AutomationInputRevision: snapshot.AutomationInputRevision,
		}
	})
}

func (r *Registry) ListViews(projectID string) []ViewSummary {
	project, err := r.project(projectID)
	if err != nil {
		return nil
	}
	project.mu.RLock()
	result := make([]ViewSummary, 0, len(project.views))
	for _, view := range project.views {
		result = append(result, view.summary())
	}
	project.mu.RUnlock()
	sort.Slice(result, func(i, j int) bool { return result[i].File < result[j].File })
	return result
}

func (r *Registry) CloseView(projectID, viewID string) error {
	project, err := r.project(projectID)
	if err != nil {
		return err
	}
	project.mu.Lock()
	view := project.views[viewID]
	if view == nil {
		project.mu.Unlock()
		return fmt.Errorf("unknown view %q", viewID)
	}
	delete(project.views, viewID)
	delete(project.byEntry, viewKey(view.entry, view.hostMode))
	project.revision++
	project.mu.Unlock()
	view.closeOverlay()
	if view.host != nil {
		view.host.close()
	}
	if view.runtime != nil {
		view.runtime.Close()
	}
	if view.driver != nil {
		view.driver.Close()
	}
	return nil
}

func (r *Registry) Runtime(projectID, viewID string) (*studio.Runtime, error) {
	project, err := r.project(projectID)
	if err != nil {
		return nil, err
	}
	project.mu.RLock()
	view := project.views[viewID]
	project.mu.RUnlock()
	if view == nil {
		return nil, fmt.Errorf("unknown view %q", viewID)
	}
	if view.runtime == nil {
		return nil, fmt.Errorf("unsupported capability: view %q runtime operations are owned by attached %s host", viewID, view.hostMode)
	}
	return view.runtime, nil
}

func (r *Registry) Backend(projectID, viewID string) (ViewBackend, error) {
	project, err := r.project(projectID)
	if err != nil {
		return nil, err
	}
	project.mu.RLock()
	view := project.views[viewID]
	project.mu.RUnlock()
	if view == nil {
		return nil, fmt.Errorf("unknown view %q", viewID)
	}
	if view.backend == nil {
		return nil, fmt.Errorf("view %q has no backend", viewID)
	}
	return view.backend, nil
}

// AutomationDriver returns the ordered input coordinator owned by one live
// app/component view. Token views intentionally have no driver.
func (r *Registry) AutomationDriver(projectID, viewID string) (*automation.Driver, error) {
	project, err := r.project(projectID)
	if err != nil {
		return nil, err
	}
	project.mu.RLock()
	view := project.views[viewID]
	project.mu.RUnlock()
	if view == nil {
		return nil, fmt.Errorf("unknown view %q", viewID)
	}
	if view.driver == nil {
		return nil, fmt.Errorf("view %q does not support automation input", viewID)
	}
	return view.driver, nil
}

// RefreshAutomationDriver reconciles a view-owned router after a non-input
// MCP mutation (selection, viewport, state, or document control update).
// CurrentRuntimeTree avoids manufacturing a second frame when publication is
// already current; Update then clears stale pointer ownership on a context
// change.
func (r *Registry) RefreshAutomationDriver(projectID, viewID string) error {
	runtime, err := r.Runtime(projectID, viewID)
	if err != nil {
		return err
	}
	driver, err := r.AutomationDriver(projectID, viewID)
	if err != nil {
		return err
	}
	snapshot := runtime.Snapshot()
	if snapshot.Invalid {
		driver.Update(nil)
	} else {
		tree, treeErr := runtime.CurrentRuntimeTree()
		if treeErr != nil {
			return treeErr
		}
		driver.Update(tree)
	}
	runtime.PublishRouterSnapshot(driver.Router().Snapshot())
	return nil
}

func (r *Registry) ViewSummary(projectID, viewID string) (ViewSummary, error) {
	project, err := r.project(projectID)
	if err != nil {
		return ViewSummary{}, err
	}
	project.mu.RLock()
	defer project.mu.RUnlock()
	view := project.views[viewID]
	if view == nil {
		return ViewSummary{}, fmt.Errorf("unknown view %q", viewID)
	}
	return view.summary(), nil
}

// AutomationSnapshot returns the latest immutable host publication for an
// attached view or the in-process automation snapshot for a headless view.
func (r *Registry) AutomationSnapshot(projectID, viewID string) (studio.AutomationSnapshot, error) {
	project, err := r.project(projectID)
	if err != nil {
		return studio.AutomationSnapshot{}, err
	}
	project.mu.Lock()
	defer project.mu.Unlock()
	view := project.views[viewID]
	if view == nil {
		return studio.AutomationSnapshot{}, fmt.Errorf("unknown view %q", viewID)
	}
	if view.runtime != nil {
		return view.runtime.AutomationSnapshot(), nil
	}
	if view.host == nil {
		return studio.AutomationSnapshot{}, fmt.Errorf("view %q has no runtime backend", viewID)
	}
	view.host.refresh(view.entry)
	view.host.mu.Lock()
	reconnected := view.host.connected
	notify := view.host.notify
	view.host.mu.Unlock()
	if reconnected {
		view.host.startObserver(notify)
	}
	view.host.mu.Lock()
	defer view.host.mu.Unlock()
	if !view.host.connected {
		return view.host.last, fmt.Errorf("attached host is disconnected: %s", view.host.reason)
	}
	return view.host.last, nil
}

func (r *Registry) HostSnapshot(projectID, viewID string) (studio.HostSnapshot, error) {
	backend, err := r.Backend(projectID, viewID)
	if err != nil {
		return studio.HostSnapshot{}, err
	}
	if backend.Mode() == session.HostModeHeadless {
		return studio.HostSnapshot{}, fmt.Errorf("host resource is unavailable for headless views")
	}
	return backend.HostSnapshot(context.Background())
}

func (r *Registry) AutomationTrace(ctx context.Context, projectID, viewID string) (automation.TraceSnapshot, error) {
	backend, err := r.Backend(projectID, viewID)
	if err != nil {
		return automation.TraceSnapshot{}, err
	}
	return backend.Trace(ctx)
}

func (r *Registry) WaitForView(ctx context.Context, projectID, viewID string, request studio.WaitForViewRequest) (studio.AutomationSnapshot, error) {
	project, err := r.project(projectID)
	if err != nil {
		return studio.AutomationSnapshot{}, err
	}
	project.mu.RLock()
	view := project.views[viewID]
	project.mu.RUnlock()
	if view == nil {
		return studio.AutomationSnapshot{}, fmt.Errorf("unknown view %q", viewID)
	}
	if view.runtime != nil {
		return view.runtime.WaitForView(ctx, request)
	}
	if view.host == nil {
		return studio.AutomationSnapshot{}, fmt.Errorf("view %q has no runtime backend", viewID)
	}
	view.host.mu.Lock()
	socketPath := view.host.socketPath
	view.host.mu.Unlock()
	payload, _ := json.Marshal(struct {
		AfterFrameRevision   uint64 `json:"after_frame_revision"`
		AfterFrameSet        bool   `json:"after_frame_set"`
		AfterRuntimeRevision uint64 `json:"after_runtime_revision"`
		AfterRuntimeSet      bool   `json:"after_runtime_set"`
		Condition            string `json:"condition"`
		StableFrames         int    `json:"stable_frames"`
		TimeoutMS            int    `json:"timeout_ms"`
	}{request.AfterFrameRevision, request.AfterFrameSet, request.AfterRuntimeRevision, request.AfterRuntimeSet, request.Condition, request.StableFrames, int(request.Timeout / time.Millisecond)})
	response, err := session.Send(socketPath, session.Request{Version: session.ProtocolVersion, RequestID: opaqueID(), Action: session.ActionWait, Payload: payload}, request.Timeout)
	if err != nil {
		view.host.mu.Lock()
		defer view.host.mu.Unlock()
		if response.Error != "" {
			var snapshot studio.AutomationSnapshot
			if len(response.Data) != 0 {
				_ = json.Unmarshal(response.Data, &snapshot)
				view.host.last = snapshot
			}
			return snapshot, err
		}
		view.host.connected = false
		view.host.reason = err.Error()
		return view.host.last, err
	}
	var snapshot studio.AutomationSnapshot
	if len(response.Data) != 0 {
		_ = json.Unmarshal(response.Data, &snapshot)
	}
	view.host.mu.Lock()
	view.host.last = snapshot
	view.host.mu.Unlock()
	if !response.OK {
		return snapshot, errors.New(response.Error)
	}
	return snapshot, nil
}

// InspectView returns the host's published inspection tree for an attached
// view. Headless views retain the existing runtime inspection contract.
func (r *Registry) InspectView(projectID, viewID string) ([]byte, error) {
	project, err := r.project(projectID)
	if err != nil {
		return nil, err
	}
	project.mu.RLock()
	view := project.views[viewID]
	project.mu.RUnlock()
	if view == nil {
		return nil, fmt.Errorf("unknown view %q", viewID)
	}
	if view.runtime != nil {
		data, _, err := view.runtime.Inspect("headless")
		return data, err
	}
	if view.host == nil {
		return nil, fmt.Errorf("view %q has no runtime backend", viewID)
	}
	view.host.mu.Lock()
	socketPath := view.host.socketPath
	view.host.mu.Unlock()
	response, err := session.Send(socketPath, session.Request{Action: "inspect"}, 500*time.Millisecond)
	if err != nil {
		view.host.mu.Lock()
		defer view.host.mu.Unlock()
		view.host.connected = false
		view.host.reason = err.Error()
		return nil, fmt.Errorf("attached host is disconnected: %w", err)
	}
	if !response.OK {
		return nil, errors.New(response.Error)
	}
	return response.Data, nil
}

// HostCommand forwards an attached-view mutation to the host event loop. It
// is intentionally unavailable for headless views, whose existing handlers
// retain direct in-process semantics.
func (r *Registry) HostCommand(ctx context.Context, projectID, viewID, kind string, payload any) error {
	_, err := r.HostCommandResult(ctx, projectID, viewID, kind, payload)
	return err
}

func (r *Registry) HostCommandResult(ctx context.Context, projectID, viewID, kind string, payload any) (json.RawMessage, error) {
	project, err := r.project(projectID)
	if err != nil {
		return nil, err
	}
	project.mu.RLock()
	view := project.views[viewID]
	project.mu.RUnlock()
	if view == nil {
		return nil, fmt.Errorf("unknown view %q", viewID)
	}
	if view.host == nil {
		return nil, fmt.Errorf("view %q is not an attached host view", viewID)
	}
	host := view.host
	host.mu.Lock()
	capability := map[string]string{"set_viewport": "viewport", "select": "selection", "activate": "activation", "scroll": "scroll", "set_state": "state", "reset_state": "reset", "set_control_value": "state", "set_field_draft": "editing", "submit_form": "editing", "reset_form": "editing", "set_clock": "clock", "advance_clock": "clock", "run_until_idle": "clock", "set_clipboard": "editing", "get_clipboard": "editing", "dispatch": "input", "configure_trace": "trace", "clear_trace": "trace", "get_trace": "trace", "capture": "capture", "capture_host_client": "capture", "assert": "snapshot", "reload_overlay": "overlay", "configure_faults": "faults", "clear_faults": "faults", "set_window": "window", "window_action": "window", "set_studio_state": "studio"}[kind]
	if capability != "" {
		advertised := false
		for _, value := range host.identity.Capabilities {
			if value == capability {
				advertised = true
				break
			}
		}
		if !advertised {
			host.mu.Unlock()
			return nil, fmt.Errorf("unsupported capability: host does not advertise %q", capability)
		}
	}
	socketPath := host.socketPath
	host.mu.Unlock()
	body := map[string]any{"kind": kind}
	if encoded, marshalErr := json.Marshal(payload); marshalErr == nil && len(encoded) > 0 && string(encoded) != "null" {
		var fields map[string]any
		if unmarshalErr := json.Unmarshal(encoded, &fields); unmarshalErr == nil {
			for key, value := range fields {
				body[key] = value
			}
		}
	}
	data, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	response, err := session.Send(socketPath, session.Request{Version: session.ProtocolVersion, RequestID: opaqueID(), Action: session.ActionCommand, Payload: data}, 5*time.Second)
	if err != nil {
		// A decoded, correlated host response can reject a command without the
		// transport being unhealthy. Only I/O/protocol failures disconnect the
		// attached view.
		if response.Version == session.ProtocolVersion && response.RequestID != "" && response.Error != "" {
			return nil, err
		}
		host.mu.Lock()
		host.connected = false
		host.reason = err.Error()
		host.mu.Unlock()
		return nil, fmt.Errorf("attached host disconnected: %w", err)
	}
	if !response.OK {
		return nil, errors.New(response.Error)
	}
	view.host.refresh(view.entry)
	view.host.mu.Lock()
	reconnected := view.host.connected
	notify := view.host.notify
	view.host.mu.Unlock()
	if reconnected {
		view.host.startObserver(notify)
	}
	return response.Data, nil
}

func (r *Registry) ProjectRoot(projectID string) (string, error) {
	project, err := r.project(projectID)
	if err != nil {
		return "", err
	}
	return project.root, nil
}

func (r *Registry) Close() {
	r.mu.Lock()
	projects := r.projects
	r.projects = make(map[string]*Project)
	r.byRoot = make(map[string]string)
	r.mu.Unlock()
	for _, project := range projects {
		project.close()
	}
}

func (r *Registry) project(id string) (*Project, error) {
	r.mu.RLock()
	project := r.projects[id]
	r.mu.RUnlock()
	if project == nil {
		return nil, fmt.Errorf("unknown project %q", id)
	}
	return project, nil
}

func (p *Project) summary() ProjectSummary {
	p.mu.RLock()
	defer p.mu.RUnlock()
	result := ProjectSummary{ID: p.id, Root: p.root, Revision: p.revision, Valid: true}
	for _, view := range p.views {
		summary := view.summary()
		result.Views = append(result.Views, summary)
		result.Valid = result.Valid && summary.Valid
	}
	sort.Slice(result.Views, func(i, j int) bool { return result.Views[i].File < result.Views[j].File })
	return result
}

func (p *Project) close() {
	if p.watch != nil {
		p.watch.Close()
	}
	p.mu.Lock()
	views := make([]*View, 0, len(p.views))
	for _, view := range p.views {
		views = append(views, view)
	}
	p.views = make(map[string]*View)
	p.byEntry = make(map[string]string)
	p.sources = make(map[string]bool)
	p.mu.Unlock()
	for _, view := range views {
		view.closeOverlay()
		if view.host != nil {
			view.host.close()
		}
		if view.driver != nil {
			view.driver.Close()
		}
		if view.runtime != nil {
			view.runtime.Close()
		}
	}
}

func (v *View) closeOverlay() {
	if v.overlay != nil {
		v.overlay.staged = nil
		v.overlay.installed = nil
		v.overlay.pending = nil
		v.overlay.pendingSet = false
		v.overlay.pendingInstall = false
		v.overlay.pendingRevision = ""
	}
	v.faults = nil
}

func (v *View) summary() ViewSummary {
	if v.host != nil {
		v.host.refresh(v.entry)
		v.host.mu.Lock()
		reconnected := v.host.connected
		notify := v.host.notify
		v.host.mu.Unlock()
		if reconnected {
			v.host.startObserver(notify)
		}
	}
	mode := v.hostMode
	if mode == "" {
		mode = session.HostModeHeadless
	}
	result := ViewSummary{ID: v.id, File: v.entry, Kind: v.kind, HostMode: mode, ConnectionState: "connected", Valid: len(v.diagnostics) == 0, RuntimeAvailable: v.runtime != nil, Diagnostics: append([]document.Diagnostic(nil), v.diagnostics...), Selections: []string{}}
	if v.runtime != nil {
		snapshot := v.runtime.Snapshot()
		result.Valid = !snapshot.Invalid
		result.Diagnostics = snapshot.Diagnostics
		result.Selection = snapshot.Screen
		result.Selections = append([]string(nil), snapshot.Screens...)
		result.Viewport.Width = snapshot.Viewport.X
		result.Viewport.Height = snapshot.Viewport.Y
		result.Revision = snapshot.RuntimeRevision
	}
	if v.host != nil {
		v.host.mu.Lock()
		result.RuntimeAvailable = true
		result.ConnectionState = "disconnected"
		if v.host.connected {
			result.ConnectionState = "connected"
		}
		result.HostInstanceID = v.host.identity.InstanceID
		result.ProtocolVersion = v.host.protocol
		result.Capabilities = append([]string(nil), v.host.identity.Capabilities...)
		result.HostPID = v.host.identity.PID
		result.DisconnectReason = v.host.reason
		result.Revision = v.host.last.FrameRevision
		result.Valid = v.host.last.Valid
		result.Viewport.Width = v.host.last.Viewport.X
		result.Viewport.Height = v.host.last.Viewport.Y
		result.Diagnostics = append([]document.Diagnostic(nil), v.host.last.Diagnostics...)
		v.host.mu.Unlock()
	}
	if result.Diagnostics == nil {
		result.Diagnostics = []document.Diagnostic{}
	}
	return result
}

func (host *hostBackend) refresh(entry string) {
	if host == nil {
		return
	}
	host.mu.Lock()
	defer host.mu.Unlock()
	payload, _ := json.Marshal(session.HandshakePayload{Root: host.identity.Root, Document: entry, Mode: host.identity.Mode, Protocol: session.ProtocolVersion})
	response, err := session.Send(host.socketPath, session.Request{Version: session.ProtocolVersion, RequestID: opaqueID(), Action: session.ActionHandshake, Payload: payload}, 200*time.Millisecond)
	if err != nil || !response.OK {
		host.connected = false
		if err != nil {
			host.reason = err.Error()
		} else {
			host.reason = response.Error
		}
		return
	}
	var result session.HandshakeResult
	if err := json.Unmarshal(response.Data, &result); err != nil {
		host.connected = false
		host.reason = err.Error()
		return
	}
	if err := session.ValidateHandshake(session.HostIdentity{Root: host.identity.Root, Document: entry}, result.Host, result.Protocol, host.identity.Mode); err != nil {
		host.connected = false
		host.reason = err.Error()
		return
	}
	host.identity = result.Host
	host.protocol = result.Protocol
	host.connected = true
	host.reason = ""
	data, err := session.Send(host.socketPath, session.Request{Version: session.ProtocolVersion, RequestID: opaqueID(), Action: session.ActionSnapshot}, 200*time.Millisecond)
	if err != nil || !data.OK {
		if err != nil {
			host.reason = err.Error()
		} else {
			host.reason = data.Error
		}
		host.connected = false
		return
	}
	_ = json.Unmarshal(data.Data, &host.last)
}

func viewKey(entry string, mode session.HostMode) string { return entry + "\x00" + string(mode) }

func attachHost(root, entry string, mode session.HostMode) (*hostBackend, error) {
	socketPath, err := session.SocketPath(root, entry, string(mode))
	if err != nil {
		return nil, err
	}
	requestPayload, _ := json.Marshal(session.HandshakePayload{Root: root, Document: entry, Mode: mode, Protocol: session.ProtocolVersion})
	response, err := session.Send(socketPath, session.Request{Version: session.ProtocolVersion, RequestID: opaqueID(), Action: session.ActionHandshake, Payload: requestPayload}, 500*time.Millisecond)
	if err != nil {
		return nil, fmt.Errorf("no matching automation-enabled %s host: %w", mode, err)
	}
	if !response.OK {
		return nil, fmt.Errorf("host handshake rejected: %s", response.Error)
	}
	var result session.HandshakeResult
	if err := json.Unmarshal(response.Data, &result); err != nil {
		return nil, fmt.Errorf("invalid host handshake response: %w", err)
	}
	if err := session.ValidateHandshake(session.HostIdentity{Root: root, Document: entry}, result.Host, result.Protocol, mode); err != nil {
		return nil, err
	}
	return &hostBackend{socketPath: socketPath, identity: result.Host, protocol: result.Protocol, connected: true, lastHost: studio.HostSnapshot{SchemaVersion: 1, HostProtocolVersion: result.Protocol, HostInstanceID: result.Host.InstanceID, Mode: string(result.Host.Mode), ConnectionState: "connected", ProcessID: result.Host.PID, Capabilities: append([]string(nil), result.Host.Capabilities...), Visible: true, WindowMode: "windowed"}}, nil
}

func canonicalDirectory(root string) (string, error) {
	if !filepath.IsAbs(root) {
		return "", fmt.Errorf("project root must be absolute")
	}
	canonical, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(canonical)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", fmt.Errorf("project root must be a directory")
	}
	return filepath.Clean(canonical), nil
}

func containedFile(root, file string) (string, error) {
	if !filepath.IsAbs(file) {
		file = filepath.Join(root, file)
	}
	canonical, err := filepath.EvalSymlinks(file)
	if err != nil {
		return "", err
	}
	canonical, err = filepath.Abs(canonical)
	if err != nil {
		return "", err
	}
	relative, err := filepath.Rel(root, canonical)
	if err != nil || relative == ".." || filepath.IsAbs(relative) || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("document is outside project root")
	}
	if filepath.Ext(canonical) != ".gora" {
		return "", fmt.Errorf("document must use the .gora extension")
	}
	return canonical, nil
}

func opaqueID() string {
	var bytes [12]byte
	if _, err := rand.Read(bytes[:]); err != nil {
		panic(err)
	}
	return base64.RawURLEncoding.EncodeToString(bytes[:])
}

var projectpkgValidate = project.Validate
