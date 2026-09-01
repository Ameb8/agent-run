package runtime

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"
)

const defaultCleanupTimeout = 10 * time.Second

var (
	// ErrTimeout, ErrCancelled, and ErrForcedTermination deliberately describe
	// lifecycle facts only. The run coordinator maps them to result categories.
	ErrTimeout           = errors.New("run timeout")
	ErrCancelled         = errors.New("run cancelled")
	ErrForcedTermination = errors.New("forced termination")
)

// Process is the full isolated runtime process tree. Implementations must
// make Terminate kill every descendant, not just a client process used to
// launch the runtime.
type Process interface {
	Start(context.Context) error
	Wait(context.Context) (int, error)
	Terminate(context.Context) error
}

// CleanupFunc removes private run artifacts after Process.Terminate. It must
// be safe after a setup failure and safe to call at most once.
type CleanupFunc func(context.Context) error

// PreparedRun is returned by preparation, which includes sandbox creation.
// Cleanup includes the run scope so configuration, temporary storage, and
// staged resources are removed on every handled outcome.
type PreparedRun struct {
	Process Process
	Cleanup CleanupFunc
}

// NewPreparedRun binds a process to its private scope. It is the normal
// construction path for the execution layer: Supervisor will terminate the
// process first, then this cleanup removes generated configuration, temporary
// storage, and the immutable resource snapshot. It deliberately never touches
// the separately mounted workspace, whose changes are not transactional.
func NewPreparedRun(process Process, scope *RunScope) PreparedRun {
	return PreparedRun{
		Process: process,
		Cleanup: func(ctx context.Context) error {
			if err := ctx.Err(); err != nil {
				return err
			}
			if scope == nil {
				return nil
			}
			return scope.Close()
		},
	}
}

// PrepareFunc must observe its context. This permits timeout and cancellation
// during Docker setup, before a runtime process has started.
type PrepareFunc func(context.Context) (PreparedRun, error)

// LifecycleFacts are transport-neutral facts for the later run coordinator.
// They intentionally do not decide an error category or emit a result.
type LifecycleFacts struct {
	TimedOut         bool
	Cancelled        bool
	Forced           bool
	SetupCompleted   bool
	RuntimeExited    bool
	RuntimeExitCode  int
	CleanupAttempted bool
	CleanupDuration  time.Duration
	CleanupTimedOut  bool
}

// Supervisor bounds one sandbox lifecycle. Timeout begins at entry, directly
// before PrepareFunc is called, and therefore excludes resolution, input
// loading, snapshotting, validation, and prompt rendering done by the caller.
// A second SIGINT/SIGTERM marks Forced: a CLI may then exit without producing a
// result object, as permitted by the v1 contract.
type Supervisor struct {
	Timeout        time.Duration
	CleanupTimeout time.Duration
	Signals        <-chan os.Signal
}

// Supervise runs preparation and the isolated runtime, then always attempts
// bounded cleanup. The cleanup budget is independent of the runtime context:
// once a timeout cancels docker wait, Docker rm still needs a short opportunity
// to terminate the container and remove its artifacts.
func (s Supervisor) Supervise(parent context.Context, prepare PrepareFunc) (LifecycleFacts, error) {
	var facts LifecycleFacts
	if prepare == nil {
		return facts, fmt.Errorf("prepare sandbox: unavailable")
	}
	if s.Timeout <= 0 {
		return facts, fmt.Errorf("run timeout must be positive")
	}
	cleanupTimeout := s.CleanupTimeout
	if cleanupTimeout <= 0 {
		cleanupTimeout = defaultCleanupTimeout
	}
	started := time.Now()

	work, cancel := context.WithCancel(parent)
	defer cancel()
	timer := time.NewTimer(s.Timeout)
	defer timer.Stop()
	signals, stopSignals := s.signalSource()
	defer stopSignals()

	type preparedResult struct {
		run PreparedRun
		err error
	}
	prepared := make(chan preparedResult, 1)
	go func() {
		run, err := prepare(work)
		prepared <- preparedResult{run: run, err: err}
	}()

	var (
		run      PreparedRun
		outcome  error
		stopOnce sync.Once
	)
	stop := func(reason error) {
		stopOnce.Do(func() {
			outcome = reason
			cancel()
		})
	}

	for {
		select {
		case result := <-prepared:
			run = result.run
			facts.SetupCompleted = result.err == nil && outcome == nil
			if outcome == nil {
				outcome = result.err
			}
			goto preparedDone
		case <-parent.Done():
			facts.Cancelled = true
			stop(ErrCancelled)
		case <-timer.C:
			facts.TimedOut = true
			stop(ErrTimeout)
		case <-signals:
			if facts.Cancelled || secondSignal(signals) {
				facts.Cancelled = true
				facts.Forced = true
				stop(ErrForcedTermination)
			} else {
				facts.Cancelled = true
				stop(ErrCancelled)
				// For real OS signals, restore the default disposition after the
				// graceful cancellation request. A second SIGINT/SIGTERM then
				// force-terminates AgentRun and may prevent its result object,
				// exactly as SIGKILL or a host crash may do. Injected signal
				// channels remain observable for deterministic callers and tests.
				if s.Signals == nil {
					stopSignals()
				}
			}
		}
	}

preparedDone:
	if run.Process != nil && outcome == nil {
		if err := run.Process.Start(work); err != nil {
			outcome = err
		} else {
			exitCode, err := s.wait(work, run.Process, timer.C, signals, &facts, stop, stopSignals)
			facts.RuntimeExited = err == nil
			facts.RuntimeExitCode = exitCode
			if outcome == nil {
				if err != nil {
					outcome = err
				} else if exitCode != 0 {
					outcome = &RuntimeExitError{ExitCode: exitCode}
				}
			}
		}
	}
	if run.Process != nil {
		// Even a normal exit receives Terminate: Docker rm --force is idempotent
		// for an exited container and removes the container artifact.
		run.Cleanup = chainCleanup(func(ctx context.Context) error { return run.Process.Terminate(ctx) }, run.Cleanup)
	}
	cleanupErr := cleanup(run.Cleanup, cleanupTimeout, &facts)
	// The run budget intentionally remains in force while cleanup runs. A
	// cleanup operation is granted its own short cancellation context so that a
	// timed-out docker wait cannot prevent removal of the container it left
	// behind, but time spent there still makes the overall run a timeout.
	if !facts.Cancelled && !facts.Forced && time.Since(started) >= s.Timeout {
		facts.TimedOut = true
		outcome = ErrTimeout
	}
	if outcome == nil && cleanupErr != nil {
		outcome = cleanupErr
	}
	return facts, outcome
}

func (s Supervisor) wait(work context.Context, process Process, timeout <-chan time.Time, signals <-chan os.Signal, facts *LifecycleFacts, stop func(error), stopSignals func()) (int, error) {
	// A waiter is needed so signals and the timer remain observable while docker
	// wait is blocked. Its result channel is buffered to avoid a leaked sender.
	// The work context cancellation unblocks the Docker client.
	type result struct {
		exitCode int
		err      error
	}
	done := make(chan result, 1)
	go func() {
		exitCode, err := process.Wait(work)
		done <- result{exitCode: exitCode, err: err}
	}()
	for {
		select {
		case result := <-done:
			return result.exitCode, result.err
		case <-work.Done():
			if facts.Forced {
				return 0, ErrForcedTermination
			}
			if facts.TimedOut {
				return 0, ErrTimeout
			}
			return 0, ErrCancelled
		case <-timeout:
			facts.TimedOut = true
			stop(ErrTimeout)
		case <-signals:
			if facts.Cancelled || secondSignal(signals) {
				facts.Cancelled = true
				facts.Forced = true
				stop(ErrForcedTermination)
			} else {
				facts.Cancelled = true
				stop(ErrCancelled)
				if s.Signals == nil {
					stopSignals()
				}
			}
		}
	}
}

func secondSignal(signals <-chan os.Signal) bool {
	select {
	case <-signals:
		return true
	default:
		return false
	}
}

// RuntimeExitError retains the raw exit fact without assigning a final result
// category; categorization is expressly owned by the run coordinator.
type RuntimeExitError struct{ ExitCode int }

func (e *RuntimeExitError) Error() string {
	return fmt.Sprintf("runtime exited with status %d", e.ExitCode)
}

func (s Supervisor) signalSource() (<-chan os.Signal, func()) {
	if s.Signals != nil {
		return s.Signals, func() {}
	}
	channel := make(chan os.Signal, 2)
	signal.Notify(channel, os.Interrupt, syscall.SIGTERM)
	return channel, func() { signal.Stop(channel) }
}

func chainCleanup(first, second CleanupFunc) CleanupFunc {
	return func(ctx context.Context) error {
		var firstErr, secondErr error
		if first != nil {
			firstErr = first(ctx)
		}
		if second != nil {
			secondErr = second(ctx)
		}
		return errors.Join(firstErr, secondErr)
	}
}

func cleanup(fn CleanupFunc, budget time.Duration, facts *LifecycleFacts) error {
	if fn == nil {
		return nil
	}
	facts.CleanupAttempted = true
	started := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), budget)
	defer cancel()
	err := fn(ctx)
	facts.CleanupDuration = time.Since(started)
	if ctx.Err() == context.DeadlineExceeded {
		facts.CleanupTimedOut = true
		if err == nil {
			err = ctx.Err()
		}
	}
	return err
}
