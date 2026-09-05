package execution

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Ameb8/agent-run/internal/contract"
	"github.com/Ameb8/agent-run/internal/provider"
	agentruntime "github.com/Ameb8/agent-run/internal/runtime"
)

func TestAdapterRunsPromptThroughHostProviderAndReturnsFactualOutcome(t *testing.T) {
	root := t.TempDir()
	configuration, temporary, resources, workspace := filepath.Join(root, "config"), filepath.Join(root, "tmp"), filepath.Join(root, "resources"), filepath.Join(root, "workspace")
	for _, directory := range []string{configuration, temporary, resources, workspace} {
		if err := os.Mkdir(directory, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	client := &http.Client{Transport: roundTrip(func(request *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: io.NopCloser(strings.NewReader("data: done\n\n")), Request: request}, nil
	})}
	transport, err := provider.NewOpenAICompatibleWithClient("https://provider.test/v1", "secret-canary", client)
	if err != nil {
		t.Fatal(err)
	}
	providerListener := newPipeListener()
	egressListener := newPipeListener()
	process := newScriptedPiProcess(providerListener)
	adapter := Adapter{
		Resolver:      testResolver{},
		CreateProcess: func(context.Context, agentruntime.SandboxRequest) (SandboxProcess, error) { return process, nil },
		NewProviderBridge: func(_ string, selected *provider.Transport, turns int) (*ProviderBridge, error) {
			return NewProviderBridgeWithListener(providerListener, selected, turns)
		},
		NewEgressListener: func(string) (net.Listener, string, error) { return egressListener, "", nil },
	}
	outcome := adapter.Execute(context.Background(), AdapterRequest{
		Workspace: workspace, Resources: resources, Configuration: configuration, Temporary: temporary,
		Permission: contract.PermissionReadOnly, Pi: agentruntime.PiConfiguration{Model: "gpt-test"}, Prompt: "prompt-canary",
		Network: contract.Network{Mode: contract.NetworkNone}, MaxTurns: 2, Timeout: time.Second, Transport: transport, SelectedProvider: contract.ProviderOpenAICompatible,
	})
	if !outcome.Success() || outcome.TurnsUsed != 1 || outcome.Result != "finished" {
		t.Fatalf("outcome = %#v", outcome)
	}
	if !process.terminated.Load() {
		t.Fatal("container process was not terminated during cleanup")
	}
}

func TestAdapterTimeoutUnblocksRPCAndTerminatesContainer(t *testing.T) {
	root := t.TempDir()
	configuration, temporary, resources, workspace := filepath.Join(root, "config"), filepath.Join(root, "tmp"), filepath.Join(root, "resources"), filepath.Join(root, "workspace")
	for _, directory := range []string{configuration, temporary, resources, workspace} {
		if err := os.Mkdir(directory, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	transport, err := provider.NewOpenAICompatible("https://provider.test/v1", "secret")
	if err != nil {
		t.Fatal(err)
	}
	providerListener, egressListener := newPipeListener(), newPipeListener()
	process := newHangingPiProcess()
	adapter := Adapter{
		Resolver: testResolver{}, CreateProcess: func(context.Context, agentruntime.SandboxRequest) (SandboxProcess, error) { return process, nil },
		NewProviderBridge: func(_ string, selected *provider.Transport, turns int) (*ProviderBridge, error) {
			return NewProviderBridgeWithListener(providerListener, selected, turns)
		},
		NewEgressListener: func(string) (net.Listener, string, error) { return egressListener, "", nil },
	}
	done := make(chan Outcome, 1)
	go func() {
		done <- adapter.Execute(context.Background(), AdapterRequest{
			Workspace: workspace, Resources: resources, Configuration: configuration, Temporary: temporary,
			Permission: contract.PermissionReadOnly, Pi: agentruntime.PiConfiguration{Model: "gpt-test"}, Prompt: "prompt",
			Network: contract.Network{Mode: contract.NetworkNone}, MaxTurns: 1, Timeout: 20 * time.Millisecond,
			Transport: transport, SelectedProvider: contract.ProviderOpenAICompatible,
		})
	}()
	select {
	case outcome := <-done:
		if outcome.ErrorType != contract.ErrorTimeout || !process.terminated.Load() {
			t.Fatalf("outcome=%#v terminated=%v", outcome, process.terminated.Load())
		}
	case <-time.After(200 * time.Millisecond):
		_ = process.Terminate(context.Background())
		<-done
		t.Fatal("timeout did not unblock Pi RPC")
	}
}

func TestAdapterCancellationUnblocksRPCAndTerminatesContainer(t *testing.T) {
	root := t.TempDir()
	configuration, temporary, resources, workspace := filepath.Join(root, "config"), filepath.Join(root, "tmp"), filepath.Join(root, "resources"), filepath.Join(root, "workspace")
	for _, directory := range []string{configuration, temporary, resources, workspace} {
		if err := os.Mkdir(directory, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	transport, err := provider.NewOpenAICompatible("https://provider.test/v1", "secret")
	if err != nil {
		t.Fatal(err)
	}
	providerListener, egressListener := newPipeListener(), newPipeListener()
	process := newHangingPiProcess()
	adapter := Adapter{
		Resolver: testResolver{}, CreateProcess: func(context.Context, agentruntime.SandboxRequest) (SandboxProcess, error) { return process, nil },
		NewProviderBridge: func(_ string, selected *provider.Transport, turns int) (*ProviderBridge, error) {
			return NewProviderBridgeWithListener(providerListener, selected, turns)
		},
		NewEgressListener: func(string) (net.Listener, string, error) { return egressListener, "", nil },
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan Outcome, 1)
	go func() {
		done <- adapter.Execute(ctx, AdapterRequest{
			Workspace: workspace, Resources: resources, Configuration: configuration, Temporary: temporary,
			Permission: contract.PermissionReadOnly, Pi: agentruntime.PiConfiguration{Model: "gpt-test"}, Prompt: "prompt",
			Network: contract.Network{Mode: contract.NetworkNone}, MaxTurns: 1, Timeout: time.Second,
			Transport: transport, SelectedProvider: contract.ProviderOpenAICompatible,
		})
	}()
	<-process.attached
	cancel()
	select {
	case outcome := <-done:
		if outcome.ErrorType != contract.ErrorCancelled || !process.terminated.Load() {
			t.Fatalf("outcome=%#v terminated=%v", outcome, process.terminated.Load())
		}
	case <-time.After(200 * time.Millisecond):
		_ = process.Terminate(context.Background())
		<-done
		t.Fatal("cancellation did not unblock Pi RPC")
	}
}

type testResolver struct{}

func (testResolver) LookupNetIP(context.Context, string, string) ([]net.IP, error) {
	return []net.IP{net.ParseIP("203.0.113.10")}, nil
}

type scriptedPiProcess struct {
	listener   *pipeListener
	terminated atomic.Bool
	done       chan struct{}
}

func newScriptedPiProcess(listener *pipeListener) *scriptedPiProcess {
	return &scriptedPiProcess{listener: listener, done: make(chan struct{})}
}

func (p *scriptedPiProcess) Attach(context.Context) (ProcessIO, error) {
	commandReader, commandWriter := io.Pipe()
	eventReader, eventWriter := io.Pipe()
	stderrReader, stderrWriter := io.Pipe()
	_ = stderrWriter.Close()
	go func() {
		defer close(p.done)
		defer func() { _ = eventWriter.Close() }()
		line, err := bufio.NewReader(commandReader).ReadBytes('\n')
		if err != nil || !strings.Contains(string(line), "prompt-canary") {
			return
		}
		client, server := net.Pipe()
		p.listener.connections <- server
		request := bridgeRequest{ID: "turn-1", Method: http.MethodPost, Target: "responses", Body: base64.StdEncoding.EncodeToString([]byte(`{"model":"gpt-test"}`))}
		encoded, _ := json.Marshal(request)
		_, _ = client.Write(append(encoded, '\n'))
		_, _ = bufio.NewReader(client).ReadBytes('\n')
		_ = client.Close()
		_, _ = io.WriteString(eventWriter, `{"type":"response","id":"agentrun-prompt","command":"prompt","success":true}`+"\n")
		_, _ = io.WriteString(eventWriter, `{"type":"agent_end","messages":[{"role":"assistant","content":[{"type":"text","text":"finished"}],"stopReason":"stop"}]}`+"\n")
	}()
	return ProcessIO{Stdin: commandWriter, Stdout: eventReader, Stderr: stderrReader}, nil
}

func (p *scriptedPiProcess) Wait(context.Context) (int, error) { <-p.done; return 0, nil }
func (p *scriptedPiProcess) Terminate(context.Context) error {
	p.terminated.Store(true)
	return nil
}

type hangingPiProcess struct {
	terminated atomic.Bool
	events     *io.PipeWriter
	commands   *io.PipeReader
	done       chan struct{}
	once       atomic.Bool
	attached   chan struct{}
}

func newHangingPiProcess() *hangingPiProcess {
	return &hangingPiProcess{done: make(chan struct{}), attached: make(chan struct{})}
}

func (p *hangingPiProcess) Attach(context.Context) (ProcessIO, error) {
	commandReader, commandWriter := io.Pipe()
	eventReader, eventWriter := io.Pipe()
	stderrReader, stderrWriter := io.Pipe()
	_ = stderrWriter.Close()
	p.events = eventWriter
	p.commands = commandReader
	close(p.attached)
	return ProcessIO{Stdin: commandWriter, Stdout: eventReader, Stderr: stderrReader}, nil
}

func (p *hangingPiProcess) Wait(context.Context) (int, error) { <-p.done; return 0, nil }
func (p *hangingPiProcess) Terminate(context.Context) error {
	p.terminated.Store(true)
	if p.once.CompareAndSwap(false, true) {
		if p.events != nil {
			_ = p.events.Close()
		}
		if p.commands != nil {
			_ = p.commands.Close()
		}
		close(p.done)
	}
	return nil
}
