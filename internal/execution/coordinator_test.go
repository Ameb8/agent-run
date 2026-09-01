package execution

import (
	"context"
	"errors"
	"testing"

	"github.com/Ameb8/agent-run/internal/contract"
	"github.com/Ameb8/agent-run/internal/output"
)

type scriptedDriver struct {
	responses []Response
	errors    []error
	requests  []Request
	onRound   func(int)
}

func (d *scriptedDriver) Round(_ context.Context, request Request) (Response, error) {
	i := len(d.requests)
	d.requests = append(d.requests, request)
	if d.onRound != nil {
		d.onRound(i)
	}
	var response Response
	if i < len(d.responses) {
		response = d.responses[i]
	}
	if i < len(d.errors) {
		return response, d.errors[i]
	}
	return response, nil
}

func final(value string) *string { return &value }

func TestCoordinateCountsAcceptedRequestsAndToolFollowUps(t *testing.T) {
	driver := &scriptedDriver{responses: []Response{
		{Accepted: true, Continue: true},
		{Accepted: true, Final: final("done")},
	}}
	got := Coordinate(context.Background(), 2, driver)
	if !got.Success() || got.TurnsUsed != 2 || *got.Final != "done" {
		t.Fatalf("outcome = %#v", got)
	}
	if len(driver.requests) != 2 || !driver.requests[0].Initial || driver.requests[1].Initial {
		t.Fatalf("requests = %#v", driver.requests)
	}
}

func TestCoordinateDoesNotSendRequestPastTurnLimit(t *testing.T) {
	driver := &scriptedDriver{responses: []Response{{Accepted: true, Continue: true}}}
	got := Coordinate(context.Background(), 1, driver)
	if got.ErrorType != contract.ErrorLimit || got.TurnsUsed != 1 || len(driver.requests) != 1 {
		t.Fatalf("outcome=%#v requests=%d", got, len(driver.requests))
	}
}

func TestCoordinateCountsAcceptedFailedRequest(t *testing.T) {
	driver := &scriptedDriver{
		responses: []Response{{Accepted: true}},
		errors:    []error{&contract.CommandError{Category: contract.ErrorProvider, Message: "provider failed"}},
	}
	got := Coordinate(context.Background(), 2, driver)
	if got.ErrorType != contract.ErrorProvider || got.TurnsUsed != 1 {
		t.Fatalf("outcome=%#v", got)
	}
}

func TestCoordinateRejectedRequestDoesNotConsumeTurn(t *testing.T) {
	driver := &scriptedDriver{errors: []error{&contract.CommandError{Category: contract.ErrorProvider, Message: "rejected"}}}
	got := Coordinate(context.Background(), 2, driver)
	if got.ErrorType != contract.ErrorProvider || got.TurnsUsed != 0 {
		t.Fatalf("outcome=%#v", got)
	}
}

func TestCoordinateTimeoutWinsOutputAndLimitRace(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	driver := &scriptedDriver{responses: []Response{{Accepted: true, Final: final("done")}}, onRound: func(int) { cancel() }}
	got := Coordinate(ctx, 1, driver)
	if got.ErrorType != contract.ErrorCancelled || got.TurnsUsed != 1 {
		t.Fatalf("outcome=%#v", got)
	}

	deadline, deadlineCancel := context.WithTimeout(context.Background(), 0)
	defer deadlineCancel()
	got = Coordinate(deadline, 1, &scriptedDriver{})
	if got.ErrorType != contract.ErrorTimeout || got.TurnsUsed != 0 {
		t.Fatalf("outcome=%#v", got)
	}
}

func TestCoordinateLimitsFinalOutput(t *testing.T) {
	large := string(make([]byte, MaxFinalOutputBytes+1))
	got := Coordinate(context.Background(), 1, &scriptedDriver{responses: []Response{{Accepted: true, Final: &large}}})
	if got.ErrorType != contract.ErrorLimit || got.TurnsUsed != 1 {
		t.Fatalf("outcome=%#v", got)
	}
}

func TestCoordinateMapsUnknownFailureToExecution(t *testing.T) {
	got := Coordinate(context.Background(), 1, &scriptedDriver{errors: []error{errors.New("private")}})
	if got.ErrorType != contract.ErrorExecution || got.Error == "private" {
		t.Fatalf("outcome=%#v", got)
	}
}

func TestFinalizePreservesUnstructuredFinalOutput(t *testing.T) {
	got := Finalize(Outcome{Final: final("not JSON"), TurnsUsed: 1}, nil)
	if !got.Success() || got.Result != "not JSON" || got.TurnsUsed != 1 {
		t.Fatalf("outcome = %#v", got)
	}
}

func TestFinalizeMapsInvalidStructuredOutputToOutput(t *testing.T) {
	validator, err := output.Compile([]byte(`{"type":"object","required":["ok"]}`))
	if err != nil {
		t.Fatal(err)
	}
	for _, response := range []string{"not JSON", `{"missing":true}`, "```json\n{}\n```"} {
		got := Finalize(Outcome{Final: final(response), TurnsUsed: 2}, validator)
		if got.Success() || got.ErrorType != contract.ErrorOutput || got.TurnsUsed != 2 || got.Final != nil {
			t.Errorf("Finalize(%q) = %#v", response, got)
		}
	}
}

func TestFinalizeReturnsParsedStructuredValue(t *testing.T) {
	validator, err := output.Compile([]byte(`{"type":"array","items":{"type":"number"}}`))
	if err != nil {
		t.Fatal(err)
	}
	got := Finalize(Outcome{Final: final(`[1,2]`), TurnsUsed: 1}, validator)
	if !got.Success() || got.Result == nil {
		t.Fatalf("outcome = %#v", got)
	}
}
