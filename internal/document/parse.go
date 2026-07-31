package document

import (
	"bytes"
	"fmt"
	"io"
	"math"
	"sort"
	"strconv"
	"strings"

	"go.yaml.in/yaml/v4"
)

func Parse(file string, src []byte) (*Document, []Diagnostic) {
	decoder := yaml.NewDecoder(bytes.NewReader(src))
	var root yaml.Node
	if err := decoder.Decode(&root); err != nil {
		return nil, []Diagnostic{diagnostic(file, "parse.yaml", err.Error(), 1, 1)}
	}
	var extra yaml.Node
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return nil, []Diagnostic{diagnostic(file, "parse.multiple_documents", "multiple YAML documents are not supported", nodeLine(&extra), nodeColumn(&extra))}
		}
		return nil, []Diagnostic{diagnostic(file, "parse.yaml", err.Error(), 1, 1)}
	}
	if len(root.Content) != 1 || root.Content[0].Kind != yaml.MappingNode {
		return nil, []Diagnostic{diagnostic(file, "parse.root", "document root must be a mapping", nodeLine(&root), nodeColumn(&root))}
	}

	var diagnostics []Diagnostic
	checkYAMLSubset(file, root.Content[0], &diagnostics)
	if len(diagnostics) != 0 {
		return nil, diagnostics
	}

	doc := &Document{
		File:        file,
		Breakpoints: make(map[string]Breakpoint),
		Screens:     make(map[string]*Node),
		Parameters:  make(map[string]Parameter),
		Slots:       make(map[string]Slot),
		Previews:    make(map[string]Preview),
		Tokens:      make(map[string]map[string]any),
		Imports: Imports{
			Components: make(map[string]string),
			Tokens:     make(map[string]string),
		},
	}
	m := mapping(file, root.Content[0], topLevelFields(), &diagnostics)
	doc.Version = integerField(file, m, "gora", true, &diagnostics)
	if doc.Version != 0 && doc.Version != 1 {
		addNodeDiagnostic(file, m["gora"], "schema.version", fmt.Sprintf("unsupported Gora format version %d", doc.Version), nil, &diagnostics)
	}
	doc.Kind = Kind(stringField(file, m, "kind", true, &diagnostics))
	doc.Name = stringField(file, m, "name", false, &diagnostics)
	validateKindFields(file, root.Content[0], doc.Kind, &diagnostics)
	parseImports(file, m["imports"], &doc.Imports, &diagnostics)
	doc.Viewport = parseViewport(file, m["viewport"], doc.Kind != KindTokens, &diagnostics)
	doc.Breakpoints = parseBreakpoints(file, m["breakpoints"], &diagnostics)

	switch doc.Kind {
	case KindApp:
		doc.Entry = stringField(file, m, "entry", true, &diagnostics)
		doc.Screens = parseNodeMap(file, m["screens"], "screens", true, &diagnostics)
		if doc.Entry != "" {
			if _, ok := doc.Screens[doc.Entry]; !ok {
				addNodeDiagnostic(file, m["entry"], "schema.entry", fmt.Sprintf("entry screen %q does not exist", doc.Entry), mapKeys(doc.Screens), &diagnostics)
			}
		}
	case KindComponent:
		doc.Parameters = parseParameters(file, m["parameters"], &diagnostics)
		doc.Slots = parseSlots(file, m["slots"], &diagnostics)
		doc.Previews = parsePreviews(file, m["previews"], &diagnostics)
		doc.Root = parseNode(file, m["root"], "root", true, &diagnostics)
	case KindTokens:
		doc.Tokens = parseTokens(file, m["tokens"], &diagnostics)
	default:
		addNodeDiagnostic(file, m["kind"], "schema.kind", fmt.Sprintf("unknown document kind %q", doc.Kind), []string{string(KindApp), string(KindComponent), string(KindTokens)}, &diagnostics)
	}

	validateNames(file, doc, &diagnostics)
	validateBreakpoints(file, doc.Breakpoints, &diagnostics)
	validateDocument(doc, &diagnostics)
	if len(diagnostics) != 0 {
		return doc, diagnostics
	}
	return doc, nil
}

func topLevelFields() []string {
	return []string{"gora", "kind", "name", "imports", "viewport", "breakpoints", "entry", "screens", "parameters", "slots", "previews", "root", "tokens"}
}

func parseImports(file string, n *yaml.Node, out *Imports, diagnostics *[]Diagnostic) {
	if n == nil {
		return
	}
	m := mapping(file, n, []string{"components", "tokens"}, diagnostics)
	out.Components = stringMap(file, m["components"], diagnostics)
	out.Tokens = stringMap(file, m["tokens"], diagnostics)
}

func parseViewport(file string, n *yaml.Node, required bool, diagnostics *[]Diagnostic) Viewport {
	if n == nil {
		if required {
			*diagnostics = append(*diagnostics, diagnostic(file, "schema.required", "missing required field \"viewport\"", 1, 1))
		}
		return Viewport{}
	}
	m := mapping(file, n, []string{"width", "height", "background"}, diagnostics)
	v := Viewport{
		Width:      integerField(file, m, "width", true, diagnostics),
		Height:     integerField(file, m, "height", true, diagnostics),
		Background: valueOf(file, m["background"], diagnostics),
	}
	if background := v.Background; background != nil {
		switch background := background.(type) {
		case string:
			if !validColor(background) {
				addNodeDiagnostic(file, m["background"], "schema.color", "viewport background must be a color or typed reference", nil, diagnostics)
			}
		case map[string]any:
			reference, ok := background["ref"].(string)
			if len(background) != 1 || !ok || reference == "" {
				addNodeDiagnostic(file, m["background"], "schema.color", "viewport background must be a color or typed reference", nil, diagnostics)
			}
		default:
			addNodeDiagnostic(file, m["background"], "schema.color", "viewport background must be a color or typed reference", nil, diagnostics)
		}
	}
	if v.Width <= 0 {
		addNodeDiagnostic(file, m["width"], "schema.viewport", "viewport width must be positive", nil, diagnostics)
	}
	if v.Height <= 0 {
		addNodeDiagnostic(file, m["height"], "schema.viewport", "viewport height must be positive", nil, diagnostics)
	}
	return v
}

func parseBreakpoints(file string, n *yaml.Node, diagnostics *[]Diagnostic) map[string]Breakpoint {
	out := make(map[string]Breakpoint)
	if n == nil {
		return out
	}
	for key, value := range rawMapping(file, n, diagnostics) {
		m := mapping(file, value, []string{"min_width", "max_width"}, diagnostics)
		b := Breakpoint{Source: source(file, value)}
		if min := m["min_width"]; min != nil {
			v := integerNode(file, min, diagnostics)
			b.MinWidth = &v
		}
		if max := m["max_width"]; max != nil {
			v := integerNode(file, max, diagnostics)
			b.MaxWidth = &v
		}
		if b.MinWidth == nil && b.MaxWidth == nil {
			addNodeDiagnostic(file, value, "schema.breakpoint", "breakpoint requires min_width or max_width", nil, diagnostics)
		}
		out[key] = b
	}
	return out
}

func parseNodeMap(file string, n *yaml.Node, path string, required bool, diagnostics *[]Diagnostic) map[string]*Node {
	out := make(map[string]*Node)
	if n == nil {
		if required {
			*diagnostics = append(*diagnostics, diagnostic(file, "schema.required", fmt.Sprintf("missing required field %q", path), 1, 1))
		}
		return out
	}
	for key, value := range rawMapping(file, n, diagnostics) {
		out[key] = parseNode(file, value, path+"."+key, true, diagnostics)
	}
	return out
}

func parseNode(file string, n *yaml.Node, path string, required bool, diagnostics *[]Diagnostic) *Node {
	if n == nil {
		if required {
			*diagnostics = append(*diagnostics, diagnostic(file, "schema.required", fmt.Sprintf("missing required field %q", path), 1, 1))
		}
		return nil
	}
	m := mapping(file, n, []string{"type", "name", "props", "place", "responsive", "children"}, diagnostics)
	node := &Node{
		Type:       stringField(file, m, "type", true, diagnostics),
		Name:       stringField(file, m, "name", false, diagnostics),
		Props:      valueMap(file, m["props"], diagnostics),
		Place:      valueMap(file, m["place"], diagnostics),
		Responsive: parseResponsive(file, m["responsive"], diagnostics),
		Source:     source(file, n),
	}
	if children := m["children"]; children != nil {
		if children.Kind != yaml.SequenceNode {
			addNodeDiagnostic(file, children, "schema.type", "children must be a list", nil, diagnostics)
		} else {
			for i, child := range children.Content {
				node.Children = append(node.Children, parseNode(file, child, fmt.Sprintf("%s.children[%d]", path, i), true, diagnostics))
			}
		}
	}
	return node
}

func parseResponsive(file string, n *yaml.Node, diagnostics *[]Diagnostic) map[string]Responsive {
	out := make(map[string]Responsive)
	if n == nil {
		return out
	}
	for key, value := range rawMapping(file, n, diagnostics) {
		m := mapping(file, value, []string{"visible", "props", "place"}, diagnostics)
		r := Responsive{
			Props:  valueMap(file, m["props"], diagnostics),
			Place:  valueMap(file, m["place"], diagnostics),
			Source: source(file, value),
		}
		if visible := m["visible"]; visible != nil {
			v := boolNode(file, visible, diagnostics)
			r.Visible = &v
		}
		out[key] = r
	}
	return out
}

func parseParameters(file string, n *yaml.Node, diagnostics *[]Diagnostic) map[string]Parameter {
	out := make(map[string]Parameter)
	if n == nil {
		return out
	}
	for key, value := range rawMapping(file, n, diagnostics) {
		m := mapping(file, value, []string{"type", "required", "default", "values"}, diagnostics)
		p := Parameter{
			Type:     stringField(file, m, "type", true, diagnostics),
			Required: boolField(file, m, "required", false, diagnostics),
			Default:  scalarValue(file, m["default"], diagnostics),
			Source:   source(file, value),
		}
		if values := m["values"]; values != nil {
			p.Values = stringSlice(file, values, diagnostics)
		}
		out[key] = p
	}
	return out
}

func parseSlots(file string, n *yaml.Node, diagnostics *[]Diagnostic) map[string]Slot {
	out := make(map[string]Slot)
	if n == nil {
		return out
	}
	for key, value := range rawMapping(file, n, diagnostics) {
		m := mapping(file, value, []string{"required"}, diagnostics)
		out[key] = Slot{Required: boolField(file, m, "required", false, diagnostics), Source: source(file, value)}
	}
	return out
}

func parsePreviews(file string, n *yaml.Node, diagnostics *[]Diagnostic) map[string]Preview {
	out := make(map[string]Preview)
	if n == nil {
		return out
	}
	for key, value := range rawMapping(file, n, diagnostics) {
		m := mapping(file, value, []string{"viewport", "parameters", "children"}, diagnostics)
		p := Preview{Parameters: valueMap(file, m["parameters"], diagnostics), Source: source(file, value)}
		if viewport := m["viewport"]; viewport != nil {
			v := parseViewport(file, viewport, true, diagnostics)
			p.Viewport = &v
		}
		if children := m["children"]; children != nil {
			if children.Kind != yaml.SequenceNode {
				addNodeDiagnostic(file, children, "schema.type", "preview children must be a list", nil, diagnostics)
			} else {
				for i, child := range children.Content {
					p.Children = append(p.Children, parseNode(file, child, fmt.Sprintf("previews.%s.children[%d]", key, i), true, diagnostics))
				}
			}
		}
		out[key] = p
	}
	return out
}

func parseTokens(file string, n *yaml.Node, diagnostics *[]Diagnostic) map[string]map[string]any {
	out := make(map[string]map[string]any)
	if n == nil {
		*diagnostics = append(*diagnostics, diagnostic(file, "schema.required", "missing required field \"tokens\"", 1, 1))
		return out
	}
	allowed := []string{"color", "dimension", "font_face", "text_style", "shadow", "linear_gradient"}
	m := mapping(file, n, allowed, diagnostics)
	for _, kind := range allowed {
		if value := m[kind]; value != nil {
			out[kind] = valueMap(file, value, diagnostics)
		}
	}
	return out
}

func validateNames(file string, doc *Document, diagnostics *[]Diagnostic) {
	seen := make(map[string]Source)
	var walk func(*Node)
	walk = func(n *Node) {
		if n == nil {
			return
		}
		if n.Name != "" {
			if first, ok := seen[n.Name]; ok {
				d := diagnostic(file, "schema.duplicate_name", fmt.Sprintf("node name %q is already used at %d:%d", n.Name, first.Line, first.Column), n.Source.Line, n.Source.Column)
				d.NodeName = n.Name
				*diagnostics = append(*diagnostics, d)
			} else {
				seen[n.Name] = n.Source
			}
		}
		for _, child := range n.Children {
			walk(child)
		}
	}
	for _, screen := range doc.Screens {
		walk(screen)
	}
	walk(doc.Root)
	for _, preview := range doc.Previews {
		for _, child := range preview.Children {
			walk(child)
		}
	}
}

func validateBreakpoints(file string, breakpoints map[string]Breakpoint, diagnostics *[]Diagnostic) {
	type interval struct {
		name     string
		min, max int
		source   Source
	}
	var intervals []interval
	for name, b := range breakpoints {
		if strings.TrimSpace(name) == "" {
			*diagnostics = append(*diagnostics, diagnostic(file, "schema.breakpoint", "breakpoint names cannot be empty", b.Source.Line, b.Source.Column))
			continue
		}
		min, max := math.MinInt, math.MaxInt
		if b.MinWidth != nil {
			min = *b.MinWidth
			if min < 0 {
				*diagnostics = append(*diagnostics, diagnostic(file, "schema.breakpoint", fmt.Sprintf("breakpoint %q min_width must be non-negative", name), b.Source.Line, b.Source.Column))
			}
		}
		if b.MaxWidth != nil {
			max = *b.MaxWidth
			if max < 0 {
				*diagnostics = append(*diagnostics, diagnostic(file, "schema.breakpoint", fmt.Sprintf("breakpoint %q max_width must be non-negative", name), b.Source.Line, b.Source.Column))
			}
		}
		if min > max {
			*diagnostics = append(*diagnostics, diagnostic(file, "schema.breakpoint", fmt.Sprintf("breakpoint %q has min_width greater than max_width", name), b.Source.Line, b.Source.Column))
			continue
		}
		intervals = append(intervals, interval{name: name, min: min, max: max, source: b.Source})
	}
	for i := range intervals {
		for j := i + 1; j < len(intervals); j++ {
			if intervals[i].min <= intervals[j].max && intervals[j].min <= intervals[i].max {
				*diagnostics = append(*diagnostics, diagnostic(file, "schema.breakpoint_overlap", fmt.Sprintf("breakpoints %q and %q overlap", intervals[i].name, intervals[j].name), intervals[j].source.Line, intervals[j].source.Column))
			}
		}
	}
}

func validateDocument(doc *Document, diagnostics *[]Diagnostic) {
	for alias := range doc.Imports.Components {
		if strings.TrimSpace(alias) == "" {
			*diagnostics = append(*diagnostics, diagnostic(doc.File, "import.alias", "component import aliases cannot be empty", 1, 1))
		}
		if _, duplicate := doc.Imports.Tokens[alias]; duplicate {
			*diagnostics = append(*diagnostics, diagnostic(doc.File, "import.alias_collision", fmt.Sprintf("import alias %q is used for both a component and token module", alias), 1, 1))
		}
	}
	for alias := range doc.Imports.Tokens {
		if strings.TrimSpace(alias) == "" {
			*diagnostics = append(*diagnostics, diagnostic(doc.File, "import.alias", "token import aliases cannot be empty", 1, 1))
		}
	}
	if doc.Kind == KindTokens && len(doc.Imports.Components) != 0 {
		*diagnostics = append(*diagnostics, diagnostic(doc.File, "import.kind", "token documents may import only token modules", 1, 1))
	}
	parameterTypes := []string{"text", "string", "number", "boolean", "color", "dimension", "enum"}
	for name, parameter := range doc.Parameters {
		if !contains(parameterTypes, parameter.Type) {
			d := diagnostic(doc.File, "parameter.type", fmt.Sprintf("parameter %q has unsupported type %q", name, parameter.Type), parameter.Source.Line, parameter.Source.Column)
			d.Suggestions = nearest(parameter.Type, parameterTypes)
			*diagnostics = append(*diagnostics, d)
		}
		if parameter.Type == "enum" && len(parameter.Values) == 0 {
			*diagnostics = append(*diagnostics, diagnostic(doc.File, "parameter.enum", fmt.Sprintf("enum parameter %q requires values", name), parameter.Source.Line, parameter.Source.Column))
		}
		if parameter.Type != "enum" && len(parameter.Values) != 0 {
			*diagnostics = append(*diagnostics, diagnostic(doc.File, "parameter.values", fmt.Sprintf("only enum parameter %q may declare values", name), parameter.Source.Line, parameter.Source.Column))
		}
		seenValues := make(map[string]bool)
		for _, value := range parameter.Values {
			if seenValues[value] {
				*diagnostics = append(*diagnostics, diagnostic(doc.File, "parameter.values", fmt.Sprintf("enum parameter %q contains duplicate value %q", name, value), parameter.Source.Line, parameter.Source.Column))
			}
			seenValues[value] = true
		}
		if parameter.Default != nil && !documentParameterValueMatches(parameter, parameter.Default) {
			*diagnostics = append(*diagnostics, diagnostic(doc.File, "parameter.default", fmt.Sprintf("parameter %q default does not match %s", name, parameter.Type), parameter.Source.Line, parameter.Source.Column))
		}
	}
	validateTokens(doc, diagnostics)
	if doc.Kind == KindComponent && len(doc.Previews) == 0 {
		*diagnostics = append(*diagnostics, diagnostic(doc.File, "component.preview", "component documents require at least one named preview fixture", 1, 1))
	}

	var walk func(*Node)
	walk = func(node *Node) {
		if node == nil {
			return
		}
		nodeTypes := []string{"stack", "grid", "overlay", "scroll", "surface", "text", "image", "spacer", "divider", "instance", "slot", "slot_content"}
		if !contains(nodeTypes, node.Type) {
			d := diagnostic(doc.File, "schema.node_type", fmt.Sprintf("unknown node type %q", node.Type), node.Source.Line, node.Source.Column)
			d.NodeName = node.Name
			d.Suggestions = nearest(node.Type, nodeTypes)
			*diagnostics = append(*diagnostics, d)
		}
		validateNodeFields(node, diagnostics)
		switch node.Type {
		case "surface":
			if len(node.Children) > 1 {
				*diagnostics = append(*diagnostics, nodeDiagnostic(node, "schema.children", "surface accepts at most one child"))
			}
		case "scroll":
			if len(node.Children) != 1 {
				*diagnostics = append(*diagnostics, nodeDiagnostic(node, "schema.children", "scroll requires exactly one child"))
			}
		case "text", "image", "spacer", "divider":
			if len(node.Children) != 0 {
				*diagnostics = append(*diagnostics, nodeDiagnostic(node, "schema.children", fmt.Sprintf("%s cannot have children", node.Type)))
			}
		case "instance":
			if component, ok := node.Props["component"].(string); !ok || component == "" {
				*diagnostics = append(*diagnostics, nodeDiagnostic(node, "component.instance", "instance requires a component alias"))
			}
			for _, child := range node.Children {
				if child != nil && child.Type != "slot_content" {
					*diagnostics = append(*diagnostics, nodeDiagnostic(child, "component.slot_content", "instance children must be slot_content wrappers"))
				}
			}
		case "slot":
			name, _ := node.Props["name"].(string)
			if name == "" {
				name = "default"
			}
			if name != "default" {
				if _, ok := doc.Slots[name]; !ok {
					d := nodeDiagnostic(node, "component.slot", fmt.Sprintf("unknown declared slot %q", name))
					d.Suggestions = nearest(name, mapKeys(doc.Slots))
					*diagnostics = append(*diagnostics, d)
				}
			}
		}
		for breakpoint := range node.Responsive {
			if _, ok := doc.Breakpoints[breakpoint]; !ok {
				d := nodeDiagnostic(node, "responsive.breakpoint", fmt.Sprintf("unknown breakpoint %q", breakpoint))
				d.Suggestions = nearest(breakpoint, mapKeys(doc.Breakpoints))
				*diagnostics = append(*diagnostics, d)
			}
		}
		for _, child := range node.Children {
			walk(child)
		}
	}
	for _, screen := range doc.Screens {
		walk(screen)
	}
	walk(doc.Root)
	for _, preview := range doc.Previews {
		for _, child := range preview.Children {
			walk(child)
		}
	}
}

func documentParameterValueMatches(parameter Parameter, value any) bool {
	switch parameter.Type {
	case "text", "string":
		_, ok := value.(string)
		return ok
	case "color":
		text, ok := value.(string)
		return ok && validColor(text)
	case "number", "dimension":
		_, ok := finiteNumber(value)
		return ok
	case "boolean":
		_, ok := value.(bool)
		return ok
	case "enum":
		text, ok := value.(string)
		return ok && contains(parameter.Values, text)
	default:
		return false
	}
}

func validateKindFields(file string, root *yaml.Node, kind Kind, diagnostics *[]Diagnostic) {
	common := []string{"gora", "kind", "name", "imports"}
	allowed := append([]string(nil), common...)
	switch kind {
	case KindApp:
		allowed = append(allowed, "viewport", "breakpoints", "entry", "screens")
	case KindComponent:
		allowed = append(allowed, "viewport", "breakpoints", "parameters", "slots", "previews", "root")
	case KindTokens:
		allowed = append(allowed, "tokens")
	default:
		return
	}
	for key := range rawMapping(file, root, diagnostics) {
		if !contains(allowed, key) {
			addNodeDiagnostic(file, mappingKeyNode(root, key), "schema.kind_field", fmt.Sprintf("field %q is not valid for %s documents", key, kind), nearest(key, allowed), diagnostics)
		}
	}
}

func validateTokens(doc *Document, diagnostics *[]Diagnostic) {
	for name, value := range doc.Tokens["color"] {
		text, ok := value.(string)
		if !ok || !validColor(text) {
			*diagnostics = append(*diagnostics, diagnostic(doc.File, "token.color", fmt.Sprintf("color token %q must be #RRGGBB, #RRGGBBAA, or transparent", name), 1, 1))
		}
	}
	for name, value := range doc.Tokens["dimension"] {
		switch value.(type) {
		case int64, float64:
		default:
			*diagnostics = append(*diagnostics, diagnostic(doc.File, "token.dimension", fmt.Sprintf("dimension token %q must be a finite number", name), 1, 1))
		}
	}
	for _, kind := range []string{"font_face", "text_style", "shadow", "linear_gradient"} {
		for name, value := range doc.Tokens[kind] {
			mapping, ok := value.(map[string]any)
			if !ok {
				*diagnostics = append(*diagnostics, diagnostic(doc.File, "token."+kind, fmt.Sprintf("%s token %q must be a mapping", kind, name), 1, 1))
				continue
			}
			validateTokenFields(doc, kind, name, mapping, diagnostics)
		}
	}
}

func validateTokenFields(doc *Document, kind, name string, value map[string]any, diagnostics *[]Diagnostic) {
	allowed := map[string][]string{
		"font_face":       {"src"},
		"text_style":      {"font", "size", "weight", "italic", "line_height", "letter_spacing"},
		"shadow":          {"x", "y", "blur", "spread", "color"},
		"linear_gradient": {"angle", "stops"},
	}[kind]
	for key := range value {
		if !contains(allowed, key) {
			d := diagnostic(doc.File, "token.unknown_field", fmt.Sprintf("unknown %s token field %q on %q", kind, key, name), 1, 1)
			d.Suggestions = nearest(key, allowed)
			*diagnostics = append(*diagnostics, d)
		}
	}
	if kind == "font_face" {
		if source, ok := value["src"].(string); !ok || source == "" {
			*diagnostics = append(*diagnostics, diagnostic(doc.File, "token.font_face", fmt.Sprintf("font_face token %q requires a string src", name), 1, 1))
		}
	}
	if kind == "shadow" {
		if colorText, ok := value["color"].(string); ok && !validColor(colorText) {
			*diagnostics = append(*diagnostics, diagnostic(doc.File, "token.shadow", fmt.Sprintf("shadow token %q has an invalid color", name), 1, 1))
		}
	}
	if kind != "linear_gradient" {
		return
	}
	stops, ok := value["stops"].([]any)
	if !ok || len(stops) < 2 {
		*diagnostics = append(*diagnostics, diagnostic(doc.File, "token.linear_gradient", fmt.Sprintf("linear_gradient token %q requires at least two stops", name), 1, 1))
		return
	}
	lastOffset := -1.0
	for _, raw := range stops {
		stop, ok := raw.(map[string]any)
		if !ok {
			*diagnostics = append(*diagnostics, diagnostic(doc.File, "token.linear_gradient", fmt.Sprintf("linear_gradient token %q stops must be mappings", name), 1, 1))
			continue
		}
		for key := range stop {
			if key != "offset" && key != "color" {
				*diagnostics = append(*diagnostics, diagnostic(doc.File, "token.unknown_field", fmt.Sprintf("unknown gradient stop field %q", key), 1, 1))
			}
		}
		offset, offsetOK := finiteNumber(stop["offset"])
		colorText, colorOK := stop["color"].(string)
		if !offsetOK || offset < 0 || offset > 1 || offset < lastOffset || !colorOK || !validColor(colorText) {
			*diagnostics = append(*diagnostics, diagnostic(doc.File, "token.linear_gradient", fmt.Sprintf("linear_gradient token %q has an invalid stop", name), 1, 1))
		}
		lastOffset = offset
	}
}

func validateNodeFields(node *Node, diagnostics *[]Diagnostic) {
	common := []string{"width", "height", "min_width", "max_width", "min_height", "max_height", "opacity"}
	byType := map[string][]string{
		"stack":        {"direction", "padding", "gap", "alignment", "distribution"},
		"grid":         {"columns", "rows", "gap", "column_gap", "row_gap"},
		"overlay":      {"alignment"},
		"scroll":       {"axis", "scrollbar"},
		"surface":      {"padding", "background", "border", "radius", "shadow", "clip"},
		"text":         {"text", "content", "style", "font", "size", "weight", "italic", "color", "alignment", "line_height", "letter_spacing", "wrap", "max_lines", "overflow", "background"},
		"image":        {"src", "fit", "alignment"},
		"spacer":       {},
		"divider":      {"orientation", "thickness", "color"},
		"instance":     {"component", "parameters"},
		"slot":         {"name"},
		"slot_content": {"slot"},
	}
	allowed := append(append([]string(nil), common...), byType[node.Type]...)
	for key := range node.Props {
		if !contains(allowed, key) {
			d := nodeDiagnostic(node, "schema.prop", fmt.Sprintf("unknown %s prop %q", node.Type, key))
			d.Suggestions = nearest(key, allowed)
			*diagnostics = append(*diagnostics, d)
		}
	}
	validateReferenceObjects(node, node.Props, diagnostics)
	validateReferenceObjects(node, node.Place, diagnostics)
	placeFields := []string{"x", "y", "offset_x", "offset_y", "alignment", "grow", "row", "column", "row_span", "column_span"}
	for key := range node.Place {
		if !contains(placeFields, key) {
			d := nodeDiagnostic(node, "schema.place", fmt.Sprintf("unknown placement field %q", key))
			d.Suggestions = nearest(key, placeFields)
			*diagnostics = append(*diagnostics, d)
		}
	}
	for _, responsive := range node.Responsive {
		validateReferenceObjects(node, responsive.Props, diagnostics)
		validateReferenceObjects(node, responsive.Place, diagnostics)
		for key := range responsive.Props {
			if !contains(allowed, key) {
				d := nodeDiagnostic(node, "schema.prop", fmt.Sprintf("unknown responsive %s prop %q", node.Type, key))
				d.Suggestions = nearest(key, allowed)
				*diagnostics = append(*diagnostics, d)
			}
		}
		for key := range responsive.Place {
			if !contains(placeFields, key) {
				d := nodeDiagnostic(node, "schema.place", fmt.Sprintf("unknown responsive placement field %q", key))
				d.Suggestions = nearest(key, placeFields)
				*diagnostics = append(*diagnostics, d)
			}
		}
	}
	validatePrimitiveValues(node, diagnostics)
}

func validatePrimitiveValues(node *Node, diagnostics *[]Diagnostic) {
	enum := func(key string, choices []string) {
		value, ok := node.Props[key].(string)
		if !ok || value == "" {
			return
		}
		if !contains(choices, value) {
			d := nodeDiagnostic(node, "schema.prop_value", fmt.Sprintf("%s.%s must be one of %s", node.Type, key, strings.Join(choices, ", ")))
			d.Suggestions = nearest(value, choices)
			*diagnostics = append(*diagnostics, d)
		}
	}
	switch node.Type {
	case "stack":
		enum("direction", []string{"horizontal", "vertical"})
		enum("alignment", []string{"start", "center", "end", "stretch"})
		enum("distribution", []string{"start", "center", "end", "space_between", "space_around"})
	case "scroll":
		enum("axis", []string{"horizontal", "vertical"})
	case "image":
		enum("fit", []string{"contain", "cover", "fill"})
	case "divider":
		enum("orientation", []string{"horizontal", "vertical"})
	}
	if padding, exists := node.Props["padding"]; exists {
		validateEdges(node, "padding", padding, diagnostics)
	}
	if radius, ok := node.Props["radius"].(map[string]any); ok {
		if !isReferenceValue(radius) {
			expected := []string{"top_left", "top_right", "bottom_right", "bottom_left"}
			for _, key := range expected {
				if _, exists := radius[key]; !exists {
					*diagnostics = append(*diagnostics, nodeDiagnostic(node, "schema.radius", "corner radius mappings require top_left, top_right, bottom_right, and bottom_left"))
					break
				}
			}
			for key := range radius {
				if !contains(expected, key) {
					*diagnostics = append(*diagnostics, nodeDiagnostic(node, "schema.radius", fmt.Sprintf("unknown radius corner %q", key)))
				}
			}
			for key, value := range radius {
				if number, ok := finiteNumber(value); !ok || number < 0 {
					*diagnostics = append(*diagnostics, nodeDiagnostic(node, "schema.radius", fmt.Sprintf("radius corner %q must be non-negative", key)))
				}
			}
		}
	} else if radius, exists := node.Props["radius"]; exists {
		if number, ok := finiteNumber(radius); !ok || number < 0 {
			*diagnostics = append(*diagnostics, nodeDiagnostic(node, "schema.radius", "radius must be a non-negative number or four-corner mapping"))
		}
	}
	validateObjectFields(node, "border", node.Props["border"], []string{"thickness", "color"}, diagnostics)
	validateObjectFields(node, "shadow", node.Props["shadow"], []string{"x", "y", "blur", "spread", "color"}, diagnostics)
	if background, ok := node.Props["background"].(map[string]any); ok {
		if _, reference := background["ref"]; !reference {
			validateObjectFields(node, "background", background, []string{"angle", "stops"}, diagnostics)
		}
	}
	for _, key := range []string{"color", "background"} {
		if value, ok := node.Props[key].(string); ok && strings.HasPrefix(value, "#") && !validColor(value) {
			*diagnostics = append(*diagnostics, nodeDiagnostic(node, "schema.color", fmt.Sprintf("%s must be #RRGGBB, #RRGGBBAA, transparent, or a typed reference", key)))
		}
	}
	for _, key := range []string{"width", "height", "min_width", "max_width", "min_height", "max_height"} {
		value, exists := node.Props[key]
		if !exists {
			continue
		}
		if isReferenceValue(value) {
			continue
		}
		if text, ok := value.(string); ok {
			if text != "auto" && text != "fill" {
				*diagnostics = append(*diagnostics, nodeDiagnostic(node, "schema.size", fmt.Sprintf("%s must be a non-negative number, auto, or fill", key)))
			}
			continue
		}
		if number, ok := finiteNumber(value); !ok || number < 0 {
			*diagnostics = append(*diagnostics, nodeDiagnostic(node, "schema.size", fmt.Sprintf("%s must be a non-negative number, auto, or fill", key)))
		}
	}
	for _, key := range []string{"gap", "column_gap", "row_gap", "thickness"} {
		if value, exists := node.Props[key]; exists {
			if isReferenceValue(value) {
				continue
			}
			if number, ok := finiteNumber(value); !ok || number < 0 {
				*diagnostics = append(*diagnostics, nodeDiagnostic(node, "schema.number_range", fmt.Sprintf("%s must be non-negative", key)))
			}
		}
	}
	if value, exists := node.Props["opacity"]; exists {
		if !isReferenceValue(value) {
			if number, ok := finiteNumber(value); !ok || number < 0 || number > 1 {
				*diagnostics = append(*diagnostics, nodeDiagnostic(node, "schema.number_range", "opacity must be between 0 and 1"))
			}
		}
	}
	for _, key := range []string{"grow", "row", "column", "row_span", "column_span"} {
		if value, exists := node.Place[key]; exists {
			if isReferenceValue(value) {
				continue
			}
			if number, ok := finiteNumber(value); !ok || number < 0 || (strings.HasSuffix(key, "_span") && number < 1) {
				*diagnostics = append(*diagnostics, nodeDiagnostic(node, "schema.number_range", fmt.Sprintf("place.%s has an invalid value", key)))
			}
		}
	}
	validateMinMax(node, "width", diagnostics)
	validateMinMax(node, "height", diagnostics)
}

func validateObjectFields(node *Node, name string, raw any, allowed []string, diagnostics *[]Diagnostic) {
	if raw == nil {
		return
	}
	value, ok := raw.(map[string]any)
	if !ok {
		*diagnostics = append(*diagnostics, nodeDiagnostic(node, "schema.type", fmt.Sprintf("%s must be a mapping", name)))
		return
	}
	if _, reference := value["ref"]; reference {
		return
	}
	for key := range value {
		if !contains(allowed, key) {
			d := nodeDiagnostic(node, "schema.unknown_field", fmt.Sprintf("unknown %s field %q", name, key))
			d.Suggestions = nearest(key, allowed)
			*diagnostics = append(*diagnostics, d)
		}
	}
}

func finiteNumber(value any) (float64, bool) {
	switch value := value.(type) {
	case int64:
		return float64(value), true
	case float64:
		return value, !math.IsInf(value, 0) && !math.IsNaN(value)
	default:
		return 0, false
	}
}

func validateMinMax(node *Node, dimension string, diagnostics *[]Diagnostic) {
	minimum, minOK := finiteNumber(node.Props["min_"+dimension])
	maximum, maxOK := finiteNumber(node.Props["max_"+dimension])
	if minOK && maxOK && minimum > maximum {
		*diagnostics = append(*diagnostics, nodeDiagnostic(node, "schema.number_range", fmt.Sprintf("min_%s cannot exceed max_%s", dimension, dimension)))
	}
}

func validateEdges(node *Node, key string, value any, diagnostics *[]Diagnostic) {
	if isReferenceValue(value) {
		return
	}
	edges, ok := value.(map[string]any)
	if !ok {
		*diagnostics = append(*diagnostics, nodeDiagnostic(node, "schema.insets", key+" must name top, right, bottom, and left"))
		return
	}
	expected := []string{"top", "right", "bottom", "left"}
	for _, edge := range expected {
		if _, exists := edges[edge]; !exists {
			*diagnostics = append(*diagnostics, nodeDiagnostic(node, "schema.insets", key+" must name top, right, bottom, and left"))
			return
		}
	}
	for edge := range edges {
		if !contains(expected, edge) {
			*diagnostics = append(*diagnostics, nodeDiagnostic(node, "schema.insets", fmt.Sprintf("unknown inset edge %q", edge)))
		}
	}
	for edge, value := range edges {
		if number, ok := finiteNumber(value); !ok || number < 0 {
			*diagnostics = append(*diagnostics, nodeDiagnostic(node, "schema.insets", fmt.Sprintf("inset edge %q must be non-negative", edge)))
		}
	}
}

func isReferenceValue(value any) bool {
	mapping, ok := value.(map[string]any)
	if !ok || len(mapping) != 1 {
		return false
	}
	reference, ok := mapping["ref"].(string)
	return ok && reference != ""
}

func validateReferenceObjects(node *Node, value any, diagnostics *[]Diagnostic) {
	switch value := value.(type) {
	case map[string]any:
		if raw, hasRef := value["ref"]; hasRef {
			reference, stringRef := raw.(string)
			if len(value) != 1 || !stringRef || reference == "" {
				*diagnostics = append(*diagnostics, nodeDiagnostic(node, "reference.syntax", "a reference must be exactly { ref: non-empty-string }"))
				return
			}
		}
		for _, nested := range value {
			validateReferenceObjects(node, nested, diagnostics)
		}
	case []any:
		for _, nested := range value {
			validateReferenceObjects(node, nested, diagnostics)
		}
	}
}

func validColor(value string) bool {
	if value == "transparent" {
		return true
	}
	if len(value) != 7 && len(value) != 9 || !strings.HasPrefix(value, "#") {
		return false
	}
	_, err := strconv.ParseUint(value[1:], 16, 32)
	return err == nil
}

func nodeDiagnostic(node *Node, code, message string) Diagnostic {
	d := diagnostic(node.Source.File, code, message, node.Source.Line, node.Source.Column)
	d.NodeName = node.Name
	return d
}

func contains(values []string, value string) bool {
	for _, candidate := range values {
		if candidate == value {
			return true
		}
	}
	return false
}

func checkYAMLSubset(file string, n *yaml.Node, diagnostics *[]Diagnostic) {
	if n == nil {
		return
	}
	if n.Kind == yaml.AliasNode || n.Alias != nil || n.Anchor != "" {
		addNodeDiagnostic(file, n, "parse.alias", "YAML anchors and aliases are not supported", nil, diagnostics)
	}
	if n.Style&yaml.TaggedStyle != 0 {
		addNodeDiagnostic(file, n, "parse.tag", "explicit YAML tags are not supported", nil, diagnostics)
	} else if strings.HasPrefix(n.Tag, "!") && !strings.HasPrefix(n.Tag, "!!") {
		addNodeDiagnostic(file, n, "parse.tag", "custom YAML tags are not supported", nil, diagnostics)
	}
	if n.Tag == "!!timestamp" {
		addNodeDiagnostic(file, n, "parse.timestamp", "YAML timestamps are not supported; quote this value as text", nil, diagnostics)
	}
	if n.Kind == yaml.MappingNode {
		seen := make(map[string]Source)
		for i := 0; i+1 < len(n.Content); i += 2 {
			key := n.Content[i]
			if key.Value == "<<" {
				addNodeDiagnostic(file, key, "parse.merge", "YAML merge keys are not supported", nil, diagnostics)
			}
			if first, ok := seen[key.Value]; ok {
				addNodeDiagnostic(file, key, "parse.duplicate_key", fmt.Sprintf("duplicate key %q; first declared at %d:%d", key.Value, first.Line, first.Column), nil, diagnostics)
			} else {
				seen[key.Value] = source(file, key)
			}
		}
	}
	for _, child := range n.Content {
		checkYAMLSubset(file, child, diagnostics)
	}
}

func mapping(file string, n *yaml.Node, allowed []string, diagnostics *[]Diagnostic) map[string]*yaml.Node {
	out := rawMapping(file, n, diagnostics)
	allowedSet := make(map[string]struct{}, len(allowed))
	for _, key := range allowed {
		allowedSet[key] = struct{}{}
	}
	for key, value := range out {
		if _, ok := allowedSet[key]; !ok {
			addNodeDiagnostic(file, mappingKeyNode(n, key), "schema.unknown_field", fmt.Sprintf("unknown field %q", key), nearest(key, allowed), diagnostics)
			delete(out, key)
			_ = value
		}
	}
	return out
}

func rawMapping(file string, n *yaml.Node, diagnostics *[]Diagnostic) map[string]*yaml.Node {
	out := make(map[string]*yaml.Node)
	if n == nil {
		return out
	}
	if n.Kind != yaml.MappingNode {
		addNodeDiagnostic(file, n, "schema.type", "expected a mapping", nil, diagnostics)
		return out
	}
	for i := 0; i+1 < len(n.Content); i += 2 {
		key := n.Content[i]
		if key.Kind != yaml.ScalarNode || key.Tag != "!!str" {
			addNodeDiagnostic(file, key, "schema.key", "mapping keys must be strings", nil, diagnostics)
			continue
		}
		out[key.Value] = n.Content[i+1]
	}
	return out
}

func valueMap(file string, n *yaml.Node, diagnostics *[]Diagnostic) map[string]any {
	out := make(map[string]any)
	for key, value := range rawMapping(file, n, diagnostics) {
		out[key] = valueOf(file, value, diagnostics)
	}
	return out
}

func stringMap(file string, n *yaml.Node, diagnostics *[]Diagnostic) map[string]string {
	out := make(map[string]string)
	for key, value := range rawMapping(file, n, diagnostics) {
		if value.Kind != yaml.ScalarNode || value.Tag != "!!str" {
			addNodeDiagnostic(file, value, "schema.type", "import paths must be strings", nil, diagnostics)
			continue
		}
		out[key] = value.Value
	}
	return out
}

func valueOf(file string, n *yaml.Node, diagnostics *[]Diagnostic) any {
	if n == nil {
		return nil
	}
	switch n.Kind {
	case yaml.MappingNode:
		return valueMap(file, n, diagnostics)
	case yaml.SequenceNode:
		out := make([]any, 0, len(n.Content))
		for _, child := range n.Content {
			out = append(out, valueOf(file, child, diagnostics))
		}
		return out
	case yaml.ScalarNode:
		return scalarValue(file, n, diagnostics)
	default:
		addNodeDiagnostic(file, n, "schema.value", "unsupported YAML value", nil, diagnostics)
		return nil
	}
}

func scalarValue(file string, n *yaml.Node, diagnostics *[]Diagnostic) any {
	if n == nil {
		return nil
	}
	switch n.Tag {
	case "!!str":
		return n.Value
	case "!!null":
		return nil
	case "!!bool":
		if n.Value != "true" && n.Value != "false" {
			addNodeDiagnostic(file, n, "schema.boolean", "booleans must be written as true or false", nil, diagnostics)
			return false
		}
		return n.Value == "true"
	case "!!int":
		v, err := strconv.ParseInt(n.Value, 10, 64)
		if err != nil {
			addNodeDiagnostic(file, n, "schema.number", "invalid integer", nil, diagnostics)
			return int64(0)
		}
		return v
	case "!!float":
		v, err := strconv.ParseFloat(n.Value, 64)
		if err != nil || math.IsInf(v, 0) || math.IsNaN(v) {
			addNodeDiagnostic(file, n, "schema.number", "numbers must be finite", nil, diagnostics)
			return float64(0)
		}
		return v
	default:
		addNodeDiagnostic(file, n, "schema.scalar", fmt.Sprintf("unsupported scalar type %s", n.Tag), nil, diagnostics)
		return nil
	}
}

func stringField(file string, m map[string]*yaml.Node, key string, required bool, diagnostics *[]Diagnostic) string {
	n := m[key]
	if n == nil {
		if required {
			*diagnostics = append(*diagnostics, diagnostic(file, "schema.required", fmt.Sprintf("missing required field %q", key), 1, 1))
		}
		return ""
	}
	if n.Kind != yaml.ScalarNode || n.Tag != "!!str" {
		addNodeDiagnostic(file, n, "schema.type", fmt.Sprintf("%s must be a string", key), nil, diagnostics)
		return ""
	}
	return n.Value
}

func integerField(file string, m map[string]*yaml.Node, key string, required bool, diagnostics *[]Diagnostic) int {
	n := m[key]
	if n == nil {
		if required {
			*diagnostics = append(*diagnostics, diagnostic(file, "schema.required", fmt.Sprintf("missing required field %q", key), 1, 1))
		}
		return 0
	}
	return integerNode(file, n, diagnostics)
}

func integerNode(file string, n *yaml.Node, diagnostics *[]Diagnostic) int {
	if n.Kind != yaml.ScalarNode || n.Tag != "!!int" {
		addNodeDiagnostic(file, n, "schema.type", "expected an integer", nil, diagnostics)
		return 0
	}
	v, err := strconv.Atoi(n.Value)
	if err != nil {
		addNodeDiagnostic(file, n, "schema.number", "invalid integer", nil, diagnostics)
		return 0
	}
	return v
}

func boolField(file string, m map[string]*yaml.Node, key string, required bool, diagnostics *[]Diagnostic) bool {
	n := m[key]
	if n == nil {
		if required {
			*diagnostics = append(*diagnostics, diagnostic(file, "schema.required", fmt.Sprintf("missing required field %q", key), 1, 1))
		}
		return false
	}
	return boolNode(file, n, diagnostics)
}

func boolNode(file string, n *yaml.Node, diagnostics *[]Diagnostic) bool {
	if n.Kind != yaml.ScalarNode || n.Tag != "!!bool" || (n.Value != "true" && n.Value != "false") {
		addNodeDiagnostic(file, n, "schema.type", "expected true or false", nil, diagnostics)
		return false
	}
	return n.Value == "true"
}

func stringSlice(file string, n *yaml.Node, diagnostics *[]Diagnostic) []string {
	if n.Kind != yaml.SequenceNode {
		addNodeDiagnostic(file, n, "schema.type", "expected a list of strings", nil, diagnostics)
		return nil
	}
	out := make([]string, 0, len(n.Content))
	for _, child := range n.Content {
		if child.Kind != yaml.ScalarNode || child.Tag != "!!str" {
			addNodeDiagnostic(file, child, "schema.type", "expected a string", nil, diagnostics)
			continue
		}
		out = append(out, child.Value)
	}
	return out
}

func source(file string, n *yaml.Node) Source {
	return Source{File: file, Line: nodeLine(n), Column: nodeColumn(n)}
}

func diagnostic(file, code, message string, line, column int) Diagnostic {
	return Diagnostic{Severity: "error", Code: code, Message: message, File: file, Line: line, Column: column}
}

func addNodeDiagnostic(file string, n *yaml.Node, code, message string, suggestions []string, diagnostics *[]Diagnostic) {
	d := diagnostic(file, code, message, nodeLine(n), nodeColumn(n))
	d.Suggestions = suggestions
	*diagnostics = append(*diagnostics, d)
}

func nodeLine(n *yaml.Node) int {
	if n == nil || n.Line == 0 {
		return 1
	}
	if n.Kind == yaml.DocumentNode && len(n.Content) != 0 {
		return nodeLine(n.Content[0])
	}
	return n.Line
}

func nodeColumn(n *yaml.Node) int {
	if n == nil || n.Column == 0 {
		return 1
	}
	if n.Kind == yaml.DocumentNode && len(n.Content) != 0 {
		return nodeColumn(n.Content[0])
	}
	return n.Column
}

func mappingKeyNode(n *yaml.Node, key string) *yaml.Node {
	if n == nil {
		return nil
	}
	for i := 0; i+1 < len(n.Content); i += 2 {
		if n.Content[i].Value == key {
			return n.Content[i]
		}
	}
	return n
}

func nearest(value string, choices []string) []string {
	choices = append([]string(nil), choices...)
	sort.Strings(choices)
	bestDistance := math.MaxInt
	var best string
	for _, choice := range choices {
		d := editDistance(value, choice)
		if d < bestDistance {
			bestDistance, best = d, choice
		}
	}
	if best == "" || bestDistance > 3 {
		return nil
	}
	return []string{best}
}

func editDistance(a, b string) int {
	prev := make([]int, len(b)+1)
	for j := range prev {
		prev[j] = j
	}
	for i, ar := range a {
		current := make([]int, len(b)+1)
		current[0] = i + 1
		for j, br := range b {
			cost := 0
			if ar != br {
				cost = 1
			}
			current[j+1] = min(current[j]+1, prev[j+1]+1, prev[j]+cost)
		}
		prev = current
	}
	return prev[len(b)]
}

func mapKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for key := range m {
		out = append(out, key)
	}
	return out
}
