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
- Editing: `gora_apply_document_changes` creates, replaces, or applies ordered
  RFC 6902-style `add`, `replace`, and `remove` operations to structured Gora
  documents. Existing files require their current SHA-256 revision.

Capture returns inline PNG image content. An optional output path must be a new
`.png` beneath the project root. Document changes are validated as an in-memory
multi-file candidate before staged writes. Changed files use deterministic
two-space Gora YAML; comments and prior manual formatting in those files are
discarded. Unchanged files remain byte-identical.

`gora_set_control_value` accepts a semantic control ID and typed value, so an
agent can operate toggles, choice groups, selects, sliders, and steppers
without discovering lexical scope IDs. Choice values must name an enabled
option. Numeric values use the bound state's clamp-and-step normalization. The
result includes the normalized value and updated view/tree resource links.

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
