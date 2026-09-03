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
// not.  It accepts exactly one peer: the generated extension is the sole
// consumer of this private per-run socket.
type ProviderBridge struct {
	listener  net.Listener
	transport *provider.Transport
	maxTurns  int

	mu     sync.Mutex
	turns  int
	err    error
	closed bool
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

// Serve blocks until context cancellation, listener closure, or peer failure.
// A malformed peer frame is an execution failure; raw request/response data is
// intentionally never incorporated into its error.
func (b *ProviderBridge) Serve(ctx context.Context) error {
	if b == nil || b.listener == nil {
		return errors.New("provider bridge unavailable")
	}
	accepted := make(chan net.Conn, 1)
	go func() {
		c, err := b.listener.Accept()
		if err == nil {
			accepted <- c
		} else {
			close(accepted)
		}
	}()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case c, ok := <-accepted:
		if !ok {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return errors.New("provider bridge closed")
		}
		defer c.Close()
		return b.servePeer(ctx, c)
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
	return line[:len(line)-1], nil
}

func (b *ProviderBridge) forward(ctx context.Context, request bridgeRequest) bridgeResponse {
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
	defer response.Body.Close()
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
	b.mu.Unlock()
	if b.listener == nil {
		return nil
	}
	path := b.listener.Addr().String()
	err := b.listener.Close()
	_ = os.Remove(path)
	return err
}
