# Gora

Gora is a macOS-first preview runtime for strict, local `.gora` UI documents.
It turns a small YAML document into a native Studio canvas with live reload,
responsive breakpoints, inspection, scrolling, and deterministic PNG capture.

V1 is deliberately a preview tool. It does not generate production UI code and
does not include interaction state, navigation, inputs, animation, remote
assets, SVG, or MCP.

## Install

Requirements:

- macOS on Apple silicon
- Go 1.26.5

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

The v1 release gate is macOS arm64. Other operating systems are not release
targets.
