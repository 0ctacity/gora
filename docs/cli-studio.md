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
gora run <file> [--root <dir>]
```

Apps and components open in the native Studio. Token modules are
validation-only. A second invocation for the same canonical root and document
focuses the existing session.

An initially invalid app or component still opens Studio with diagnostics and
no preview; the process exits with status `1` when that Studio closes. This
allows the file to be repaired through live reload without restarting Gora.

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
  [--scale <positive-integer>] [--root <dir>]
```

Rendering requires the matching live Studio session. It captures the session's
current screen or fixture, explicit viewport, responsive result, scroll
offsets, persistent values, and current button interaction visuals. Zoom,
checkerboards, inspection highlighting, guides, diagnostics, and all Studio
chrome are excluded.

The command refuses an existing output file. If the current source is invalid
but Studio has a last-good frame, the command captures that visible frame and
prints a warning to standard error.

## Session security

The local Unix socket is keyed by the canonical root and document. Its parent
directory is owner-only (`0700`) and its socket is owner-only (`0600`). Stale
sockets are removed after connection failure and on clean shutdown.
