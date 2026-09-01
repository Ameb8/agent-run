package contract

import (
	"encoding/json"
	"testing"
)

func TestV1Defaults(t *testing.T) {
	t.Parallel()

	got := V1Defaults()
	if got.NetworkMode != NetworkNone || got.MaxTurns != 25 || got.TimeoutS != 900 {
		t.Fatalf("V1Defaults() = %#v", got)
	}
}

func TestV1ErrorCategories(t *testing.T) {
	t.Parallel()

	categories := []ErrorCategory{
		ErrorValidation, ErrorConfiguration, ErrorAuthentication, ErrorProvider,
		ErrorTool, ErrorOutput, ErrorTimeout, ErrorLimit, ErrorCancelled,
		ErrorExecution, ErrorInternal,
	}
	if len(categories) != 11 {
		t.Fatalf("category count = %d, want 11", len(categories))
	}
}

func TestRunResultRepresentsStructuredOutput(t *testing.T) {
	t.Parallel()

	result := RunResult{
		SchemaVersion: ResultSchemaVersion,
		RunID:         "run-1",
		Status:        RunStatusSuccess,
		Result:        map[string]any{"approved": true},
		Runtime:       RuntimeIdentity{AgentRunVersion: "test"},
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	if !json.Valid(encoded) {
		t.Fatalf("invalid JSON: %s", encoded)
	}
}
