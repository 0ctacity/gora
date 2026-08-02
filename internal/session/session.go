package session

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"
)

var ErrAlreadyRunning = errors.New("a Gora session is already running")

type Request struct {
	Action string `json:"action"`
	Output string `json:"output,omitempty"`
	Scale  int    `json:"scale,omitempty"`
}

type Response struct {
	OK      bool            `json:"ok"`
	Error   string          `json:"error,omitempty"`
	Warning string          `json:"warning,omitempty"`
	Data    json.RawMessage `json:"data,omitempty"`
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
	if err := json.NewDecoder(connection).Decode(&request); err != nil {
		_ = json.NewEncoder(connection).Encode(Response{Error: err.Error()})
		return
	}
	response := handler(context.Background(), request)
	_ = json.NewEncoder(connection).Encode(response)
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
	if err := json.NewDecoder(connection).Decode(&response); err != nil {
		return Response{}, err
	}
	if !response.OK && response.Error != "" {
		return response, errors.New(response.Error)
	}
	return response, nil
}
