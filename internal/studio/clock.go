package studio

import (
	"fmt"
	"sort"
	"time"
)

const caretBlinkIntervalMS int64 = 500
const maxInteractionTimers = 4096

// ViewClockSnapshot is the bounded automation clock state exposed in view
// snapshots. Timer identity is descriptive and contains no implementation
// pointers or arbitrary data.
type ViewClockSnapshot struct {
	Mode         string `json:"mode"`
	TimeMS       int64  `json:"time_ms"`
	NextTimerMS  *int64 `json:"next_timer_ms,omitempty"`
	NextTimer    string `json:"next_timer,omitempty"`
	BlinkVisible bool   `json:"blink_visible"`
}

type viewTimer struct {
	dueMS       int64
	source      string
	sourceOrder uint64
}

func (runtime *Runtime) clockNowLocked() int64 {
	if runtime.clockMode == "frozen" {
		return runtime.clockTimeMS
	}
	now := time.Now().UnixMilli()
	if now > runtime.clockTimeMS {
		runtime.clockTimeMS = now
	}
	return runtime.clockTimeMS
}

func (runtime *Runtime) scheduleBlinkLocked() {
	runtime.timerOrder++
	runtime.enqueueTimerLocked(viewTimer{dueMS: runtime.clockTimeMS + caretBlinkIntervalMS, source: "caret_blink", sourceOrder: runtime.timerOrder})
}

func (runtime *Runtime) enqueueTimerLocked(timer viewTimer) {
	runtime.timerQueue = append(runtime.timerQueue, timer)
	runtime.sortTimersLocked()
	if len(runtime.timerQueue) > maxInteractionTimers {
		runtime.timerQueue = runtime.timerQueue[:maxInteractionTimers]
	}
	runtime.refreshNextTimerLocked()
}

func (runtime *Runtime) sortTimersLocked() {
	sort.SliceStable(runtime.timerQueue, func(i, j int) bool {
		if runtime.timerQueue[i].dueMS != runtime.timerQueue[j].dueMS {
			return runtime.timerQueue[i].dueMS < runtime.timerQueue[j].dueMS
		}
		return runtime.timerQueue[i].sourceOrder < runtime.timerQueue[j].sourceOrder
	})
}

func (runtime *Runtime) refreshNextTimerLocked() {
	if len(runtime.timerQueue) == 0 {
		runtime.nextTimer = nil
		return
	}
	next := runtime.timerQueue[0]
	runtime.nextTimer = &next
}

func (runtime *Runtime) publishTransientFrameLocked() {
	if runtime.publishedTree == nil || !runtime.publishedValid {
		return
	}
	runtime.frameRevision++
	runtime.publishedRuntimeRevision = runtime.runtimeRevision
	runtime.publishedGeometryRevision = runtime.geometryRevision
	runtime.publicationStreak++
	runtime.publicationStartFrame = runtime.frameRevision
}

func (runtime *Runtime) clockSnapshotLocked() ViewClockSnapshot {
	now := runtime.clockTimeMS
	result := ViewClockSnapshot{Mode: runtime.clockMode, TimeMS: now, BlinkVisible: runtime.blinkVisible}
	if runtime.nextTimer != nil {
		due := runtime.nextTimer.dueMS
		result.NextTimerMS = &due
		result.NextTimer = runtime.nextTimer.source
	}
	return result
}

// SetViewClock switches a view between real and frozen interaction time.
func (runtime *Runtime) SetViewClock(mode string) (ViewClockSnapshot, error) {
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	if mode != "real" && mode != "frozen" {
		return ViewClockSnapshot{}, fmt.Errorf("clock mode must be real or frozen")
	}
	previous := runtime.clockMode
	if mode == "frozen" {
		runtime.clockNowLocked()
	} else {
		now := time.Now().UnixMilli()
		if now > runtime.clockTimeMS {
			runtime.clockTimeMS = now
		}
	}
	runtime.clockMode = mode
	if previous != mode {
		runtime.timerQueue = nil
		runtime.nextTimer = nil
		runtime.scheduleBlinkLocked()
		runtime.automationInputRevision++
		runtime.publishTransientFrameLocked()
		runtime.signalLocked()
	} else if len(runtime.timerQueue) == 0 {
		runtime.scheduleBlinkLocked()
		runtime.automationInputRevision++
		runtime.publishTransientFrameLocked()
		runtime.signalLocked()
	}
	return runtime.clockSnapshotLocked(), nil
}

func (runtime *Runtime) fireDueTimersLocked() int {
	fired := 0
	for fired < maxInteractionTimers && len(runtime.timerQueue) != 0 && runtime.timerQueue[0].dueMS <= runtime.clockTimeMS {
		timer := runtime.timerQueue[0]
		runtime.timerQueue = runtime.timerQueue[1:]
		runtime.refreshNextTimerLocked()
		fired++
		runtime.timerDispatchLog = append(runtime.timerDispatchLog, timer.source)
		if len(runtime.timerDispatchLog) > 64 {
			runtime.timerDispatchLog = runtime.timerDispatchLog[len(runtime.timerDispatchLog)-64:]
		}
		if timer.source == "caret_blink" {
			runtime.blinkVisible = !runtime.blinkVisible
			runtime.scheduleBlinkLocked()
		}
	}
	return fired
}

// AdvanceViewClock advances frozen interaction time by a positive duration.
// Due timers are processed deterministically in due/source order.
func (runtime *Runtime) AdvanceViewClock(deltaMS int64, runUntilIdle bool) (ViewClockSnapshot, error) {
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	if runtime.clockMode != "frozen" {
		return ViewClockSnapshot{}, fmt.Errorf("clock must be frozen before advancing")
	}
	if deltaMS <= 0 {
		return ViewClockSnapshot{}, fmt.Errorf("clock advance must be positive")
	}
	runtime.clockTimeMS += deltaMS
	fired := runtime.fireDueTimersLocked()
	if runUntilIdle {
		if fired == maxInteractionTimers && len(runtime.timerQueue) != 0 && runtime.timerQueue[0].dueMS <= runtime.clockTimeMS {
			// Keep a pathological large advance bounded; the next explicit
			// run_until_idle call continues from the retained due timer.
			runtime.timerQueue[0].dueMS = runtime.clockTimeMS
			runtime.sortTimersLocked()
			runtime.refreshNextTimerLocked()
		}
	}
	if fired != 0 {
		runtime.automationInputRevision++
		runtime.publishTransientFrameLocked()
		runtime.signalLocked()
	}
	return runtime.clockSnapshotLocked(), nil
}

// RunUntilIdle drains timers already due at the current frozen time.
func (runtime *Runtime) RunUntilIdle() (ViewClockSnapshot, error) {
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	if runtime.clockMode != "frozen" {
		return ViewClockSnapshot{}, fmt.Errorf("clock must be frozen before run_until_idle")
	}
	fired := runtime.fireDueTimersLocked()
	if fired != 0 {
		runtime.automationInputRevision++
		runtime.publishTransientFrameLocked()
		runtime.signalLocked()
	}
	return runtime.clockSnapshotLocked(), nil
}
