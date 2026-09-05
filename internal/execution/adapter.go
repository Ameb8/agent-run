package execution

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/Ameb8/agent-run/internal/contract"
	"github.com/Ameb8/agent-run/internal/egress"
	"github.com/Ameb8/agent-run/internal/output"
	"github.com/Ameb8/agent-run/internal/provider"
	agentruntime "github.com/Ameb8/agent-run/internal/runtime"
)

const ToolEgressSocket = "egress.sock"

type ProcessIO struct {
	Stdin  io.WriteCloser
	Stdout io.ReadCloser
	Stderr io.ReadCloser
}

// SandboxProcess is the narrow attached-container seam. Production wraps a
// runtime.Container; deterministic tests provide a scripted Pi peer.
type SandboxProcess interface {
	Attach(context.Context) (ProcessIO, error)
	Wait(context.Context) (int, error)
	Terminate(context.Context) error
}

type ProcessFactory func(context.Context, agentruntime.SandboxRequest) (SandboxProcess, error)

type Adapter struct {
	CreateProcess     ProcessFactory
	Resolver          egress.Resolver
	CleanupTimeout    time.Duration
	NewProviderBridge func(string, *provider.Transport, int) (*ProviderBridge, error)
	NewEgressListener func(string) (net.Listener, string, error)
}

type AdapterRequest struct {
	Workspace        string
	Resources        string
	Configuration    string
	Temporary        string
	Permission       contract.Permission
	WorkspacePackage bool
	Environment      agentruntime.Environment
	Pi               agentruntime.PiConfiguration
	Prompt           string
	Network          contract.Network
	MaxTurns         int
	Timeout          time.Duration
	Transport        *provider.Transport
	Validator        *output.Validator
	SelectedProvider contract.Provider
}

// Execute runs exactly one Pi process and owns every control/data-plane
// resource associated with it. All child bytes are consumed internally.
func (a Adapter) Execute(parent context.Context, request AdapterRequest) Outcome {
	if parent == nil {
		parent = context.Background()
	}
	if request.Timeout <= 0 || request.MaxTurns <= 0 || a.CreateProcess == nil || a.Resolver == nil {
		return failure(contract.ErrorConfiguration, "execution adapter is unavailable", 0)
	}
	ctx, cancel := context.WithTimeout(parent, request.Timeout)
	defer cancel()
	if outcome, stopped := stoppedOutcome(ctx, 0); stopped {
		return outcome
	}

	selectedProvider := request.SelectedProvider
	if selectedProvider == "" && request.Transport != nil {
		selectedProvider = request.Transport.Model().Provider
	}
	providerAdapter, err := agentruntime.GenerateProviderAdapter(request.Configuration, contract.Model{Provider: selectedProvider, Model: request.Pi.Model})
	if err != nil {
		return errorOutcome(err, 0)
	}
	request.Pi.ProviderAdapter = providerAdapter
	request.Pi.Provider = "agentrun"

	newProviderBridge := a.NewProviderBridge
	if newProviderBridge == nil {
		newProviderBridge = NewProviderBridge
	}
	providerBridge, err := newProviderBridge(request.Temporary, request.Transport, request.MaxTurns)
	if err != nil {
		return errorOutcome(err, 0)
	}
	defer func() { _ = providerBridge.Close() }()

	toolProxy, err := egress.New(request.Network, a.Resolver)
	if err != nil {
		return failure(contract.ErrorConfiguration, "tool egress policy is unavailable", 0)
	}
	newEgressListener := a.NewEgressListener
	if newEgressListener == nil {
		newEgressListener = listenToolEgress
	}
	egressListener, egressPath, err := newEgressListener(request.Temporary)
	if err != nil {
		return failure(contract.ErrorConfiguration, "create tool egress bridge", 0)
	}
	if egressPath != "" {
		if err := os.Chmod(egressPath, 0o600); err != nil {
			_ = egressListener.Close()
			_ = os.Remove(egressPath)
			return failure(contract.ErrorConfiguration, "secure tool egress bridge", 0)
		}
	}
	defer func() { _ = egressListener.Close(); _ = os.Remove(egressPath) }()

	bridgeErrors := make(chan error, 2)
	go func() { bridgeErrors <- providerBridge.Serve(ctx) }()
	go func() { bridgeErrors <- toolProxy.Serve(egressListener) }()

	process, err := a.CreateProcess(ctx, agentruntime.SandboxRequest{
		Workspace: request.Workspace, Resources: request.Resources, Configuration: request.Configuration, Temporary: request.Temporary,
		Permission: request.Permission, WorkspacePackage: request.WorkspacePackage, Environment: request.Environment,
		RuntimeEnvironment: request.Pi.Environment(), Command: request.Pi.Command(),
	})
	if err != nil {
		if outcome, stopped := stoppedOutcome(ctx, providerBridge.TurnsUsed()); stopped {
			return outcome
		}
		return errorOutcome(err, 0)
	}
	cleanupTimeout := a.CleanupTimeout
	if cleanupTimeout <= 0 {
		cleanupTimeout = 10 * time.Second
	}
	defer func() {
		cleanup, stop := context.WithTimeout(context.Background(), cleanupTimeout)
		defer stop()
		_ = process.Terminate(cleanup)
	}()

	streams, err := process.Attach(ctx)
	if err != nil {
		if outcome, stopped := stoppedOutcome(ctx, providerBridge.TurnsUsed()); stopped {
			return outcome
		}
		return failure(contract.ErrorExecution, "start Pi runtime", 0)
	}
	defer closeProcessIO(streams)
	stderrDone := make(chan struct{})
	go func() { _, _ = io.Copy(io.Discard, streams.Stderr); close(stderrDone) }()
	waitDone := make(chan error, 1)
	go func() {
		code, waitErr := process.Wait(ctx)
		if waitErr == nil && code != 0 {
			waitErr = executionError("Pi runtime exited unsuccessfully")
		}
		waitDone <- waitErr
	}()

	type rpcResult struct {
		final string
		err   error
	}
	rpcDone := make(chan rpcResult, 1)
	go func() {
		final, rpcErr := RunPiRPC(ctx, streams.Stdout, streams.Stdin, request.Prompt)
		rpcDone <- rpcResult{final: final, err: rpcErr}
	}()
	var final string
	var rpcErr error
	select {
	case result := <-rpcDone:
		final, rpcErr = result.final, result.err
	case <-ctx.Done():
		turns := providerBridge.TurnsUsed()
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return failure(contract.ErrorTimeout, "run timeout reached", turns)
		}
		return failure(contract.ErrorCancelled, "run cancelled", turns)
	}
	turns := providerBridge.TurnsUsed()
	if ctx.Err() != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return failure(contract.ErrorTimeout, "run timeout reached", turns)
		}
		return failure(contract.ErrorCancelled, "run cancelled", turns)
	}
	if bridgeErr := providerBridge.Err(); bridgeErr != nil {
		return errorOutcome(bridgeErr, turns)
	}
	select {
	case bridgeErr := <-bridgeErrors:
		if bridgeErr != nil && !errors.Is(bridgeErr, context.Canceled) && !errors.Is(bridgeErr, net.ErrClosed) && !errors.Is(bridgeErr, http.ErrServerClosed) {
			return errorOutcome(bridgeErr, turns)
		}
	default:
	}
	if rpcErr != nil {
		select {
		case waitErr := <-waitDone:
			if waitErr != nil {
				return errorOutcome(waitErr, turns)
			}
		default:
		}
		return errorOutcome(rpcErr, turns)
	}
	if turns == 0 {
		return failure(contract.ErrorExecution, "Pi completed without a provider request", 0)
	}
	return Finalize(Outcome{Final: &final, TurnsUsed: turns}, request.Validator)
}

func stoppedOutcome(ctx context.Context, turns int) (Outcome, bool) {
	category, message, stopped := contextFailure(ctx)
	if !stopped {
		return Outcome{}, false
	}
	return failure(category, message, turns), true
}

func listenToolEgress(temporary string) (net.Listener, string, error) {
	path := filepath.Join(temporary, ToolEgressSocket)
	_ = os.Remove(path)
	listener, err := net.Listen("unix", path)
	return listener, path, err
}

func closeProcessIO(streams ProcessIO) {
	var group sync.WaitGroup
	for _, closer := range []io.Closer{streams.Stdin, streams.Stdout, streams.Stderr} {
		if closer != nil {
			group.Add(1)
			go func(value io.Closer) { defer group.Done(); _ = value.Close() }(closer)
		}
	}
	group.Wait()
}

type dockerProcess struct {
	container agentruntime.Container
	attached  *agentruntime.AttachedProcess
}

func (p *dockerProcess) Attach(ctx context.Context) (ProcessIO, error) {
	attached, err := p.container.AttachAndStart(ctx)
	if err != nil {
		return ProcessIO{}, err
	}
	p.attached = attached
	return ProcessIO{Stdin: attached.Stdin, Stdout: attached.Stdout, Stderr: attached.Stderr}, nil
}

func (p *dockerProcess) Wait(ctx context.Context) (int, error) { return p.container.Wait(ctx) }
func (p *dockerProcess) Terminate(ctx context.Context) error {
	if p.attached != nil {
		p.attached.Close()
	}
	return p.container.Terminate(ctx)
}

func DockerProcessFactory(sandbox agentruntime.DockerSandbox) ProcessFactory {
	return func(ctx context.Context, request agentruntime.SandboxRequest) (SandboxProcess, error) {
		container, err := sandbox.Create(ctx, request)
		if err != nil {
			return nil, err
		}
		return &dockerProcess{container: container}, nil
	}
}
