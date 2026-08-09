package automation

import (
	"fmt"
	"sync"
)

const defaultTraceCapacity = 512

// TraceEntry is intentionally scalar and bounded: it contains no pointers,
// arbitrary payloads, or references into a mutable runtime tree.
type TraceEntry struct {
	Sequence       uint64   `json:"sequence"`
	Revision       uint64   `json:"revision"`
	Stage          string   `json:"stage"`
	EventIndex     int      `json:"event_index"`
	Type           string   `json:"type,omitempty"`
	TargetID       string   `json:"target_id,omitempty"`
	SemanticID     string   `json:"semantic_id,omitempty"`
	Axis           string   `json:"axis,omitempty"`
	DeltaX         float64  `json:"delta_x,omitempty"`
	DeltaY         float64  `json:"delta_y,omitempty"`
	Value          float64  `json:"value,omitempty"`
	Consumed       float64  `json:"consumed,omitempty"`
	Residual       float64  `json:"residual,omitempty"`
	ConsumedX      float64  `json:"consumed_x,omitempty"`
	ConsumedY      float64  `json:"consumed_y,omitempty"`
	ResidualX      float64  `json:"residual_x,omitempty"`
	ResidualY      float64  `json:"residual_y,omitempty"`
	RuntimeBefore  uint64   `json:"runtime_before,omitempty"`
	RuntimeAfter   uint64   `json:"runtime_after,omitempty"`
	GeometryBefore uint64   `json:"geometry_before,omitempty"`
	GeometryAfter  uint64   `json:"geometry_after,omitempty"`
	FrameBefore    uint64   `json:"frame_before,omitempty"`
	FrameAfter     uint64   `json:"frame_after,omitempty"`
	TraceBefore    uint64   `json:"trace_before,omitempty"`
	TraceAfter     uint64   `json:"trace_after,omitempty"`
	Outcome        string   `json:"outcome,omitempty"`
	IDs            []string `json:"ids,omitempty"`
}

// TraceSnapshot is an immutable copy of one view's trace ring.
type TraceSnapshot struct {
	Enabled    bool         `json:"enabled"`
	Capacity   int          `json:"capacity"`
	Generation uint64       `json:"generation"`
	Revision   uint64       `json:"revision"`
	Entries    []TraceEntry `json:"entries"`
}

// TraceRecorder stores a fixed-capacity per-view ring. Disabled recording has
// no steady-state entry allocation.
type TraceRecorder struct {
	mu          sync.Mutex
	enabled     bool
	capacity    int
	generation  uint64
	revision    uint64
	sequence    uint64
	entries     []TraceEntry
	next        int
	subscribers map[int]chan uint64
	nextSub     int
	closed      bool
}

func NewTraceRecorder() *TraceRecorder {
	return &TraceRecorder{capacity: defaultTraceCapacity, subscribers: make(map[int]chan uint64)}
}

func (r *TraceRecorder) Configure(enabled bool, capacity int) error {
	if r == nil {
		return fmt.Errorf("trace recorder is unavailable")
	}
	if capacity == 0 {
		capacity = defaultTraceCapacity
	}
	if capacity < 1 || capacity > 4096 {
		return fmt.Errorf("trace capacity must be between 1 and 4096")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return fmt.Errorf("trace recorder is closed")
	}
	if enabled {
		r.generation++
	}
	r.enabled = enabled
	r.capacity = capacity
	r.entries = nil
	r.next = 0
	r.revision++
	r.notifyLocked()
	return nil
}

func (r *TraceRecorder) Clear() {
	if r == nil {
		return
	}
	r.mu.Lock()
	if !r.closed {
		r.entries = nil
		r.next = 0
		r.revision++
		r.notifyLocked()
	}
	r.mu.Unlock()
}

func (r *TraceRecorder) Record(entry TraceEntry) {
	if r == nil {
		return
	}
	r.mu.Lock()
	if r.closed || !r.enabled {
		r.mu.Unlock()
		return
	}
	r.sequence++
	r.revision++
	entry.Sequence = r.sequence
	entry.Revision = r.revision
	entry.IDs = append([]string(nil), entry.IDs...)
	if len(r.entries) < r.capacity {
		r.entries = append(r.entries, entry)
	} else {
		r.entries[r.next] = entry
		r.next = (r.next + 1) % r.capacity
	}
	r.notifyLocked()
	r.mu.Unlock()
}

func (r *TraceRecorder) notifyLocked() {
	for _, ch := range r.subscribers {
		select {
		case ch <- r.revision:
		default:
			select {
			case <-ch:
			default:
			}
			select {
			case ch <- r.revision:
			default:
			}
		}
	}
}

func (r *TraceRecorder) Snapshot() TraceSnapshot {
	if r == nil {
		return TraceSnapshot{Capacity: defaultTraceCapacity}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	snapshot := TraceSnapshot{Enabled: r.enabled, Capacity: r.capacity, Generation: r.generation, Revision: r.revision}
	if len(r.entries) == 0 {
		return snapshot
	}
	snapshot.Entries = make([]TraceEntry, 0, len(r.entries))
	for i := 0; i < len(r.entries); i++ {
		index := (r.next + i) % len(r.entries)
		entry := r.entries[index]
		entry.IDs = append([]string(nil), entry.IDs...)
		snapshot.Entries = append(snapshot.Entries, entry)
	}
	return snapshot
}

// Subscribe returns a coalescing revision channel. At most one pending
// notification is retained, so a slow subscriber observes the newest revision.
func (r *TraceRecorder) Subscribe() (<-chan uint64, func()) {
	if r == nil {
		return nil, func() {}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		closed := make(chan uint64)
		close(closed)
		return closed, func() {}
	}
	id := r.nextSub
	r.nextSub++
	ch := make(chan uint64, 1)
	r.subscribers[id] = ch
	return ch, func() {
		r.mu.Lock()
		if existing, ok := r.subscribers[id]; ok {
			delete(r.subscribers, id)
			close(existing)
		}
		r.mu.Unlock()
	}
}

func (r *TraceRecorder) Close() {
	if r == nil {
		return
	}
	r.mu.Lock()
	if !r.closed {
		r.closed = true
		for id, ch := range r.subscribers {
			delete(r.subscribers, id)
			close(ch)
		}
		r.entries = nil
	}
	r.mu.Unlock()
}
