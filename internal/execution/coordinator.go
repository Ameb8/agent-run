// Package execution coordinates one already-prepared agent run.
//
// It deliberately knows nothing about Docker, Pi's wire protocol, or result
// serialization.  Those adapters report provider acceptance and whether the
// runtime has reached a final response; this package owns the v1 turn and
// terminal-outcome rules shared by all adapters.
package execution

import (
	"context"
	"errors"
	"fmt"

	"github.com/Ameb8/agent-run/internal/contract"
)

// MaxFinalOutputBytes bounds an unstructured final response retained by the
// host.  It matches the rendered-prompt bound: neither side of one model turn
// may make AgentRun retain an unbounded payload.
const MaxFinalOutputBytes = 32 << 20

// Request describes one request the runtime is about to send to its selected
// provider. Initial is true only for the rendered prompt request. Tool results
// are opaque to the coordinator: a follow-up is still exactly one turn.
type Request struct {
	Initial bool
}

// Response is the factual result of attempting one provider request.
// Accepted must be set as soon as the provider has accepted the request, even
// when decoding its response or executing a later tool fails. Continue means
// the runtime has tool results to submit in a subsequent request. A Final is a
// normal agent final response; an empty string is a valid final response.
type Response struct {
	Accepted bool
	Continue bool
	Final    *string
}

// Driver performs one model request and any tool activity needed to decide
// whether another request is required. It must not retry policy decisions;
// retries of the same request stay inside one call and do not create a turn.
// It may return a *contract.CommandError for provider, tool, or execution
// facts. Recoverable tool errors should be supplied to the model and reported
// by returning Continue rather than an error.
type Driver interface {
	Round(context.Context, Request) (Response, error)
}

// Outcome is the one terminal execution record handed to result serialization.
// Exactly one of Final and ErrorType is set.
type Outcome struct {
	Final     *string
	ErrorType contract.ErrorCategory
	Error     string
	TurnsUsed int
}

func (o Outcome) Success() bool { return o.Final != nil && o.ErrorType == "" }

// Coordinate runs the model/tool loop. It checks the turn budget before every
// request and deliberately checks context termination after each round as
// well, giving TIMEOUT precedence over a simultaneous output/turn failure.
func Coordinate(ctx context.Context, maxTurns int, driver Driver) Outcome {
	if maxTurns <= 0 {
		return failure(contract.ErrorLimit, "maximum turns must be positive", 0)
	}
	if driver == nil {
		return failure(contract.ErrorExecution, "runtime driver is unavailable", 0)
	}

	turns := 0
	initial := true
	for {
		if category, message, stopped := contextFailure(ctx); stopped {
			return failure(category, message, turns)
		}
		if turns >= maxTurns {
			return failure(contract.ErrorLimit, fmt.Sprintf("maximum turns (%d) reached", maxTurns), turns)
		}

		response, err := driver.Round(ctx, Request{Initial: initial})
		initial = false
		if response.Accepted {
			turns++
		}
		// A timeout observed alongside any response/error wins by §7.4.1.
		if category, message, stopped := contextFailure(ctx); stopped {
			return failure(category, message, turns)
		}
		if err != nil {
			return errorOutcome(err, turns)
		}
		if response.Final != nil {
			if len(*response.Final) > MaxFinalOutputBytes {
				return failure(contract.ErrorLimit, fmt.Sprintf("final output exceeds %d bytes", MaxFinalOutputBytes), turns)
			}
			return Outcome{Final: response.Final, TurnsUsed: turns}
		}
		if !response.Continue {
			return failure(contract.ErrorExecution, "runtime ended without a final response", turns)
		}
	}
}

func contextFailure(ctx context.Context) (contract.ErrorCategory, string, bool) {
	if ctx == nil || ctx.Err() == nil {
		return "", "", false
	}
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return contract.ErrorTimeout, "run timeout reached", true
	}
	return contract.ErrorCancelled, "run cancelled", true
}

func errorOutcome(err error, turns int) Outcome {
	var command *contract.CommandError
	if errors.As(err, &command) && command.Category != "" {
		return failure(command.Category, command.Message, turns)
	}
	return failure(contract.ErrorExecution, "runtime execution failed", turns)
}

func failure(category contract.ErrorCategory, message string, turns int) Outcome {
	return Outcome{ErrorType: category, Error: message, TurnsUsed: turns}
}
