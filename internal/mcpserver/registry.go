package mcpserver

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"gora/internal/document"
	"gora/internal/project"
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

func (r *Registry) OpenView(projectID, file string) (ViewSummary, error) {
	project, err := r.project(projectID)
	if err != nil {
		return ViewSummary{}, err
	}
	entry, err := containedFile(project.root, file)
	if err != nil {
		return ViewSummary{}, err
	}
	project.mu.Lock()
	defer project.mu.Unlock()
	if id := project.byEntry[entry]; id != "" {
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
	view := &View{id: opaqueID(), entry: entry, kind: kind, diagnostics: diagnostics}
	if kind != document.KindTokens {
		view.runtime = studio.NewRuntimeAllowInvalid(project.root, entry)
	}
	project.views[view.id] = view
	project.byEntry[entry] = view.id
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
		_, _ = view.runtime.RuntimeTree()
	}
	project.revision++
	project.refreshWatchLocked()
	return view.summary(), nil
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
	delete(project.byEntry, view.entry)
	project.revision++
	project.mu.Unlock()
	if view.runtime != nil {
		view.runtime.Close()
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
		return nil, fmt.Errorf("view %q does not support runtime operations", viewID)
	}
	return view.runtime, nil
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
		if view.runtime != nil {
			view.runtime.Close()
		}
	}
}

func (v *View) summary() ViewSummary {
	result := ViewSummary{ID: v.id, File: v.entry, Kind: v.kind, Valid: len(v.diagnostics) == 0, RuntimeAvailable: v.runtime != nil, Diagnostics: append([]document.Diagnostic(nil), v.diagnostics...), Selections: []string{}}
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
	if result.Diagnostics == nil {
		result.Diagnostics = []document.Diagnostic{}
	}
	return result
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
