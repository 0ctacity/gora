# Gora MCP Server

Gora exposes a persistent local MCP server for coding agents:

```sh
gora mcp
# custom loopback port
gora mcp --listen 127.0.0.1:9191
```

The endpoint is `http://127.0.0.1:8787/mcp` and uses stateless Streamable HTTP
with MCP `2026-07-28`. Gora pins the official Go SDK at `v1.7.0`, accepts only
IPv4 loopback listeners, and enables Go's Host/Origin request protection. V1
has no authentication; any local client able to connect can control every open
project and view.

An MCP client configuration points its Streamable HTTP transport at that URL.
The exact configuration key varies by client; the transport type is
`streamable-http` and the URL ends in `/mcp`.

## Projects and views

Call `gora_open_project` with an absolute existing directory. Its canonical
path is the project identity, so symlink-equivalent roots reuse one opaque
`project_id`. Projects remain alive across client disconnects until
`gora_close_project` or server shutdown.

Call `gora_open_view` with that project ID and a contained `.gora` entry. The
same canonical entry reuses one `view_id`. Each app or component view owns its
viewport, selection, navigation, state, scroll offsets, tree, and last-good
frame. Token views support sources, diagnostics, and editing, but reject
runtime operations. A project shares known sources, dependency watching, and
asset work across its views; it does not recursively discover the root.

## Tools

- Lifecycle: `gora_open_project`, `gora_list_projects`,
  `gora_close_project`, `gora_open_view`, `gora_list_views`, and
  `gora_close_view`.
- Runtime: `gora_set_viewport`, `gora_select`, `gora_activate`, `gora_scroll`,
  `gora_set_state`, `gora_reset_state`, `gora_set_control_value`, and
  `gora_capture`. Runtime calls require matching project and view IDs.
- Automation (enabled only with `gora mcp --automation`):
  `gora_wait_for_view`, `gora_dispatch_input`, `gora_configure_event_trace`,
  and `gora_clear_event_trace`. Dispatch accepts one fully validated ordered
  batch of renderer-neutral pointer, keyboard, or wheel/trackpad scroll events
  and returns one result per event with canonical target, focus/capture,
  consumed state, effects, scroll-axis routing, and resulting revisions. A malformed event
  anywhere in a batch is rejected before the first event is delivered.
  `wait` may be `none`, `published`, or `idle`; the default timeout is 5s and
  the accepted range is 1–60000ms. Secondary/middle/none pointer buttons are
  reported unconsumed rather than converted into primary activation. Text
  insertion and OS-level input injection remain outside this phase.
- Editing: `gora_apply_document_changes` creates, replaces, or applies ordered
  RFC 6902-style `add`, `replace`, and `remove` operations to structured Gora
  documents. Existing files require their current SHA-256 revision.

Capture returns inline PNG image content. An optional output path must be a new
`.png` beneath the project root. Document changes are validated as an in-memory
multi-file candidate before staged writes. Changed files use deterministic
two-space Gora YAML; comments and prior manual formatting in those files are
discarded. Unchanged files remain byte-identical.

`gora_set_control_value` accepts a semantic control ID and typed value, so an
agent can operate fields, toggles, choice groups, selects, sliders, and steppers
without discovering lexical scope IDs. Choice values must name an enabled
option. Numeric values use the bound state's clamp-and-step normalization. The
result includes the normalized value and updated view/tree resource links.

Fields additionally expose `gora_set_field_draft`. Valid drafts publish their
typed or normalized value immediately; invalid or incomplete drafts remain
visible while preserving the last valid bound value. `gora_submit_form`
validates and atomically synchronizes a form's enabled field drafts before
running authored submit effects; `gora_reset_form` restores only bindings
represented by that form.
All three require `project_id`, `view_id`, and a stable semantic field/form ID.

## Renderer-neutral input automation

Start the server with the feature gate when an agent needs raw semantic input:

```sh
gora mcp --automation
```

Open a project and view as usual, then send a batch such as:

```json
{
  "project_id": "project-id",
  "view_id": "view-id",
  "wait": "published",
  "events": [
    {"type":"pointer","kind":"press","pointer_id":1,"source":"mouse","x":24,"y":20,"button":"primary","time_ms":1},
    {"type":"pointer","kind":"release","pointer_id":1,"source":"mouse","x":24,"y":20,"button":"primary","time_ms":2}
  ]
}
```

Pointer coordinates are logical view coordinates and may be outside the
viewport for release/cancel tests. Pointer IDs are positive and stable through
a press sequence; a release must use the same source and button as its press,
while cancel releases the sequence without activation. Pointer kinds are `enter`, `leave`, `move`, `press`,
`release`, and `cancel`; sources are `mouse` or `touch`; buttons are
`primary`, `secondary`, `middle`, or `none`. Keyboard kinds are `down` and
`up`, with portable names `Tab`, `Enter`, `Space`, `Escape`, directional,
page, Home/End, Backspace/Delete, `A`–`Z`, and `0`–`9`. Modifiers are a unique
subset of `shift`, `control`, `command`, and `option`; event times are finite,
non-negative, and monotonic within a batch.

Scroll events use `source` `wheel` or `trackpad`, `units` `logical` or
`physical_pixels`, independent finite `delta_x`/`delta_y`, `phase` `begin`,
`update`, `end`, or `cancel`, and `momentum` `none`, `begin`, `update`, or
`end`. Physical deltas are converted once using the published view metric.
Zero/zero deltas are valid phase-only transitions. The pointer location selects
the deepest topmost clipped scroll candidate; each axis reports ordered
consumers, consumed amount, residual, containment, and final offsets. Command-
modified scroll is reported unconsumed for ordinary headless views.

Each app/component view owns one bounded automation router. It uses the same
canonical clipped hit testing, pointer capture, focus traversal, control
activation, scroll metrics, and state/value reducers as the runtime. A valid
event publishes its resulting frame before the response and automation
resource notification; `published`/`idle` waits reuse the frame barrier and do
not synthesize an extra frame. Closing or reloading a view clears stale
pointer and keyboard ownership. Token views reject automation tools and
resources, and project/view mismatches are rejected before delivery.

Event tracing is opt-in and view-local. Configure a fixed ring with capacity
`1..4096` (default `512`) through `gora_configure_event_trace`; enabling starts
a generation and `gora_clear_event_trace` removes entries without changing it.
Read `gora://project/{project-id}/views/{view-id}/automation/trace` for immutable
bounded entries covering acceptance, conversion, final-paint candidates,
ownership/capture, per-axis residuals, mutation, invalidation, and publication
or no-frame reasons. Trace updates use the same revisioned resource notifications
as the automation snapshot.

## Resources and subscriptions

The server exposes `gora://projects`, project manifests and diagnostics,
structured documents and exact YAML sources, view metadata and trees, plus a
parameterized semantic-node resource. Source IDs are percent-encoded
root-relative paths.

MCP `subscriptions/listen` receives SDK resource-list changes and revisioned
resource updates after view mutations, edits, dependency reloads, and
project/view lifecycle changes. Filesystem events are directory-watched and
debounced; one project watcher rebuilds affected open views while invalid
external edits retain their last-good runtime snapshots.

## Typical agent workflow

1. Open or reuse the project root.
2. Create initial documents through `gora_apply_document_changes`, if needed.
3. Open an app or component view.
4. Read its tree and diagnostics resources.
5. Set the viewport, activate semantic controls, adjust state or scrolling, and
   capture the visible result.
6. Patch documents with the latest source revisions and re-read affected
   resources after the coalesced update.
