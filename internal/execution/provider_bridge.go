package execution

// This file is the data-plane boundary used by the Pi extension.  It is
// deliberately a small protocol rather than an HTTP proxy: the peer can only
// ask the already-selected host Transport to perform an operation, and cannot
// learn a provider origin or credential.

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync"

	"github.com/Ameb8/agent-run/internal/contract"
	"github.com/Ameb8/agent-run/internal/provider"
)

const maxBridgeFrameBytes = 32 << 20

// ProviderBridgeSocket is the fixed path visible inside a run's private tmp
// mount.  The host path is created below RunScope.Temporary; no host socket is
// shared with a container.
const ProviderBridgeSocket = "provider.sock"

type bridgeRequest struct {
	ID      string      `json:"id"`
	Method  string      `json:"method"`
	Target  string      `json:"target"`
	Headers http.Header `json:"headers"`
	Body    string      `json:"body"` // base64
}

type bridgeResponse struct {
	ID       string      `json:"id"`
	Accepted bool        `json:"accepted"`
	Status   int         `json:"status,omitempty"`
	Headers  http.Header `json:"headers,omitempty"`
	Body     string      `json:"body,omitempty"` // base64
	Error    string      `json:"error,omitempty"`
}

// ProviderBridge gates every provider operation before Transport.Do.  Turns
// are incremented only after Do has returned a successful HTTP response, so a
// truncated response body still consumes its turn while rejected requests do
// not. It accepts one request-scoped connection at a time from the generated
// extension, which is the sole consumer of this private per-run socket.
type ProviderBridge struct {
	listener   net.Listener
	transport  *provider.Transport
	maxTurns   int
	socketPath string
	requestMu  sync.Mutex

	mu         sync.Mutex
	turns      int
	err        error
	closed     bool
	connection net.Conn
}

func NewProviderBridge(temporary string, transport *provider.Transport, maxTurns int) (*ProviderBridge, error) {
	if transport == nil || maxTurns <= 0 {
		return nil, &contract.CommandError{Category: contract.ErrorConfiguration, Message: "provider bridge is unavailable"}
	}
	path := filepath.Join(temporary, ProviderBridgeSocket)
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("remove provider bridge socket: %w", err)
	}
	listener, err := net.Listen("unix", path)
	if err != nil {
		return nil, fmt.Errorf("listen provider bridge: %w", err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		_ = listener.Close()
		return nil, err
	}
	bridge, err := NewProviderBridgeWithListener(listener, transport, maxTurns)
	if err != nil {
		_ = listener.Close()
		_ = os.Remove(path)
		return nil, err
	}
	bridge.socketPath = path
	return bridge, nil
}

// NewProviderBridgeWithListener is the deterministic transport seam used by
// protocol tests. Production uses NewProviderBridge with a private Unix socket.
func NewProviderBridgeWithListener(listener net.Listener, transport *provider.Transport, maxTurns int) (*ProviderBridge, error) {
	if listener == nil || transport == nil || maxTurns <= 0 {
		return nil, &contract.CommandError{Category: contract.ErrorConfiguration, Message: "provider bridge is unavailable"}
	}
	return &ProviderBridge{listener: listener, transport: transport, maxTurns: maxTurns}, nil
}

func (b *ProviderBridge) Path() string {
	if b == nil || b.listener == nil {
		return ""
	}
	return b.listener.Addr().String()
}

func (b *ProviderBridge) TurnsUsed() int { b.mu.Lock(); defer b.mu.Unlock(); return b.turns }

func (b *ProviderBridge) Err() error { b.mu.Lock(); defer b.mu.Unlock(); return b.err }

func (b *ProviderBridge) setErr(err error) {
	b.mu.Lock()
	if b.err == nil {
		b.err = err
	}
	b.mu.Unlock()
}

// Serve accepts successive request-scoped peers until context cancellation,
// listener closure, or peer failure.
// A malformed peer frame is an execution failure; raw request/response data is
// intentionally never incorporated into its error.
func (b *ProviderBridge) Serve(ctx context.Context) error {
	if b == nil || b.listener == nil {
		return errors.New("provider bridge unavailable")
	}
	stopped := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			_ = b.Close()
		case <-stopped:
		}
	}()
	defer close(stopped)
	for {
		c, err := b.listener.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			b.mu.Lock()
			closed := b.closed
			b.mu.Unlock()
			if closed {
				return nil
			}
			return errors.New("provider bridge closed")
		}
		b.mu.Lock()
		b.connection = c
		b.mu.Unlock()
		err = b.servePeer(ctx, c)
		_ = c.Close()
		b.mu.Lock()
		if b.connection == c {
			b.connection = nil
		}
		b.mu.Unlock()
		if err != nil {
			return err
		}
	}
}

func (b *ProviderBridge) servePeer(ctx context.Context, c net.Conn) error {
	reader := bufio.NewReaderSize(c, maxBridgeFrameBytes+1)
	writer := bufio.NewWriterSize(c, maxBridgeFrameBytes+1)
	for {
		line, err := readBridgeLine(reader)
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			b.setErr(&contract.CommandError{Category: contract.ErrorExecution, Message: "invalid provider bridge frame"})
			return b.Err()
		}
		var request bridgeRequest
		if json.Unmarshal(line, &request) != nil || request.ID == "" || request.Method == "" || request.Target == "" {
			b.setErr(&contract.CommandError{Category: contract.ErrorExecution, Message: "invalid provider bridge frame"})
			return b.Err()
		}
		response := b.forward(ctx, request)
		encoded, _ := json.Marshal(response)
		if len(encoded) > maxBridgeFrameBytes {
			b.setErr(&contract.CommandError{Category: contract.ErrorLimit, Message: "provider bridge response exceeds limit"})
			return b.Err()
		}
		if _, err := writer.Write(append(encoded, '\n')); err != nil {
			return err
		}
		if err := writer.Flush(); err != nil {
			return err
		}
	}
}

func readBridgeLine(reader *bufio.Reader) ([]byte, error) {
	line, err := reader.ReadBytes('\n')
	if len(line) > maxBridgeFrameBytes || (err == nil && (len(line) == 0 || line[len(line)-1] != '\n')) {
		return nil, errors.New("invalid frame")
	}
	if err != nil {
		return nil, err
	}
	if len(line) >= 2 && line[len(line)-2] == '\r' {
		return nil, errors.New("invalid frame")
	}
	return line[:len(line)-1], nil
}

func (b *ProviderBridge) forward(ctx context.Context, request bridgeRequest) bridgeResponse {
	b.requestMu.Lock()
	defer b.requestMu.Unlock()
	result := bridgeResponse{ID: request.ID}
	body, err := base64.StdEncoding.DecodeString(request.Body)
	if err != nil {
		result.Error = "invalid request"
		return result
	}
	b.mu.Lock()
	if b.turns >= b.maxTurns {
		b.mu.Unlock()
		result.Error = "turn limit reached"
		b.setErr(&contract.CommandError{Category: contract.ErrorLimit, Message: "maximum turns reached"})
		return result
	}
	b.mu.Unlock()
	response, err := b.transport.Do(ctx, request.Method, request.Target, bytesReader(body), request.Headers)
	if err != nil {
		result.Error = "provider request failed"
		b.setErr(err)
		return result
	}
	// This is the acceptance boundary. Do not move it below ReadAll.
	b.mu.Lock()
	b.turns++
	b.mu.Unlock()
	defer func() { _ = response.Body.Close() }()
	payload, readErr := io.ReadAll(io.LimitReader(response.Body, maxBridgeFrameBytes+1))
	if readErr != nil || len(payload) > maxBridgeFrameBytes {
		result.Accepted = true
		result.Error = "provider response could not be read"
		b.setErr(&contract.CommandError{Category: contract.ErrorProvider, Message: "model provider response could not be decoded"})
		return result
	}
	result.Accepted, result.Status, result.Headers, result.Body = true, response.StatusCode, response.Header, base64.StdEncoding.EncodeToString(payload)
	return result
}

func bytesReader(value []byte) io.Reader { return &bridgeBytesReader{value: value} }

type bridgeBytesReader struct{ value []byte }

func (r *bridgeBytesReader) Read(p []byte) (int, error) {
	if len(r.value) == 0 {
		return 0, io.EOF
	}
	n := copy(p, r.value)
	r.value = r.value[n:]
	return n, nil
}

func (b *ProviderBridge) Close() error {
	if b == nil {
		return nil
	}
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return nil
	}
	b.closed = true
	connection := b.connection
	b.mu.Unlock()
	if connection != nil {
		_ = connection.Close()
	}
	if b.listener == nil {
		return nil
	}
	err := b.listener.Close()
	if b.socketPath != "" {
		_ = os.Remove(b.socketPath)
	}
	return err
}
