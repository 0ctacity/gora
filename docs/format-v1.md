# Gora Format v1

This document is the normative `.gora` format contract.

## YAML subset

Every file has the `.gora` extension, `gora: 1`, and one `kind`: `app`,
`component`, or `tokens`.

Accepted values are mappings, lists, strings, finite numbers, lowercase
`true`/`false`, and null. Gora rejects:

- anchors, aliases, merge keys, and custom tags;
- multiple YAML documents;
- timestamps and other implicit YAML-specific scalar types;
- duplicate mapping keys;
- unknown fields and unsupported format versions.

References are explicit single-field objects:

```yaml
color: { ref: theme.color.primary }
text: { ref: parameter.title }
content: { ref: state.status }
```

## Imports and containment

Imports have separate namespaces:

```yaml
imports:
  components:
    card: components/card.gora
  tokens:
    theme: theme.gora
```

Paths resolve relative to the importing file. The entry, imports, fonts, and
images must remain inside the canonical current-directory or `--root`
boundary, including after symlink resolution. Import cycles are invalid.

## Applications

```yaml
gora: 1
kind: app
imports: {}
viewport:
  width: 1280
  height: 800
  background: transparent
breakpoints:
  compact: { max_width: 699 }
  wide: { min_width: 700 }
entry: dashboard
screens:
  dashboard: { type: surface }
```

`viewport` is required. Width and height are positive logical-unit integers.
`entry` names an existing screen. Breakpoint ranges use inclusive integer
widths and may not overlap.

## Components

```yaml
gora: 1
kind: component
viewport: { width: 360, height: 240 }
parameters:
  title: { type: text, required: true }
  emphasis: { type: enum, values: [normal, strong], default: normal }
slots:
  default: { required: false }
previews:
  default:
    parameters: { title: Preview }
root:
  type: stack
```

Parameter types are `text`, `string`, `number`, `boolean`, `color`,
`dimension`, and `enum`. Enum parameters require `values`.

A component instance is explicit:

```yaml
type: instance
name: revenue-card
props:
  component: card
  parameters: { title: Revenue }
children:
  - type: slot_content
    props: { slot: default }
    children:
      - { type: text, props: { text: Details } }
```

The component template receives content through a `slot` node. Required
parameters and slots must be supplied. Unknown component aliases, parameters,
and slots are errors.

Stateful component instances require a unique authored `name`. Template nodes
use that instance's state scope; caller-authored `slot_content` keeps the
caller's state scope.

## Local state and variants

Apps and components may declare ephemeral, document-owned state:

```yaml
state:
  expanded: { type: boolean, default: false }
  seats: { type: number, default: 3, min: 1, max: 10, step: 1 }
  status: { type: text, default: Ready }
  plan: { type: enum, values: [monthly, annual], default: monthly }
```

Types are `boolean`, `number`, `text`, and `enum`; a correctly typed default is
required. Component fixtures may set typed initial values under
`previews.<name>.state`. Each app screen and each named stateful component
instance owns an independent scope. `{ ref: state.name }` reads the nearest
lexical scope. Direct state references in text/content format booleans as
`true`/`false`, numbers with the shortest finite decimal representation, and
text/enums unchanged.

Number state may declare finite `min`, `max`, and positive `step`. Bounds may
be omitted for steppers but are required by sliders. All writes clamp to the
domain and snap to the nearest step anchored at `min`, or zero without a
minimum; exact ties round toward positive infinity.

Any node may declare source-ordered persistent state variants:

```yaml
variants:
  - when: { state: expanded, equals: true }
    props: { height: 240 }
    visible: true
```

Conditions use exactly one of `equals`, `not_equals`, `less_than`,
`less_than_or_equal`, `greater_than`, or `greater_than_or_equal`. Equality is
same-type and ordering is numeric. Variants apply after responsive overrides;
later matching values win. They may override props, placement, and visibility,
but never node structure.

## Tokens

Token modules expose typed maps:

```yaml
gora: 1
kind: tokens
tokens:
  color:
    primary: "#635BFF"
    clear: transparent
  dimension:
    space_md: 16
    half: { percent: 50 }
  font_face:
    ui: { src: assets/StudioSans.ttf }
  text_style:
    heading: { font: ui, size: 28, weight: 700, line_height: 34 }
  shadow:
    card: { x: 0, y: 8, blur: 28, color: "#1B20301A" }
  linear_gradient:
    hero:
      angle: 135
      stops:
        - { offset: 0, color: "#635BFF" }
        - { offset: 1, color: "#9B8CFF" }
```

Colors are `#RRGGBB`, `#RRGGBBAA`, or `transparent`. Dimensions are either
finite non-negative logical-unit numbers or exact percentage objects such as
`{ percent: 50 }`.

## Nodes

Every authored node uses the same envelope:

```yaml
type: surface
name: optional-unique-readable-name
props: {}
place: {}
responsive: {}
children: []
```

`name` is optional and unique within its source document, except that buttons
and links require one. Runtime handles are generated and never serialized.

`responsive` is keyed by a breakpoint declared in the same document. It may
override only `props`, `place`, and `visible`; it cannot change type, name, or
children.

Insets always name all edges:

```yaml
padding: { top: 8, right: 12, bottom: 8, left: 12 }
```

Radius is either one value or four named corners. Numbers are logical units.

Sizing accepts fixed numbers, `auto`, `fill`, and exact percentage objects:

```yaml
width: { percent: 50 }
min_width: { percent: 25 }
aspect_ratio: { width: 16, height: 9 }
```

Percentages are finite and non-negative; values above 100 intentionally
overflow. They resolve against the same-axis size of the immediate parent's
inner content box, after padding and before child gaps. Percentages are valid
for width, height, all min/max constraints, stack `place.basis`, dimension
parameters, and dimension tokens.

Both aspect-ratio members must be positive finite numbers. When exactly one
axis is definite, the ratio derives the other axis. Two definite axes win. If
both axes are automatic, intrinsic size wins and the ratio does not invent a
fill size. Min/max constraints apply after ratio derivation.

## Runtime vocabulary

### `stack`

Horizontal or vertical linear layout. Props are `direction`, explicit
`padding`, `gap`, `row_gap`, `column_gap`, `wrap`, `alignment`, and
`distribution`. `wrap` defaults to false. Directional gaps override `gap` on
their axes.

Each child may set `place.basis`, `place.grow`, `place.shrink`, and
`place.alignment`. Basis is `auto`, a non-negative logical dimension, a
percentage, or a compatible reference; `fill` is not a valid basis. Grow and
shrink are non-negative weights and shrink defaults to zero. Unsized
zero-intrinsic and `fill` children retain implicit growth for compatibility.

The base main-axis size comes from basis, the authored main-axis dimension, or
intrinsic preferred size, in that order. Wrapped lines pack in source order.
Horizontal stacks create rows top-to-bottom; vertical stacks create columns
left-to-right. An oversized item occupies a line alone. Distribution is applied
within each line, and lines remain start-packed on the cross axis.

Child alignment accepts `start`, `center`, `end`, or `stretch` and overrides
the container alignment. Free space follows grow weights. Deficits follow
`base size × shrink weight`; min/max-constrained children freeze before the
remaining deficit is redistributed. Any unresolved deficit remains visible
overflow.

### `grid`

Explicit fixed, `auto`, and fractional row/column tracks; row and column gaps;
row-major auto-placement; explicit row/column placement; and spans.

### `overlay`

Children use alignment anchors and logical offsets. List order is paint order,
with later children on top.

### `scroll`

Exactly one child, one selected `axis`, clipping, an optional scrollbar, and an
ephemeral offset. Only named scroll nodes preserve offsets across reloads.

### `surface`

Zero or one child, padding, solid or linear-gradient background, opacity,
uniform border, radius, one shadow, and optional clipping.

### `button`

Exactly one visual child and a non-empty semantic `label` are required. A
button accepts surface box styling plus a boolean or state-referenced
`disabled` prop. Interactive descendants are invalid.

`on.activate` is an ordered list of `set`, `toggle`, `increment`, `decrement`,
and `reset` actions targeting the button's lexical state scope. Actions reduce
against a working copy and commit once, so later actions observe earlier ones.
`increment` and `decrement` default `by` to 1.

Button-only interaction variants use `when: { interaction: hovered }`,
`pressed`, `focused`, or `disabled`. They may change only `background`,
`border`, `shadow`, and `opacity`.

### `link`

A link is a surface-like semantic control with a required authored `name`, a
non-empty `label`, a named-screen `to` target, and exactly one visual child.
It shares button pointer capture, focus traversal, Enter/Space activation,
disabled behavior, and paint-only interaction variants. Interactive descendants
inside either control are invalid.

`when: { interaction: current }` is link-only and matches when `to` is the
active app screen. A component link target may come from a text parameter; its
final value is checked against the consuming app after expansion. In a
standalone component fixture, activation is a deterministic navigation no-op.

Links navigate implicitly after their authored state actions commit. Their
`on.activate` lists therefore contain state actions only.

### Semantic controls

Dedicated controls bind directly to a writable state name in their nearest
lexical scope with `props.bind`; binding references are not `{ ref: ... }`
objects. Every control root and every radio, tab, and option has a unique
authored `name` and non-empty semantic `label`.

- `toggle` and `checkbox` bind boolean state and contain one authored visual.
- `radio_group` contains one or more `radio` children and binds text, number,
  or enum state. Values are unique after reference resolution.
- `tabs` contains source-ordered `tab` children followed by one matching
  `tab_panel` per value. Inactive panels remain semantic but have null geometry.
- `select` contains one `select_trigger` and one `select_popup`; the popup
  contains named `option` children and renders in the viewport-clipped top
  layer. Selection commits explicitly and Escape, Tab, or an outside click
  cancels.
- `slider` binds bounded number state and contains one `slider_track`, optional
  `slider_fill`, and one `slider_thumb`.
- `stepper` binds number state and contains decrement, value, and increment
  parts.

Radio groups and tabs use roving focus with wrapping arrows and Home/End.
Select uses arrows, Home/End, PageUp/PageDown, Enter/Space, Escape, and Tab.
Slider and stepper use arrows, ten-step Page keys, and available Home/End
bounds. Slider pointer dragging owns its gesture until release or cancellation.

Semantic variants are `checked` for toggles/checkboxes, `selected` for radio,
tab, and option, `open` for select subtrees, and `active` for the select's
transient option. Checked/selected variants may change persistent layout;
open/active variants remain paint-only. Control visual subtrees cannot contain
nested controls, while tab panels may contain arbitrary controls.

### Navigation actions and history

Buttons may author one `navigate`, `replace`, `back`, or `forward` command in
an activation list. `navigate` and `replace` require a named-screen `to` target;
`back` and `forward` accept no operands. State actions reduce and commit first,
then navigation runs. Same-screen navigation and history-boundary operations
are successful no-ops.

Apps keep at most 100 history entries. New navigation saves the current
entry's named scroll offsets, truncates forward history, and starts the target
at the top. Back and forward restore the entry's screen and offsets. Replace
starts the replacement at the top. State is not snapshotted. Studio screen
selection is preview-only: it resets history to one entry and starts at the
top.

### `text`

Go-font fallback or a local TTF/OTF; text, size, weight, italic, color,
alignment, line height, letter spacing, wrapping, maximum lines, and
clip/ellipsis overflow.

### `image`

A local PNG, JPEG, or WebP with `contain`, `cover`, or `fill` fitting,
alignment, and opacity.

### `spacer` and `divider`

Fixed/fill sizing. Dividers add orientation, thickness, and color.

### Structural nodes

`instance` expands a component. Component templates use `slot`; instance and
preview content use `slot_content`.

## Deferred from v1

Text inputs/forms, URL/deep-link routing, safe
areas, SVG/vector paths, animation, remote assets, native accessibility-tree
integration, translation, code generation, and a production runtime are not
part of v1. Multi-select, typeahead, editable spinbuttons, mixed checkboxes,
native pickers, and press-and-hold repetition remain deferred.
