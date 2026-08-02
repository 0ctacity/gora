package mcpserver

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"math"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"go.yaml.in/yaml/v4"

	"gora/internal/document"
	projectpkg "gora/internal/project"
	"gora/internal/studio"
)

type PatchOperation struct {
	Op    string `json:"op"`
	Path  string `json:"path"`
	Value any    `json:"value,omitempty"`
}

type DocumentChange struct {
	Mode             string           `json:"mode"`
	File             string           `json:"file"`
	ExpectedRevision string           `json:"expected_revision,omitempty"`
	Document         any              `json:"document,omitempty"`
	Operations       []PatchOperation `json:"operations,omitempty"`
}

type DocumentResource struct {
	SourceID string `json:"source_id"`
	File     string `json:"file"`
	Revision string `json:"revision"`
	Document any    `json:"document,omitempty"`
	Source   string `json:"source"`
}

type ChangeResult struct {
	ProjectID   string                `json:"project_id"`
	Revision    uint64                `json:"project_revision"`
	Documents   []DocumentResource    `json:"documents"`
	Diagnostics []document.Diagnostic `json:"diagnostics,omitempty"`
}

type CandidateValidationError struct {
	Diagnostics []document.Diagnostic
}

func (err *CandidateValidationError) Error() string {
	if len(err.Diagnostics) == 0 {
		return "candidate project is invalid"
	}
	return fmt.Sprintf("candidate project is invalid: %s: %s", err.Diagnostics[0].Code, err.Diagnostics[0].Message)
}

type SourceSummary struct {
	SourceID string `json:"source_id"`
	File     string `json:"file"`
	Revision string `json:"revision"`
}

type DocumentSummary struct {
	SourceID    string          `json:"source_id"`
	Kind        document.Kind   `json:"kind,omitempty"`
	Name        string          `json:"name,omitempty"`
	Imports     ImportSummary   `json:"imports"`
	Entry       string          `json:"entry,omitempty"`
	Viewport    ViewportSummary `json:"viewport"`
	Breakpoints []string        `json:"breakpoints"`
	Screens     []string        `json:"screens"`
	Fixtures    []string        `json:"fixtures"`
	Parameters  []string        `json:"parameters"`
	Slots       []string        `json:"slots"`
	Revision    string          `json:"revision"`
}

type ViewportSummary struct {
	Width  int `json:"width"`
	Height int `json:"height"`
}

type ImportSummary struct {
	Components map[string]string `json:"components"`
	Tokens     map[string]string `json:"tokens"`
}

func (r *Registry) DocumentSummaries(projectID string) ([]DocumentSummary, []string, error) {
	project, err := r.project(projectID)
	if err != nil {
		return nil, nil, err
	}
	sources, err := r.KnownSources(projectID)
	if err != nil {
		return nil, nil, err
	}
	documents := make([]DocumentSummary, 0, len(sources))
	for _, source := range sources {
		data, readErr := os.ReadFile(source.File)
		if readErr != nil {
			continue
		}
		doc, _ := document.Parse(source.File, data)
		if doc == nil {
			documents = append(documents, DocumentSummary{SourceID: source.SourceID, Revision: source.Revision, Screens: []string{}, Fixtures: []string{}, Parameters: []string{}, Slots: []string{}, Breakpoints: []string{}})
			continue
		}
		summary := DocumentSummary{SourceID: source.SourceID, Kind: doc.Kind, Name: doc.Name, Imports: ImportSummary{Components: doc.Imports.Components, Tokens: doc.Imports.Tokens}, Entry: doc.Entry, Viewport: ViewportSummary{Width: doc.Viewport.Width, Height: doc.Viewport.Height}, Revision: source.Revision}
		summary.Breakpoints = sortedKeys(doc.Breakpoints)
		summary.Screens = sortedKeys(doc.Screens)
		summary.Fixtures = sortedKeys(doc.Previews)
		summary.Parameters = sortedKeys(doc.Parameters)
		summary.Slots = sortedKeys(doc.Slots)
		documents = append(documents, summary)
	}
	project.mu.RLock()
	assetSet := make(map[string]bool)
	for _, view := range project.views {
		if view.runtime == nil {
			continue
		}
		for _, dependency := range view.runtime.Dependencies() {
			if filepath.Ext(dependency) != ".gora" {
				relative, _ := filepath.Rel(project.root, dependency)
				assetSet[filepath.ToSlash(relative)] = true
			}
		}
	}
	project.mu.RUnlock()
	assets := make([]string, 0, len(assetSet))
	for asset := range assetSet {
		assets = append(assets, asset)
	}
	sort.Strings(assets)
	return documents, assets, nil
}

func sortedKeys[V any](mapping map[string]V) []string {
	result := make([]string, 0, len(mapping))
	for key := range mapping {
		result = append(result, key)
	}
	sort.Strings(result)
	return result
}

func (r *Registry) KnownSources(projectID string) ([]SourceSummary, error) {
	project, err := r.project(projectID)
	if err != nil {
		return nil, err
	}
	project.mu.RLock()
	paths := project.knownPathsLocked()
	project.mu.RUnlock()
	result := make([]SourceSummary, 0, len(paths))
	for path := range paths {
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			continue
		}
		relative, _ := filepath.Rel(project.root, path)
		result = append(result, SourceSummary{SourceID: url.PathEscape(filepath.ToSlash(relative)), File: path, Revision: sourceRevision(data)})
	}
	sort.Slice(result, func(i, j int) bool { return result[i].File < result[j].File })
	return result, nil
}

type preparedChange struct {
	path      string
	data      []byte
	original  []byte
	mode      os.FileMode
	created   bool
	temporary string
}

func (r *Registry) DocumentResource(projectID, sourceID string) (DocumentResource, error) {
	project, err := r.project(projectID)
	if err != nil {
		return DocumentResource{}, err
	}
	path, err := containedKnownPath(project, sourceID)
	if err != nil {
		return DocumentResource{}, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return DocumentResource{}, err
	}
	value, _ := decodeYAMLValue(data)
	relative, _ := filepath.Rel(project.root, path)
	return DocumentResource{
		SourceID: url.PathEscape(filepath.ToSlash(relative)), File: path, Revision: sourceRevision(data), Document: value, Source: string(data),
	}, nil
}

func (r *Registry) ApplyDocumentChanges(projectID string, changes []DocumentChange) (ChangeResult, error) {
	project, err := r.project(projectID)
	if err != nil {
		return ChangeResult{}, err
	}
	if len(changes) == 0 {
		return ChangeResult{}, fmt.Errorf("at least one document change is required")
	}
	project.mu.Lock()
	defer project.mu.Unlock()
	overlay := make(map[string][]byte, len(changes))
	prepared := make([]preparedChange, 0, len(changes))
	seen := make(map[string]bool)
	known := project.knownPathsLocked()
	for index, change := range changes {
		path, err := containedDestination(project.root, change.File)
		if err != nil {
			return ChangeResult{}, fmt.Errorf("change %d: %w", index, err)
		}
		if seen[path] {
			return ChangeResult{}, fmt.Errorf("change %d duplicates %s", index, change.File)
		}
		seen[path] = true
		item := preparedChange{path: path, mode: 0o600}
		var value any
		switch change.Mode {
		case "create":
			if _, err := os.Lstat(path); err == nil || !os.IsNotExist(err) {
				return ChangeResult{}, fmt.Errorf("change %d: destination already exists", index)
			}
			item.created = true
			value = change.Document
		case "replace", "patch":
			if !known[path] {
				return ChangeResult{}, fmt.Errorf("change %d: document is not known by the project", index)
			}
			item.original, err = os.ReadFile(path)
			if err != nil {
				return ChangeResult{}, fmt.Errorf("change %d: %w", index, err)
			}
			if change.ExpectedRevision == "" || change.ExpectedRevision != sourceRevision(item.original) {
				return ChangeResult{}, fmt.Errorf("change %d: source revision conflict", index)
			}
			if info, statErr := os.Stat(path); statErr == nil {
				item.mode = info.Mode().Perm()
			}
			if change.Mode == "replace" {
				value = change.Document
			} else {
				value, err = decodeYAMLValue(item.original)
				if err != nil {
					return ChangeResult{}, fmt.Errorf("change %d cannot patch invalid YAML; use replace: %w", index, err)
				}
				for operationIndex, operation := range change.Operations {
					value, err = applyPatch(value, operation)
					if err != nil {
						return ChangeResult{}, fmt.Errorf("change %d operation %d: %w", index, operationIndex, err)
					}
				}
			}
		default:
			return ChangeResult{}, fmt.Errorf("change %d mode must be create, replace, or patch", index)
		}
		item.data, err = encodeCanonicalYAML(value)
		if err != nil {
			return ChangeResult{}, fmt.Errorf("change %d: %w", index, err)
		}
		overlay[path] = item.data
		prepared = append(prepared, item)
	}
	if diagnostics := project.validateOverlayLocked(overlay); len(diagnostics) != 0 {
		return ChangeResult{}, &CandidateValidationError{Diagnostics: diagnostics}
	}
	if err := stageChanges(prepared); err != nil {
		return ChangeResult{}, err
	}
	if err := commitChanges(prepared); err != nil {
		return ChangeResult{}, err
	}
	for _, item := range prepared {
		project.sources[item.path] = true
		if project.watch != nil {
			project.watch.Suppress(item.path, item.data)
		}
	}
	project.revision++
	project.reloadViewsLocked()
	project.refreshWatchLocked()
	result := ChangeResult{ProjectID: project.id, Revision: project.revision}
	for _, item := range prepared {
		relative, _ := filepath.Rel(project.root, item.path)
		value, _ := decodeYAMLValue(item.data)
		result.Documents = append(result.Documents, DocumentResource{
			SourceID: url.PathEscape(filepath.ToSlash(relative)), File: item.path, Revision: sourceRevision(item.data), Document: value, Source: string(item.data),
		})
	}
	sort.Slice(result.Documents, func(i, j int) bool { return result.Documents[i].SourceID < result.Documents[j].SourceID })
	return result, nil
}

func (p *Project) knownPathsLocked() map[string]bool {
	known := make(map[string]bool, len(p.sources))
	for path := range p.sources {
		known[path] = true
	}
	for _, view := range p.views {
		known[view.entry] = true
		if view.runtime != nil {
			for _, dependency := range view.runtime.Dependencies() {
				if filepath.Ext(dependency) == ".gora" {
					known[filepath.Clean(dependency)] = true
				}
			}
		}
	}
	return known
}

func (p *Project) validateOverlayLocked(overlay map[string][]byte) []document.Diagnostic {
	checked := make(map[string]bool)
	var diagnostics []document.Diagnostic
	validate := func(path string) {
		path = filepath.Clean(path)
		if checked[path] {
			return
		}
		checked[path] = true
		data, ok := overlay[path]
		if !ok {
			data, _ = os.ReadFile(path)
		}
		doc, parsed := document.Parse(path, data)
		if len(parsed) != 0 {
			diagnostics = append(diagnostics, parsed...)
			return
		}
		if doc == nil || doc.Kind == document.KindTokens {
			return
		}
		_, loadedDiagnostics := projectpkg.LoadSelectionOverlay(p.root, path, max(1, doc.Viewport.Width), "", overlay)
		diagnostics = append(diagnostics, loadedDiagnostics...)
	}
	for path := range overlay {
		validate(path)
	}
	for _, view := range p.views {
		validate(view.entry)
	}
	return diagnostics
}

func (p *Project) reloadViewsLocked() {
	p.reloadAffectedViewsLocked(nil)
}

func (p *Project) reloadAffectedViewsLocked(changed map[string]bool) {
	for _, view := range p.views {
		if len(changed) != 0 && !viewAffected(view, changed) {
			continue
		}
		data, err := os.ReadFile(view.entry)
		if err != nil {
			view.diagnostics = []document.Diagnostic{{Severity: "error", Code: "import.read", Message: err.Error(), File: view.entry, Line: 1, Column: 1}}
			continue
		}
		doc, diagnostics := document.Parse(view.entry, data)
		if doc != nil {
			view.kind = doc.Kind
		}
		if view.kind == document.KindTokens {
			view.diagnostics = diagnostics
			continue
		}
		if view.runtime == nil {
			view.runtime = studio.NewRuntimeAllowInvalid(p.root, view.entry)
		} else {
			view.runtime.Reload()
		}
		view.diagnostics = view.runtime.Snapshot().Diagnostics
		for _, dependency := range view.runtime.Dependencies() {
			if filepath.Ext(dependency) == ".gora" {
				p.sources[filepath.Clean(dependency)] = true
			}
		}
	}
}

func viewAffected(view *View, changed map[string]bool) bool {
	if changed[filepath.Clean(view.entry)] {
		return true
	}
	if view.runtime != nil {
		for _, dependency := range view.runtime.Dependencies() {
			if changed[filepath.Clean(dependency)] {
				return true
			}
		}
	}
	return false
}

func stageChanges(changes []preparedChange) error {
	for index := range changes {
		file, err := os.CreateTemp(filepath.Dir(changes[index].path), ".gora-mcp-*")
		if err != nil {
			cleanupStages(changes)
			return err
		}
		changes[index].temporary = file.Name()
		if err := file.Chmod(changes[index].mode); err == nil {
			_, err = file.Write(changes[index].data)
		}
		if err == nil {
			err = file.Sync()
		}
		closeErr := file.Close()
		if err == nil {
			err = closeErr
		}
		if err != nil {
			cleanupStages(changes)
			return err
		}
	}
	return nil
}

func commitChanges(changes []preparedChange) error {
	committed := 0
	for index := range changes {
		if err := os.Rename(changes[index].temporary, changes[index].path); err != nil {
			for rollback := 0; rollback < committed; rollback++ {
				if changes[rollback].created {
					_ = os.Remove(changes[rollback].path)
				} else {
					_ = os.WriteFile(changes[rollback].path, changes[rollback].original, changes[rollback].mode)
				}
			}
			cleanupStages(changes[index:])
			return err
		}
		committed++
	}
	return nil
}

func cleanupStages(changes []preparedChange) {
	for _, change := range changes {
		if change.temporary != "" {
			_ = os.Remove(change.temporary)
		}
	}
}

func sourceRevision(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func decodeYAMLValue(data []byte) (any, error) {
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	var documentNode yaml.Node
	if err := decoder.Decode(&documentNode); err != nil {
		return nil, err
	}
	var extra yaml.Node
	if err := decoder.Decode(&extra); err != io.EOF {
		return nil, fmt.Errorf("multiple YAML documents are not supported")
	}
	if len(documentNode.Content) != 1 {
		return nil, fmt.Errorf("empty YAML document")
	}
	return yamlNodeValue(documentNode.Content[0])
}

func yamlNodeValue(node *yaml.Node) (any, error) {
	switch node.Kind {
	case yaml.MappingNode:
		result := make(map[string]any, len(node.Content)/2)
		for index := 0; index+1 < len(node.Content); index += 2 {
			value, err := yamlNodeValue(node.Content[index+1])
			if err != nil {
				return nil, err
			}
			result[node.Content[index].Value] = value
		}
		return result, nil
	case yaml.SequenceNode:
		result := make([]any, 0, len(node.Content))
		for _, child := range node.Content {
			value, err := yamlNodeValue(child)
			if err != nil {
				return nil, err
			}
			result = append(result, value)
		}
		return result, nil
	case yaml.ScalarNode:
		switch node.Tag {
		case "!!null":
			return nil, nil
		case "!!bool":
			return node.Value == "true", nil
		case "!!int":
			return strconv.ParseInt(node.Value, 10, 64)
		case "!!float":
			return strconv.ParseFloat(node.Value, 64)
		default:
			return node.Value, nil
		}
	default:
		return nil, fmt.Errorf("unsupported YAML node")
	}
}

func encodeCanonicalYAML(value any) ([]byte, error) {
	node, err := valueYAMLNode(value)
	if err != nil {
		return nil, err
	}
	documentNode := &yaml.Node{Kind: yaml.DocumentNode, Content: []*yaml.Node{node}}
	var output bytes.Buffer
	encoder := yaml.NewEncoder(&output)
	encoder.SetIndent(2)
	if err := encoder.Encode(documentNode); err != nil {
		return nil, err
	}
	if err := encoder.Close(); err != nil {
		return nil, err
	}
	return output.Bytes(), nil
}

func valueYAMLNode(value any) (*yaml.Node, error) {
	switch value := value.(type) {
	case nil:
		return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!null", Value: "null"}, nil
	case string:
		return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: value}, nil
	case bool:
		return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!bool", Value: strconv.FormatBool(value)}, nil
	case int:
		return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!int", Value: strconv.Itoa(value)}, nil
	case int64:
		return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!int", Value: strconv.FormatInt(value, 10)}, nil
	case float64:
		if math.IsNaN(value) || math.IsInf(value, 0) {
			return nil, fmt.Errorf("numbers must be finite")
		}
		if math.Trunc(value) == value {
			return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!int", Value: strconv.FormatInt(int64(value), 10)}, nil
		}
		return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!float", Value: strconv.FormatFloat(value, 'g', -1, 64)}, nil
	case map[string]any:
		keys := make([]string, 0, len(value))
		for key := range value {
			keys = append(keys, key)
		}
		sort.Slice(keys, func(i, j int) bool {
			a, b := schemaFieldRank(keys[i]), schemaFieldRank(keys[j])
			if a != b {
				return a < b
			}
			return keys[i] < keys[j]
		})
		node := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
		for _, key := range keys {
			child, err := valueYAMLNode(value[key])
			if err != nil {
				return nil, err
			}
			node.Content = append(node.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key}, child)
		}
		return node, nil
	case []any:
		node := &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}
		for _, item := range value {
			child, err := valueYAMLNode(item)
			if err != nil {
				return nil, err
			}
			node.Content = append(node.Content, child)
		}
		return node, nil
	default:
		return nil, fmt.Errorf("unsupported structured value %T", value)
	}
}

func schemaFieldRank(key string) int {
	fields := []string{"gora", "kind", "name", "imports", "viewport", "breakpoints", "state", "parameters", "slots", "previews", "entry", "screens", "root", "tokens", "type", "props", "place", "responsive", "variants", "on", "children"}
	for index, field := range fields {
		if key == field {
			return index
		}
	}
	return len(fields)
}

func applyPatch(value any, operation PatchOperation) (any, error) {
	if operation.Path == "" {
		switch operation.Op {
		case "add", "replace":
			return operation.Value, nil
		default:
			return nil, fmt.Errorf("root supports only add or replace")
		}
	}
	if !strings.HasPrefix(operation.Path, "/") {
		return nil, fmt.Errorf("JSON Pointer must begin with /")
	}
	parts := strings.Split(operation.Path[1:], "/")
	for index := range parts {
		decoded, err := decodePointerSegment(parts[index])
		if err != nil {
			return nil, err
		}
		parts[index] = decoded
	}
	return patchAt(value, parts, operation)
}

func patchAt(value any, parts []string, operation PatchOperation) (any, error) {
	if len(parts) == 0 {
		return applyPatch(value, PatchOperation{Op: operation.Op, Path: "", Value: operation.Value})
	}
	key := parts[0]
	switch container := value.(type) {
	case map[string]any:
		if len(parts) == 1 {
			_, exists := container[key]
			switch operation.Op {
			case "add":
				container[key] = operation.Value
			case "replace":
				if !exists {
					return nil, fmt.Errorf("replace target does not exist")
				}
				container[key] = operation.Value
			case "remove":
				if !exists {
					return nil, fmt.Errorf("remove target does not exist")
				}
				delete(container, key)
			default:
				return nil, fmt.Errorf("operation must be add, replace, or remove")
			}
			return container, nil
		}
		child, exists := container[key]
		if !exists {
			return nil, fmt.Errorf("path segment %q does not exist", key)
		}
		updated, err := patchAt(child, parts[1:], operation)
		if err != nil {
			return nil, err
		}
		container[key] = updated
		return container, nil
	case []any:
		if len(parts) == 1 && operation.Op == "add" && key == "-" {
			return append(container, operation.Value), nil
		}
		index, err := strconv.Atoi(key)
		allowEnd := len(parts) == 1 && operation.Op == "add" && index == len(container)
		if err != nil || index < 0 || (index >= len(container) && !allowEnd) {
			return nil, fmt.Errorf("invalid array index %q", key)
		}
		if len(parts) == 1 {
			switch operation.Op {
			case "add":
				container = append(container, nil)
				copy(container[index+1:], container[index:])
				container[index] = operation.Value
			case "replace":
				container[index] = operation.Value
			case "remove":
				container = append(container[:index], container[index+1:]...)
			default:
				return nil, fmt.Errorf("operation must be add, replace, or remove")
			}
			return container, nil
		}
		updated, err := patchAt(container[index], parts[1:], operation)
		if err != nil {
			return nil, err
		}
		container[index] = updated
		return container, nil
	default:
		return nil, fmt.Errorf("path traverses scalar value")
	}
}

func decodePointerSegment(segment string) (string, error) {
	var result strings.Builder
	for index := 0; index < len(segment); index++ {
		if segment[index] != '~' {
			result.WriteByte(segment[index])
			continue
		}
		if index+1 >= len(segment) || (segment[index+1] != '0' && segment[index+1] != '1') {
			return "", fmt.Errorf("invalid JSON Pointer escape")
		}
		index++
		if segment[index] == '0' {
			result.WriteByte('~')
		} else {
			result.WriteByte('/')
		}
	}
	return result.String(), nil
}

func containedDestination(root, file string) (string, error) {
	if !filepath.IsAbs(file) {
		file = filepath.Join(root, file)
	}
	file, err := filepath.Abs(file)
	if err != nil {
		return "", err
	}
	parent, err := filepath.EvalSymlinks(filepath.Dir(file))
	if err != nil {
		return "", err
	}
	file = filepath.Join(parent, filepath.Base(file))
	relative, err := filepath.Rel(root, file)
	if err != nil || relative == ".." || filepath.IsAbs(relative) || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("document is outside project root")
	}
	if filepath.Ext(file) != ".gora" {
		return "", fmt.Errorf("document must use the .gora extension")
	}
	return filepath.Clean(file), nil
}

func containedKnownPath(project *Project, sourceID string) (string, error) {
	relative, err := url.PathUnescape(sourceID)
	if err != nil {
		return "", fmt.Errorf("invalid source id")
	}
	path, err := containedDestination(project.root, filepath.FromSlash(relative))
	if err != nil {
		return "", err
	}
	project.mu.RLock()
	known := project.knownPathsLocked()[path]
	project.mu.RUnlock()
	if !known {
		return "", fmt.Errorf("document is not known by the project")
	}
	return path, nil
}
