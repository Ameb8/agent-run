package execution

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Ameb8/agent-run/internal/contract"
	"github.com/Ameb8/agent-run/internal/provider"
)

func TestProviderBridgeCountsAcceptanceBeforeResponseBodyDecode(t *testing.T) {
	client := &http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
		if r.Header.Get("Authorization") != "Bearer secret-canary" {
			t.Error("credential was not added by host transport")
		}
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(`{"ok":true}`)), Request: r}, nil
	})}
	transport, err := provider.NewOpenAICompatibleWithClient("https://provider.test", "secret-canary", client)
	if err != nil {
		t.Fatal(err)
	}
	bridge, err := NewProviderBridge(t.TempDir(), transport, 1)
	if err != nil && strings.Contains(err.Error(), "operation not permitted") {
		t.Skip("Unix sockets unavailable in this test sandbox")
	}
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = bridge.Close() }()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- bridge.Serve(ctx) }()
	connection, err := net.Dial("unix", bridge.Path())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = connection.Close() }()
	response := bridgeRoundOn(t, connection, bridgeRequest{ID: "one", Method: "POST", Target: "responses", Body: base64.StdEncoding.EncodeToString([]byte(`{}`))})
	if !response.Accepted || bridge.TurnsUsed() != 1 {
		t.Fatalf("response=%#v turns=%d", response, bridge.TurnsUsed())
	}
	// Pi can make this follow-up automatically; the host gate rejects it before
	// Transport.Do, so it cannot reach the provider after the limit is spent.
	response = bridgeRoundOn(t, connection, bridgeRequest{ID: "two", Method: "POST", Target: "responses", Body: base64.StdEncoding.EncodeToString([]byte(`{}`))})
	if response.Accepted || bridge.TurnsUsed() != 1 {
		t.Fatalf("follow-up=%#v turns=%d", response, bridge.TurnsUsed())
	}
	var command *contract.CommandError
	if !jsonErrorAs(bridge.Err(), &command) || command.Category != contract.ErrorLimit {
		t.Fatalf("bridge error=%v", bridge.Err())
	}
	_ = bridge.Close()
	<-done
}

func TestProviderBridgeServesOneConnectionPerPiRequest(t *testing.T) {
	var requests atomic.Int32
	client := &http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
		requests.Add(1)
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`data: done\n\n`)), Request: r}, nil
	})}
	transport, err := provider.NewOpenAICompatibleWithClient("https://provider.test/v1", "secret", client)
	if err != nil {
		t.Fatal(err)
	}
	bridge, listener, err := testProviderBridge(t, transport, 2)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = bridge.Close() }()
	done := make(chan error, 1)
	go func() { done <- bridge.Serve(context.Background()) }()

	for _, id := range []string{"initial", "follow-up"} {
		connection := listener.connect(t)
		_ = connection.SetDeadline(time.Now().Add(time.Second))
		response := bridgeRoundOn(t, connection, bridgeRequest{ID: id, Method: http.MethodPost, Target: "responses", Body: base64.StdEncoding.EncodeToString([]byte(`{}`))})
		_ = connection.Close()
		if !response.Accepted {
			t.Fatalf("%s response = %#v", id, response)
		}
	}
	if got := requests.Load(); got != 2 {
		t.Fatalf("provider requests = %d", got)
	}
	_ = bridge.Close()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("bridge did not stop after close")
	}
}

type failingBody struct{}

func (failingBody) Read([]byte) (int, error) { return 0, io.ErrUnexpectedEOF }
func (failingBody) Close() error             { return nil }

func TestProviderBridgeCountsAcceptedResponseWhoseBodyFails(t *testing.T) {
	client := &http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: failingBody{}, Request: r}, nil
	})}
	transport, err := provider.NewOpenAICompatibleWithClient("https://provider.test/v1", "secret", client)
	if err != nil {
		t.Fatal(err)
	}
	bridge, _, err := testProviderBridge(t, transport, 1)
	if err != nil {
		t.Fatal(err)
	}
	response := bridge.forward(context.Background(), bridgeRequest{ID: "accepted", Method: http.MethodPost, Target: "responses", Body: base64.StdEncoding.EncodeToString([]byte(`{}`))})
	if !response.Accepted || bridge.TurnsUsed() != 1 || bridge.Err() == nil {
		t.Fatalf("response=%#v turns=%d error=%v", response, bridge.TurnsUsed(), bridge.Err())
	}
	_ = bridge.Close()
}

func testProviderBridge(t *testing.T, transport *provider.Transport, maxTurns int) (*ProviderBridge, *pipeListener, error) {
	t.Helper()
	listener := newPipeListener()
	bridge, err := NewProviderBridgeWithListener(listener, transport, maxTurns)
	if err != nil {
		_ = listener.Close()
	}
	return bridge, listener, err
}

type pipeListener struct {
	connections chan net.Conn
	closed      chan struct{}
	once        sync.Once
}

func newPipeListener() *pipeListener {
	return &pipeListener{connections: make(chan net.Conn), closed: make(chan struct{})}
}

func (l *pipeListener) Accept() (net.Conn, error) {
	select {
	case connection := <-l.connections:
		return connection, nil
	case <-l.closed:
		return nil, net.ErrClosed
	}
}

func (l *pipeListener) Close() error {
	l.once.Do(func() { close(l.closed) })
	return nil
}

func (l *pipeListener) Addr() net.Addr { return pipeAddress("provider") }

func (l *pipeListener) connect(t *testing.T) net.Conn {
	t.Helper()
	client, server := net.Pipe()
	select {
	case l.connections <- server:
		return client
	case <-l.closed:
		t.Fatal("listener closed")
		return nil
	case <-time.After(time.Second):
		t.Fatal("listener did not accept connection")
		return nil
	}
}

type pipeAddress string

func (a pipeAddress) Network() string { return "pipe" }
func (a pipeAddress) String() string  { return string(a) }

type roundTrip func(*http.Request) (*http.Response, error)

func (f roundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func bridgeRoundOn(t *testing.T, connection net.Conn, request bridgeRequest) bridgeResponse {
	t.Helper()
	bytes, _ := json.Marshal(request)
	if _, err := connection.Write(append(bytes, '\n')); err != nil {
		t.Fatal(err)
	}
	line, err := bufio.NewReader(connection).ReadBytes('\n')
	if err != nil {
		t.Fatal(err)
	}
	var response bridgeResponse
	if err := json.Unmarshal(line, &response); err != nil {
		t.Fatal(err)
	}
	return response
}

func jsonErrorAs(err error, target **contract.CommandError) bool {
	return errors.As(err, target)
}

func TestProviderBridgeRejectsMalformedFrameWithoutLeakingPayload(t *testing.T) {
	transport, err := provider.NewOpenAICompatible("https://example.test", "secret-canary")
	if err != nil {
		t.Fatal(err)
	}
	bridge, err := NewProviderBridge(t.TempDir(), transport, 1)
	if err != nil && strings.Contains(err.Error(), "operation not permitted") {
		t.Skip("Unix sockets unavailable in this test sandbox")
	}
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = bridge.Close() }()
	done := make(chan error, 1)
	go func() { done <- bridge.Serve(context.Background()) }()
	connection, err := net.Dial("unix", bridge.Path())
	if err != nil {
		t.Fatal(err)
	}
	_, _ = connection.Write([]byte("not-json secret-canary\n"))
	_ = connection.Close()
	<-done
	if bridge.Err() == nil || bridge.Err().Error() == "not-json secret-canary" {
		t.Fatalf("unsafe error=%v", bridge.Err())
	}
}

func TestProviderBridgeRejectsCRLFFrames(t *testing.T) {
	transport, err := provider.NewOpenAICompatible("https://example.test", "secret")
	if err != nil {
		t.Fatal(err)
	}
	bridge, listener, err := testProviderBridge(t, transport, 1)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = bridge.Close() }()
	done := make(chan error, 1)
	go func() { done <- bridge.Serve(context.Background()) }()
	connection := listener.connect(t)
	_, _ = connection.Write([]byte(`{"id":"one","method":"POST","target":"responses","body":"e30="}` + "\r\n"))
	_ = connection.Close()
	<-done
	var command *contract.CommandError
	if !jsonErrorAs(bridge.Err(), &command) || command.Category != contract.ErrorExecution {
		t.Fatalf("bridge error = %v", bridge.Err())
	}
}
