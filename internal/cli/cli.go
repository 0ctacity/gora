package cli

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"time"

	"gora/internal/document"
	"gora/internal/project"
	"gora/internal/render"
	"gora/internal/session"
)

const (
	ExitSuccess    = 0
	ExitValidation = 1
	ExitFailure    = 2
)

type LaunchMode string

const (
	LaunchApp      LaunchMode = "app"
	LaunchStudio   LaunchMode = "studio"
	LaunchHeadless LaunchMode = "headless"
	LaunchMCP      LaunchMode = "mcp"
)

type LaunchConfig struct {
	Root       string
	Document   string
	SocketPath string
	Mode       LaunchMode
	Listen     string
	Automation bool
}

type Launcher func(LaunchConfig) error

type JSONReport struct {
	SchemaVersion int                   `json:"schema_version"`
	Valid         bool                  `json:"valid"`
	Diagnostics   []document.Diagnostic `json:"diagnostics"`
}

func Run(args []string, stdout, stderr io.Writer, launch Launcher) int {
	if len(args) == 0 {
		usage(stderr)
		return ExitFailure
	}
	switch args[0] {
	case "validate":
		return validateCommand(args[1:], stdout, stderr)
	case "run":
		return runCommand(args[1:], stderr, launch)
	case "render":
		return renderCommand(args[1:], stderr)
	case "inspect":
		return inspectCommand(args[1:], stdout, stderr)
	case "mcp":
		return mcpCommand(args[1:], stderr, launch)
	default:
		fmt.Fprintf(stderr, "unknown command %q\n", args[0])
		usage(stderr)
		return ExitFailure
	}
}

func mcpCommand(args []string, stderr io.Writer, launch Launcher) int {
	flags := flag.NewFlagSet("mcp", flag.ContinueOnError)
	flags.SetOutput(stderr)
	listen := flags.String("listen", "127.0.0.1:8787", "loopback listen address")
	automation := flags.Bool("automation", false, "enable deterministic MCP automation tools and resources")
	if err := flags.Parse(args); err != nil || flags.NArg() != 0 {
		return ExitFailure
	}
	host, port, err := net.SplitHostPort(*listen)
	if err != nil || host != "127.0.0.1" || port == "" {
		fmt.Fprintln(stderr, "--listen must use 127.0.0.1:<port>")
		return ExitFailure
	}
	parsedPort, err := strconv.Atoi(port)
	if err != nil || parsedPort < 1 || parsedPort > 65535 {
		fmt.Fprintln(stderr, "--listen must use a valid TCP port")
		return ExitFailure
	}
	if launch == nil {
		fmt.Fprintln(stderr, "MCP server launcher is unavailable")
		return ExitFailure
	}
	if err := launch(LaunchConfig{Mode: LaunchMCP, Listen: *listen, Automation: *automation}); err != nil {
		fmt.Fprintln(stderr, err)
		return ExitFailure
	}
	return ExitSuccess
}

func inspectCommand(args []string, stdout, stderr io.Writer) int {
	file, options, ok := splitFile(args, stderr, "inspect")
	if !ok {
		return ExitFailure
	}
	flags := flag.NewFlagSet("inspect", flag.ContinueOnError)
	flags.SetOutput(stderr)
	root := flags.String("root", "", "containment root")
	from := flags.String("from", string(LaunchApp), "live app, studio, or headless session")
	if err := flags.Parse(options); err != nil || flags.NArg() != 0 {
		return ExitFailure
	}
	if *from != string(LaunchApp) && *from != string(LaunchStudio) && *from != string(LaunchHeadless) {
		fmt.Fprintln(stderr, "--from must be app, studio, or headless")
		return ExitFailure
	}
	resolvedRoot, resolvedFile, err := canonicalPair(*root, file)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return ExitFailure
	}
	socket, err := session.SocketPath(resolvedRoot, resolvedFile, *from)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return ExitFailure
	}
	response, err := session.Send(socket, session.Request{Action: "inspect"}, 30*time.Second)
	if err != nil {
		fmt.Fprintf(stderr, "no matching live %s session: %v\n", *from, err)
		return ExitFailure
	}
	if len(response.Data) == 0 {
		fmt.Fprintln(stderr, "live session returned no inspection tree")
		return ExitFailure
	}
	var status struct {
		Valid bool            `json:"valid"`
		Root  json.RawMessage `json:"root"`
	}
	if err := json.Unmarshal(response.Data, &status); err != nil {
		fmt.Fprintln(stderr, "invalid live inspection response:", err)
		return ExitFailure
	}
	if _, err := stdout.Write(append(append([]byte(nil), response.Data...), '\n')); err != nil {
		fmt.Fprintln(stderr, err)
		return ExitFailure
	}
	if response.Warning != "" {
		fmt.Fprintln(stderr, "warning:", response.Warning)
	}
	if !status.Valid && (len(status.Root) == 0 || string(status.Root) == "null") {
		return ExitValidation
	}
	return ExitSuccess
}

func validateCommand(args []string, stdout, stderr io.Writer) int {
	file, options, ok := splitFile(args, stderr, "validate")
	if !ok {
		return ExitFailure
	}
	flags := flag.NewFlagSet("validate", flag.ContinueOnError)
	flags.SetOutput(stderr)
	root := flags.String("root", "", "containment root")
	format := flags.String("format", "text", "text or json")
	if err := flags.Parse(options); err != nil || flags.NArg() != 0 {
		return ExitFailure
	}
	resolvedRoot, err := rootFor(*root)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return ExitFailure
	}
	diagnostics := project.Validate(resolvedRoot, file)
	sortDiagnostics(diagnostics)
	if *format == "json" {
		if diagnostics == nil {
			diagnostics = []document.Diagnostic{}
		}
		report := JSONReport{SchemaVersion: 1, Valid: len(diagnostics) == 0, Diagnostics: diagnostics}
		encoder := json.NewEncoder(stdout)
		encoder.SetEscapeHTML(false)
		if err := encoder.Encode(report); err != nil {
			fmt.Fprintln(stderr, err)
			return ExitFailure
		}
	} else if *format == "text" {
		for _, diagnostic := range diagnostics {
			fmt.Fprintf(stdout, "%s:%d:%d: %s %s: %s\n", diagnostic.File, diagnostic.Line, diagnostic.Column, diagnostic.Severity, diagnostic.Code, diagnostic.Message)
		}
	} else {
		fmt.Fprintln(stderr, "--format must be text or json")
		return ExitFailure
	}
	if len(diagnostics) != 0 {
		if operationalDiagnostics(diagnostics) {
			return ExitFailure
		}
		return ExitValidation
	}
	return ExitSuccess
}

func operationalDiagnostics(diagnostics []document.Diagnostic) bool {
	for _, diagnostic := range diagnostics {
		switch diagnostic.Code {
		case "project.root", "project.entry":
			return true
		}
	}
	return false
}

func runCommand(args []string, stderr io.Writer, launch Launcher) int {
	file, options, ok := splitFile(args, stderr, "run")
	if !ok {
		return ExitFailure
	}
	flags := flag.NewFlagSet("run", flag.ContinueOnError)
	flags.SetOutput(stderr)
	root := flags.String("root", "", "containment root")
	studioMode := flags.Bool("studio", false, "open Studio")
	headlessMode := flags.Bool("headless", false, "run without a visible window")
	if err := flags.Parse(options); err != nil || flags.NArg() != 0 {
		return ExitFailure
	}
	if *studioMode && *headlessMode {
		fmt.Fprintln(stderr, "--studio and --headless are mutually exclusive")
		return ExitFailure
	}
	mode := LaunchApp
	if *studioMode {
		mode = LaunchStudio
	} else if *headlessMode {
		mode = LaunchHeadless
	}
	resolvedRoot, resolvedFile, err := canonicalPair(*root, file)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return ExitFailure
	}
	source, err := os.ReadFile(resolvedFile)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return ExitFailure
	}
	parsedHint, _ := document.Parse(resolvedFile, source)
	if parsedHint != nil && parsedHint.Kind == document.KindTokens {
		fmt.Fprintln(stderr, "token modules are validation-only; run an app or component document")
		return ExitFailure
	}
	socket, err := session.SocketPath(resolvedRoot, resolvedFile, string(mode))
	if err != nil {
		fmt.Fprintln(stderr, err)
		return ExitFailure
	}
	if response, err := session.Send(socket, session.Request{Action: "focus"}, 250*time.Millisecond); err == nil && response.OK {
		return ExitSuccess
	}
	diagnostics := project.Validate(resolvedRoot, resolvedFile)
	exit := ExitSuccess
	if len(diagnostics) != 0 {
		sortDiagnostics(diagnostics)
		for _, diagnostic := range diagnostics {
			fmt.Fprintf(stderr, "%s:%d:%d: %s: %s\n", diagnostic.File, diagnostic.Line, diagnostic.Column, diagnostic.Code, diagnostic.Message)
		}
		exit = ExitValidation
		if mode == LaunchApp {
			return exit
		}
	}
	if launch == nil {
		fmt.Fprintln(stderr, "runtime launcher is unavailable")
		return ExitFailure
	}
	if err := launch(LaunchConfig{Root: resolvedRoot, Document: resolvedFile, SocketPath: socket, Mode: mode}); err != nil {
		fmt.Fprintln(stderr, err)
		return ExitFailure
	}
	return exit
}

func renderCommand(args []string, stderr io.Writer) int {
	file, options, ok := splitFile(args, stderr, "render")
	if !ok {
		return ExitFailure
	}
	flags := flag.NewFlagSet("render", flag.ContinueOnError)
	flags.SetOutput(stderr)
	root := flags.String("root", "", "containment root")
	output := flags.String("output", "", "new PNG output")
	scaleText := flags.String("scale", "1", "positive integer scale")
	from := flags.String("from", string(LaunchApp), "live app, studio, or headless session")
	if err := flags.Parse(options); err != nil || flags.NArg() != 0 {
		return ExitFailure
	}
	if *output == "" {
		fmt.Fprintln(stderr, "render requires --output <new.png>")
		return ExitFailure
	}
	if *from != string(LaunchApp) && *from != string(LaunchStudio) && *from != string(LaunchHeadless) {
		fmt.Fprintln(stderr, "--from must be app, studio, or headless")
		return ExitFailure
	}
	scale, err := strconv.Atoi(*scaleText)
	if err != nil || scale <= 0 {
		fmt.Fprintln(stderr, "--scale must be a positive integer")
		return ExitFailure
	}
	if err := render.ValidateOutput(*output); err != nil {
		fmt.Fprintln(stderr, err)
		return ExitFailure
	}
	resolvedRoot, resolvedFile, err := canonicalPair(*root, file)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return ExitFailure
	}
	socket, err := session.SocketPath(resolvedRoot, resolvedFile, *from)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return ExitFailure
	}
	response, err := session.Send(socket, session.Request{Action: "render", Output: *output, Scale: scale}, 30*time.Second)
	if err != nil {
		fmt.Fprintf(stderr, "no matching live %s session: %v\n", *from, err)
		return ExitFailure
	}
	if !response.OK {
		fmt.Fprintf(stderr, "live %s session rejected the capture request\n", *from)
		return ExitFailure
	}
	if response.Warning != "" {
		fmt.Fprintln(stderr, "warning:", response.Warning)
	}
	return ExitSuccess
}

func splitFile(args []string, stderr io.Writer, command string) (string, []string, bool) {
	if len(args) == 0 || args[0] == "" || args[0][0] == '-' {
		fmt.Fprintf(stderr, "%s requires a .gora file\n", command)
		return "", nil, false
	}
	if filepath.Ext(args[0]) != ".gora" {
		fmt.Fprintln(stderr, "document must use the .gora extension")
		return "", nil, false
	}
	return args[0], args[1:], true
}

func canonicalPair(root, file string) (string, string, error) {
	resolvedRoot, err := rootFor(root)
	if err != nil {
		return "", "", err
	}
	resolvedFile, err := filepath.Abs(file)
	if err != nil {
		return "", "", err
	}
	resolvedFile, err = filepath.EvalSymlinks(resolvedFile)
	if err != nil {
		return "", "", err
	}
	relative, err := filepath.Rel(resolvedRoot, resolvedFile)
	if err != nil || relative == ".." || filepath.IsAbs(relative) || len(relative) >= 3 && relative[:3] == "../" {
		return "", "", errors.New("document is outside the containment root")
	}
	return resolvedRoot, resolvedFile, nil
}

func rootFor(root string) (string, error) {
	if root == "" {
		var err error
		root, err = os.Getwd()
		if err != nil {
			return "", err
		}
	}
	root, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	return filepath.EvalSymlinks(root)
}

func sortDiagnostics(diagnostics []document.Diagnostic) {
	sort.SliceStable(diagnostics, func(i, j int) bool {
		a, b := diagnostics[i], diagnostics[j]
		if a.File != b.File {
			return a.File < b.File
		}
		if a.Line != b.Line {
			return a.Line < b.Line
		}
		if a.Column != b.Column {
			return a.Column < b.Column
		}
		if a.Code != b.Code {
			return a.Code < b.Code
		}
		return a.Message < b.Message
	})
}

func usage(output io.Writer) {
	fmt.Fprintln(output, "usage:")
	fmt.Fprintln(output, "  gora run <file> [--root <dir>] [--studio|--headless]")
	fmt.Fprintln(output, "  gora validate <file> [--root <dir>] [--format text|json]")
	fmt.Fprintln(output, "  gora render <file> --output <new.png> [--scale <positive-integer>] [--root <dir>] [--from app|studio|headless]")
	fmt.Fprintln(output, "  gora inspect <file> [--root <dir>] [--from app|studio|headless]")
	fmt.Fprintln(output, "  gora mcp [--listen 127.0.0.1:<port>] [--automation]")
}
