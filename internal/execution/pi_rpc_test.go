package execution

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/Ameb8/agent-run/internal/contract"
)

func TestRunPiRPCReturnsFinalAssistantTextAfterPromptAcknowledgement(t *testing.T) {
	input := strings.Join([]string{
		`{"type":"response","id":"agentrun-prompt","command":"prompt","success":true}`,
		`{"type":"agent_start"}`,
		`{"type":"turn_end","message":{"role":"assistant","content":[{"type":"toolCall","id":"one","name":"read","arguments":{"path":"README.md"}}],"stopReason":"toolUse"},"toolResults":[]}`,
		`{"type":"agent_end","messages":[{"role":"assistant","content":[{"type":"text","text":"finished"}],"stopReason":"stop"}]}`,
	}, "\n") + "\n"
	var commands bytes.Buffer

	final, err := RunPiRPC(context.Background(), strings.NewReader(input), &commands, "inspect this")
	if err != nil {
		t.Fatal(err)
	}
	if final != "finished" {
		t.Fatalf("final = %q", final)
	}
	if got := commands.String(); got != `{"id":"agentrun-prompt","type":"prompt","message":"inspect this"}`+"\n" {
		t.Fatalf("command = %q", got)
	}
}

func TestRunPiRPCRejectsPromptFailureAndMalformedOrFailedFinalEvents(t *testing.T) {
	tests := []struct {
		name  string
		input string
	}{
		{"prompt rejected", `{"type":"response","id":"agentrun-prompt","command":"prompt","success":false,"error":"private"}` + "\n"},
		{"malformed", "not-json\n"},
		{"provider failed", strings.Join([]string{
			`{"type":"response","id":"agentrun-prompt","command":"prompt","success":true}`,
			`{"type":"agent_end","messages":[{"role":"assistant","content":[],"stopReason":"error","errorMessage":"private"}]}`,
		}, "\n") + "\n"},
		{"no assistant", strings.Join([]string{
			`{"type":"response","id":"agentrun-prompt","command":"prompt","success":true}`,
			`{"type":"agent_end","messages":[]}`,
		}, "\n") + "\n"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := RunPiRPC(context.Background(), strings.NewReader(test.input), &bytes.Buffer{}, "prompt-canary")
			if err == nil || strings.Contains(err.Error(), "private") || strings.Contains(err.Error(), "prompt-canary") {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func TestRunPiRPCMapsTerminalToolFailureToToolCategory(t *testing.T) {
	input := strings.Join([]string{
		`{"type":"response","id":"agentrun-prompt","command":"prompt","success":true}`,
		`{"type":"tool_execution_end","toolCallId":"one","toolName":"read","isError":true,"result":{"content":[{"type":"text","text":"private"}]}}`,
		`{"type":"agent_end","messages":[{"role":"assistant","content":[{"type":"toolCall","id":"one","name":"read","arguments":{}}],"stopReason":"toolUse"}]}`,
	}, "\n") + "\n"
	_, err := RunPiRPC(context.Background(), strings.NewReader(input), &bytes.Buffer{}, "prompt")
	var command *contract.CommandError
	if !errors.As(err, &command) || command.Category != contract.ErrorTool || strings.Contains(err.Error(), "private") {
		t.Fatalf("error = %v", err)
	}
}

func TestRunPiRPCLimitsOversizedFrames(t *testing.T) {
	var commands bytes.Buffer
	_, err := RunPiRPC(context.Background(), strings.NewReader(strings.Repeat("x", MaxRPCFrameBytes+1)), &commands, "prompt")
	var command *contract.CommandError
	if !errors.As(err, &command) || command.Category != contract.ErrorLimit {
		t.Fatalf("error = %v", err)
	}
}
