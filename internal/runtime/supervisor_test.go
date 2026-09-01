package runtime

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func TestSupervisorCleansNormalAndFailedRuntimeExits(t *testing.T) {
	t.Parallel()
	for _, exitCode := range []int{0, 23} {
		t.Run("exit", func(t *testing.T) {
			process := &fakeProcess{exitCode: exitCode}
			cleaned := false
			facts, err := (Supervisor{Timeout: time.Second}).Supervise(context.Background(), func(context.Context) (PreparedRun, error) {
				return PreparedRun{Process: process, Cleanup: func(context.Context) error { cleaned = true; return nil }}, nil
			})
			if !facts.RuntimeExited || facts.RuntimeExitCode != exitCode || !facts.CleanupAttempted || !cleaned || process.terminations != 1 {
				t.Fatalf("facts=%+v cleaned=%t terminated=%d", facts, cleaned, process.terminations)
			}
			if exitCode == 0 && err != nil {
				t.Fatal(err)
			}
			var runtimeExit *RuntimeExitError
			if exitCode != 0 && (!errors.As(err, &runtimeExit) || runtimeExit.ExitCode != exitCode) {
				t.Fatalf("error=%v", err)
			}
		})
	}
}

func TestSupervisorTimeoutCancelsHangingSetupAndCleansReturnedArtifacts(t *testing.T) {
	t.Parallel()
	process := &fakeProcess{}
	cleaned := false
	facts, err := (Supervisor{Timeout: 20 * time.Millisecond}).Supervise(context.Background(), func(ctx context.Context) (PreparedRun, error) {
		<-ctx.Done()
		return PreparedRun{Process: process, Cleanup: func(context.Context) error { cleaned = true; return nil }}, nil
	})
	if !errors.Is(err, ErrTimeout) || !facts.TimedOut || facts.SetupCompleted || !facts.CleanupAttempted || !cleaned || process.terminations != 1 {
		t.Fatalf("facts=%+v error=%v cleaned=%t terminated=%d", facts, err, cleaned, process.terminations)
	}
}

func TestSupervisorCancelsRuntimeOnSIGINTAndSIGTERM(t *testing.T) {
	t.Parallel()
	for _, signal := range []os.Signal{os.Interrupt, syscall.SIGTERM} {
		t.Run(signal.String(), func(t *testing.T) {
			signals := make(chan os.Signal, 1)
			process := &fakeProcess{wait: make(chan struct{}), started: make(chan struct{})}
			cleaned := false
			go func() {
				<-process.started
				signals <- signal
			}()
			facts, err := (Supervisor{Timeout: time.Second, Signals: signals}).Supervise(context.Background(), func(context.Context) (PreparedRun, error) {
				return PreparedRun{Process: process, Cleanup: func(context.Context) error { cleaned = true; return nil }}, nil
			})
			if !errors.Is(err, ErrCancelled) || !facts.Cancelled || !facts.CleanupAttempted || !cleaned || process.terminations != 1 {
				t.Fatalf("facts=%+v error=%v cleaned=%t terminated=%d", facts, err, cleaned, process.terminations)
			}
		})
	}
}

func TestSupervisorBoundsCleanup(t *testing.T) {
	t.Parallel()
	process := &fakeProcess{}
	facts, err := (Supervisor{Timeout: time.Second, CleanupTimeout: 20 * time.Millisecond}).Supervise(context.Background(), func(context.Context) (PreparedRun, error) {
		return PreparedRun{Process: process, Cleanup: func(ctx context.Context) error { <-ctx.Done(); return ctx.Err() }}, nil
	})
	if !facts.CleanupTimedOut || facts.CleanupDuration < 15*time.Millisecond || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("facts=%+v error=%v", facts, err)
	}
}

func TestSupervisorTimeoutIncludesCleanup(t *testing.T) {
	t.Parallel()
	facts, err := (Supervisor{Timeout: 20 * time.Millisecond, CleanupTimeout: time.Second}).Supervise(context.Background(), func(context.Context) (PreparedRun, error) {
		return PreparedRun{Process: &fakeProcess{}, Cleanup: func(context.Context) error {
			time.Sleep(25 * time.Millisecond)
			return nil
		}}, nil
	})
	if !facts.TimedOut || !errors.Is(err, ErrTimeout) {
		t.Fatalf("facts=%+v error=%v", facts, err)
	}
}

func TestSupervisorRecordsSecondCancellationSignalAsForced(t *testing.T) {
	t.Parallel()
	signals := make(chan os.Signal, 2)
	signals <- os.Interrupt
	signals <- syscall.SIGTERM
	process := &fakeProcess{}
	facts, err := (Supervisor{Timeout: time.Second, Signals: signals}).Supervise(context.Background(), func(context.Context) (PreparedRun, error) {
		return PreparedRun{Process: process}, nil
	})
	if !errors.Is(err, ErrForcedTermination) || !facts.Forced || !facts.Cancelled || process.terminations != 1 {
		t.Fatalf("facts=%+v error=%v terminated=%d", facts, err, process.terminations)
	}
}

func TestNewPreparedRunRemovesPrivateScopeButNotWorkspace(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	workspace := filepath.Join(root, "workspace")
	if err := os.Mkdir(workspace, 0o700); err != nil {
		t.Fatal(err)
	}
	workspaceFile := filepath.Join(workspace, "changed-by-runtime")
	if err := os.WriteFile(workspaceFile, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	scope, err := NewRunScope()
	if err != nil {
		t.Fatal(err)
	}
	rootPath := scope.Root
	if err := os.WriteFile(filepath.Join(scope.Configuration, "private-config"), []byte("remove"), 0o600); err != nil {
		t.Fatal(err)
	}
	process := &fakeProcess{}
	_, err = (Supervisor{Timeout: time.Second}).Supervise(context.Background(), func(context.Context) (PreparedRun, error) {
		return NewPreparedRun(process, scope), nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(rootPath); !os.IsNotExist(err) {
		t.Fatalf("private scope remains: %v", err)
	}
	if contents, err := os.ReadFile(workspaceFile); err != nil || string(contents) != "keep" {
		t.Fatalf("workspace was removed or changed: %q, %v", contents, err)
	}
}

type fakeProcess struct {
	exitCode     int
	startErr     error
	wait         chan struct{}
	started      chan struct{}
	terminations int
}

func (p *fakeProcess) Start(context.Context) error {
	if p.started == nil {
		p.started = make(chan struct{})
	}
	close(p.started)
	return p.startErr
}

func (p *fakeProcess) Wait(ctx context.Context) (int, error) {
	if p.wait == nil {
		return p.exitCode, nil
	}
	select {
	case <-p.wait:
		return p.exitCode, nil
	case <-ctx.Done():
		return 0, ctx.Err()
	}
}

func (p *fakeProcess) Terminate(context.Context) error { p.terminations++; return nil }
