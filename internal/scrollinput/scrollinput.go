// Package scrollinput defines the renderer-neutral scroll event and routing
// records shared by native hosts and automation.
package scrollinput

import (
	"fmt"
	"image"
	"math"
)

// Event is one normalized scroll gesture before renderer ownership routing.
// Deltas are logical coordinates when Units is logical and physical pixels
// otherwise. Point is always a logical document coordinate.
type Event struct {
	Source    string
	Point     image.Point
	DeltaX    float64
	DeltaY    float64
	Units     string
	Phase     string
	Momentum  string
	Modifiers []string
}

// Consumer describes one axis-local owner in inner-to-outer routing order.
type Consumer struct {
	ID       string  `json:"id"`
	Axis     string  `json:"axis"`
	Before   float64 `json:"before"`
	After    float64 `json:"after"`
	Consumed float64 `json:"consumed"`
}

// AxisResult preserves independent X/Y residual propagation.
type AxisResult struct {
	Axis            string     `json:"axis"`
	Consumers       []Consumer `json:"consumers,omitempty"`
	Consumed        float64    `json:"consumed"`
	Residual        float64    `json:"residual"`
	ContainmentStop bool       `json:"containment_stop,omitempty"`
}

// Outcome is the typed routing record used by both tool results and tracing.
type Outcome struct {
	Source        string                 `json:"source"`
	Units         string                 `json:"units"`
	Phase         string                 `json:"phase"`
	Momentum      string                 `json:"momentum"`
	Point         image.Point            `json:"point"`
	LogicalDeltaX float64                `json:"logical_delta_x"`
	LogicalDeltaY float64                `json:"logical_delta_y"`
	ConsumedX     float64                `json:"consumed_x"`
	ConsumedY     float64                `json:"consumed_y"`
	ResidualX     float64                `json:"residual_x"`
	ResidualY     float64                `json:"residual_y"`
	Candidates    []string               `json:"candidates,omitempty"`
	OwnerID       string                 `json:"owner_id,omitempty"`
	FieldOwnerID  string                 `json:"field_owner_id,omitempty"`
	CanvasOwnerID string                 `json:"canvas_owner_id,omitempty"`
	Axes          []AxisResult           `json:"axes,omitempty"`
	FinalOffsets  map[string]image.Point `json:"final_offsets,omitempty"`
	Changed       bool                   `json:"changed"`
	NoFrameReason string                 `json:"no_frame_reason,omitempty"`
}

// Normalize converts one event to logical integer-like components using the
// published metric. It deliberately rounds each axis independently and keeps
// a nonzero sign when a sub-unit movement would otherwise round to zero.
func Normalize(event Event, scale float64) (Outcome, error) {
	if event.Source != "wheel" && event.Source != "trackpad" {
		return Outcome{}, fmt.Errorf("scroll source must be wheel or trackpad")
	}
	if event.Units != "logical" && event.Units != "physical_pixels" {
		return Outcome{}, fmt.Errorf("scroll units must be logical or physical_pixels")
	}
	if event.Phase != "begin" && event.Phase != "update" && event.Phase != "end" && event.Phase != "cancel" {
		return Outcome{}, fmt.Errorf("scroll phase must be begin, update, end, or cancel")
	}
	if event.Momentum == "" {
		event.Momentum = "none"
	}
	if event.Momentum != "none" && event.Momentum != "begin" && event.Momentum != "update" && event.Momentum != "end" {
		return Outcome{}, fmt.Errorf("scroll momentum must be none, begin, update, or end")
	}
	if !finite(event.DeltaX) || !finite(event.DeltaY) {
		return Outcome{}, fmt.Errorf("scroll deltas must be finite")
	}
	if !finite(float64(event.Point.X)) || !finite(float64(event.Point.Y)) {
		return Outcome{}, fmt.Errorf("scroll point must be finite")
	}
	if scale <= 0 || !finite(scale) {
		scale = 1
	}
	dx, dy := event.DeltaX, event.DeltaY
	if event.Units == "physical_pixels" {
		dx /= scale
		dy /= scale
	}
	dx = float64(roundComponent(dx))
	dy = float64(roundComponent(dy))
	return Outcome{Source: event.Source, Units: event.Units, Phase: event.Phase, Momentum: event.Momentum, Point: event.Point, LogicalDeltaX: dx, LogicalDeltaY: dy, ResidualX: dx, ResidualY: dy}, nil
}

func roundComponent(value float64) int {
	result := int(math.Round(value))
	if result == 0 && value != 0 {
		if value < 0 {
			return -1
		}
		return 1
	}
	return result
}

func finite(value float64) bool { return !math.IsNaN(value) && !math.IsInf(value, 0) }
