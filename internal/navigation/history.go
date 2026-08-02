package navigation

import (
	"image"
	"sort"
)

const Capacity = 100

type Entry struct {
	Screen string
	Scroll map[string]image.Point
}

type Transition struct {
	Screen  string
	Scroll  map[string]image.Point
	Changed bool
}

// History owns bounded named-screen navigation and per-entry scroll state.
type History struct {
	entries []Entry
	index   int
}

func New(screen string) *History {
	return &History{entries: []Entry{{Screen: screen}}}
}

func (history *History) Current() string {
	if history == nil || history.index < 0 || history.index >= len(history.entries) {
		return ""
	}
	return history.entries[history.index].Screen
}

func (history *History) Len() int {
	if history == nil {
		return 0
	}
	return len(history.entries)
}

func (history *History) CanBack() bool {
	return history != nil && history.index > 0
}

func (history *History) CanForward() bool {
	return history != nil && history.index+1 < len(history.entries)
}

func (history *History) Navigate(screen string, currentScroll map[string]image.Point) Transition {
	if history == nil || screen == "" || screen == history.Current() {
		return history.transition(false)
	}
	history.saveCurrent(currentScroll)
	history.entries = append(history.entries[:history.index+1], Entry{Screen: screen})
	history.index++
	if len(history.entries) > Capacity {
		overflow := len(history.entries) - Capacity
		history.entries = append([]Entry(nil), history.entries[overflow:]...)
		history.index -= overflow
	}
	return history.transition(true)
}

func (history *History) Replace(screen string, _ map[string]image.Point) Transition {
	if history == nil || screen == "" || screen == history.Current() {
		return history.transition(false)
	}
	history.entries[history.index] = Entry{Screen: screen}
	return history.transition(true)
}

func (history *History) Back(currentScroll map[string]image.Point) Transition {
	if !history.CanBack() {
		return history.transition(false)
	}
	history.saveCurrent(currentScroll)
	history.index--
	return history.transition(true)
}

func (history *History) Forward(currentScroll map[string]image.Point) Transition {
	if !history.CanForward() {
		return history.transition(false)
	}
	history.saveCurrent(currentScroll)
	history.index++
	return history.transition(true)
}

func (history *History) Reset(screen string) Transition {
	changed := history == nil || history.Current() != screen || history.Len() != 1
	if history == nil {
		return Transition{Screen: screen, Changed: changed}
	}
	history.entries = []Entry{{Screen: screen}}
	history.index = 0
	return history.transition(changed)
}

func (history *History) Reconcile(valid map[string]map[string]bool, fallback string, currentScroll map[string]image.Point) Transition {
	if history == nil {
		return Transition{Screen: fallback}
	}
	previous := history.Current()
	history.saveCurrent(currentScroll)
	if _, currentValid := valid[previous]; !currentValid {
		fallback = validFallback(valid, fallback)
		history.entries = []Entry{{Screen: fallback}}
		history.index = 0
		return history.transition(previous != fallback)
	}

	next := make([]Entry, 0, len(history.entries))
	nextIndex := 0
	for index, entry := range history.entries {
		allowed, ok := valid[entry.Screen]
		if !ok {
			continue
		}
		entry.Scroll = pruneScroll(entry.Scroll, allowed)
		if index <= history.index {
			nextIndex = len(next)
		}
		next = append(next, entry)
	}
	history.entries = next
	history.index = nextIndex
	return history.transition(false)
}

func (history *History) saveCurrent(scroll map[string]image.Point) {
	if history == nil || history.index < 0 || history.index >= len(history.entries) {
		return
	}
	history.entries[history.index].Scroll = cloneScroll(scroll)
}

func (history *History) transition(changed bool) Transition {
	if history == nil || history.index < 0 || history.index >= len(history.entries) {
		return Transition{Changed: changed}
	}
	entry := history.entries[history.index]
	return Transition{Screen: entry.Screen, Scroll: cloneScroll(entry.Scroll), Changed: changed}
}

func cloneScroll(scroll map[string]image.Point) map[string]image.Point {
	if len(scroll) == 0 {
		return nil
	}
	clone := make(map[string]image.Point, len(scroll))
	for key, value := range scroll {
		clone[key] = value
	}
	return clone
}

func pruneScroll(scroll map[string]image.Point, allowed map[string]bool) map[string]image.Point {
	if len(scroll) == 0 {
		return nil
	}
	result := make(map[string]image.Point)
	for key, value := range scroll {
		if allowed[key] {
			result[key] = value
		}
	}
	if len(result) == 0 {
		return nil
	}
	return result
}

func validFallback(valid map[string]map[string]bool, fallback string) string {
	if _, ok := valid[fallback]; ok {
		return fallback
	}
	names := make([]string, 0, len(valid))
	for name := range valid {
		names = append(names, name)
	}
	sort.Strings(names)
	if len(names) == 0 {
		return ""
	}
	return names[0]
}
