package project

import (
	"fmt"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"os"
	"path/filepath"
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
}

type Node struct {
	Handle     string
	Type       string
	Name       string
	Props      map[string]any
	Place      map[string]any
	Children   []*Node
	Source     document.Source
	Breadcrumb []string
}

type loader struct {
	root         string
	width        int
	cache        map[string]*document.Document
	loading      map[string]bool
	dependencies map[string]struct{}
	diagnostics  []document.Diagnostic
	nextHandle   int
}

type resolveContext struct {
	doc        *document.Document
	parameters map[string]any
	components map[string]*document.Document
	tokens     map[string]*document.Document
	slots      map[string][]*Node
	breadcrumb []string
	source     document.Source
}

func Load(root, entry string, viewportWidth int) (*Loaded, []document.Diagnostic) {
	return LoadSelection(root, entry, viewportWidth, "")
}

func LoadSelection(root, entry string, viewportWidth int, selection string) (*Loaded, []document.Diagnostic) {
	canonicalRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return nil, []document.Diagnostic{loadDiagnostic(root, "project.root", err.Error())}
	}
	canonicalRoot, err = filepath.Abs(canonicalRoot)
	if err != nil {
		return nil, []document.Diagnostic{loadDiagnostic(root, "project.root", err.Error())}
	}
	canonicalEntry, err := canonicalPath(canonicalRoot, entry)
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
	}
	doc := l.loadDocument(canonicalEntry)
	if doc == nil || len(l.diagnostics) != 0 {
		return nil, l.diagnostics
	}

	components, tokens := l.importsFor(doc)
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
			loaded.Screens[name] = l.viewportNode(l.resolveNode(screen, ctx), background, doc)
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

func (l *loader) validateSlots(component *document.Document, values map[string][]*Node, source document.Source) {
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
		path, err := canonicalPath(l.root, filepath.Join(filepath.Dir(node.Source.File), raw))
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
			file, openErr := os.Open(path)
			if openErr != nil {
				l.add(node.Source.File, node.Source.Line, node.Source.Column, "asset.decode", openErr.Error())
				continue
			}
			_, _, decodeErr := image.DecodeConfig(file)
			_ = file.Close()
			if decodeErr != nil {
				l.add(node.Source.File, node.Source.Line, node.Source.Column, "asset.decode", decodeErr.Error())
				continue
			}
		} else {
			data, readErr := os.ReadFile(path)
			if readErr != nil {
				l.add(node.Source.File, node.Source.Line, node.Source.Column, "asset.decode", readErr.Error())
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
				switch resolved.(type) {
				case int64, float64:
					valid = true
				}
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

	src, err := os.ReadFile(path)
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
		resolved, err := canonicalPath(l.root, filepath.Join(filepath.Dir(path), importPath))
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
		resolved, err := canonicalPath(l.root, filepath.Join(filepath.Dir(path), importPath))
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
		path, err := canonicalPath(l.root, filepath.Join(filepath.Dir(doc.File), source))
		if err != nil {
			l.add(doc.File, 1, 1, "asset.path", err.Error())
			continue
		}
		extension := strings.ToLower(filepath.Ext(path))
		if extension != ".ttf" && extension != ".otf" {
			l.add(doc.File, 1, 1, "asset.type", "font faces must be TTF or OTF")
			continue
		}
		data, err := os.ReadFile(path)
		if err != nil {
			l.add(doc.File, 1, 1, "asset.decode", err.Error())
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

func (l *loader) importsFor(doc *document.Document) (map[string]*document.Document, map[string]*document.Document) {
	components := make(map[string]*document.Document)
	tokens := make(map[string]*document.Document)
	for alias, importPath := range doc.Imports.Components {
		path, err := canonicalPath(l.root, filepath.Join(filepath.Dir(doc.File), importPath))
		if err == nil {
			components[alias] = l.cache[path]
		}
	}
	for alias, importPath := range doc.Imports.Tokens {
		path, err := canonicalPath(l.root, filepath.Join(filepath.Dir(doc.File), importPath))
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
	validateResolvedValues(source, props, place, l)
	if !visible {
		return nil
	}

	if source.Type == "instance" {
		return l.resolveInstance(source, props, place, ctx)
	}
	if source.Type == "slot" {
		name, _ := props["name"].(string)
		if content, ok := ctx.slots[name]; ok {
			return groupNode(source, content, ctx.breadcrumb, l)
		}
		var defaults []*Node
		for _, child := range source.Children {
			if resolved := l.resolveNode(child, ctx); resolved != nil {
				defaults = append(defaults, resolved)
			}
		}
		return groupNode(source, defaults, ctx.breadcrumb, l)
	}
	if source.Type == "slot_content" {
		l.add(source.Source.File, source.Source.Line, source.Source.Column, "component.slot_content", "slot_content is only valid as a direct child of an instance")
		return nil
	}

	node := &Node{
		Handle:     l.handle(),
		Type:       source.Type,
		Name:       source.Name,
		Props:      props,
		Place:      place,
		Source:     source.Source,
		Breadcrumb: append([]string(nil), ctx.breadcrumb...),
	}
	for _, child := range source.Children {
		if resolved := l.resolveNode(child, ctx); resolved != nil {
			if resolved.Type == "_group" {
				node.Children = append(node.Children, resolved.Children...)
			} else {
				node.Children = append(node.Children, resolved)
			}
		}
	}
	return node
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
		enum("axis", "horizontal", "vertical")
	case "image":
		enum("fit", "contain", "cover", "fill")
	case "divider":
		enum("orientation", "horizontal", "vertical")
	}
	for _, key := range []string{"clip", "italic", "wrap", "scrollbar"} {
		if value, exists := props[key]; exists {
			if _, ok := value.(bool); !ok {
				l.add(source.Source.File, source.Source.Line, source.Source.Column, "reference.type", fmt.Sprintf("prop %q requires true or false", key))
			}
		}
	}
	for _, key := range []string{"width", "height", "min_width", "max_width", "min_height", "max_height", "gap", "column_gap", "row_gap", "size", "line_height", "letter_spacing", "thickness", "opacity"} {
		value, exists := props[key]
		if !exists {
			continue
		}
		switch value.(type) {
		case int64, float64:
		case string:
			if key != "width" && key != "height" || value != "auto" && value != "fill" {
				l.add(source.Source.File, source.Source.Line, source.Source.Column, "reference.type", fmt.Sprintf("prop %q requires a number", key))
			}
		default:
			l.add(source.Source.File, source.Source.Line, source.Source.Column, "reference.type", fmt.Sprintf("prop %q requires a number", key))
		}
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
				if _, ok := value.(string); !ok {
					l.add(source.Source.File, source.Source.Line, source.Source.Column, "reference.type", fmt.Sprintf("text prop %q requires text", key))
				}
			}
		}
	}
	for key, value := range place {
		switch key {
		case "x", "y", "offset_x", "offset_y", "grow", "row", "column", "row_span", "column_span":
			switch value.(type) {
			case int64, float64:
			default:
				l.add(source.Source.File, source.Source.Line, source.Source.Column, "reference.type", fmt.Sprintf("placement %q requires a number", key))
			}
		}
	}
}

func (l *loader) resolveInstance(source *document.Node, instanceProps, place map[string]any, caller resolveContext) *Node {
	alias, _ := instanceProps["component"].(string)
	component := caller.components[alias]
	if component == nil {
		l.addSuggestions(source.Source.File, source.Source.Line, source.Source.Column, "component.unknown", fmt.Sprintf("unknown component alias %q", alias), stringKeys(caller.components))
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
	slots := make(map[string][]*Node)
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
			if resolved := l.resolveNode(child, fillContext); resolved != nil {
				slots[name] = append(slots[name], resolved)
			}
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
	}
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
	case "number", "dimension":
		switch value.(type) {
		case int64, float64:
			return true
		default:
			return false
		}
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

func previewSlots(preview document.Preview, ctx resolveContext, l *loader) map[string][]*Node {
	slots := make(map[string][]*Node)
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
			if resolved := l.resolveNode(child, ctx); resolved != nil {
				slots[name] = append(slots[name], resolved)
			}
		}
	}
	return slots
}

func canonicalPath(root, path string) (string, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	resolved, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return "", err
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
