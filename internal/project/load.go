package project

import (
	"bytes"
	"fmt"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"golang.org/x/image/font/opentype"
	_ "golang.org/x/image/webp"

	"gora/internal/document"
)

type Loaded struct {
	Document     *document.Document
	Screens      map[string]*Node
	Previews     []string
	Selected     string
	Root         *Node
	Dependencies []string
	Viewport     document.Viewport
	StateScopes  []StateScope
}

// OverlayFile is a renderer-neutral provider entry. Missing deliberately
// shadows a disk path and returns os.ErrNotExist; zero-length bytes remain a
// valid file.
type OverlayFile struct {
	Kind       string
	Data       []byte
	Delegate   bool
	FaultKinds []string
	Usage      *OverlayUsage
}

// OverlayUsage is an internal, renderer-neutral read/decode receipt. The
// loader marks it only when the corresponding path is actually requested;
// callers use it to consume counted automation faults deterministically.
type OverlayUsage struct {
	Accesses []OverlayAccess
}

type OverlayAccess struct {
	Index uint64
	Kind  string
}

type StateReference struct {
	Scope string
	Name  string
}

type StateScope struct {
	ID      string
	Context string
	State   map[string]document.StateDeclaration
	Initial map[string]any
}

type Node struct {
	Handle       string
	Type         string
	Name         string
	SourceName   string
	Hidden       bool
	Props        map[string]any
	Place        map[string]any
	Children     []*Node
	Source       document.Source
	Breadcrumb   []string
	Scope        string
	Binding      string
	BindingState *document.StateDeclaration
	Form         string
	On           document.Events
	Variants     []document.Variant
}

type loader struct {
	root         string
	width        int
	cache        map[string]*document.Document
	loading      map[string]bool
	dependencies map[string]struct{}
	diagnostics  []document.Diagnostic
	nextHandle   int
	nextAccess   uint64
	stateScopes  map[string]StateScope
	appScreens   map[string]*document.Node
	overlayFiles map[string]OverlayFile
}

type resolveContext struct {
	doc        *document.Document
	parameters map[string]any
	components map[string]*document.Document
	tokens     map[string]*document.Document
	slots      map[string][]slotFill
	breadcrumb []string
	source     document.Source
	scope      string
	context    string
	form       string
}

// slotFill defers resolving caller-authored slot content until the component
// template reaches the slot. That preserves the caller's lexical scope while
// allowing the content to inherit structural context such as a surrounding
// component-owned form.
type slotFill struct {
	source  *document.Node
	context resolveContext
}

func Load(root, entry string, viewportWidth int) (*Loaded, []document.Diagnostic) {
	return LoadSelection(root, entry, viewportWidth, "")
}

func LoadSelection(root, entry string, viewportWidth int, selection string) (*Loaded, []document.Diagnostic) {
	return loadSelection(root, entry, viewportWidth, selection, nil)
}

// LoadSelectionOverlay resolves a project against in-memory source replacements.
func LoadSelectionOverlay(root, entry string, viewportWidth int, selection string, overlay map[string][]byte) (*Loaded, []document.Diagnostic) {
	files := make(map[string]OverlayFile, len(overlay))
	for path, data := range overlay {
		files[path] = OverlayFile{Kind: "bytes", Data: append([]byte(nil), data...)}
	}
	return LoadSelectionOverlayFiles(root, entry, viewportWidth, selection, files)
}

func LoadSelectionOverlayFiles(root, entry string, viewportWidth int, selection string, overlay map[string]OverlayFile) (*Loaded, []document.Diagnostic) {
	return loadSelection(root, entry, viewportWidth, selection, overlay)
}

func loadSelection(root, entry string, viewportWidth int, selection string, overlay map[string]OverlayFile) (*Loaded, []document.Diagnostic) {
	canonicalRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return nil, []document.Diagnostic{loadDiagnostic(root, "project.root", err.Error())}
	}
	canonicalRoot, err = filepath.Abs(canonicalRoot)
	if err != nil {
		return nil, []document.Diagnostic{loadDiagnostic(root, "project.root", err.Error())}
	}
	canonicalEntry, err := canonicalPathOverlay(canonicalRoot, entry, overlay)
	if err != nil {
		return nil, []document.Diagnostic{entryDiagnostic(entry, err)}
	}
	if filepath.Ext(canonicalEntry) != ".gora" {
		return nil, []document.Diagnostic{loadDiagnostic(canonicalEntry, "project.extension", "documents must use the .gora extension")}
	}

	l := &loader{
		root:         canonicalRoot,
		width:        viewportWidth,
		cache:        make(map[string]*document.Document),
		loading:      make(map[string]bool),
		dependencies: make(map[string]struct{}),
		stateScopes:  make(map[string]StateScope),
		overlayFiles: overlay,
	}
	doc := l.loadDocument(canonicalEntry)
	if doc == nil || len(l.diagnostics) != 0 {
		return nil, l.diagnostics
	}

	components, tokens := l.importsFor(doc)
	if doc.Kind == document.KindApp {
		l.appScreens = doc.Screens
	}
	ctx := resolveContext{doc: doc, components: components, tokens: tokens}
	loaded := &Loaded{
		Document: doc,
		Screens:  make(map[string]*Node),
		Viewport: doc.Viewport,
	}
	switch doc.Kind {
	case document.KindApp:
		background := resolveValue(doc.Viewport.Background, ctx, l)
		l.validateBackground(doc.File, background)
		for name, screen := range doc.Screens {
			screenContext := ctx
			screenContext.scope = "screen:" + name
			screenContext.context = name
			l.registerStateScope(screenContext.scope, name, doc.State, nil, screenContext)
			loaded.Screens[name] = l.viewportNode(l.resolveNode(screen, screenContext), background, doc)
		}
		loaded.Selected = doc.Entry
	case document.KindComponent:
		parameters := make(map[string]any)
		for name, parameter := range doc.Parameters {
			if parameter.Default != nil {
				parameters[name] = parameter.Default
			}
		}
		previewName, preview, ok := selectPreview(doc.Previews, selection)
		if ok {
			loaded.Selected = previewName
			for name, value := range preview.Parameters {
				parameters[name] = resolveValue(value, ctx, l)
			}
			if preview.Viewport != nil {
				loaded.Viewport = *preview.Viewport
			}
		}
		for name := range doc.Previews {
			loaded.Previews = append(loaded.Previews, name)
		}
		sort.Strings(loaded.Previews)
		ctx.parameters = parameters
		ctx.scope = "fixture:" + previewName
		ctx.context = previewName
		l.registerStateScope(ctx.scope, previewName, doc.State, preview.State, ctx)
		l.validateParameters(doc, parameters, preview.Source)
		ctx.slots = previewSlots(preview, ctx, l)
		l.validateSlots(doc, ctx.slots, preview.Source)
		background := resolveValue(loaded.Viewport.Background, ctx, l)
		l.validateBackground(doc.File, background)
		loaded.Root = l.viewportNode(l.resolveNode(doc.Root, ctx), background, doc)
	default:
		l.add(doc.File, 1, 1, "project.kind", "token modules cannot be previewed")
	}
	for _, screen := range loaded.Screens {
		l.trackAssets(screen)
	}
	l.trackAssets(loaded.Root)
	for dependency := range l.dependencies {
		loaded.Dependencies = append(loaded.Dependencies, dependency)
	}
	for _, scope := range l.stateScopes {
		loaded.StateScopes = append(loaded.StateScopes, scope)
	}
	sort.Slice(loaded.StateScopes, func(i, j int) bool { return loaded.StateScopes[i].ID < loaded.StateScopes[j].ID })
	sort.Strings(loaded.Dependencies)
	if len(l.diagnostics) != 0 {
		return nil, l.diagnostics
	}
	return loaded, nil
}

func (l *loader) validateBackground(file string, background any) {
	if background == nil {
		return
	}
	switch background.(type) {
	case string:
		if !resolvedColor(background.(string)) {
			l.add(file, 1, 1, "reference.type", "viewport background requires a color or linear gradient")
		}
	case map[string]any:
		if _, ok := background.(map[string]any)["stops"].([]any); !ok {
			l.add(file, 1, 1, "reference.type", "viewport background requires a color or linear gradient")
		}
	default:
		l.add(file, 1, 1, "reference.type", "viewport background requires a color or linear gradient")
	}
}

func (l *loader) viewportNode(child *Node, background any, doc *document.Document) *Node {
	if background == nil {
		return child
	}
	return &Node{
		Handle: l.handle(), Type: "_viewport",
		Props:    map[string]any{"background": background},
		Children: []*Node{child},
		Source:   document.Source{File: doc.File, Line: 1, Column: 1},
	}
}

func (l *loader) validateParameters(component *document.Document, values map[string]any, source document.Source) {
	if source.File == "" {
		source = document.Source{File: component.File, Line: 1, Column: 1}
	}
	for name, parameter := range component.Parameters {
		value, present := values[name]
		if parameter.Required && !present {
			l.add(source.File, source.Line, source.Column, "component.parameter", fmt.Sprintf("missing required parameter %q", name))
			continue
		}
		if present && !parameterValueMatches(parameter, value) {
			l.add(source.File, source.Line, source.Column, "component.parameter_type", fmt.Sprintf("parameter %q requires %s", name, parameter.Type))
		}
	}
	for name := range values {
		if _, exists := component.Parameters[name]; !exists {
			l.addSuggestions(source.File, source.Line, source.Column, "component.parameter", fmt.Sprintf("unknown parameter %q", name), stringKeys(component.Parameters))
		}
	}
}

func (l *loader) validateSlots(component *document.Document, values map[string][]slotFill, source document.Source) {
	if source.File == "" {
		source = document.Source{File: component.File, Line: 1, Column: 1}
	}
	for name, slot := range component.Slots {
		if slot.Required && len(values[name]) == 0 {
			l.add(source.File, source.Line, source.Column, "component.slot", fmt.Sprintf("missing required slot %q", name))
		}
	}
	for name := range values {
		if name == "default" {
			continue
		}
		if _, exists := component.Slots[name]; !exists {
			l.addSuggestions(source.File, source.Line, source.Column, "component.slot", fmt.Sprintf("unknown slot %q", name), stringKeys(component.Slots))
		}
	}
}

func (l *loader) trackAssets(node *Node) {
	if node == nil {
		return
	}
	var keys []string
	switch node.Type {
	case "image":
		keys = append(keys, "src")
	case "text":
		if fontPath, ok := node.Props["font"].(string); ok {
			extension := strings.ToLower(filepath.Ext(fontPath))
			if extension == ".ttf" || extension == ".otf" {
				keys = append(keys, "font")
			}
		}
	}
	for _, key := range keys {
		raw, _ := node.Props[key].(string)
		if raw == "" {
			l.add(node.Source.File, node.Source.Line, node.Source.Column, "asset.path", fmt.Sprintf("%s requires %s", node.Type, key))
			continue
		}
		path, err := canonicalPathOverlay(l.root, filepath.Join(filepath.Dir(node.Source.File), raw), l.overlayFiles)
		if err != nil {
			l.add(node.Source.File, node.Source.Line, node.Source.Column, "asset.path", err.Error())
			continue
		}
		extension := strings.ToLower(filepath.Ext(path))
		if key == "src" && extension != ".png" && extension != ".jpg" && extension != ".jpeg" && extension != ".webp" {
			l.add(node.Source.File, node.Source.Line, node.Source.Column, "asset.type", "images must be PNG, JPEG, or WebP")
			continue
		}
		if key == "src" {
			data, readErr := l.readFile(path, "asset_read")
			if readErr != nil {
				l.add(node.Source.File, node.Source.Line, node.Source.Column, "asset.decode", readErr.Error())
				continue
			}
			// Decode faults are injected after bytes are obtained and before the
			// concrete decoder runs, preserving source bytes for recovery.
			if l.decodeFault(path, "image_decode") {
				l.add(node.Source.File, node.Source.Line, node.Source.Column, "asset.decode", "injected decode error")
				continue
			}
			_, _, decodeErr := image.DecodeConfig(bytes.NewReader(data))
			if decodeErr != nil {
				l.add(node.Source.File, node.Source.Line, node.Source.Column, "asset.decode", decodeErr.Error())
				continue
			}
		} else {
			data, readErr := l.readFile(path, "asset_read")
			if readErr != nil {
				l.add(node.Source.File, node.Source.Line, node.Source.Column, "asset.decode", readErr.Error())
				continue
			}
			if l.decodeFault(path, "font_decode") {
				l.add(node.Source.File, node.Source.Line, node.Source.Column, "asset.decode", "injected decode error")
				continue
			}
			if _, parseErr := opentype.Parse(data); parseErr != nil {
				l.add(node.Source.File, node.Source.Line, node.Source.Column, "asset.decode", parseErr.Error())
				continue
			}
		}
		node.Props[key] = path
		l.dependencies[path] = struct{}{}
	}
	for _, child := range node.Children {
		l.trackAssets(child)
	}
}

// Validate loads the complete import graph without requiring a previewable
// document kind. It is the validation entry point used by the CLI.
func Validate(root, entry string) []document.Diagnostic {
	canonicalRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return []document.Diagnostic{loadDiagnostic(root, "project.root", err.Error())}
	}
	canonicalRoot, err = filepath.Abs(canonicalRoot)
	if err != nil {
		return []document.Diagnostic{loadDiagnostic(root, "project.root", err.Error())}
	}
	canonicalEntry, err := canonicalPath(canonicalRoot, entry)
	if err != nil {
		return []document.Diagnostic{entryDiagnostic(entry, err)}
	}
	if filepath.Ext(canonicalEntry) != ".gora" {
		return []document.Diagnostic{loadDiagnostic(canonicalEntry, "project.extension", "documents must use the .gora extension")}
	}
	l := &loader{
		root: canonicalRoot, cache: make(map[string]*document.Document),
		loading: make(map[string]bool), dependencies: make(map[string]struct{}),
	}
	doc := l.loadDocument(canonicalEntry)
	if doc == nil || len(l.diagnostics) != 0 {
		return l.diagnostics
	}
	for _, imported := range l.cache {
		if imported.Kind == document.KindTokens {
			l.validateTokenReferences(imported)
		}
	}
	if len(l.diagnostics) != 0 {
		return l.diagnostics
	}
	var diagnostics []document.Diagnostic
	if doc.Kind == document.KindApp {
		for _, width := range validationWidths(doc) {
			_, appDiagnostics := LoadSelection(canonicalRoot, canonicalEntry, width, "")
			diagnostics = appendUniqueDiagnostics(diagnostics, appDiagnostics)
		}
	}
	paths := make([]string, 0, len(l.cache))
	for path, imported := range l.cache {
		if imported.Kind == document.KindComponent {
			paths = append(paths, path)
		}
	}
	sort.Strings(paths)
	for _, path := range paths {
		diagnostics = appendUniqueDiagnostics(diagnostics, validateComponentFixtures(canonicalRoot, path, l.cache[path]))
	}
	return diagnostics
}

func (l *loader) validateTokenReferences(tokens *document.Document) {
	components, importedTokens := l.importsFor(tokens)
	context := resolveContext{
		doc: tokens, components: components, tokens: importedTokens,
		source: document.Source{File: tokens.File, Line: 1, Column: 1},
	}
	for kind, values := range tokens.Tokens {
		for name, value := range values {
			resolved := resolveValue(value, context, l)
			valid := false
			switch kind {
			case "color":
				text, ok := resolved.(string)
				valid = ok && resolvedColor(text)
			case "dimension":
				valid = resolvedDimension(resolved, false)
			case "font_face":
				value, ok := resolved.(map[string]any)
				_, sourceOK := value["src"].(string)
				valid = ok && sourceOK
			case "text_style", "shadow", "linear_gradient":
				_, valid = resolved.(map[string]any)
			}
			if !valid {
				l.add(tokens.File, 1, 1, "token.type", fmt.Sprintf("%s token %q resolved to the wrong type", kind, name))
			}
		}
	}
}

func validateComponentFixtures(root, path string, component *document.Document) []document.Diagnostic {
	names := make([]string, 0, len(component.Previews))
	for name := range component.Previews {
		names = append(names, name)
	}
	sort.Strings(names)
	var diagnostics []document.Diagnostic
	for _, name := range names {
		for _, width := range validationWidths(component) {
			_, fixtureDiagnostics := LoadSelection(root, path, width, name)
			diagnostics = appendUniqueDiagnostics(diagnostics, fixtureDiagnostics)
		}
	}
	return diagnostics
}

func validationWidths(doc *document.Document) []int {
	widths := map[int]bool{max(1, doc.Viewport.Width): true}
	for _, preview := range doc.Previews {
		if preview.Viewport != nil {
			widths[max(1, preview.Viewport.Width)] = true
		}
	}
	for _, breakpoint := range doc.Breakpoints {
		switch {
		case breakpoint.MinWidth != nil:
			widths[max(1, *breakpoint.MinWidth)] = true
		case breakpoint.MaxWidth != nil:
			widths[max(1, *breakpoint.MaxWidth)] = true
		}
	}
	result := make([]int, 0, len(widths))
	for width := range widths {
		result = append(result, width)
	}
	sort.Ints(result)
	return result
}

func appendUniqueDiagnostics(current, additions []document.Diagnostic) []document.Diagnostic {
	seen := make(map[string]bool, len(current))
	for _, diagnostic := range current {
		seen[diagnosticKey(diagnostic)] = true
	}
	for _, diagnostic := range additions {
		key := diagnosticKey(diagnostic)
		if !seen[key] {
			seen[key] = true
			current = append(current, diagnostic)
		}
	}
	return current
}

func diagnosticKey(diagnostic document.Diagnostic) string {
	return fmt.Sprintf("%s\x00%d\x00%d\x00%s\x00%s", diagnostic.File, diagnostic.Line, diagnostic.Column, diagnostic.Code, diagnostic.Message)
}

func (l *loader) loadDocument(path string) *document.Document {
	if l.loading[path] {
		l.add(path, 1, 1, "import.cycle", "component or token import cycle detected")
		return nil
	}
	if doc := l.cache[path]; doc != nil {
		return doc
	}
	l.loading[path] = true
	defer delete(l.loading, path)

	src, err := l.readFile(path, "source_read")
	if err != nil {
		l.add(path, 1, 1, "import.read", err.Error())
		return nil
	}
	l.dependencies[path] = struct{}{}
	doc, diagnostics := document.Parse(path, src)
	if len(diagnostics) != 0 {
		l.diagnostics = append(l.diagnostics, diagnostics...)
		return nil
	}
	l.cache[path] = doc
	l.trackTokenAssets(doc)

	for alias, importPath := range doc.Imports.Components {
		if filepath.Ext(importPath) != ".gora" {
			l.add(path, 1, 1, "import.extension", fmt.Sprintf("component import %q must use the .gora extension", alias))
			continue
		}
		resolved, err := canonicalPathOverlay(l.root, filepath.Join(filepath.Dir(path), importPath), l.overlayFiles)
		if err != nil {
			l.add(path, 1, 1, "import.path", fmt.Sprintf("component import %q: %v", alias, err))
			continue
		}
		imported := l.loadDocument(resolved)
		if imported != nil && imported.Kind != document.KindComponent {
			l.add(path, 1, 1, "import.kind", fmt.Sprintf("component import %q points to a %s document", alias, imported.Kind))
		}
	}
	for alias, importPath := range doc.Imports.Tokens {
		if filepath.Ext(importPath) != ".gora" {
			l.add(path, 1, 1, "import.extension", fmt.Sprintf("token import %q must use the .gora extension", alias))
			continue
		}
		resolved, err := canonicalPathOverlay(l.root, filepath.Join(filepath.Dir(path), importPath), l.overlayFiles)
		if err != nil {
			l.add(path, 1, 1, "import.path", fmt.Sprintf("token import %q: %v", alias, err))
			continue
		}
		imported := l.loadDocument(resolved)
		if imported != nil && imported.Kind != document.KindTokens {
			l.add(path, 1, 1, "import.kind", fmt.Sprintf("token import %q points to a %s document", alias, imported.Kind))
		}
	}
	return doc
}

func (l *loader) trackTokenAssets(doc *document.Document) {
	for name, raw := range doc.Tokens["font_face"] {
		value, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		source, _ := value["src"].(string)
		if source == "" {
			l.add(doc.File, 1, 1, "asset.path", fmt.Sprintf("font_face token %q requires src", name))
			continue
		}
		path, err := canonicalPathOverlay(l.root, filepath.Join(filepath.Dir(doc.File), source), l.overlayFiles)
		if err != nil {
			l.add(doc.File, 1, 1, "asset.path", err.Error())
			continue
		}
		extension := strings.ToLower(filepath.Ext(path))
		if extension != ".ttf" && extension != ".otf" {
			l.add(doc.File, 1, 1, "asset.type", "font faces must be TTF or OTF")
			continue
		}
		data, err := l.readFile(path, "asset_read")
		if err != nil {
			l.add(doc.File, 1, 1, "asset.decode", err.Error())
			continue
		}
		if l.decodeFault(path, "font_decode") {
			l.add(doc.File, 1, 1, "asset.decode", "injected decode error")
			continue
		}
		if _, err := opentype.Parse(data); err != nil {
			l.add(doc.File, 1, 1, "asset.decode", err.Error())
			continue
		}
		value["src"] = path
		l.dependencies[path] = struct{}{}
	}
}

func (l *loader) markAccess(path, kind string) {
	file, ok := l.overlayFiles[filepath.Clean(path)]
	if !ok || file.Usage == nil {
		return
	}
	l.nextAccess++
	file.Usage.Accesses = append(file.Usage.Accesses, OverlayAccess{Index: l.nextAccess, Kind: kind})
	l.overlayFiles[filepath.Clean(path)] = file
}

func containsFault(kinds []string, want string) bool {
	for _, kind := range kinds {
		if kind == want {
			return true
		}
	}
	return false
}

func (l *loader) readFile(path, accessKind string) ([]byte, error) {
	if file, ok := l.overlayFiles[filepath.Clean(path)]; ok {
		l.markAccess(path, accessKind)
		if containsFault(file.FaultKinds, accessKind) {
			return nil, fmt.Errorf("injected read error")
		}
		if file.Delegate {
			return os.ReadFile(path)
		}
		if file.Kind == "missing" {
			return nil, os.ErrNotExist
		}
		return append([]byte(nil), file.Data...), nil
	}
	return os.ReadFile(path)
}

func (l *loader) decodeFault(path, decodeKind string) bool {
	file, ok := l.overlayFiles[filepath.Clean(path)]
	if !ok {
		return false
	}
	l.markAccess(path, decodeKind)
	return containsFault(file.FaultKinds, decodeKind)
}

func (l *loader) importsFor(doc *document.Document) (map[string]*document.Document, map[string]*document.Document) {
	components := make(map[string]*document.Document)
	tokens := make(map[string]*document.Document)
	for alias, importPath := range doc.Imports.Components {
		path, err := canonicalPathOverlay(l.root, filepath.Join(filepath.Dir(doc.File), importPath), l.overlayFiles)
		if err == nil {
			components[alias] = l.cache[path]
		}
	}
	for alias, importPath := range doc.Imports.Tokens {
		path, err := canonicalPathOverlay(l.root, filepath.Join(filepath.Dir(doc.File), importPath), l.overlayFiles)
		if err == nil {
			tokens[alias] = l.cache[path]
		}
	}
	return components, tokens
}

func (l *loader) resolveNode(source *document.Node, ctx resolveContext) *Node {
	if source == nil {
		return nil
	}
	ctx.source = source.Source
	props := resolveMap(source.Props, ctx, l)
	place := resolveMap(source.Place, ctx, l)
	visible := true
	if breakpoint := activeBreakpoint(ctx.doc.Breakpoints, l.width); breakpoint != "" {
		if override, ok := source.Responsive[breakpoint]; ok {
			props = mergeMaps(props, resolveMap(override.Props, ctx, l))
			place = mergeMaps(place, resolveMap(override.Place, ctx, l))
			if override.Visible != nil {
				visible = *override.Visible
			}
		}
	}
	if style, ok := props["style"].(map[string]any); ok {
		delete(props, "style")
		props = mergeMaps(style, props)
	}
	validateResolvedScrollPolicyMix(source, props, l)
	normalizeResolvedValues(source, props, place)
	validateResolvedValues(source, props, place, l)
	if source.Type == "instance" {
		resolved := l.resolveInstance(source, props, place, ctx)
		if resolved != nil {
			resolved.Hidden = resolved.Hidden || !visible
		}
		return resolved
	}
	if source.Type == "slot" {
		name, _ := props["name"].(string)
		if content, ok := ctx.slots[name]; ok {
			children := make([]*Node, 0, len(content))
			for _, fill := range content {
				fillContext := fill.context
				fillContext.form = ctx.form
				if resolved := l.resolveNode(fill.source, fillContext); resolved != nil {
					children = append(children, resolved)
				}
			}
			group := groupNode(source, children, ctx.breadcrumb, l)
			group.Hidden = !visible
			return group
		}
		var defaults []*Node
		for _, child := range source.Children {
			if resolved := l.resolveNode(child, ctx); resolved != nil {
				defaults = append(defaults, resolved)
			}
		}
		group := groupNode(source, defaults, ctx.breadcrumb, l)
		group.Hidden = !visible
		return group
	}
	if source.Type == "slot_content" {
		l.add(source.Source.File, source.Source.Line, source.Source.Column, "component.slot_content", "slot_content is only valid as a direct child of an instance")
		return nil
	}

	node := &Node{
		Handle:     l.handle(),
		Type:       source.Type,
		Name:       source.Name,
		SourceName: source.Name,
		Hidden:     !visible,
		Props:      props,
		Place:      place,
		Source:     source.Source,
		Breadcrumb: append([]string(nil), ctx.breadcrumb...),
		Scope:      ctx.scope,
		Binding:    stringProp(props, "bind"),
		Form:       ctx.form,
		On:         resolveEvents(source.On, ctx, l),
		Variants:   resolveVariants(source.Variants, ctx, l),
	}
	if node.Type == "form" && ctx.form != "" {
		l.add(node.Source.File, node.Source.Line, node.Source.Column, "form.nested", "forms cannot be nested after component expansion")
	}
	if declaration, ok := ctx.doc.State[node.Binding]; ok {
		copy := declaration
		node.BindingState = &copy
	}
	l.validateResolvedInteraction(node, ctx)
	childContext := ctx
	if node.Type == "form" {
		childContext.form = node.Handle
	}
	for _, child := range source.Children {
		if resolved := l.resolveNode(child, childContext); resolved != nil {
			if resolved.Type == "_group" {
				node.Children = append(node.Children, resolved.Children...)
			} else {
				node.Children = append(node.Children, resolved)
			}
		}
	}
	propagateControlBinding(node, node.Binding, node.BindingState)
	l.validateResolvedControls(node)
	return node
}

func propagateControlBinding(node *Node, binding string, declaration *document.StateDeclaration) {
	if node == nil || binding == "" {
		return
	}
	for _, child := range node.Children {
		if child == nil {
			continue
		}
		switch child.Type {
		case "field_box", "field_support", "radio", "tab", "tab_panel", "select_trigger", "select_popup", "option", "slider_track", "slider_fill", "slider_thumb", "stepper_decrement", "stepper_value", "stepper_increment":
			child.Binding = binding
			child.BindingState = declaration
			propagateControlBinding(child, binding, declaration)
		}
	}
}

func stringProp(values map[string]any, key string) string {
	value, _ := values[key].(string)
	return value
}

func (l *loader) validateResolvedControls(node *Node) {
	if node == nil {
		return
	}
	if disabled, exists := node.Props["disabled"]; exists && !resolvedStateType(disabled, "boolean", l) {
		l.add(node.Source.File, node.Source.Line, node.Source.Column, "control.disabled_type", node.Type+" disabled must resolve to boolean")
	}
	allowed := map[string][]string{
		"text_field": {"text", "number"}, "text_area": {"text"},
		"toggle": {"boolean"}, "checkbox": {"boolean"},
		"radio_group": {"text", "number", "enum"}, "tabs": {"text", "number", "enum"}, "select": {"text", "number", "enum"},
		"slider": {"number"}, "stepper": {"number"},
	}
	if expected := allowed[node.Type]; len(expected) != 0 {
		if node.BindingState == nil || !containsString(expected, node.BindingState.Type) {
			l.add(node.Source.File, node.Source.Line, node.Source.Column, "control.binding", node.Type+" binding resolves to an incompatible state type")
		}
	}
	switch node.Type {
	case "form":
		if len(node.Children) != 1 {
			l.add(node.Source.File, node.Source.Line, node.Source.Column, "schema.children", "form requires exactly one visual child after component expansion")
		}
	case "text_field", "text_area":
		l.validateResolvedField(node)
		for _, child := range directChildren(node, "field_support") {
			for _, content := range child.Children {
				if containsResolvedInteractive(content) {
					l.add(child.Source.File, child.Source.Line, child.Source.Column, "control.nested", "field_support cannot contain interactive descendants after component expansion")
					break
				}
			}
		}
	case "radio_group":
		l.validateResolvedChoices(node, directChildren(node, "radio"))
	case "tabs":
		l.validateResolvedChoices(node, directChildren(node, "tab"))
		l.validateResolvedTabPanels(node)
	case "select":
		l.validateResolvedChoices(node, descendants(node, "option"))
	}
}

func containsResolvedInteractive(node *Node) bool {
	if node == nil {
		return false
	}
	switch node.Type {
	case "button", "link", "text_field", "text_area", "toggle", "checkbox", "radio_group", "radio", "tabs", "tab", "select", "option", "slider", "stepper":
		return true
	}
	for _, child := range node.Children {
		if containsResolvedInteractive(child) {
			return true
		}
	}
	return false
}

func (l *loader) validateResolvedField(node *Node) {
	label, ok := node.Props["label"].(string)
	if !ok || strings.TrimSpace(label) == "" {
		l.add(node.Source.File, node.Source.Line, node.Source.Column, node.Type+".label", node.Type+" label must resolve to non-empty text")
	}
	for _, key := range []string{"disabled", "read_only", "required"} {
		if value, exists := node.Props[key]; exists && !resolvedStateType(value, "boolean", l) {
			l.add(node.Source.File, node.Source.Line, node.Source.Column, "field."+key, key+" must resolve to boolean")
		}
	}
	for _, key := range []string{"min_length", "max_length"} {
		if value, exists := node.Props[key]; exists {
			number, valid := numberAsFloat64(value)
			if !valid || math.IsNaN(number) || math.IsInf(number, 0) || number < 0 || number != math.Trunc(number) {
				l.add(node.Source.File, node.Source.Line, node.Source.Column, "field.length", key+" must resolve to a non-negative integer")
			}
		}
	}
	if minimum, minOK := numberAsFloat64(node.Props["min_length"]); minOK {
		if maximum, maxOK := numberAsFloat64(node.Props["max_length"]); maxOK && minimum > maximum {
			l.add(node.Source.File, node.Source.Line, node.Source.Column, "field.length", "min_length must not exceed max_length")
		}
	}
	if value, exists := node.Props["pattern"]; exists {
		pattern, valid := value.(string)
		if !valid {
			l.add(node.Source.File, node.Source.Line, node.Source.Column, "field.pattern", "pattern must resolve to text")
		} else if _, err := regexp.Compile("^(?:" + pattern + ")$"); err != nil {
			l.add(node.Source.File, node.Source.Line, node.Source.Column, "field.pattern", "pattern must resolve to a valid RE2 expression")
		}
	}
	if node.Type == "text_area" {
		for _, key := range []string{"min_lines", "max_lines"} {
			if value, exists := node.Props[key]; exists {
				number, valid := numberAsFloat64(value)
				if !valid || math.IsNaN(number) || math.IsInf(number, 0) || number <= 0 || number != math.Trunc(number) {
					l.add(node.Source.File, node.Source.Line, node.Source.Column, "field.lines", key+" must resolve to a positive integer")
				}
			}
		}
		if minimum, minOK := numberAsFloat64(node.Props["min_lines"]); minOK {
			if maximum, maxOK := numberAsFloat64(node.Props["max_lines"]); maxOK && minimum > maximum {
				l.add(node.Source.File, node.Source.Line, node.Source.Column, "field.lines", "min_lines must not exceed max_lines")
			}
		}
	}
}

func (l *loader) validateResolvedChoices(owner *Node, choices []*Node) {
	if owner.BindingState == nil {
		return
	}
	seen := make(map[string]bool)
	for _, choice := range choices {
		value, exists := choice.Props["value"]
		if !exists || !resolvedStateValueMatches(*owner.BindingState, value, l) {
			l.add(choice.Source.File, choice.Source.Line, choice.Source.Column, "control.value", choice.Type+" value resolves to the wrong bound-state type")
			continue
		}
		key := fmt.Sprintf("%T:%v", value, value)
		if seen[key] {
			l.add(choice.Source.File, choice.Source.Line, choice.Source.Column, "control.value_duplicate", owner.Type+" values must be unique after resolution")
		}
		seen[key] = true
	}
}

func (l *loader) validateResolvedTabPanels(node *Node) {
	tabs := make(map[string]bool)
	panels := make(map[string]bool)
	for _, child := range node.Children {
		if child == nil {
			continue
		}
		key := fmt.Sprintf("%T:%v", child.Props["value"], child.Props["value"])
		switch child.Type {
		case "tab":
			tabs[key] = true
		case "tab_panel":
			panels[key] = true
		}
	}
	if len(tabs) != len(panels) {
		l.add(node.Source.File, node.Source.Line, node.Source.Column, "tabs.pairing", "tabs require one matching panel per resolved tab value")
		return
	}
	for key := range tabs {
		if !panels[key] {
			l.add(node.Source.File, node.Source.Line, node.Source.Column, "tabs.pairing", "tabs require one matching panel per resolved tab value")
			return
		}
	}
}

func directChildren(node *Node, nodeType string) []*Node {
	var result []*Node
	for _, child := range node.Children {
		if child != nil && child.Type == nodeType {
			result = append(result, child)
		}
	}
	return result
}

func descendants(node *Node, nodeType string) []*Node {
	var result []*Node
	for _, child := range node.Children {
		if child == nil {
			continue
		}
		if child.Type == nodeType {
			result = append(result, child)
		}
		result = append(result, descendants(child, nodeType)...)
	}
	return result
}

func (l *loader) validateResolvedInteraction(node *Node, ctx resolveContext) {
	if node.Type == "button" || node.Type == "link" {
		if disabled, exists := node.Props["disabled"]; exists && !resolvedStateType(disabled, "boolean", l) {
			l.add(node.Source.File, node.Source.Line, node.Source.Column, node.Type+".disabled_type", node.Type+" disabled must resolve to boolean state")
		}
	}
	if node.Type == "link" {
		target, ok := node.Props["to"].(string)
		if !ok || strings.TrimSpace(target) == "" {
			l.add(node.Source.File, node.Source.Line, node.Source.Column, "link.target", "link target must resolve to text")
		} else if l.appScreens != nil {
			if _, exists := l.appScreens[target]; !exists {
				l.addSuggestions(node.Source.File, node.Source.Line, node.Source.Column, "link.target", fmt.Sprintf("link target %q does not exist", target), stringKeys(l.appScreens))
			}
		}
	}
	actions := append(append([]document.Action(nil), node.On.Activate...), node.On.Submit...)
	for _, action := range actions {
		if action.Action == "navigate" || action.Action == "replace" {
			if l.appScreens != nil {
				if _, exists := l.appScreens[action.To]; !exists {
					l.addSuggestions(action.Source.File, action.Source.Line, action.Source.Column, "action.target", fmt.Sprintf("navigation target %q does not exist", action.To), stringKeys(l.appScreens))
				}
			}
			continue
		}
		if action.Action == "back" || action.Action == "forward" {
			continue
		}
		declaration, ok := ctx.doc.State[action.State]
		if !ok {
			continue
		}
		switch action.Action {
		case "set":
			if !resolvedStateValueMatches(declaration, action.Value, l) {
				l.add(action.Source.File, action.Source.Line, action.Source.Column, "action.resolved_type", fmt.Sprintf("set value for %q resolves to the wrong type", action.State))
			}
		case "increment", "decrement":
			if action.By != nil && !resolvedStateType(action.By, "number", l) {
				l.add(action.Source.File, action.Source.Line, action.Source.Column, "action.resolved_type", fmt.Sprintf("%s by for %q must resolve to a number", action.Action, action.State))
			}
		}
	}
	for _, variant := range node.Variants {
		if variant.When.State == "" {
			continue
		}
		declaration, ok := ctx.doc.State[variant.When.State]
		if ok && !resolvedStateValueMatches(declaration, variant.When.Value, l) {
			l.add(variant.When.Source.File, variant.When.Source.Line, variant.When.Source.Column, "variant.resolved_type", fmt.Sprintf("comparison for state %q resolves to the wrong type", variant.When.State))
		}
	}
}

func validateResolvedScrollPolicyMix(source *document.Node, props map[string]any, l *loader) {
	if source == nil || source.Type != "scroll" {
		return
	}
	if _, legacy := props["scrollbar"]; !legacy {
		return
	}
	_, hasX := props["scrollbar_x"]
	_, hasY := props["scrollbar_y"]
	if hasX || hasY {
		l.add(source.Source.File, source.Source.Line, source.Source.Column, "reference.type", "legacy scrollbar cannot be combined with scrollbar_x or scrollbar_y")
	}
}

// normalizeResolvedValues fills the effective defaults used by renderers and
// inspectors after references and responsive overrides have been resolved.
// Authored legacy fields are intentionally retained in props.
func normalizeResolvedValues(source *document.Node, props, place map[string]any) {
	if source == nil {
		return
	}
	// Instance placement is caller-authored input to component expansion. Do
	// not materialize the flow default here, or it would overwrite a positioned
	// component root when the instance has no placement override.
	if source.Type == "instance" {
		return
	}
	position := "flow"
	if raw, exists := place["position"]; exists {
		if text, ok := raw.(string); ok {
			position = text
		}
	} else {
		place["position"] = position
	}
	if position == "sticky" || position == "fixed" {
		if _, exists := place["z_index"]; !exists {
			place["z_index"] = int64(0)
		}
	}

	if source.Type != "scroll" {
		return
	}
	axis := "vertical"
	if raw, exists := props["axis"]; exists {
		if text, ok := raw.(string); ok {
			axis = text
		}
	} else {
		props["axis"] = axis
	}
	enabled := func(axisName string) bool {
		return axis == "both" || axis == axisName
	}
	legacy, hasLegacy := props["scrollbar"]
	if hasLegacy {
		if visible, ok := legacy.(bool); ok {
			policy := "hidden"
			if visible {
				policy = "auto"
			}
			if enabled("horizontal") {
				props["scrollbar_x"] = policy
			} else {
				props["scrollbar_x"] = "hidden"
			}
			if enabled("vertical") {
				props["scrollbar_y"] = policy
			} else {
				props["scrollbar_y"] = "hidden"
			}
		}
	} else {
		if _, exists := props["scrollbar_x"]; !exists {
			if enabled("horizontal") {
				props["scrollbar_x"] = "auto"
			} else {
				props["scrollbar_x"] = "hidden"
			}
		}
		if _, exists := props["scrollbar_y"]; !exists {
			if enabled("vertical") {
				props["scrollbar_y"] = "auto"
			} else {
				props["scrollbar_y"] = "hidden"
			}
		}
	}
	if _, exists := props["scroll_chain"]; !exists {
		props["scroll_chain"] = "auto"
	}
}

func resolvedStateType(value any, expected string, l *loader) bool {
	if reference, ok := value.(StateReference); ok {
		scope, exists := l.stateScopes[reference.Scope]
		declaration, declared := scope.State[reference.Name]
		return exists && declared && declaration.Type == expected
	}
	switch expected {
	case "boolean":
		_, ok := value.(bool)
		return ok
	case "number":
		return resolvedNumber(value, false)
	case "text", "enum":
		_, ok := value.(string)
		return ok
	}
	return false
}

func resolvedStateValueMatches(declaration document.StateDeclaration, value any, l *loader) bool {
	if reference, ok := value.(StateReference); ok {
		scope, exists := l.stateScopes[reference.Scope]
		referenced, declared := scope.State[reference.Name]
		return exists && declared && referenced.Type == declaration.Type
	}
	return stateDeclarationMatches(declaration, value)
}

func validateResolvedValues(source *document.Node, props, place map[string]any, l *loader) {
	enum := func(key string, choices ...string) {
		value, exists := props[key]
		if !exists {
			return
		}
		text, ok := value.(string)
		if !ok {
			l.add(source.Source.File, source.Source.Line, source.Source.Column, "reference.type", fmt.Sprintf("prop %q requires text", key))
			return
		}
		for _, choice := range choices {
			if text == choice {
				return
			}
		}
		l.addSuggestions(source.Source.File, source.Source.Line, source.Source.Column, "reference.type", fmt.Sprintf("invalid %s %q", key, text), choices)
	}
	switch source.Type {
	case "stack":
		enum("direction", "horizontal", "vertical")
		enum("alignment", "start", "center", "end", "stretch")
		enum("distribution", "start", "center", "end", "space_between", "space_around")
	case "scroll":
		enum("axis", "horizontal", "vertical", "both")
	case "image":
		enum("fit", "contain", "cover", "fill")
	case "divider":
		enum("orientation", "horizontal", "vertical")
	}
	for _, key := range []string{"clip", "italic", "wrap"} {
		if value, exists := props[key]; exists {
			if _, dynamic := value.(StateReference); dynamic {
				continue
			}
			if _, ok := value.(bool); !ok {
				l.add(source.Source.File, source.Source.Line, source.Source.Column, "reference.type", fmt.Sprintf("prop %q requires true or false", key))
			}
		}
	}
	if source.Type == "scroll" {
		if value, exists := props["scrollbar"]; exists {
			if _, ok := value.(bool); !ok {
				l.add(source.Source.File, source.Source.Line, source.Source.Column, "reference.type", "prop \"scrollbar\" requires true or false")
			}
		}
		for _, key := range []string{"scrollbar_x", "scrollbar_y"} {
			if value, exists := props[key]; exists {
				text, ok := value.(string)
				if !ok {
					l.add(source.Source.File, source.Source.Line, source.Source.Column, "reference.type", fmt.Sprintf("prop %q requires auto, always, or hidden", key))
					continue
				}
				if !containsString([]string{"auto", "always", "hidden"}, text) {
					l.addSuggestions(source.Source.File, source.Source.Line, source.Source.Column, "reference.type", fmt.Sprintf("invalid %s %q", key, text), []string{"auto", "always", "hidden"})
				}
			}
		}
		if value, exists := props["scroll_chain"]; exists {
			text, ok := value.(string)
			if !ok || !containsString([]string{"auto", "contain"}, text) {
				l.addSuggestions(source.Source.File, source.Source.Line, source.Source.Column, "reference.type", "scroll_chain must resolve to auto or contain", []string{"auto", "contain"})
			}
		}
		axis, axisOK := props["axis"].(string)
		if axisOK && (axis == "horizontal" || axis == "vertical") {
			checkDisabled := func(key, axisName string) {
				value, exists := props[key]
				if !exists {
					return
				}
				if _, dynamic := value.(StateReference); dynamic {
					return
				}
				if text, ok := value.(string); ok && text != "hidden" {
					l.add(source.Source.File, source.Source.Line, source.Source.Column, "reference.type", fmt.Sprintf("%s must resolve to hidden when %s scroll axis is disabled", key, axisName))
				}
			}
			if axis == "horizontal" {
				checkDisabled("scrollbar_y", "horizontal")
			} else {
				checkDisabled("scrollbar_x", "vertical")
			}
		}
	}
	for _, key := range []string{"width", "height", "min_width", "max_width", "min_height", "max_height"} {
		value, exists := props[key]
		if !exists {
			continue
		}
		if !resolvedDimension(value, true) {
			l.add(source.Source.File, source.Source.Line, source.Source.Column, "reference.type", fmt.Sprintf("prop %q requires a dimension", key))
		}
	}
	for _, key := range []string{"gap", "column_gap", "row_gap", "size", "line_height", "letter_spacing", "thickness", "opacity"} {
		if value, exists := props[key]; exists && !resolvedNumber(value, false) {
			l.add(source.Source.File, source.Source.Line, source.Source.Column, "reference.type", fmt.Sprintf("prop %q requires a number", key))
		}
	}
	if value, exists := props["aspect_ratio"]; exists && !resolvedAspectRatio(value) {
		l.add(source.Source.File, source.Source.Line, source.Source.Column, "reference.type", "prop \"aspect_ratio\" requires positive width and height")
	}
	for _, key := range []string{"color"} {
		if value, exists := props[key]; exists {
			text, ok := value.(string)
			if !ok || !resolvedColor(text) {
				l.add(source.Source.File, source.Source.Line, source.Source.Column, "reference.type", fmt.Sprintf("prop %q requires a color", key))
			}
		}
	}
	if value, exists := props["background"]; exists {
		switch value.(type) {
		case string:
			if !resolvedColor(value.(string)) {
				l.add(source.Source.File, source.Source.Line, source.Source.Column, "reference.type", "background requires a color or linear gradient")
			}
		case map[string]any:
			if _, ok := value.(map[string]any)["stops"].([]any); !ok {
				l.add(source.Source.File, source.Source.Line, source.Source.Column, "reference.type", "background requires a color or linear gradient")
			}
		default:
			l.add(source.Source.File, source.Source.Line, source.Source.Column, "reference.type", "background requires a color or linear gradient")
		}
	}
	if value, exists := props["shadow"]; exists {
		shadow, ok := value.(map[string]any)
		colorText, colorOK := shadow["color"].(string)
		if !ok || !colorOK || !resolvedColor(colorText) {
			l.add(source.Source.File, source.Source.Line, source.Source.Column, "reference.type", "shadow requires a typed shadow value")
		}
	}
	if value, exists := props["font"]; exists {
		switch value := value.(type) {
		case string:
		case map[string]any:
			if _, ok := value["src"].(string); !ok {
				l.add(source.Source.File, source.Source.Line, source.Source.Column, "reference.type", "font requires a local path or font_face token")
			}
		default:
			l.add(source.Source.File, source.Source.Line, source.Source.Column, "reference.type", "font requires a local path or font_face token")
		}
	}
	if source.Type == "text" {
		for _, key := range []string{"text", "content"} {
			if value, exists := props[key]; exists {
				if _, dynamic := value.(StateReference); dynamic {
					continue
				}
				if _, ok := value.(string); !ok {
					l.add(source.Source.File, source.Source.Line, source.Source.Column, "reference.type", fmt.Sprintf("text prop %q requires text", key))
				}
			}
		}
	}
	for key, value := range place {
		switch key {
		case "x", "y", "offset_x", "offset_y", "grow", "shrink", "row", "column", "row_span", "column_span":
			if !resolvedNumber(value, true) {
				l.add(source.Source.File, source.Source.Line, source.Source.Column, "reference.type", fmt.Sprintf("placement %q requires a number", key))
			}
		case "basis":
			if !resolvedDimension(value, false) && value != "auto" {
				l.add(source.Source.File, source.Source.Line, source.Source.Column, "reference.type", "placement \"basis\" requires auto or a dimension")
			}
		case "alignment":
			text, ok := value.(string)
			alignments := []string{"start", "center", "end", "stretch", "left", "right", "top", "bottom", "top_left", "top_right", "bottom_left", "bottom_right"}
			if !ok || !containsString(alignments, text) {
				l.add(source.Source.File, source.Source.Line, source.Source.Column, "reference.type", "placement \"alignment\" resolves to an unsupported value")
			}
		case "position":
			text, ok := value.(string)
			if !ok || !containsString([]string{"flow", "sticky", "fixed"}, text) {
				l.addSuggestions(source.Source.File, source.Source.Line, source.Source.Column, "reference.type", "placement \"position\" resolves to an unsupported value", []string{"flow", "sticky", "fixed"})
			}
		case "inset":
			validateResolvedPositionInset(source, value, l)
		case "z_index":
			if !resolvedInteger(value) {
				l.add(source.Source.File, source.Source.Line, source.Source.Column, "reference.type", "placement \"z_index\" requires a signed integer")
				continue
			}
			position, _ := place["position"].(string)
			if position != "sticky" && position != "fixed" {
				l.add(source.Source.File, source.Source.Line, source.Source.Column, "reference.type", "placement \"z_index\" is valid only for sticky or fixed positioning")
			}
		}
	}
}

func validateResolvedPositionInset(source *document.Node, value any, l *loader) {
	if source == nil {
		return
	}
	edges, ok := value.(map[string]any)
	if !ok || len(edges) != 4 {
		l.add(source.Source.File, source.Source.Line, source.Source.Column, "reference.type", "placement \"inset\" requires top, right, bottom, and left")
		return
	}
	for _, edge := range []string{"top", "right", "bottom", "left"} {
		raw, exists := edges[edge]
		if !exists {
			l.add(source.Source.File, source.Source.Line, source.Source.Column, "reference.type", "placement \"inset\" requires top, right, bottom, and left")
			continue
		}
		if raw == nil || resolvedNumber(raw, false) {
			continue
		}
		percent, percentOK := raw.(map[string]any)
		if percentOK && len(percent) == 1 {
			if value, exists := percent["percent"]; exists && resolvedNumber(value, false) {
				continue
			}
		}
		l.add(source.Source.File, source.Source.Line, source.Source.Column, "reference.type", fmt.Sprintf("placement inset %q requires null, a finite number, or a percentage", edge))
	}
}

func (l *loader) resolveInstance(source *document.Node, instanceProps, place map[string]any, caller resolveContext) *Node {
	alias, _ := instanceProps["component"].(string)
	component := caller.components[alias]
	if component == nil {
		l.addSuggestions(source.Source.File, source.Source.Line, source.Source.Column, "component.unknown", fmt.Sprintf("unknown component alias %q", alias), stringKeys(caller.components))
		return nil
	}
	if len(component.State) != 0 && source.Name == "" {
		l.add(source.Source.File, source.Source.Line, source.Source.Column, "state.instance_name", "instances of stateful components require a unique authored name")
		return nil
	}

	rawParameters, _ := instanceProps["parameters"].(map[string]any)
	parameters := make(map[string]any)
	for name, parameter := range component.Parameters {
		if parameter.Default != nil {
			parameters[name] = parameter.Default
		}
	}
	for name, value := range rawParameters {
		parameters[name] = resolveValue(value, caller, l)
	}
	for name, parameter := range component.Parameters {
		if parameter.Required {
			if _, ok := parameters[name]; !ok {
				l.add(source.Source.File, source.Source.Line, source.Source.Column, "component.parameter", fmt.Sprintf("missing required parameter %q", name))
			}
		}
		if value, ok := parameters[name]; ok && !parameterValueMatches(parameter, value) {
			l.add(source.Source.File, source.Source.Line, source.Source.Column, "component.parameter_type", fmt.Sprintf("parameter %q requires %s", name, parameter.Type))
		}
	}
	for name := range parameters {
		if _, ok := component.Parameters[name]; !ok {
			l.addSuggestions(source.Source.File, source.Source.Line, source.Source.Column, "component.parameter", fmt.Sprintf("unknown parameter %q", name), stringKeys(component.Parameters))
		}
	}

	instanceName := source.Name
	if instanceName == "" {
		instanceName = alias
	}
	breadcrumb := append(append([]string(nil), caller.breadcrumb...), instanceName)
	slots := make(map[string][]slotFill)
	for _, fill := range source.Children {
		if fill.Type != "slot_content" {
			l.add(fill.Source.File, fill.Source.Line, fill.Source.Column, "component.child", "instance children must be slot_content nodes")
			continue
		}
		name, _ := fill.Props["slot"].(string)
		if name == "" {
			name = "default"
		}
		if _, ok := component.Slots[name]; !ok && name != "default" {
			l.addSuggestions(fill.Source.File, fill.Source.Line, fill.Source.Column, "component.slot", fmt.Sprintf("unknown slot %q", name), stringKeys(component.Slots))
			continue
		}
		fillContext := caller
		fillContext.breadcrumb = breadcrumb
		for _, child := range fill.Children {
			slots[name] = append(slots[name], slotFill{source: child, context: fillContext})
		}
	}
	for name, slot := range component.Slots {
		if slot.Required && len(slots[name]) == 0 {
			l.add(source.Source.File, source.Source.Line, source.Source.Column, "component.slot", fmt.Sprintf("missing required slot %q", name))
		}
	}

	components, tokens := l.importsFor(component)
	componentContext := resolveContext{
		doc:        component,
		parameters: parameters,
		components: components,
		tokens:     tokens,
		slots:      slots,
		breadcrumb: breadcrumb,
		context:    caller.context,
		form:       caller.form,
	}
	if len(component.State) != 0 {
		baseScope := "screen:" + caller.context
		if strings.HasPrefix(caller.scope, "fixture:") {
			baseScope = "fixture:" + caller.context
		}
		componentContext.scope = baseScope + "/" + strings.Join(breadcrumb, "/")
	} else {
		componentContext.scope = caller.scope
	}
	l.registerStateScope(componentContext.scope, caller.context, component.State, nil, componentContext)
	resolved := l.resolveNode(component.Root, componentContext)
	if resolved == nil {
		return nil
	}
	resolved.Place = mergeMaps(resolved.Place, place)
	for key, value := range instanceProps {
		if key == "component" || key == "parameters" {
			continue
		}
		resolved.Props[key] = value
	}
	resolvedSource := &document.Node{Type: resolved.Type, Source: resolved.Source}
	normalizeResolvedValues(resolvedSource, resolved.Props, resolved.Place)
	validateResolvedValues(resolvedSource, resolved.Props, resolved.Place, l)
	if source.Name != "" {
		resolved.Name = source.Name
	}
	return resolved
}

func parameterValueMatches(parameter document.Parameter, value any) bool {
	switch parameter.Type {
	case "text", "string":
		_, ok := value.(string)
		return ok
	case "color":
		text, ok := value.(string)
		return ok && resolvedColor(text)
	case "number":
		return resolvedNumber(value, false)
	case "dimension":
		return resolvedDimension(value, false)
	case "boolean":
		_, ok := value.(bool)
		return ok
	case "enum":
		text, ok := value.(string)
		if !ok {
			return false
		}
		for _, candidate := range parameter.Values {
			if candidate == text {
				return true
			}
		}
		return false
	default:
		return false
	}
}

func resolvedNumber(value any, nonNegative bool) bool {
	var number float64
	switch value := value.(type) {
	case int64:
		number = float64(value)
	case float64:
		number = value
	default:
		return false
	}
	return !math.IsNaN(number) && !math.IsInf(number, 0) && (!nonNegative || number >= 0)
}

func resolvedInteger(value any) bool {
	switch value := value.(type) {
	case int64:
		return true
	case float64:
		return validSignedIntegerFloat(value)
	default:
		return false
	}
}

func validSignedIntegerFloat(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0) && math.Trunc(value) == value && value >= -9223372036854775808.0 && value < 9223372036854775808.0
}

func resolvedDimension(value any, allowKeywords bool) bool {
	if resolvedNumber(value, true) {
		return true
	}
	if text, ok := value.(string); ok {
		return allowKeywords && (text == "auto" || text == "fill")
	}
	percent, ok := value.(map[string]any)
	if !ok || len(percent) != 1 {
		return false
	}
	return resolvedNumber(percent["percent"], true)
}

func resolvedAspectRatio(value any) bool {
	ratio, ok := value.(map[string]any)
	if !ok || len(ratio) != 2 {
		return false
	}
	for _, key := range []string{"width", "height"} {
		if !resolvedNumber(ratio[key], false) {
			return false
		}
		number, _ := numberAsFloat64(ratio[key])
		if number <= 0 {
			return false
		}
	}
	return true
}

func numberAsFloat64(value any) (float64, bool) {
	switch value := value.(type) {
	case int64:
		return float64(value), true
	case float64:
		return value, true
	default:
		return 0, false
	}
}

func resolvedColor(value string) bool {
	if value == "transparent" {
		return true
	}
	if len(value) != 7 && len(value) != 9 || !strings.HasPrefix(value, "#") {
		return false
	}
	_, err := strconv.ParseUint(value[1:], 16, 32)
	return err == nil
}

func resolveMap(values map[string]any, ctx resolveContext, l *loader) map[string]any {
	out := make(map[string]any, len(values))
	for key, value := range values {
		out[key] = resolveValue(value, ctx, l)
	}
	return out
}

func resolveEvents(events document.Events, ctx resolveContext, l *loader) document.Events {
	resolved := document.Events{
		Activate: make([]document.Action, len(events.Activate)),
		Submit:   make([]document.Action, len(events.Submit)),
	}
	for index, action := range events.Activate {
		resolved.Activate[index] = action
		resolved.Activate[index].Value = resolveValue(action.Value, ctx, l)
		resolved.Activate[index].By = resolveValue(action.By, ctx, l)
	}
	for index, action := range events.Submit {
		resolved.Submit[index] = action
		resolved.Submit[index].Value = resolveValue(action.Value, ctx, l)
		resolved.Submit[index].By = resolveValue(action.By, ctx, l)
	}
	return resolved
}

func resolveVariants(variants []document.Variant, ctx resolveContext, l *loader) []document.Variant {
	resolved := make([]document.Variant, len(variants))
	for index, variant := range variants {
		resolved[index] = variant
		resolved[index].Props = resolveMap(variant.Props, ctx, l)
		resolved[index].Place = resolveMap(variant.Place, ctx, l)
		resolved[index].When.Value = resolveValue(variant.When.Value, ctx, l)
	}
	return resolved
}

func (l *loader) registerStateScope(id, context string, declarations map[string]document.StateDeclaration, overrides map[string]any, ctx resolveContext) {
	if id == "" || len(declarations) == 0 {
		return
	}
	state := make(map[string]document.StateDeclaration, len(declarations))
	initial := make(map[string]any, len(overrides))
	for name, declaration := range declarations {
		declaration.Default = resolveValue(declaration.Default, ctx, l)
		if !stateDeclarationMatches(declaration, declaration.Default) {
			l.add(declaration.Source.File, declaration.Source.Line, declaration.Source.Column, "state.default_type", fmt.Sprintf("state %q default does not match %s", name, declaration.Type))
		}
		state[name] = declaration
	}
	for name, value := range overrides {
		declaration, ok := state[name]
		if !ok {
			continue
		}
		value = resolveValue(value, ctx, l)
		if !stateDeclarationMatches(declaration, value) {
			l.add(declaration.Source.File, declaration.Source.Line, declaration.Source.Column, "state.fixture_type", fmt.Sprintf("fixture state %q does not match %s", name, declaration.Type))
			continue
		}
		initial[name] = value
	}
	l.stateScopes[id] = StateScope{ID: id, Context: context, State: state, Initial: initial}
}

func stateDeclarationMatches(declaration document.StateDeclaration, value any) bool {
	switch declaration.Type {
	case "boolean":
		_, ok := value.(bool)
		return ok
	case "number":
		return resolvedNumber(value, false)
	case "text":
		_, ok := value.(string)
		return ok
	case "enum":
		text, ok := value.(string)
		if !ok {
			return false
		}
		for _, allowed := range declaration.Values {
			if text == allowed {
				return true
			}
		}
	}
	return false
}

func resolveValue(value any, ctx resolveContext, l *loader) any {
	switch value := value.(type) {
	case map[string]any:
		if len(value) == 1 {
			if raw, ok := value["ref"]; ok {
				ref, _ := raw.(string)
				return resolveReference(ref, ctx, l)
			}
		}
		return resolveMap(value, ctx, l)
	case []any:
		out := make([]any, len(value))
		for i, item := range value {
			out[i] = resolveValue(item, ctx, l)
		}
		return out
	default:
		return value
	}
}

func resolveReference(ref string, ctx resolveContext, l *loader) any {
	source := ctx.source
	if source.File == "" {
		source = document.Source{File: ctx.doc.File, Line: 1, Column: 1}
	}
	parts := strings.Split(ref, ".")
	if len(parts) == 2 && parts[0] == "state" {
		if _, ok := ctx.doc.State[parts[1]]; !ok {
			l.addSuggestions(source.File, source.Line, source.Column, "reference.state", fmt.Sprintf("unknown state %q", parts[1]), stringKeys(ctx.doc.State))
			return nil
		}
		return StateReference{Scope: ctx.scope, Name: parts[1]}
	}
	if len(parts) == 2 && parts[0] == "parameter" {
		value, ok := ctx.parameters[parts[1]]
		if !ok {
			l.addSuggestions(source.File, source.Line, source.Column, "reference.parameter", fmt.Sprintf("unknown parameter %q", parts[1]), stringKeys(ctx.doc.Parameters))
		}
		return value
	}
	if len(parts) == 3 {
		tokens := ctx.tokens[parts[0]]
		if tokens == nil {
			l.addSuggestions(source.File, source.Line, source.Column, "reference.token", fmt.Sprintf("unknown token alias %q", parts[0]), stringKeys(ctx.tokens))
			return nil
		}
		kind := tokens.Tokens[parts[1]]
		if kind == nil {
			l.addSuggestions(source.File, source.Line, source.Column, "reference.token", fmt.Sprintf("unknown token kind %q", parts[1]), stringKeys(tokens.Tokens))
			return nil
		}
		value, ok := kind[parts[2]]
		if !ok {
			l.addSuggestions(source.File, source.Line, source.Column, "reference.token", fmt.Sprintf("unknown token %q", parts[2]), stringKeys(kind))
			return nil
		}
		if parts[1] == "text_style" {
			if style, ok := value.(map[string]any); ok {
				style = mergeMaps(nil, style)
				if fontName, ok := style["font"].(string); ok {
					if face := tokens.Tokens["font_face"][fontName]; face != nil {
						style["font"] = face
					}
				}
				value = style
			}
		}
		components, importedTokens := l.importsFor(tokens)
		tokenContext := resolveContext{doc: tokens, components: components, tokens: importedTokens, source: source}
		return resolveValue(value, tokenContext, l)
	}
	l.add(source.File, source.Line, source.Column, "reference.syntax", fmt.Sprintf("invalid reference %q", ref))
	return nil
}

func activeBreakpoint(breakpoints map[string]document.Breakpoint, width int) string {
	for name, breakpoint := range breakpoints {
		if breakpoint.MinWidth != nil && width < *breakpoint.MinWidth {
			continue
		}
		if breakpoint.MaxWidth != nil && width > *breakpoint.MaxWidth {
			continue
		}
		return name
	}
	return ""
}

func mergeMaps(base, override map[string]any) map[string]any {
	out := make(map[string]any, len(base)+len(override))
	for key, value := range base {
		out[key] = value
	}
	for key, value := range override {
		out[key] = value
	}
	return out
}

func groupNode(source *document.Node, children []*Node, breadcrumb []string, l *loader) *Node {
	return &Node{
		Handle:     l.handle(),
		Type:       "_group",
		Children:   children,
		Source:     source.Source,
		Breadcrumb: append([]string(nil), breadcrumb...),
	}
}

func selectPreview(previews map[string]document.Preview, selection string) (string, document.Preview, bool) {
	if preview, ok := previews[selection]; selection != "" && ok {
		return selection, preview, true
	}
	if preview, ok := previews["default"]; ok {
		return "default", preview, true
	}
	names := make([]string, 0, len(previews))
	for name := range previews {
		names = append(names, name)
	}
	sort.Strings(names)
	if len(names) != 0 {
		return names[0], previews[names[0]], true
	}
	return "", document.Preview{}, false
}

func previewSlots(preview document.Preview, ctx resolveContext, l *loader) map[string][]slotFill {
	slots := make(map[string][]slotFill)
	for _, wrapper := range preview.Children {
		name := "default"
		children := []*document.Node{wrapper}
		if wrapper.Type == "slot_content" {
			if declared, ok := wrapper.Props["slot"].(string); ok && declared != "" {
				name = declared
			}
			children = wrapper.Children
		}
		for _, child := range children {
			slots[name] = append(slots[name], slotFill{source: child, context: ctx})
		}
	}
	return slots
}

func canonicalPath(root, path string) (string, error) {
	return canonicalPathOverlay(root, path, nil)
}

func canonicalPathOverlay(root, path string, overlay map[string]OverlayFile) (string, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	resolved, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		if _, ok := overlay[filepath.Clean(absolute)]; !ok || !os.IsNotExist(err) {
			return "", err
		}
		parts := []string{filepath.Base(absolute)}
		probe := filepath.Dir(absolute)
		for {
			parent, parentErr := filepath.EvalSymlinks(probe)
			if parentErr == nil {
				for index := len(parts) - 1; index >= 0; index-- {
					parent = filepath.Join(parent, parts[index])
				}
				resolved = parent
				break
			}
			if !os.IsNotExist(parentErr) || filepath.Dir(probe) == probe {
				return "", parentErr
			}
			parts = append(parts, filepath.Base(probe))
			probe = filepath.Dir(probe)
		}
	}
	relative, err := filepath.Rel(root, resolved)
	if err != nil {
		return "", err
	}
	if relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) || filepath.IsAbs(relative) {
		return "", fmt.Errorf("path %q escapes project root", path)
	}
	return resolved, nil
}

func (l *loader) handle() string {
	l.nextHandle++
	return "n" + strconv.Itoa(l.nextHandle)
}

func (l *loader) add(file string, line, column int, code, message string) {
	l.diagnostics = append(l.diagnostics, document.Diagnostic{
		Severity: "error",
		Code:     code,
		Message:  message,
		File:     file,
		Line:     line,
		Column:   column,
	})
}

func (l *loader) addSuggestions(file string, line, column int, code, message string, choices []string) {
	diagnostic := document.Diagnostic{
		Severity: "error", Code: code, Message: message,
		File: file, Line: line, Column: column,
		Suggestions: closest(choices, quotedValue(message)),
	}
	l.diagnostics = append(l.diagnostics, diagnostic)
}

func quotedValue(message string) string {
	start := strings.LastIndex(message, "\"")
	if start <= 0 {
		return ""
	}
	end := strings.LastIndex(message[:start], "\"")
	if end < 0 {
		return ""
	}
	return message[end+1 : start]
}

func closest(choices []string, value string) []string {
	sort.Strings(choices)
	bestDistance := 4
	best := ""
	for _, choice := range choices {
		distance := levenshtein(value, choice)
		if distance < bestDistance {
			bestDistance, best = distance, choice
		}
	}
	if best == "" {
		return nil
	}
	return []string{best}
}

func levenshtein(a, b string) int {
	previous := make([]int, len(b)+1)
	for index := range previous {
		previous[index] = index
	}
	for i, left := range a {
		current := make([]int, len(b)+1)
		current[0] = i + 1
		for j, right := range b {
			cost := 0
			if left != right {
				cost = 1
			}
			current[j+1] = min(previous[j+1]+1, current[j]+1, previous[j]+cost)
		}
		previous = current
	}
	return previous[len(b)]
}

func stringKeys[V any](values map[string]V) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	return keys
}

func containsString(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}

func loadDiagnostic(file, code, message string) document.Diagnostic {
	return document.Diagnostic{Severity: "error", Code: code, Message: message, File: file, Line: 1, Column: 1}
}

func entryDiagnostic(file string, err error) document.Diagnostic {
	code := "project.entry"
	if strings.Contains(err.Error(), "escapes project root") {
		code = "project.containment"
	}
	return loadDiagnostic(file, code, err.Error())
}
