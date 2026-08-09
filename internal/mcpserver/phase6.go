package mcpserver

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"gora/internal/automation"
	"gora/internal/document"
	"gora/internal/project"
	"gora/internal/session"
	"gora/internal/studio"
)

const (
	maxOverlayEntries = 256
	maxOverlayEntry   = 16 << 20
	maxOverlayBytes   = 64 << 20
	maxFaultRules     = 256
)

type TestOverlayEntry struct {
	Path       string `json:"path"`
	Kind       string `json:"kind" jsonschema:"source,bytes,missing"`
	Text       string `json:"text,omitempty"`
	DataBase64 string `json:"data_base64,omitempty"`
}

type ApplyTestOverlayInput struct {
	ProjectID           string             `json:"project_id"`
	ViewID              string             `json:"view_id"`
	BaseOverlayRevision string             `json:"base_overlay_revision,omitempty"`
	Entries             []TestOverlayEntry `json:"entries"`
	Install             bool               `json:"install,omitempty"`
	Wait                string             `json:"wait,omitempty" jsonschema:"none, published, or idle"`
	TimeoutMS           int                `json:"timeout_ms,omitempty"`
}

type ClearTestOverlayInput struct {
	ProjectID           string   `json:"project_id"`
	ViewID              string   `json:"view_id"`
	BaseOverlayRevision string   `json:"base_overlay_revision,omitempty"`
	Paths               []string `json:"paths,omitempty"`
	All                 bool     `json:"all,omitempty"`
	Wait                string   `json:"wait,omitempty" jsonschema:"none, published, or idle"`
	TimeoutMS           int      `json:"timeout_ms,omitempty"`
}

type ReloadEvent struct {
	Kind string `json:"kind" jsonschema:"write, create, remove, rename"`
	Path string `json:"path"`
	To   string `json:"to,omitempty"`
}

type InjectReloadEventsInput struct {
	ProjectID            string        `json:"project_id"`
	ViewID               string        `json:"view_id"`
	BaseOverlayRevision  string        `json:"base_overlay_revision,omitempty"`
	Events               []ReloadEvent `json:"events"`
	FinalOverlayRevision string        `json:"final_overlay_revision,omitempty"`
	Wait                 string        `json:"wait,omitempty" jsonschema:"none, published, or idle"`
	TimeoutMS            int           `json:"timeout_ms,omitempty"`
}

type TestFaultRule struct {
	Kind      string `json:"kind" jsonschema:"source_read,asset_read,image_decode,font_decode,candidate_cancel,capture_failure,delayed_candidate,stale_overlay"`
	Path      string `json:"path,omitempty"`
	Remaining int    `json:"remaining"`
}

type ConfigureTestFaultsInput struct {
	ProjectID string          `json:"project_id"`
	ViewID    string          `json:"view_id"`
	Rules     []TestFaultRule `json:"rules"`
}

type ClearTestFaultsInput struct {
	ProjectID string `json:"project_id"`
	ViewID    string `json:"view_id"`
	Kind      string `json:"kind,omitempty"`
	Path      string `json:"path,omitempty"`
	All       bool   `json:"all,omitempty"`
}

type OverlayEntrySummary struct {
	Path string `json:"path"`
	Kind string `json:"kind"`
	Size int    `json:"size"`
	Hash string `json:"hash"`
}

type TestOverlaySnapshot struct {
	Generation                uint64                `json:"generation"`
	Revision                  string                `json:"revision,omitempty"`
	Installed                 bool                  `json:"installed"`
	Entries                   []OverlayEntrySummary `json:"entries"`
	Staged                    []OverlayEntrySummary `json:"staged"`
	Valid                     bool                  `json:"valid"`
	Diagnostics               []document.Diagnostic `json:"diagnostics"`
	RuntimeRevision           uint64                `json:"runtime_revision"`
	FrameRevision             uint64                `json:"frame_revision"`
	GeometryRevision          uint64                `json:"geometry_revision"`
	PublishedRuntimeRevision  uint64                `json:"published_runtime_revision"`
	PublishedGeometryRevision uint64                `json:"published_geometry_revision"`
	LastGoodAvailable         bool                  `json:"last_good_available"`
	CandidateReload           bool                  `json:"candidate_reload"`
	PendingRevision           string                `json:"pending_revision,omitempty"`
	Faults                    []TestFaultRule       `json:"faults,omitempty"`
}

type TestOverlayOutput struct {
	ProjectID      string                    `json:"project_id"`
	ViewID         string                    `json:"view_id"`
	Overlay        TestOverlaySnapshot       `json:"overlay"`
	Snapshot       studio.AutomationSnapshot `json:"snapshot"`
	CoalescedPaths []string                  `json:"coalesced_paths,omitempty"`
	Dependencies   []string                  `json:"dependencies,omitempty"`
}

type viewOverlayEntry struct {
	kind string
	data []byte
}

type viewOverlay struct {
	generation      uint64
	revision        string
	installed       map[string]viewOverlayEntry
	staged          map[string]viewOverlayEntry
	pending         map[string]viewOverlayEntry
	pendingSet      bool
	pendingInstall  bool
	pendingRevision string
}

type testFaultRule struct {
	kind      string
	path      string
	remaining int
}

func newViewOverlay() *viewOverlay {
	return &viewOverlay{installed: make(map[string]viewOverlayEntry), revision: overlayRevision(nil)}
}

func cloneOverlay(in map[string]viewOverlayEntry) map[string]viewOverlayEntry {
	if in == nil {
		return nil
	}
	out := make(map[string]viewOverlayEntry, len(in))
	for path, entry := range in {
		entry.data = append([]byte(nil), entry.data...)
		out[path] = entry
	}
	return out
}

func overlayBytes(in map[string]viewOverlayEntry) map[string]project.OverlayFile {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]project.OverlayFile, len(in))
	for path, entry := range in {
		out[path] = project.OverlayFile{Kind: entry.kind, Data: append([]byte(nil), entry.data...)}
	}
	return out
}

type providerFaultUsage struct {
	key   string
	kind  string
	path  string
	usage *project.OverlayUsage
}

func (p *Project) providerForViewLocked(view *View) (map[string]project.OverlayFile, []providerFaultUsage) {
	if view == nil || view.overlay == nil {
		return nil, nil
	}
	files := overlayBytes(view.overlay.installed)
	if files == nil && len(view.faults) != 0 {
		files = make(map[string]project.OverlayFile)
	}
	usageByPath := make(map[string]*project.OverlayUsage)
	for path, file := range files {
		u := &project.OverlayUsage{}
		file.Usage = u
		files[path] = file
		usageByPath[path] = u
	}
	var usage []providerFaultUsage
	for key, rule := range view.faults {
		if rule.kind != "source_read" && rule.kind != "asset_read" && rule.kind != "image_decode" && rule.kind != "font_decode" {
			continue
		}
		file := files[rule.path]
		if file.Kind == "" {
			file = project.OverlayFile{Kind: "disk", Delegate: true}
		}
		u := usageByPath[rule.path]
		if u == nil {
			u = &project.OverlayUsage{}
			usageByPath[rule.path] = u
		}
		file.Usage = u
		file.FaultKinds = append(file.FaultKinds, rule.kind)
		files[rule.path] = file
		usage = append(usage, providerFaultUsage{key: key, kind: rule.kind, path: rule.path, usage: u})
	}
	for path, u := range usageByPath {
		found := false
		for _, item := range usage {
			if item.path == path {
				found = true
				break
			}
		}
		if !found {
			usage = append(usage, providerFaultUsage{path: path, usage: u})
		}
	}
	return files, usage
}

func consumeProviderFaultsLocked(view *View, usages []providerFaultUsage) {
	for _, item := range usages {
		used := false
		for _, access := range item.usage.Accesses {
			if access.Kind == item.kind {
				used = true
				break
			}
		}
		if !used {
			continue
		}
		rule := view.faults[item.key]
		if rule == nil {
			continue
		}
		rule.remaining--
		if rule.remaining <= 0 {
			delete(view.faults, item.key)
		}
	}
}

func overlayRevision(entries map[string]viewOverlayEntry) string {
	keys := make([]string, 0, len(entries))
	for path := range entries {
		keys = append(keys, path)
	}
	sort.Strings(keys)
	h := sha256.New()
	for _, path := range keys {
		entry := entries[path]
		fmt.Fprintf(h, "%s\x00%s\x00", path, entry.kind)
		h.Write(entry.data)
		h.Write([]byte{0})
	}
	return "sha256:" + hex.EncodeToString(h.Sum(nil))
}

var emptyOverlayRevision = overlayRevision(nil)

func overlayEntries(entries map[string]viewOverlayEntry, root string) []OverlayEntrySummary {
	result := make([]OverlayEntrySummary, 0, len(entries))
	for path, entry := range entries {
		relative, _ := filepath.Rel(root, path)
		result = append(result, OverlayEntrySummary{Path: filepath.ToSlash(relative), Kind: entry.kind, Size: len(entry.data), Hash: overlayRevision(map[string]viewOverlayEntry{path: entry})})
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Path < result[j].Path })
	return result
}

func (p *Project) overlaySnapshotLocked(view *View) TestOverlaySnapshot {
	if view.overlay == nil {
		return TestOverlaySnapshot{Entries: []OverlayEntrySummary{}, Staged: []OverlayEntrySummary{}, Diagnostics: []document.Diagnostic{}}
	}
	result := TestOverlaySnapshot{Generation: view.overlay.generation, Revision: view.overlay.revision, Installed: len(view.overlay.installed) != 0, Entries: overlayEntries(view.overlay.installed, p.root), Staged: overlayEntries(view.overlay.staged, p.root), Diagnostics: []document.Diagnostic{}, CandidateReload: view.overlay.pendingSet, PendingRevision: view.overlay.pendingRevision}
	for _, rule := range view.faults {
		result.Faults = append(result.Faults, TestFaultRule{Kind: rule.kind, Path: relativeOverlayPath(p.root, rule.path), Remaining: rule.remaining})
	}
	sort.Slice(result.Faults, func(i, j int) bool {
		if result.Faults[i].Kind != result.Faults[j].Kind {
			return result.Faults[i].Kind < result.Faults[j].Kind
		}
		return result.Faults[i].Path < result.Faults[j].Path
	})
	if view.runtime != nil {
		snapshot := view.runtime.Snapshot()
		result.Valid = !snapshot.Invalid
		result.Diagnostics = append(result.Diagnostics, snapshot.Diagnostics...)
		result.RuntimeRevision = snapshot.RuntimeRevision
		result.FrameRevision = snapshot.FrameRevision
		result.GeometryRevision = snapshot.GeometryRevision
		result.PublishedRuntimeRevision = snapshot.PublishedRuntimeRevision
		result.PublishedGeometryRevision = snapshot.PublishedGeometryRevision
		result.LastGoodAvailable = snapshot.Root != nil || snapshot.PublishedValid
	} else if view.host != nil {
		view.host.refresh(view.entry)
		view.host.mu.Lock()
		snapshot := view.host.last
		view.host.mu.Unlock()
		result.Valid = snapshot.Valid
		result.Diagnostics = append(result.Diagnostics, snapshot.Diagnostics...)
		result.RuntimeRevision = snapshot.RuntimeRevision
		result.FrameRevision = snapshot.FrameRevision
		result.GeometryRevision = snapshot.GeometryRevision
		result.PublishedRuntimeRevision = snapshot.PublishedRuntimeRevision
		result.PublishedGeometryRevision = snapshot.PublishedGeometryRevision
		result.LastGoodAvailable = snapshot.LastGoodAvailable
	}
	return result
}

func relativeOverlayPath(root, path string) string {
	if path == "" {
		return ""
	}
	relative, err := filepath.Rel(root, path)
	if err != nil {
		return filepath.ToSlash(path)
	}
	return filepath.ToSlash(relative)
}

func (r *Registry) OverlaySnapshot(projectID, viewID string) (TestOverlaySnapshot, error) {
	p, err := r.project(projectID)
	if err != nil {
		return TestOverlaySnapshot{}, err
	}
	p.mu.RLock()
	defer p.mu.RUnlock()
	v := p.views[viewID]
	if v == nil {
		return TestOverlaySnapshot{}, fmt.Errorf("unknown view %q", viewID)
	}
	return p.overlaySnapshotLocked(v), nil
}

func containedOverlayPath(root, name string) (string, error) {
	if name == "" {
		return "", fmt.Errorf("overlay path is required")
	}
	path := name
	if !filepath.IsAbs(path) {
		path = filepath.Join(root, path)
	}
	path, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	if resolved, evalErr := filepath.EvalSymlinks(path); evalErr == nil {
		path = resolved
	} else if !os.IsNotExist(evalErr) {
		return "", evalErr
	} else {
		// New overlay entries may introduce a path below a directory that does
		// not exist on disk. Resolve the nearest existing parent so symlink
		// containment remains enforced without requiring test fixture mkdirs.
		parts := []string{filepath.Base(path)}
		probe := filepath.Dir(path)
		for {
			parent, parentErr := filepath.EvalSymlinks(probe)
			if parentErr == nil {
				for index := len(parts) - 1; index >= 0; index-- {
					parent = filepath.Join(parent, parts[index])
				}
				path = parent
				break
			}
			if !os.IsNotExist(parentErr) || filepath.Dir(probe) == probe {
				return "", parentErr
			}
			parts = append(parts, filepath.Base(probe))
			probe = filepath.Dir(probe)
		}
	}
	relative, err := filepath.Rel(root, path)
	if err != nil || relative == ".." || filepath.IsAbs(relative) || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("overlay path is outside project root")
	}
	return filepath.Clean(path), nil
}

func decodeOverlayEntry(root string, input TestOverlayEntry) (string, viewOverlayEntry, error) {
	path, err := containedOverlayPath(root, input.Path)
	if err != nil {
		return "", viewOverlayEntry{}, err
	}
	entry := viewOverlayEntry{kind: input.Kind}
	switch input.Kind {
	case "source":
		entry.data = []byte(input.Text)
	case "bytes":
		entry.data, err = base64.StdEncoding.DecodeString(input.DataBase64)
		if err != nil {
			return "", viewOverlayEntry{}, fmt.Errorf("%s data_base64: %w", input.Path, err)
		}
	case "missing":
		entry.data = nil
	default:
		return "", viewOverlayEntry{}, fmt.Errorf("overlay kind must be source, bytes, or missing")
	}
	if len(entry.data) > maxOverlayEntry {
		return "", viewOverlayEntry{}, fmt.Errorf("overlay entry %q exceeds 16 MiB", input.Path)
	}
	return path, entry, nil
}

func validateOverlaySet(entries map[string]viewOverlayEntry) error {
	if len(entries) > maxOverlayEntries {
		return fmt.Errorf("overlay has more than %d entries", maxOverlayEntries)
	}
	total := 0
	for _, entry := range entries {
		if len(entry.data) > maxOverlayEntry {
			return fmt.Errorf("overlay entry exceeds 16 MiB")
		}
		total += len(entry.data)
		if total > maxOverlayBytes {
			return fmt.Errorf("overlay exceeds 64 MiB")
		}
	}
	return nil
}

func (p *Project) reloadViewOverlayLocked(view *View) error {
	if view.runtime == nil {
		if view.host != nil {
			provider, _ := p.providerForViewLocked(view)
			if err := view.host.command(context.Background(), "reload_overlay", map[string]any{"overlay": provider}, nil); err != nil {
				return err
			}
		}
		return nil
	}
	before := view.runtime.AutomationSnapshot()
	traceEnabled := view.runtime.EventTrace().Enabled
	record := func(typ, outcome, target string, after studio.AutomationSnapshot) {
		if !traceEnabled {
			return
		}
		traceBefore := view.runtime.EventTrace().Revision
		entry := automation.TraceEntry{Stage: "overlay", Type: typ, Outcome: outcome, TargetID: target, RuntimeBefore: before.RuntimeRevision, RuntimeAfter: after.RuntimeRevision, GeometryBefore: before.GeometryRevision, GeometryAfter: after.GeometryRevision, FrameBefore: before.FrameRevision, FrameAfter: after.FrameRevision, TraceBefore: traceBefore, TraceAfter: traceBefore + 1}
		view.runtime.RecordEventTrace(entry)
	}
	record("candidate", "start", "", before)
	if view.overlay == nil || (len(view.overlay.installed) == 0 && len(view.faults) == 0) {
		view.runtime.Reload()
	} else {
		provider, usages := p.providerForViewLocked(view)
		view.runtime.ReloadOverlay(provider)
		// Receipts are populated by the loader in actual source/asset traversal
		// order. Emit only accesses that occurred; configured but unused paths do
		// not appear in the candidate trace.
		type accessEvent struct {
			index uint64
			path  string
			kind  string
		}
		seen := make(map[*project.OverlayUsage]string)
		var accesses []accessEvent
		for _, item := range usages {
			if item.usage == nil {
				continue
			}
			if _, ok := seen[item.usage]; !ok {
				seen[item.usage] = item.path
				for _, access := range item.usage.Accesses {
					accesses = append(accesses, accessEvent{index: access.Index, path: item.path, kind: access.Kind})
				}
			}
		}
		sort.SliceStable(accesses, func(i, j int) bool { return accesses[i].index < accesses[j].index })
		for _, access := range accesses {
			after := view.runtime.AutomationSnapshot()
			record("read", access.kind, relativeOverlayPath(p.root, access.path), after)
			for _, fault := range usages {
				if fault.path == access.path && fault.kind == access.kind {
					record("fault", access.kind, relativeOverlayPath(p.root, access.path), after)
				}
			}
		}
		consumeProviderFaultsLocked(view, usages)
	}
	snapshot := view.runtime.Snapshot()
	if !snapshot.Invalid && view.driver != nil {
		if tree, err := view.runtime.RuntimeTree(); err == nil {
			view.driver.Update(tree)
			view.runtime.PublishRouterSnapshot(view.driver.Router().Snapshot())
		}
	} else if view.driver != nil {
		view.driver.Update(nil)
		view.runtime.PublishRouterSnapshot(view.driver.Router().Snapshot())
	}
	view.diagnostics = snapshot.Diagnostics
	after := view.runtime.AutomationSnapshot()
	if snapshot.Invalid {
		record("diagnostics", "invalid", "", after)
	} else {
		record("diagnostics", "none", "", after)
	}
	record("reconciliation", "complete", "", after)
	if snapshot.Invalid {
		record("publication", "no_frame", "", after)
	} else {
		record("publication", "installed", "", after)
	}
	return nil
}

func (r *Registry) ApplyTestOverlay(projectID, viewID, base string, entries []TestOverlayEntry, install bool) (TestOverlaySnapshot, error) {
	p, err := r.project(projectID)
	if err != nil {
		return TestOverlaySnapshot{}, err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	view := p.views[viewID]
	if view == nil {
		return TestOverlaySnapshot{}, fmt.Errorf("unknown view %q", viewID)
	}
	if view.overlay == nil {
		view.overlay = newViewOverlay()
	}
	if base == "" || base != view.overlay.revision {
		return TestOverlaySnapshot{}, fmt.Errorf("overlay revision conflict; base_overlay_revision must equal the current revision")
	}
	if consumeFaultLocked(view, "stale_overlay", "") {
		return TestOverlaySnapshot{}, fmt.Errorf("forced stale overlay conflict")
	}
	candidate := make(map[string]viewOverlayEntry)
	if view.overlay.staged != nil {
		candidate = cloneOverlay(view.overlay.staged)
	}
	for _, input := range entries {
		path, entry, decodeErr := decodeOverlayEntry(p.root, input)
		if decodeErr != nil {
			return TestOverlaySnapshot{}, decodeErr
		}
		candidate[path] = entry
	}
	if err := validateOverlaySet(candidate); err != nil {
		return TestOverlaySnapshot{}, err
	}
	if consumeFaultLocked(view, "candidate_cancel", "") {
		return TestOverlaySnapshot{}, fmt.Errorf("overlay candidate was not installed")
	}
	if consumeFaultLocked(view, "delayed_candidate", "") {
		view.overlay.pending = cloneOverlay(candidate)
		view.overlay.pendingSet = true
		view.overlay.pendingInstall = install
		view.overlay.pendingRevision = overlayRevision(candidate)
		if view.runtime != nil {
			snapshot := view.runtime.AutomationSnapshot()
			trace := view.runtime.EventTrace().Revision
			view.runtime.RecordEventTrace(automation.TraceEntry{Stage: "overlay", Type: "candidate", Outcome: "delayed", RuntimeBefore: snapshot.RuntimeRevision, RuntimeAfter: snapshot.RuntimeRevision, FrameBefore: snapshot.FrameRevision, FrameAfter: snapshot.FrameRevision, TraceBefore: trace, TraceAfter: trace + 1})
		}
		return p.overlaySnapshotLocked(view), nil
	}
	view.overlay.pending = nil
	view.overlay.pendingSet = false
	view.overlay.pendingRevision = ""
	view.overlay.generation++
	view.overlay.revision = overlayRevision(candidate)
	if install {
		view.overlay.installed = candidate
		view.overlay.staged = nil
		if err := p.reloadViewOverlayLocked(view); err != nil {
			return TestOverlaySnapshot{}, err
		}
	} else {
		view.overlay.staged = candidate
	}
	return p.overlaySnapshotLocked(view), nil
}

func (r *Registry) ClearTestOverlay(projectID, viewID, base string, paths []string, all bool) (TestOverlaySnapshot, error) {
	p, err := r.project(projectID)
	if err != nil {
		return TestOverlaySnapshot{}, err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	view := p.views[viewID]
	if view == nil {
		return TestOverlaySnapshot{}, fmt.Errorf("unknown view %q", viewID)
	}
	if view.overlay == nil {
		view.overlay = newViewOverlay()
	}
	if base == "" || base != view.overlay.revision {
		return TestOverlaySnapshot{}, fmt.Errorf("overlay revision conflict; base_overlay_revision must equal the current revision")
	}
	target := view.overlay.installed
	if view.overlay.staged != nil {
		target = view.overlay.staged
	}
	if all || len(paths) == 0 {
		target = make(map[string]viewOverlayEntry)
	} else {
		target = cloneOverlay(target)
		for _, name := range paths {
			path, pathErr := containedOverlayPath(p.root, name)
			if pathErr != nil {
				return TestOverlaySnapshot{}, pathErr
			}
			delete(target, path)
		}
	}
	view.overlay.generation++
	view.overlay.revision = overlayRevision(target)
	view.overlay.pending = nil
	view.overlay.pendingSet = false
	view.overlay.pendingInstall = false
	view.overlay.pendingRevision = ""
	view.overlay.installed = target
	view.overlay.staged = nil
	if err := p.reloadViewOverlayLocked(view); err != nil {
		return TestOverlaySnapshot{}, err
	}
	return p.overlaySnapshotLocked(view), nil
}

func consumeFaultLocked(view *View, kind, path string) bool {
	if view.faults == nil {
		return false
	}
	for key, rule := range view.faults {
		if rule.kind != kind || (rule.path != "" && filepath.Clean(rule.path) != filepath.Clean(path)) {
			continue
		}
		rule.remaining--
		if rule.remaining <= 0 {
			delete(view.faults, key)
		}
		return true
	}
	return false
}

func (r *Registry) ConfigureTestFaults(projectID, viewID string, rules []TestFaultRule) error {
	p, err := r.project(projectID)
	if err != nil {
		return err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	view := p.views[viewID]
	if view == nil {
		return fmt.Errorf("unknown view %q", viewID)
	}
	if len(rules) > maxFaultRules {
		return fmt.Errorf("fault rules exceed %d entries", maxFaultRules)
	}
	allowed := map[string]bool{"source_read": true, "asset_read": true, "image_decode": true, "font_decode": true, "candidate_cancel": true, "capture_failure": true, "delayed_candidate": true, "stale_overlay": true}
	next := make(map[string]*testFaultRule)
	for index, input := range rules {
		if !allowed[input.Kind] || input.Remaining < 1 || input.Remaining > 1000 {
			return fmt.Errorf("fault %d has unsupported kind or non-positive remaining count", index)
		}
		path := ""
		if input.Path != "" {
			var pathErr error
			path, pathErr = containedOverlayPath(p.root, input.Path)
			if pathErr != nil {
				return pathErr
			}
		}
		key := input.Kind + "\x00" + path
		next[key] = &testFaultRule{kind: input.Kind, path: path, remaining: input.Remaining}
	}
	view.faults = next
	return nil
}

func (r *Registry) ClearTestFaults(projectID, viewID, kind, path string, all bool) error {
	p, err := r.project(projectID)
	if err != nil {
		return err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	view := p.views[viewID]
	if view == nil {
		return fmt.Errorf("unknown view %q", viewID)
	}
	if all || (kind == "" && path == "") {
		view.faults = make(map[string]*testFaultRule)
		p.releasePendingOverlayLocked(view)
		return nil
	}
	if path != "" {
		resolved, pathErr := containedOverlayPath(p.root, path)
		if pathErr != nil {
			return pathErr
		}
		path = resolved
	}
	for key, rule := range view.faults {
		if (kind == "" || rule.kind == kind) && (path == "" || rule.path == path) {
			delete(view.faults, key)
		}
	}
	if kind == "" || kind == "delayed_candidate" {
		p.releasePendingOverlayLocked(view)
	}
	return nil
}

// ReleaseDelayedTestFaults models the automation clock release gate for a
// delayed candidate without exposing an authorable timer or touching disk.
func (r *Registry) ReleaseDelayedTestFaults(projectID, viewID string) {
	p, err := r.project(projectID)
	if err != nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if view := p.views[viewID]; view != nil {
		p.releasePendingOverlayLocked(view)
	}
}

func (p *Project) releasePendingOverlayLocked(view *View) {
	if view == nil || view.overlay == nil || !view.overlay.pendingSet {
		return
	}
	pending := cloneOverlay(view.overlay.pending)
	install := view.overlay.pendingInstall
	view.overlay.pending = nil
	view.overlay.pendingSet = false
	view.overlay.pendingInstall = false
	view.overlay.revision = view.overlay.pendingRevision
	view.overlay.pendingRevision = ""
	view.overlay.generation++
	if install {
		view.overlay.installed = pending
		view.overlay.staged = nil
		p.reloadViewOverlayLocked(view)
	} else {
		view.overlay.staged = pending
	}
}

func (r *Registry) ConsumeTestFault(projectID, viewID, kind string) bool {
	p, err := r.project(projectID)
	if err != nil {
		return false
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	view := p.views[viewID]
	if view == nil {
		return false
	}
	return consumeFaultLocked(view, kind, "")
}

func eventSignalRevision(base string, events []ReloadEvent) string {
	h := sha256.New()
	h.Write([]byte(base))
	for _, event := range events {
		fmt.Fprintf(h, "\x00%s\x00%s\x00%s", event.Kind, filepath.Clean(event.Path), filepath.Clean(event.To))
	}
	return "sha256:" + hex.EncodeToString(h.Sum(nil))
}

func (r *Registry) InjectReloadEvents(projectID, viewID, base, final string, events []ReloadEvent) (TestOverlaySnapshot, error) {
	p, err := r.project(projectID)
	if err != nil {
		return TestOverlaySnapshot{}, err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	view := p.views[viewID]
	if view == nil {
		return TestOverlaySnapshot{}, fmt.Errorf("unknown view %q", viewID)
	}
	if view.overlay == nil {
		view.overlay = newViewOverlay()
	}
	if base == "" || base != view.overlay.revision {
		return TestOverlaySnapshot{}, fmt.Errorf("overlay revision conflict; base_overlay_revision must equal the current revision")
	}
	candidate := cloneOverlay(view.overlay.installed)
	useFinal := false
	if view.overlay.staged != nil {
		candidate = cloneOverlay(view.overlay.staged)
		useFinal = true
	}
	if final != "" {
		switch final {
		case overlayRevision(view.overlay.staged):
			candidate = cloneOverlay(view.overlay.staged)
			useFinal = true
		case view.overlay.revision:
			candidate = cloneOverlay(view.overlay.installed)
			useFinal = true
		default:
			return TestOverlaySnapshot{}, fmt.Errorf("final_overlay_revision does not identify a staged or installed generation")
		}
	}
	for index, event := range events {
		switch event.Kind {
		case "write", "create":
			path, pathErr := containedOverlayPath(p.root, event.Path)
			if pathErr != nil {
				return TestOverlaySnapshot{}, pathErr
			}
			_ = path
		case "remove":
			path, pathErr := containedOverlayPath(p.root, event.Path)
			if pathErr != nil {
				return TestOverlaySnapshot{}, pathErr
			}
			if useFinal {
				delete(candidate, path)
			}
		case "rename":
			from, fromErr := containedOverlayPath(p.root, event.Path)
			to, toErr := containedOverlayPath(p.root, event.To)
			if fromErr != nil || toErr != nil {
				if fromErr != nil {
					return TestOverlaySnapshot{}, fromErr
				}
				return TestOverlaySnapshot{}, toErr
			}
			if useFinal {
				if entry, ok := candidate[from]; ok {
					delete(candidate, from)
					candidate[to] = entry
				}
			}
		default:
			return TestOverlaySnapshot{}, fmt.Errorf("event %d kind must be write, create, remove, or rename", index)
		}
	}
	if err := validateOverlaySet(candidate); err != nil {
		return TestOverlaySnapshot{}, err
	}
	if consumeFaultLocked(view, "candidate_cancel", "") {
		return TestOverlaySnapshot{}, fmt.Errorf("reload candidate was not installed")
	}
	if consumeFaultLocked(view, "delayed_candidate", "") {
		view.overlay.pending = cloneOverlay(candidate)
		view.overlay.pendingSet = true
		view.overlay.pendingInstall = true
		view.overlay.pendingRevision = overlayRevision(candidate)
		if view.runtime != nil {
			snapshot := view.runtime.AutomationSnapshot()
			trace := view.runtime.EventTrace().Revision
			view.runtime.RecordEventTrace(automation.TraceEntry{Stage: "overlay", Type: "candidate", Outcome: "delayed", RuntimeBefore: snapshot.RuntimeRevision, RuntimeAfter: snapshot.RuntimeRevision, FrameBefore: snapshot.FrameRevision, FrameAfter: snapshot.FrameRevision, TraceBefore: trace, TraceAfter: trace + 1})
		}
		return p.overlaySnapshotLocked(view), nil
	}
	view.overlay.pending = nil
	view.overlay.pendingSet = false
	view.overlay.pendingInstall = false
	view.overlay.pendingRevision = ""
	previousRevision := view.overlay.revision
	view.overlay.generation++
	view.overlay.revision = overlayRevision(candidate)
	if !useFinal && view.overlay.revision == previousRevision && len(events) != 0 {
		view.overlay.revision = eventSignalRevision(previousRevision, events)
	}
	view.overlay.installed = candidate
	view.overlay.staged = nil
	if err := p.reloadViewOverlayLocked(view); err != nil {
		return TestOverlaySnapshot{}, err
	}
	return p.overlaySnapshotLocked(view), nil
}

func (s *Service) registerPhase6Tools() {
	mutation := &mcp.ToolAnnotations{ReadOnlyHint: false, IdempotentHint: false, OpenWorldHint: boolPointer(false), DestructiveHint: boolPointer(false)}
	validateWait := func(wait string) error {
		if wait != "" && wait != "none" && wait != "published" && wait != "idle" {
			return fmt.Errorf("wait must be none, published, or idle")
		}
		return nil
	}
	registerWait := func(ctx context.Context, projectID, viewID, wait string, timeoutMS int, baseline uint64) error {
		if err := validateWait(wait); err != nil {
			return err
		}
		if wait == "" || wait == "none" {
			return nil
		}
		runtime, err := s.registry.Runtime(projectID, viewID)
		timeout := 5 * time.Second
		if timeoutMS != 0 {
			if timeoutMS < 1 || timeoutMS > 60000 {
				return fmt.Errorf("timeout_ms must be between 1 and 60000")
			}
			timeout = time.Duration(timeoutMS) * time.Millisecond
		}
		request := studio.WaitForViewRequest{Condition: wait, StableFrames: 1, Timeout: timeout, AfterFrameSet: true, AfterFrameRevision: baseline, AllowAlreadySatisfied: true}
		if runtime != nil {
			_, err = runtime.WaitForView(ctx, request)
		} else {
			_, err = s.registry.WaitForView(ctx, projectID, viewID, request)
		}
		return err
	}
	mcp.AddTool(s.server, &mcp.Tool{Name: "gora_apply_test_overlay", Description: "Apply a bounded view-local source or asset overlay, optionally installing it atomically.", Annotations: mutation}, func(ctx context.Context, _ *mcp.CallToolRequest, input ApplyTestOverlayInput) (*mcp.CallToolResult, TestOverlayOutput, error) {
		runtime, err := s.registry.Runtime(input.ProjectID, input.ViewID)
		var before studio.AutomationSnapshot
		if runtime != nil {
			before = runtime.AutomationSnapshot()
		} else {
			before, err = s.registry.AutomationSnapshot(input.ProjectID, input.ViewID)
			if err != nil {
				return nil, TestOverlayOutput{}, err
			}
		}
		overlay, err := s.registry.ApplyTestOverlay(input.ProjectID, input.ViewID, input.BaseOverlayRevision, input.Entries, input.Install)
		if err != nil {
			return nil, TestOverlayOutput{}, err
		}
		if err := validateWait(input.Wait); err != nil {
			return nil, TestOverlayOutput{}, err
		}
		if input.Install && !overlay.CandidateReload {
			if err := registerWait(ctx, input.ProjectID, input.ViewID, input.Wait, input.TimeoutMS, before.FrameRevision); err != nil {
				return nil, TestOverlayOutput{}, err
			}
		}
		s.notifyView(input.ProjectID, input.ViewID)
		snapshot, snapshotErr := s.registry.AutomationSnapshot(input.ProjectID, input.ViewID)
		if snapshotErr != nil && runtime != nil {
			return nil, TestOverlayOutput{}, snapshotErr
		}
		return nil, TestOverlayOutput{ProjectID: input.ProjectID, ViewID: input.ViewID, Overlay: overlay, Snapshot: snapshot}, nil
	})
	mcp.AddTool(s.server, &mcp.Tool{Name: "gora_clear_test_overlay", Description: "Clear selected or all entries from one view-local test overlay.", Annotations: mutation}, func(ctx context.Context, _ *mcp.CallToolRequest, input ClearTestOverlayInput) (*mcp.CallToolResult, TestOverlayOutput, error) {
		runtime, err := s.registry.Runtime(input.ProjectID, input.ViewID)
		var before studio.AutomationSnapshot
		if runtime != nil {
			before = runtime.AutomationSnapshot()
		} else {
			before, err = s.registry.AutomationSnapshot(input.ProjectID, input.ViewID)
			if err != nil {
				return nil, TestOverlayOutput{}, err
			}
		}
		overlay, err := s.registry.ClearTestOverlay(input.ProjectID, input.ViewID, input.BaseOverlayRevision, input.Paths, input.All)
		if err != nil {
			return nil, TestOverlayOutput{}, err
		}
		if !overlay.CandidateReload {
			if err := registerWait(ctx, input.ProjectID, input.ViewID, input.Wait, input.TimeoutMS, before.FrameRevision); err != nil {
				return nil, TestOverlayOutput{}, err
			}
		}
		s.notifyView(input.ProjectID, input.ViewID)
		snapshot, snapshotErr := s.registry.AutomationSnapshot(input.ProjectID, input.ViewID)
		if snapshotErr != nil && runtime != nil {
			return nil, TestOverlayOutput{}, snapshotErr
		}
		return nil, TestOverlayOutput{ProjectID: input.ProjectID, ViewID: input.ViewID, Overlay: overlay, Snapshot: snapshot}, nil
	})
	mcp.AddTool(s.server, &mcp.Tool{Name: "gora_inject_reload_events", Description: "Inject an ordered, contained reload event batch into one view-local overlay.", Annotations: mutation}, func(ctx context.Context, _ *mcp.CallToolRequest, input InjectReloadEventsInput) (*mcp.CallToolResult, TestOverlayOutput, error) {
		runtime, err := s.registry.Runtime(input.ProjectID, input.ViewID)
		var before studio.AutomationSnapshot
		if runtime != nil {
			before = runtime.AutomationSnapshot()
		} else {
			before, err = s.registry.AutomationSnapshot(input.ProjectID, input.ViewID)
			if err != nil {
				return nil, TestOverlayOutput{}, err
			}
		}
		overlay, err := s.registry.InjectReloadEvents(input.ProjectID, input.ViewID, input.BaseOverlayRevision, input.FinalOverlayRevision, input.Events)
		if err != nil {
			return nil, TestOverlayOutput{}, err
		}
		if err := validateWait(input.Wait); err != nil {
			return nil, TestOverlayOutput{}, err
		}
		if !overlay.CandidateReload {
			if err := registerWait(ctx, input.ProjectID, input.ViewID, input.Wait, input.TimeoutMS, before.FrameRevision); err != nil {
				return nil, TestOverlayOutput{}, err
			}
		}
		s.notifyView(input.ProjectID, input.ViewID)
		paths := make(map[string]bool)
		for _, event := range input.Events {
			if event.Path != "" {
				paths[filepath.ToSlash(filepath.Clean(event.Path))] = true
			}
			if event.To != "" {
				paths[filepath.ToSlash(filepath.Clean(event.To))] = true
			}
		}
		coalesced := make([]string, 0, len(paths))
		for path := range paths {
			coalesced = append(coalesced, path)
		}
		sort.Strings(coalesced)
		root, _ := s.registry.ProjectRoot(input.ProjectID)
		dependencies := make([]string, 0)
		dependenciesSource := []string{}
		if runtime != nil {
			dependenciesSource = runtime.Dependencies()
		}
		for _, dependency := range dependenciesSource {
			relative, relErr := filepath.Rel(root, dependency)
			if relErr == nil {
				dependencies = append(dependencies, filepath.ToSlash(relative))
			}
		}
		sort.Strings(dependencies)
		snapshot, snapshotErr := s.registry.AutomationSnapshot(input.ProjectID, input.ViewID)
		if snapshotErr != nil && runtime != nil {
			return nil, TestOverlayOutput{}, snapshotErr
		}
		return nil, TestOverlayOutput{ProjectID: input.ProjectID, ViewID: input.ViewID, Overlay: overlay, Snapshot: snapshot, CoalescedPaths: coalesced, Dependencies: dependencies}, nil
	})
	mcp.AddTool(s.server, &mcp.Tool{Name: "gora_configure_test_faults", Description: "Configure finite counted, view-local automation reload faults.", Annotations: mutation}, func(ctx context.Context, _ *mcp.CallToolRequest, input ConfigureTestFaultsInput) (*mcp.CallToolResult, TestOverlayOutput, error) {
		if err := s.registry.ConfigureTestFaults(input.ProjectID, input.ViewID, input.Rules); err != nil {
			return nil, TestOverlayOutput{}, err
		}
		if backend, backendErr := s.registry.Backend(input.ProjectID, input.ViewID); backendErr == nil && backend.Mode() != session.HostModeHeadless {
			faults := make(map[string]int)
			for _, rule := range input.Rules {
				if rule.Path == "" {
					faults[rule.Kind] = rule.Remaining
				}
			}
			if err := s.registry.HostCommand(ctx, input.ProjectID, input.ViewID, "configure_faults", map[string]any{"faults": faults}); err != nil {
				return nil, TestOverlayOutput{}, err
			}
		}
		overlay, err := s.registry.OverlaySnapshot(input.ProjectID, input.ViewID)
		if err != nil {
			return nil, TestOverlayOutput{}, err
		}
		runtime, err := s.registry.Runtime(input.ProjectID, input.ViewID)
		s.notifyView(input.ProjectID, input.ViewID)
		snapshot, snapshotErr := s.registry.AutomationSnapshot(input.ProjectID, input.ViewID)
		if snapshotErr != nil && runtime != nil {
			return nil, TestOverlayOutput{}, snapshotErr
		}
		return nil, TestOverlayOutput{ProjectID: input.ProjectID, ViewID: input.ViewID, Overlay: overlay, Snapshot: snapshot}, nil
	})
	mcp.AddTool(s.server, &mcp.Tool{Name: "gora_clear_test_faults", Description: "Clear finite counted automation faults for one view.", Annotations: mutation}, func(ctx context.Context, _ *mcp.CallToolRequest, input ClearTestFaultsInput) (*mcp.CallToolResult, TestOverlayOutput, error) {
		if err := s.registry.ClearTestFaults(input.ProjectID, input.ViewID, input.Kind, input.Path, input.All); err != nil {
			return nil, TestOverlayOutput{}, err
		}
		if backend, backendErr := s.registry.Backend(input.ProjectID, input.ViewID); backendErr == nil && backend.Mode() != session.HostModeHeadless {
			if err := s.registry.HostCommand(ctx, input.ProjectID, input.ViewID, "clear_faults", nil); err != nil {
				return nil, TestOverlayOutput{}, err
			}
		}
		overlay, err := s.registry.OverlaySnapshot(input.ProjectID, input.ViewID)
		if err != nil {
			return nil, TestOverlayOutput{}, err
		}
		runtime, err := s.registry.Runtime(input.ProjectID, input.ViewID)
		s.notifyView(input.ProjectID, input.ViewID)
		snapshot, snapshotErr := s.registry.AutomationSnapshot(input.ProjectID, input.ViewID)
		if snapshotErr != nil && runtime != nil {
			return nil, TestOverlayOutput{}, snapshotErr
		}
		return nil, TestOverlayOutput{ProjectID: input.ProjectID, ViewID: input.ViewID, Overlay: overlay, Snapshot: snapshot}, nil
	})
}

func (s *Service) registerPhase6Resources() {
	s.server.AddResourceTemplate(&mcp.ResourceTemplate{URITemplate: "gora://project/{project_id}/views/{view_id}/automation/overlay", Name: "gora-view-automation-overlay", MIMEType: "application/json"}, func(_ context.Context, request *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
		projectID, viewID, ok := parseOverlayURI(request.Params.URI)
		if !ok {
			return nil, mcp.ResourceNotFoundError(request.Params.URI)
		}
		overlay, err := s.registry.OverlaySnapshot(projectID, viewID)
		if err != nil {
			return nil, mcp.ResourceNotFoundError(request.Params.URI)
		}
		return jsonResource(request.Params.URI, overlay)
	})
}

func parseOverlayURI(uri string) (string, string, bool) {
	const prefix = "gora://project/"
	const suffix = "/automation/overlay"
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
