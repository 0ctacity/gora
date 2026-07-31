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
gora run <file> [--root <dir>] [--studio|--headless]
```

Apps and components run in one of three mutually exclusive hosts. Token modules
are validation-only.

- With no host flag, Gora opens a content-only native app window. The document
  viewport follows the logical size of that window, and document scrolling,
  buttons, keyboard focus, state, capture, and live reload remain active.
- `--studio` opens the authoring Studio described below.
- `--headless` runs the same live runtime and owner-only session service without
  creating a visible platform window. It watches dependencies and retains the
  current viewport, selection, scroll offsets, and interaction state for
  automation and future MCP integration.

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
- transient button hover, press, and logical focus state.

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

Document buttons receive topmost clipped pointer hit testing. A press captures
its pointer and activates only when released inside the same enabled button.
Tab and Shift-Tab traverse visible enabled buttons in expanded source order;
Enter activates on key press, Space activates on key release, and Escape
cancels a keyboard press. Inspect mode exclusively owns clicks and clears
document hover, press, and focus.

File watching is directory-based and debounced. The watch set includes the
entry document, imports, token modules, fonts, and images. A valid reload swaps
atomically. An invalid reload keeps the last-good frame visible and replaces
the diagnostics. Named scroll nodes preserve their offsets when they still
exist; unnamed scroll nodes reset.

Inspect mode reports the deepest painted node, including its type, authored
name when present, logical bounds, effective props, source location, clip, and
component-instance breadcrumb. For buttons it also reports semantic label,
enabled/hovered/pressed/focused state, lexical scope, actions, and current
scoped values. Inspection overlays are Studio-only.

## `gora render`

```text
gora render <file> --output <new.png> \
  [--scale <positive-integer>] [--root <dir>] \
  [--from app|studio|headless]
```

Rendering requires the matching live host session. `--from` defaults to `app`.
It captures that session's current screen or fixture, viewport, responsive
result, scroll offsets, persistent values, and current button interaction
visuals. Studio zoom, checkerboards, inspection highlighting, guides,
diagnostics, native window chrome, and Studio chrome are excluded.

The command refuses an existing output file. If the current source is invalid
but the selected host has a last-good frame, the command captures that frame and
prints a warning to standard error.

## Session security

The local Unix socket is keyed by the canonical root, document, and host mode.
Its parent directory is owner-only (`0700`) and its socket is owner-only
(`0600`). Stale sockets are removed after connection failure and on clean
shutdown.
