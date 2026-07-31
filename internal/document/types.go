package document

type Kind string

const (
	KindApp       Kind = "app"
	KindComponent Kind = "component"
	KindTokens    Kind = "tokens"
)

type Source struct {
	File   string `json:"file"`
	Line   int    `json:"line"`
	Column int    `json:"column"`
}

type Diagnostic struct {
	Severity    string   `json:"severity"`
	Code        string   `json:"code"`
	Message     string   `json:"message"`
	File        string   `json:"file"`
	Line        int      `json:"line"`
	Column      int      `json:"column"`
	Path        string   `json:"path,omitempty"`
	NodeName    string   `json:"node_name,omitempty"`
	Suggestions []string `json:"suggestions,omitempty"`
}

type Document struct {
	Version     int
	Kind        Kind
	Name        string
	File        string
	Imports     Imports
	Viewport    Viewport
	Breakpoints map[string]Breakpoint
	Entry       string
	Screens     map[string]*Node
	Parameters  map[string]Parameter
	Slots       map[string]Slot
	Previews    map[string]Preview
	Root        *Node
	Tokens      map[string]map[string]any
	State       map[string]StateDeclaration
}

type Imports struct {
	Components map[string]string
	Tokens     map[string]string
}

type Viewport struct {
	Width      int
	Height     int
	Background any
}

type Breakpoint struct {
	MinWidth *int
	MaxWidth *int
	Source   Source
}

type Parameter struct {
	Type     string
	Required bool
	Default  any
	Values   []string
	Source   Source
}

type Slot struct {
	Required bool
	Source   Source
}

type Preview struct {
	Viewport   *Viewport
	Parameters map[string]any
	Children   []*Node
	Source     Source
	State      map[string]any
}

type StateDeclaration struct {
	Type    string
	Default any
	Values  []string
	Source  Source
}

type Action struct {
	Action string
	State  string
	Value  any
	By     any
	Source Source
}

type Events struct {
	Activate []Action
}

type Condition struct {
	State       string
	Interaction string
	Operator    string
	Value       any
	Source      Source
}

type Variant struct {
	When    Condition
	Props   map[string]any
	Place   map[string]any
	Visible *bool
	Source  Source
}

type Node struct {
	Type       string
	Name       string
	Props      map[string]any
	Place      map[string]any
	Responsive map[string]Responsive
	Children   []*Node
	Source     Source
	On         Events
	Variants   []Variant
}

type Responsive struct {
	Visible *bool
	Props   map[string]any
	Place   map[string]any
	Source  Source
}
