package execution

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"

	"github.com/Ameb8/agent-run/internal/contract"
)

const promptCommandID = "agentrun-prompt"

type rpcFrame struct {
	Type     string          `json:"type"`
	ID       string          `json:"id,omitempty"`
	Command  string          `json:"command,omitempty"`
	Success  bool            `json:"success,omitempty"`
	Messages []rpcMessage    `json:"messages,omitempty"`
	Message  json.RawMessage `json:"message,omitempty"`
	IsError  bool            `json:"isError,omitempty"`
}

type rpcMessage struct {
	Role    string `json:"role"`
	Content []struct {
		Type string `json:"type"`
		Text string `json:"text,omitempty"`
	} `json:"content"`
	StopReason string `json:"stopReason,omitempty"`
}

// RunPiRPC drives one Pi 0.74 prompt over its LF-delimited control stream.
// Prompt acknowledgement is control-plane state only; provider acceptance and
// turn accounting remain exclusively at ProviderBridge.
func RunPiRPC(ctx context.Context, reader io.Reader, writer io.Writer, prompt string) (string, error) {
	if ctx == nil || reader == nil || writer == nil {
		return "", executionError("Pi RPC stream is unavailable")
	}
	stream := NewJSONL(reader, writer)
	if err := stream.Write(struct {
		ID      string `json:"id"`
		Type    string `json:"type"`
		Message string `json:"message"`
	}{ID: promptCommandID, Type: "prompt", Message: prompt}); err != nil {
		return "", executionError("write Pi RPC prompt")
	}

	acknowledged := false
	terminalToolFailure := false
	for {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		var frame rpcFrame
		if err := stream.Read(&frame); err != nil {
			if errors.Is(err, ErrJSONLFrameTooLarge) {
				return "", &contract.CommandError{Category: contract.ErrorLimit, Message: "Pi RPC output exceeds limit"}
			}
			return "", executionError("read Pi RPC stream")
		}
		switch frame.Type {
		case "response":
			if frame.ID != promptCommandID || frame.Command != "prompt" {
				continue
			}
			if !frame.Success {
				return "", executionError("Pi rejected the initial prompt")
			}
			acknowledged = true
		case "extension_error":
			return "", executionError("Pi extension failed")
		case "tool_execution_end":
			terminalToolFailure = terminalToolFailure || frame.IsError
		case "agent_end":
			if !acknowledged {
				return "", executionError("Pi ended before acknowledging the prompt")
			}
			return finalAssistantText(frame.Messages, terminalToolFailure)
		}
	}
}

func finalAssistantText(messages []rpcMessage, terminalToolFailure bool) (string, error) {
	for index := len(messages) - 1; index >= 0; index-- {
		message := messages[index]
		if message.Role != "assistant" {
			continue
		}
		if message.StopReason == "error" || message.StopReason == "aborted" {
			return "", executionError("Pi model request failed")
		}
		if message.StopReason == "toolUse" {
			if terminalToolFailure {
				return "", &contract.CommandError{Category: contract.ErrorTool, Message: "allowed tool failed"}
			}
			return "", executionError("Pi ended while a tool continuation was required")
		}
		var result strings.Builder
		for _, content := range message.Content {
			if content.Type == "text" {
				result.WriteString(content.Text)
			}
		}
		return result.String(), nil
	}
	return "", executionError("Pi ended without a final assistant response")
}

func executionError(message string) error {
	return &contract.CommandError{Category: contract.ErrorExecution, Message: message}
}
