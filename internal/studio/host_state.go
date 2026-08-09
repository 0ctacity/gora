package studio

import (
	"errors"
	"image"
	"sort"
	"sync"
)

var errStudioZoomRange = errors.New("studio zoom must be between 0.25 and 4")

// HostStudioSnapshot is the immutable subset of Studio state exposed to MCP.
// It deliberately contains metadata only; document source and clipboard data
// remain behind their existing resources/tools.
type HostStudioSnapshot struct {
	Selection            string  `json:"selection,omitempty"`
	ViewportWidth        int     `json:"viewport_width"`
	ViewportHeight       int     `json:"viewport_height"`
	Zoom                 float32 `json:"zoom"`
	Inspect              bool    `json:"inspect"`
	SelectedSemanticID   string  `json:"selected_semantic_id,omitempty"`
	CanvasViewportWidth  int     `json:"canvas_viewport_width"`
	CanvasViewportHeight int     `json:"canvas_viewport_height"`
	CanvasWidth          int     `json:"canvas_width"`
	CanvasHeight         int     `json:"canvas_height"`
	CanvasPanX           int     `json:"canvas_pan_x"`
	CanvasPanY           int     `json:"canvas_pan_y"`
	Status               string  `json:"status,omitempty"`
	CaptureOutput        string  `json:"capture_output"`
}

// HostSnapshot is the single published host/window state used by attached
// MCP resources, assertions, and captures. It is copied on every read.
type HostSnapshot struct {
	SchemaVersion        int                 `json:"schema_version"`
	HostProtocolVersion  int                 `json:"host_protocol_version"`
	HostInstanceID       string              `json:"host_instance_id"`
	Mode                 string              `json:"mode"`
	ConnectionState      string              `json:"connection_state"`
	ProcessID            int                 `json:"process_id"`
	Capabilities         []string            `json:"capabilities"`
	LogicalClientWidth   int                 `json:"logical_client_width"`
	LogicalClientHeight  int                 `json:"logical_client_height"`
	PhysicalClientWidth  int                 `json:"physical_client_width"`
	PhysicalClientHeight int                 `json:"physical_client_height"`
	PxPerDp              float32             `json:"px_per_dp"`
	PxPerSp              float32             `json:"px_per_sp"`
	WindowMode           string              `json:"window_mode"`
	Focused              bool                `json:"focused"`
	Visible              bool                `json:"visible"`
	Closing              bool                `json:"closing"`
	HostRevision         uint64              `json:"host_revision"`
	RuntimeRevision      uint64              `json:"runtime_revision"`
	GeometryRevision     uint64              `json:"geometry_revision"`
	FrameRevision        uint64              `json:"frame_revision"`
	InputRevision        uint64              `json:"input_revision"`
	StudioRevision       uint64              `json:"studio_revision"`
	TraceRevision        uint64              `json:"trace_revision"`
	ConfigRevision       uint64              `json:"config_revision"`
	CommandState         string              `json:"command_state"`
	PendingCommands      int                 `json:"pending_commands"`
	Studio               *HostStudioSnapshot `json:"studio,omitempty"`
}

func cloneHostSnapshot(snapshot HostSnapshot) HostSnapshot {
	snapshot.Capabilities = append([]string(nil), snapshot.Capabilities...)
	if snapshot.Studio != nil {
		studio := *snapshot.Studio
		snapshot.Studio = &studio
	}
	return snapshot
}

// StudioController is the renderer-neutral reducer for authorable Studio
// state. Widgets and MCP commands both apply the same finite change object.
type StudioController struct {
	mu             sync.Mutex
	state          StudioState
	canvasViewport image.Point
	canvasSize     image.Point
}

type StudioState struct {
	Selection          string  `json:"selection,omitempty"`
	ViewportWidth      int     `json:"viewport_width"`
	ViewportHeight     int     `json:"viewport_height"`
	Zoom               float32 `json:"zoom"`
	Inspect            bool    `json:"inspect"`
	SelectedSemanticID string  `json:"selected_semantic_id,omitempty"`
	CanvasPanX         int     `json:"canvas_pan_x"`
	CanvasPanY         int     `json:"canvas_pan_y"`
	Status             string  `json:"status,omitempty"`
	CaptureOutput      string  `json:"capture_output,omitempty"`
	Revision           uint64  `json:"revision"`
}

type StudioStateChange struct {
	Selection          *string
	ViewportWidth      *int
	ViewportHeight     *int
	Zoom               *float32
	Inspect            *bool
	SelectedSemanticID *string
	PanX               *int
	PanY               *int
	Status             *string
	CaptureOutput      *string
	ResetState         bool
}

func NewStudioController() *StudioController {
	return &StudioController{state: StudioState{Zoom: 1}}
}

func (controller *StudioController) SetCanvas(viewport, size image.Point) {
	controller.mu.Lock()
	defer controller.mu.Unlock()
	controller.canvasViewport, controller.canvasSize = viewport, size
	controller.clampPanLocked()
}

func (controller *StudioController) Apply(change StudioStateChange) error {
	controller.mu.Lock()
	defer controller.mu.Unlock()
	candidate := controller.state
	if change.Selection != nil {
		candidate.Selection = *change.Selection
	}
	if change.ViewportWidth != nil {
		candidate.ViewportWidth = *change.ViewportWidth
	}
	if change.ViewportHeight != nil {
		candidate.ViewportHeight = *change.ViewportHeight
	}
	if candidate.ViewportWidth < 0 || candidate.ViewportHeight < 0 || (candidate.ViewportWidth == 0) != (candidate.ViewportHeight == 0) {
		return errors.New("studio viewport width and height must be supplied together")
	}
	if change.Zoom != nil {
		if *change.Zoom < 0.25 || *change.Zoom > 4 {
			return errStudioZoomRange
		}
		candidate.Zoom = *change.Zoom
	}
	if change.Inspect != nil {
		candidate.Inspect = *change.Inspect
	}
	if change.SelectedSemanticID != nil {
		candidate.SelectedSemanticID = *change.SelectedSemanticID
	}
	if change.PanX != nil {
		candidate.CanvasPanX = *change.PanX
	}
	if change.PanY != nil {
		candidate.CanvasPanY = *change.PanY
	}
	if change.Status != nil {
		candidate.Status = *change.Status
	}
	if change.CaptureOutput != nil {
		candidate.CaptureOutput = *change.CaptureOutput
	}
	controller.state = candidate
	controller.clampPanLocked()
	controller.state.Revision++
	return nil
}

func (controller *StudioController) Snapshot() StudioState {
	controller.mu.Lock()
	defer controller.mu.Unlock()
	return controller.state
}

// SyncFromUI reconciles widget-driven toolbar changes into the same reducer
// state without exposing widgets to socket goroutines.
func (controller *StudioController) SyncFromUI(state StudioState) {
	controller.mu.Lock()
	defer controller.mu.Unlock()
	state.Revision = controller.state.Revision
	if state.Zoom == 0 {
		state.Zoom = controller.state.Zoom
	}
	if state.Zoom < 0.25 {
		state.Zoom = 0.25
	}
	if state.Zoom > 4 {
		state.Zoom = 4
	}
	if state != controller.state {
		state.Revision++
		controller.state = state
		controller.clampPanLocked()
	}
}

func (controller *StudioController) clampPanLocked() {
	maxX := controller.canvasSize.X - controller.canvasViewport.X
	maxY := controller.canvasSize.Y - controller.canvasViewport.Y
	if maxX < 0 {
		maxX = 0
	}
	if maxY < 0 {
		maxY = 0
	}
	if controller.state.CanvasPanX < 0 {
		controller.state.CanvasPanX = 0
	}
	if controller.state.CanvasPanY < 0 {
		controller.state.CanvasPanY = 0
	}
	if controller.state.CanvasPanX > maxX {
		controller.state.CanvasPanX = maxX
	}
	if controller.state.CanvasPanY > maxY {
		controller.state.CanvasPanY = maxY
	}
}

func (controller *StudioController) PruneSelection(visible map[string]bool) {
	controller.mu.Lock()
	defer controller.mu.Unlock()
	if controller.state.SelectedSemanticID != "" && !visible[controller.state.SelectedSemanticID] {
		controller.state.SelectedSemanticID = ""
		controller.state.Revision++
	}
}

func sortedCapabilities(values []string) []string {
	result := append([]string(nil), values...)
	sort.Strings(result)
	return result
}
