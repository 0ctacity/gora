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

Colors are `#RRGGBB`, `#RRGGBBAA`, or `transparent`. Dimensions are finite
logical-unit numbers.

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

`name` is optional and unique within its source document. Runtime handles are
generated and never serialized.

`responsive` is keyed by a breakpoint declared in the same document. It may
override only `props`, `place`, and `visible`; it cannot change type, name, or
children.

Insets always name all edges:

```yaml
padding: { top: 8, right: 12, bottom: 8, left: 12 }
```

Radius is either one value or four named corners. Numbers are logical units.
Sizing accepts fixed numbers, `auto`, `fill`, min/max constraints, stack child
grow weights, and fractional grid tracks.

## Runtime vocabulary

### `stack`

Horizontal or vertical linear layout. Props: `direction`, explicit `padding`,
`gap`, alignment, and distribution. Child `place.grow` is a non-negative
weight. V1 does not wrap.

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

Interaction state and actions, inputs, navigation, semantic control libraries,
safe areas, SVG/vector paths, animation, remote assets, accessibility
integration, translation, project manifests, formatting, document mutation,
MCP, code generation, and a production runtime are not part of v1.
