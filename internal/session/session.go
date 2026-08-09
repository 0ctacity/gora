package session

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"
)

const maxMessageBytes = 8 << 20

var ErrAlreadyRunning = errors.New("a Gora session is already running")

type Request struct {
	// Version is zero for legacy focus/render/inspect requests. Versioned
	// requests must use ProtocolVersion.
	Version   int             `json:"version,omitempty"`
	RequestID string          `json:"request_id,omitempty"`
	Action    string          `json:"action"`
	Output    string          `json:"output,omitempty"`
	Scale     int             `json:"scale,omitempty"`
	Payload   json.RawMessage `json:"payload,omitempty"`
}

type Response struct {
	Version   int             `json:"version,omitempty"`
	RequestID string          `json:"request_id,omitempty"`
	OK        bool            `json:"ok"`
	Error     string          `json:"error,omitempty"`
	Warning   string          `json:"warning,omitempty"`
	Data      json.RawMessage `json:"data,omitempty"`
}

// ProtocolVersion is the version of the host bridge envelope. Legacy
// focus/render/inspect requests intentionally omit it and remain supported.
const ProtocolVersion = 1

const (
	ActionHandshake = "handshake"
	ActionSnapshot  = "snapshot"
	ActionWait      = "wait"
	ActionCommand   = "command"
	ActionDetach    = "detach"
)

// HostMode identifies the owner of a live runtime.
type HostMode string

const (
	HostModeHeadless HostMode = "headless"
	HostModeApp      HostMode = "app"
	HostModeStudio   HostMode = "studio"
)

// HostIdentity is exchanged during the versioned handshake. Root and
// Document are canonical absolute paths and InstanceID is unique per process.
type HostIdentity struct {
	InstanceID   string   `json:"host_instance_id"`
	Root         string   `json:"root"`
	Document     string   `json:"document"`
	Mode         HostMode `json:"mode"`
	PID          int      `json:"pid"`
	Automation   bool     `json:"automation"`
	Capabilities []string `json:"capabilities"`
}

// HandshakePayload is the request body used by the MCP attachment client.
type HandshakePayload struct {
	Root     string   `json:"root"`
	Document string   `json:"document"`
	Mode     HostMode `json:"mode"`
	Protocol int      `json:"protocol"`
}

// HandshakeResult is returned by an automation-enabled host.
type HandshakeResult struct {
	Protocol int          `json:"protocol"`
	Host     HostIdentity `json:"host"`
}

// ValidateCapabilities enforces the finite, deterministic capability list
// advertised by a host.
func ValidateCapabilities(capabilities []string) error {
	known := map[string]bool{
		"snapshot": true, "tree": true, "wait": true, "command": true,
		"viewport": true, "selection": true, "activation": true, "scroll": true,
		"state": true, "reset": true, "input": true, "editing": true,
		"clock": true, "trace": true, "capture": true, "overlay": true, "faults": true,
	}
	if len(capabilities) > 256 {
		return errors.New("host capabilities exceed limit")
	}
	previous := ""
	for index, capability := range capabilities {
		if capability == "" || len(capability) > 128 {
			return errors.New("host capability is invalid")
		}
		if !known[capability] {
			return fmt.Errorf("unknown host capability %q", capability)
		}
		if index > 0 && capability <= previous {
			return errors.New("host capabilities must be sorted and unique")
		}
		previous = capability
	}
	return nil
}

// ValidateHandshake checks identity, protocol, mode, automation opt-in, and
// capabilities without mutating host or registry state.
func ValidateHandshake(expected, got HostIdentity, protocol int, wantMode HostMode) error {
	if protocol != ProtocolVersion {
		return fmt.Errorf("unsupported session protocol version %d", protocol)
	}
	if wantMode != HostModeApp && wantMode != HostModeStudio {
		return fmt.Errorf("unsupported host mode %q", wantMode)
	}
	if got.Mode != wantMode {
		return fmt.Errorf("host mode %q does not match requested %q", got.Mode, wantMode)
	}
	if !got.Automation {
		return errors.New("host automation is not enabled")
	}
	if expected.InstanceID != "" && expected.InstanceID != got.InstanceID {
		return errors.New("host instance identity mismatch")
	}
	if expected.Root != "" && !sameCanonicalPath(expected.Root, got.Root) {
		return errors.New("host root identity mismatch")
	}
	if expected.Document != "" && !sameCanonicalPath(expected.Document, got.Document) {
		return errors.New("host document identity mismatch")
	}
	if got.PID <= 0 {
		return errors.New("host process id is invalid")
	}
	return ValidateCapabilities(got.Capabilities)
}

func sameCanonicalPath(expected, got string) bool {
	canonical := func(value string) string {
		if resolved, err := filepath.EvalSymlinks(value); err == nil {
			return filepath.Clean(resolved)
		}
		parent, base := filepath.Split(value)
		if parent != "" {
			if resolved, err := filepath.EvalSymlinks(filepath.Clean(parent)); err == nil {
				return filepath.Clean(filepath.Join(resolved, base))
			}
		}
		if resolved, err := filepath.Abs(value); err == nil {
			return filepath.Clean(resolved)
		}
		return filepath.Clean(value)
	}
	return canonical(expected) == canonical(got)
}

type Handler func(context.Context, Request) Response

type Server struct {
	listener net.Listener
	path     string
	done     chan struct{}
	once     sync.Once
}

func SocketPath(root, document, mode string) (string, error) {
	cache, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	directory := filepath.Join(cache, "gora", "sessions")
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return "", err
	}
	info, err := os.Lstat(directory)
	if err != nil {
		return "", err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return "", fmt.Errorf("session registry path is not a real directory: %s", directory)
	}
	if err := os.Chmod(directory, 0o700); err != nil {
		return "", err
	}
	sum := sha256.Sum256([]byte(root + "\x00" + document + "\x00" + mode))
	return filepath.Join(directory, hex.EncodeToString(sum[:16])+".sock"), nil
}

func Listen(path string, handler Handler) (*Server, error) {
	if err := prepareSocket(path); err != nil {
		return nil, err
	}
	listener, err := net.Listen("unix", path)
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(path, 0o600); err != nil {
		_ = listener.Close()
		_ = os.Remove(path)
		return nil, err
	}
	server := &Server{listener: listener, path: path, done: make(chan struct{})}
	go server.serve(handler)
	return server, nil
}

func prepareSocket(path string) error {
	if _, err := os.Lstat(path); errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		return err
	}
	connection, err := net.DialTimeout("unix", path, 100*time.Millisecond)
	if err == nil {
		_ = connection.Close()
		return ErrAlreadyRunning
	}
	return os.Remove(path)
}

func (server *Server) serve(handler Handler) {
	defer close(server.done)
	for {
		connection, err := server.listener.Accept()
		if err != nil {
			return
		}
		go handleConnection(connection, handler)
	}
}

func handleConnection(connection net.Conn, handler Handler) {
	defer connection.Close()
	var request Request
	decoder := json.NewDecoder(io.LimitReader(connection, maxMessageBytes))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		_ = json.NewEncoder(connection).Encode(Response{Error: err.Error()})
		return
	}
	response := handler(context.Background(), request)
	_ = json.NewEncoder(connection).Encode(response)
}

// DecodeRequest strictly decodes one bounded versioned or legacy request.
// It is useful to deterministic in-process protocol harnesses as well as
// socket clients.
func DecodeRequest(data []byte) (Request, error) {
	if len(data) > maxMessageBytes {
		return Request{}, errors.New("session request exceeds size limit")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var request Request
	if err := decoder.Decode(&request); err != nil {
		return Request{}, err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return Request{}, errors.New("session request must contain one JSON value")
		}
		return Request{}, err
	}
	return request, nil
}

func (server *Server) Close() error {
	var closeErr error
	server.once.Do(func() {
		closeErr = server.listener.Close()
		<-server.done
		if err := os.Remove(server.path); err != nil && !errors.Is(err, os.ErrNotExist) && closeErr == nil {
			closeErr = err
		}
	})
	return closeErr
}

func Send(path string, request Request, timeout time.Duration) (Response, error) {
	connection, err := net.DialTimeout("unix", path, timeout)
	if err != nil {
		return Response{}, err
	}
	defer connection.Close()
	_ = connection.SetDeadline(time.Now().Add(timeout))
	if err := json.NewEncoder(connection).Encode(request); err != nil {
		return Response{}, err
	}
	var response Response
	decoder := json.NewDecoder(io.LimitReader(connection, maxMessageBytes))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&response); err != nil {
		return Response{}, err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return Response{}, errors.New("session response must contain one JSON value")
		}
		return Response{}, err
	}
	if request.Version != 0 {
		if response.Version != ProtocolVersion {
			return response, fmt.Errorf("unsupported session response protocol version %d", response.Version)
		}
		if response.RequestID != request.RequestID {
			return response, errors.New("session response request_id mismatch")
		}
	}
	if !response.OK && response.Error != "" {
		return response, errors.New(response.Error)
	}
	return response, nil
}
