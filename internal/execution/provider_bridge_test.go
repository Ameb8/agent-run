package execution

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"

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
	defer bridge.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- bridge.Serve(ctx) }()
	connection, err := net.Dial("unix", bridge.Path())
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
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

type roundTrip func(*http.Request) (*http.Response, error)

func (f roundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func bridgeRound(t *testing.T, path string, request bridgeRequest) bridgeResponse {
	t.Helper()
	connection, err := net.Dial("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	return bridgeRoundOn(t, connection, request)
}

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
	for err != nil {
		if value, ok := err.(*contract.CommandError); ok {
			*target = value
			return true
		}
		return false
	}
	return false
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
	defer bridge.Close()
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
