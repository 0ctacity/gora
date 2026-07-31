# Gora

Gora is a cross-platform preview runtime for strict, local `.gora` UI documents.
It turns a small YAML document into a native Studio canvas with live reload,
responsive breakpoints, inspection, scrolling, deterministic local state,
semantic buttons, and deterministic PNG capture.

V1 is deliberately a preview tool. It does not generate production UI code and
does not include navigation, text inputs/forms, animation, remote assets, SVG,
or MCP.

## Install

Requirements:

- Go 1.26.5
- The platform dependencies required by [Gio](https://gioui.org/doc/install)

Build the single binary:

```sh
go build -o gora ./cmd/gora
```

## Try it

Validate an example:

```sh
./gora validate examples/dashboard/app.gora --root examples/dashboard
```

Open it in Studio:

```sh
./gora run examples/dashboard/app.gora --root examples/dashboard
```

To exercise percentage sizing, aspect ratios, intrinsic containers, and
wrapping stacks, open the web-layout conformance example from the repository
root:

```sh
./gora run examples/web-layout/app.gora --root .
```

To exercise local state, component-instance isolation, pointer and keyboard
activation, interaction variants, and Reset state:

```sh
./gora run examples/interactivity/app.gora --root .
```

While that Studio session is open, capture its current screen, viewport,
responsive state, and scroll position:

```sh
./gora render examples/dashboard/app.gora \
  --root examples/dashboard \
  --output /tmp/gora-dashboard.png \
  --scale 2
```

The output path must not already exist.

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
