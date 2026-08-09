# Gora v1 CLI and Studio

This document is normative for the v1 command-line and Studio behavior.

## Exit status

- `0`: success
- `1`: the document or one of its dependencies is invalid
- `2`: usage, filesystem, missing-session, or runtime failure

## `gora validate`

```text
gora validate <file> [--root <dir>] [--format text|json]
```

Validation accepts app, component, and token documents and never opens Studio.
The default root is the current directory. Text diagnostics use
`file:line:column` locations.

JSON output has this stable envelope:

```json
{
  "schema_version": 1,
  "valid": false,
  "diagnostics": [
    {
      "severity": "error",
      "code": "schema.version",
      "message": "unsupported Gora format version 2",
      "file": "/project/app.gora",
      "line": 1,
      "column": 7,
      "node_name": "optional",
      "path": "optional",
      "suggestions": ["optional"]
    }
  ]
}
```

Diagnostics are sorted by file, line, column, and code.

## `gora run`

```text
gora run <file> [--root <dir>] [--studio|--headless] [--automation]
```

Apps and components run in one of three mutually exclusive hosts. Token modules
are validation-only.

- With no host flag, Gora opens a content-only native app window. The document
  viewport follows the logical size of that window, and document scrolling,
  buttons, links, named-screen navigation, keyboard focus, state, capture, and
  live reload remain active.
- `--studio` opens the authoring Studio described below.
- `--headless` runs the same live runtime and owner-only session service without
  creating a visible platform window. It watches dependencies and retains the
  current viewport, selection, scroll offsets, and interaction state for
  automation and future MCP integration.
- `--automation` enables the versioned owner-only host bridge for a visible app
  or Studio session. It cannot be combined with `--headless`; headless views
  already provide an MCP-owned automation backend.

The flags `--studio` and `--headless` cannot be combined. Session identity
includes the canonical root, document, and host mode, so app, Studio, and
headless sessions may coexist. Repeating `run` in the same mode focuses the
existing visible window or reuses the existing headless process.

A plain app run rejects an initially invalid document with status `1` and does
not open a window. Studio and headless runs remain alive for an initially
invalid document: Studio shows diagnostics, while headless exposes the live
invalid session for repair through file watching. Both retain a later
last-good frame if a reload becomes invalid.

## Content-only app window

The default host contains only document pixels. It has no toolbar,
checkerboard, inspection highlight, guides, capture field, or diagnostic
chrome. Its initial logical size is the document viewport. Native resizing
updates the document viewport and responsive result. `gora render --from app`
captures its current live selection, state, scroll offsets, and resized
viewport without window chrome.

## Studio

Studio owns:

- the selected app screen or component fixture;
- an explicit integer logical viewport;
- visual zoom, which never changes logical layout;
- named scroll offsets;
- the last-good resolved document;
- current diagnostics and inspection selection;
- independent local state for every screen, fixture, and named component instance;
- bounded named-screen history and per-entry named scroll offsets;
- transient semantic-control hover, press, logical focus, select-popup, and
  active-option state.

The toolbar cycles screens or fixtures, edits viewport width and height as one
combined control (press Return to apply), changes zoom with compact minus and
plus buttons, toggles inspect mode, and captures to an explicitly entered new
PNG path. `Reset state` appears only for a selected context that declares state
and restores that context plus its component instances. The center canvas is
independent from the host window size. On macOS,
hold Command while scrolling with two fingers over the canvas for smooth zoom;
the zoom gesture exclusively owns its trackpad momentum so it cannot move the
preview document. Unmodified two-finger movement scrolls the matching document
axis directly: up/down targets vertical scroll nodes. Left/right pans an
overflowing zoomed Studio canvas; when the canvas fits, it targets horizontal
document scroll nodes instead. Retained Gio operations keep scroll-only frames
to clip, translation, and cached replay work.

Document semantic controls receive topmost clipped pointer hit testing. A
press captures its pointer and activates only when released inside the same
enabled target; sliders continuously update while retaining capture. Tab and
Shift-Tab traverse visible enabled controls in expanded source order. Radio
groups and tabs use roving arrow/Home/End focus, selects own an explicit
open/active/commit/cancel cycle, and sliders/steppers support Arrow, Page,
Home, and End range changes. Inspect mode exclusively owns clicks and clears
document transient interaction.

Document `text_field` and `text_area` controls use the same semantic focus
order. Pointer press/drag changes selection; repeated clicks select words and
lines. Platform select-all, copy, cut, paste, undo, and redo shortcuts are
supported, and Gio IME edit, selection, and composition events update the
renderer-neutral draft. Valid drafts publish typed or normalized state
immediately; invalid drafts stay visible without replacing the last valid
value. Form submit/reset buttons and Enter conventions use the local form
transaction described in the format contract. Captures and inspection include
the current draft, validation state, selection, caret, and composition state.

File watching is directory-based and debounced. The watch set includes the
entry document, imports, token modules, fonts, and images. A valid reload swaps
atomically. An invalid reload keeps the last-good frame visible and replaces
the diagnostics. Named scroll nodes preserve their offsets when they still
exist; unnamed scroll nodes reset. Valid reloads reconcile the bounded
navigation history, remove deleted screens, and prune missing named scrolls.
Invalid reloads preserve the complete last-good navigation, state, and scroll
state.

Inspect mode reports the deepest painted node, including its type, authored
name when present, logical bounds, effective props, source location, clip, and
component-instance breadcrumb. Controls also report role, label, bound value,
checked/selected/expanded state, numeric range, orientation,
enabled/current/hovered/pressed/focused state, lexical scope, semantic
operations, authored effects, and current scoped values. Studio consumes the
same canonical runtime tree as
pointer routing, keyboard traversal, live inspection, and future accessibility
adapters. Inspection overlays are Studio-only.

## `gora render`

```text
gora render <file> --output <new.png> \
  [--scale <positive-integer>] [--root <dir>] \
  [--from app|studio|headless]
```

Rendering requires the matching live host session. `--from` defaults to `app`.
It captures that session's current screen or fixture, viewport, responsive
result, scroll offsets, persistent values, and current control interaction and
open-popup visuals. Studio zoom, checkerboards, inspection highlighting, guides,
diagnostics, native window chrome, and Studio chrome are excluded.

The command refuses an existing output file. If the current source is invalid
but the selected host has a last-good frame, the command captures that frame and
prints a warning to standard error.

## `gora inspect`

```text
gora inspect <file> [--root <dir>] [--from app|studio|headless]
```

Inspection requires the matching live host and emits deterministic JSON only.
The version-1 envelope includes the document and host mode, validity and current
diagnostics, runtime revision, current screen or fixture, available selections,
viewport, Back/Forward availability, and one canonical resolved root tree.

Runtime nodes include stable semantic IDs for named controls, authored and
semantic metadata, visibility and viewport state, logical bounds and clips,
effective props and placement, source and component breadcrumb, scoped values,
focus and paint order, supported operations, normalized authored effects, and
source-ordered children. Hidden nodes remain present with null geometry but are
excluded from paint, hit testing, and focus order.

`--from` defaults to `app`. An initially invalid live session returns a valid
JSON envelope with a null root and exit status `1`. If a later invalid source
has a last-good tree, the command returns that tree with `valid: false`, current
diagnostics, a warning on standard error, and exit status `0`.

## `gora mcp`

```text
gora mcp [--listen 127.0.0.1:8787] [--automation]
```

This starts one persistent, project-oriented MCP 2026-07-28 server using
localhost Streamable HTTP. It starts without an entry document. Clients first
open a canonical project root, then open one or more independent document views
inside it. Opening the same root or entry again reuses its existing project or
view state. Only `127.0.0.1` listeners are accepted.

The MCP server is distinct from `gora run --headless`: the latter owns one
document and the existing Unix-socket CLI session, while `gora mcp` owns
multiple explicit projects and views for agent inspection, control, capture,
and structured document editing. Add `--automation` to register the
renderer-neutral `gora_dispatch_input` batch tool, the
`gora_wait_for_view`/automation resource, bounded scroll/edit event tracing,
view-local clipboard tools, deterministic view-clock controls, finite
`gora_assert_view` checks, overlay-free `gora_compare_capture` controls, and
view-local test-overlay/reload controls (`gora_apply_test_overlay`,
`gora_clear_test_overlay`, `gora_inject_reload_events`, and finite counted
fault configuration);
without it, ordinary project/view tools remain unchanged. With a matching
automation-enabled app or Studio view, the same MCP controls operate through
the real Gio host event loop; native OS injection remains deferred. See
[MCP server](mcp.md).

## Session security

The local Unix socket is keyed by the canonical root, document, and host mode.
Its parent directory is owner-only (`0700`) and its socket is owner-only
(`0600`). Stale sockets are removed after connection failure and on clean
shutdown.

When launched with `--automation`, app and Studio hosts publish their observed
window configuration and Studio state through the MCP host resource. MCP
window and Studio mutations are reduced on the same Gio event loop as toolbar
actions, and responses wait for the corresponding stable frame. A normal
headless view has no host resource and rejects host-window operations.
