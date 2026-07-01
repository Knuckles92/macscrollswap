// Package ctlsock implements the JSON-RPC protocol used by the macscrollswap
// daemon <-> CLI IPC. The daemon listens on a Unix-domain socket; CLI
// subcommands act as one-shot clients.
package ctlsock

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"sync"
	"time"
)

// Method names.
const (
	MethodStatus        = "Status"
	MethodPause         = "Pause"
	MethodResume        = "Resume"
	MethodGetDirection  = "GetDirection"
	MethodSetConnected  = "SetConnected"
	MethodSetDisconnect = "SetDisconnected"
	MethodSwapDirection = "SwapDirection"
	MethodShutdown      = "Shutdown"
)

// Request is a single RPC request. Exactly one line of JSON per request.
type Request struct {
	Method string          `json:"method"`
	Params json.RawMessage `json:"params,omitempty"`
}

// Response is a single RPC response.
type Response struct {
	Result json.RawMessage `json:"result,omitempty"`
	Error  string          `json:"error,omitempty"`
}

// Handler dispatches an RPC method. Implementations should return (result,
// error) where result is JSON-marshalable.
type Handler interface {
	Handle(method string, params json.RawMessage) (any, error)
}

// HandlerFunc adapts a function to Handler.
type HandlerFunc func(string, json.RawMessage) (any, error)

// Handle implements Handler.
func (f HandlerFunc) Handle(method string, params json.RawMessage) (any, error) {
	return f(method, params)
}

// Server listens on a Unix-domain socket and serves RPC requests using the
// given handler.
type Server struct {
	socket string
	h      Handler
	log    *slog.Logger

	mu     sync.Mutex
	ln     net.Listener
	closed bool
}

// NewServer constructs a Server bound to socketPath.
func NewServer(socketPath string, h Handler, log *slog.Logger) *Server {
	if log == nil {
		log = slog.Default()
	}
	return &Server{socket: socketPath, h: h, log: log}
}

// ListenAndServe opens the socket and serves until Close is called.
func (s *Server) ListenAndServe() error {
	// Remove any stale socket file. Best-effort.
	_ = os.Remove(s.socket)

	parent, _ := splitDir(s.socket)
	if parent != "" {
		_ = os.MkdirAll(parent, 0o755)
	}

	ln, err := net.Listen("unix", s.socket)
	if err != nil {
		return fmt.Errorf("listen %s: %w", s.socket, err)
	}
	_ = os.Chmod(s.socket, 0o600)

	s.mu.Lock()
	s.ln = ln
	s.mu.Unlock()

	for {
		conn, err := ln.Accept()
		if err != nil {
			s.mu.Lock()
			closed := s.closed
			s.mu.Unlock()
			if closed {
				return nil
			}
			return fmt.Errorf("accept: %w", err)
		}
		go s.serve(conn)
	}
}

// Close stops accepting connections and removes the socket file.
func (s *Server) Close() error {
	s.mu.Lock()
	s.closed = true
	ln := s.ln
	s.mu.Unlock()
	if ln != nil {
		_ = ln.Close()
	}
	_ = os.Remove(s.socket)
	return nil
}

func (s *Server) serve(c net.Conn) {
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(10 * time.Second))
	reader := bufio.NewReader(c)
	writer := bufio.NewWriter(c)
	defer writer.Flush()

	for {
		line, err := reader.ReadBytes('\n')
		if err != nil {
			if !errors.Is(err, io.EOF) {
				s.log.Debug("ctlsock read", "err", err)
			}
			return
		}
		var req Request
		if err := json.Unmarshal(line, &req); err != nil {
			s.writeError(writer, fmt.Sprintf("malformed request: %v", err))
			_ = writer.Flush()
			return
		}
		result, err := s.h.Handle(req.Method, req.Params)
		if err != nil {
			s.writeError(writer, err.Error())
			_ = writer.Flush()
			return
		}
		s.writeResult(writer, result)
		// Flush after every response so the client can read it before the
		// connection blocks again waiting for the next request line.
		if err := writer.Flush(); err != nil {
			s.log.Debug("ctlsock flush", "err", err)
			return
		}
	}
}

func (s *Server) writeError(w *bufio.Writer, msg string) {
	resp := Response{Error: msg}
	out, _ := json.Marshal(resp)
	_, _ = w.Write(out)
	_, _ = w.WriteString("\n")
}

func (s *Server) writeResult(w *bufio.Writer, result any) {
	var raw json.RawMessage
	if result != nil {
		b, err := json.Marshal(result)
		if err != nil {
			s.writeError(w, fmt.Sprintf("marshal result: %v", err))
			return
		}
		raw = b
	}
	resp := Response{Result: raw}
	out, err := json.Marshal(resp)
	if err != nil {
		_, _ = w.WriteString(fmt.Sprintf(`{"error":"marshal response: %s"}`+"\n", err.Error()))
		return
	}
	_, _ = w.Write(out)
	_, _ = w.WriteString("\n")
}

// Client is a one-shot RPC client over the Unix socket.
type Client struct {
	socket  string
	timeout time.Duration
}

// NewClient constructs a Client.
func NewClient(socketPath string) *Client {
	return &Client{socket: socketPath, timeout: 5 * time.Second}
}

// Call sends one request and returns the parsed response.
func (c *Client) Call(method string, params any) (*Response, error) {
	conn, err := net.DialTimeout("unix", c.socket, c.timeout)
	if err != nil {
		return nil, fmt.Errorf("dial daemon: %w (is the daemon running?)", err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(c.timeout))

	var paramBytes []byte
	if params != nil {
		b, err := json.Marshal(params)
		if err != nil {
			return nil, fmt.Errorf("marshal params: %w", err)
		}
		paramBytes = b
	}
	req := Request{Method: method, Params: paramBytes}
	reqLine, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}
	reqLine = append(reqLine, '\n')
	if _, err := conn.Write(reqLine); err != nil {
		return nil, fmt.Errorf("write request: %w", err)
	}

	reader := bufio.NewReader(conn)
	line, err := reader.ReadBytes('\n')
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}
	var resp Response
	if err := json.Unmarshal(line, &resp); err != nil {
		return nil, fmt.Errorf("unmarshal response: %w", err)
	}
	return &resp, nil
}

// DecodeResult unmarshals resp.Result into out. Returns an error if resp
// carried an RPC error.
func (resp *Response) DecodeResult(out any) error {
	if resp.Error != "" {
		return errors.New(resp.Error)
	}
	if len(resp.Result) == 0 || string(resp.Result) == "null" {
		return nil
	}
	return json.Unmarshal(resp.Result, out)
}

func splitDir(p string) (string, string) {
	for i := len(p) - 1; i >= 0; i-- {
		if p[i] == '/' {
			if i == 0 {
				return "/", p[1:]
			}
			return p[:i], p[i+1:]
		}
	}
	return "", p
}
