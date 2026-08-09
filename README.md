# Gora

Gora is a cross-platform preview runtime for strict, local `.gora` UI documents.
It turns a small YAML document into a live native app window, an optional Studio
canvas, or a headless automation session with responsive breakpoints,
scrolling, deterministic local state, semantic buttons, links, toggles,
checkboxes, radio groups, tabs, selects, sliders, steppers, text fields, text
areas, and local forms, named-screen
navigation, live semantic inspection, deterministic PNG capture, and a
project-oriented MCP control server.

V1 is deliberately a preview tool. It does not generate production UI code and
does not include URL routing, animation, remote assets, SVG, secure/password
fields, file inputs, or OS-level input injection. The optional MCP automation
gate provides deterministic renderer-neutral pointer and keyboard batches for
headless views and can attach to an automation-enabled visible app or Studio
host without creating a shadow runtime.

## Install

Requirements:

- Go 1.26.5
- The platform dependencies required by [Gio](https://gioui.org/doc/install)

Build the single binary:

```sh
go build -o gora ./cmd/gora
```

On Windows, `go build` produces `gora.exe`, and PowerShell invokes local binaries with
`.\` rather than `./`:

```powershell
go build -o gora.exe .\cmd\gora
.\gora.exe validate examples\dashboard\app.gora --root examples\dashboard
```

## Try it

Validate an example:

```sh
./gora validate examples/dashboard/app.gora --root examples/dashboard
```

Open the rendered app directly:

```sh
./gora run examples/dashboard/app.gora --root examples/dashboard
```

Open the same document in Studio:

```sh
./gora run examples/dashboard/app.gora --root examples/dashboard --studio
```

Windowed app and Studio hosts may opt into the versioned local MCP bridge:

```sh
./gora run examples/dashboard/app.gora --root examples/dashboard --automation
./gora run examples/dashboard/app.gora --root examples/dashboard --studio --automation
```

With `gora mcp --automation`, open a matching live host using
`gora_open_view` and `host_mode: "app"` or `"studio"`. Host attachment uses
the real Gio-owned runtime and published frame; it does not create a shadow
headless runtime. `--automation` is rejected with `--headless`.

Run it without a visible window for the existing live-session CLI workflow:

```sh
./gora run examples/dashboard/app.gora --root examples/dashboard --headless
```

Start the persistent project-oriented MCP server:

```sh
./gora mcp
```

It serves Streamable HTTP at `http://127.0.0.1:8787/mcp`. Agents open or reuse
projects by canonical root, then open independent app, component, or token
views inside them. See [MCP server](docs/mcp.md) for resources, tools, editing,
and client configuration.

Start it with renderer-neutral input automation enabled:

```sh
./gora mcp --automation
```

Agents can then use `gora_dispatch_input` for validated pointer, keyboard, and
wheel/trackpad event batches, `gora_wait_for_view` for publication/idle
barriers, and the opt-in bounded event trace tools/resources. Attached
app/Studio views also expose host metrics and finite window/Studio-state tools
through the same Gio event loop; native OS input injection remains deferred.

Automation batches also support focused grapheme-indexed editing, composition,
view-local clipboard copy/cut/paste, undo/redo, and deterministic frozen view
clocks (`gora_set_view_clock`, `gora_advance_view_clock`).

For deterministic acceptance checks, `gora_assert_view` evaluates finite
semantic, state, scroll, transient, and trace assertions against one published
snapshot. `gora_compare_capture` compares the overlay-free PNG with a
root-contained reference using masks, channel tolerance, and a changed-pixel
threshold; failed comparisons include an actionable diff image without
modifying the reference.

Automation can also install bounded, view-local test overlays and inject
ordered reload events (`gora_apply_test_overlay`, `gora_clear_test_overlay`,
`gora_inject_reload_events`). Overlay entries are root-contained `source`,
`bytes`, or `missing` values (maximum 256 entries, 16 MiB per entry, 64 MiB
total), identified by opaque SHA-256 revisions and never written to disk.
Finite counted read/decode, candidate, capture, delayed, and stale-revision
faults are available through the test-fault tools; the overlay resource
publishes metadata only.

To exercise percentage sizing, aspect ratios, intrinsic containers, and
wrapping stacks, open the web-layout conformance example from the repository
root:

```sh
./gora run examples/web-layout/app.gora --root .
```

To exercise local state, component-instance isolation, pointer and keyboard
activation, interaction variants, and Reset state:

```sh
./gora run examples/interactivity/app.gora --root . --studio
```

To exercise the complete semantic-control set, including popup top-layer
rendering, roving keyboard focus, slider dragging, and numeric normalization:

```sh
./gora run examples/semantic-controls/app.gora --root . --studio
```

To exercise text/number drafts, validation, IME editing, local submit/reset,
read-only and disabled fields, and component-local form bindings:

```sh
./gora run examples/forms/app.gora --root . --studio
```

While a session is open, capture its current screen, viewport, responsive
state, and scroll position. `--from` defaults to `app`:

```sh
./gora render examples/dashboard/app.gora \
  --root examples/dashboard \
  --output /tmp/gora-dashboard.png \
  --scale 2 \
  --from app
```

The output path must not already exist.

Inspect the current semantic tree of any live host as deterministic JSON:

```sh
./gora inspect examples/dashboard/app.gora \
  --root examples/dashboard \
  --from app
```

The Northstar dashboard has four real named screens. Its sidebar links update
bounded Back/Forward history while each screen keeps its own state and named
scroll offsets.

## Document shape

Every file uses `.gora`, declares `gora: 1`, and has one kind:

```yaml
gora: 1
kind: app
viewport: { width: 960, height: 640 }
entry: home
screens:
  home:
    type: surface
    props:
      background: "#F6F7FB"
    children:
      - type: text
        props: { text: Hello, size: 24, color: "#16181D" }
```

Applications may import component and token documents by explicit aliases.
All imports and local assets must resolve inside the current directory, or the
directory passed through `--root`, after symlink resolution.

See [Format v1](docs/format-v1.md) for the normative format and
[CLI and Studio](docs/cli-studio.md) for runtime behavior.

## Development

```sh
go fmt ./...
go vet ./...
go test ./...
go build ./cmd/gora
```

CI runs for every push and pull request. It verifies formatting, vetting, and
tests on macOS, and compiles the desktop binary on Linux, macOS, and Windows.

## Platform status

Gora is designed for cross-platform desktop use. macOS arm64 is currently the
manually exercised v1 release environment; Linux and Windows builds are checked
in CI while packaged releases and platform-specific guarantees are still being
established.
