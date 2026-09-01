package contract

import (
	"encoding/json"
	"os"
	"testing"
)

func TestV1Defaults(t *testing.T) {
	t.Parallel()

	got := V1Defaults()
	if got.NetworkMode != NetworkNone || got.MaxTurns != 25 || got.TimeoutS != 900 {
		t.Fatalf("V1Defaults() = %#v", got)
	}
}

func TestRunResultGoldenFixtures(t *testing.T) {
	contents, err := os.ReadFile("testdata/run_results.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixtures []struct {
		Name         string          `json:"name"`
		Status       RunStatus       `json:"status"`
		Result       json.RawMessage `json:"result"`
		ErrorType    ErrorCategory   `json:"error_type"`
		Error        string          `json:"error"`
		OmitIdentity bool            `json:"omit_identity"`
	}
	if err := json.Unmarshal(contents, &fixtures); err != nil {
		t.Fatal(err)
	}
	for _, fixture := range fixtures {
		t.Run(fixture.Name, func(t *testing.T) {
			result := RunResult{SchemaVersion: ResultSchemaVersion, RunID: "run-fixture", Status: fixture.Status, Runtime: RuntimeIdentity{AgentRunVersion: "test"}, ErrorType: fixture.ErrorType, Error: fixture.Error}
			if !fixture.OmitIdentity {
				result.Agent = &PackageIdentity{Name: "agent", Digest: "sha256:abc"}
				result.Model = &ModelIdentity{Provider: ProviderOpenAICompatible, Requested: "model"}
			}
			if fixture.Status == RunStatusSuccess {
				if err := json.Unmarshal(fixture.Result, &result.Result); err != nil {
					t.Fatal(err)
				}
			}
			encoded, err := json.Marshal(result)
			if err != nil {
				t.Fatal(err)
			}
			var got map[string]any
			if err := json.Unmarshal(encoded, &got); err != nil {
				t.Fatal(err)
			}
			if got["status"] != string(fixture.Status) {
				t.Fatalf("serialized fixture = %s", encoded)
			}
			if fixture.Status == RunStatusSuccess {
				if _, ok := got["result"]; !ok {
					t.Fatalf("success omitted result: %s", encoded)
				}
			} else {
				if got["error_type"] != string(fixture.ErrorType) || got["error"] != fixture.Error {
					t.Fatalf("failure category/detail = %s", encoded)
				}
				if _, ok := got["result"]; ok {
					t.Fatalf("failure includes result: %s", encoded)
				}
			}
			if fixture.OmitIdentity && (got["agent"] != nil || got["model"] != nil) {
				t.Fatalf("early failure resolved identity: %s", encoded)
			}
		})
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

func TestRunResultTerminalShapesAreConditional(t *testing.T) {
	t.Parallel()
	runtime := RuntimeIdentity{AgentRunVersion: "test", PiVersion: "pi", JavaScriptVersion: "js", ImageDigest: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}
	success, err := json.Marshal(RunResult{SchemaVersion: ResultSchemaVersion, RunID: "run-1", Status: RunStatusSuccess, Runtime: runtime, Result: nil})
	if err != nil {
		t.Fatal(err)
	}
	var successObject map[string]any
	if err := json.Unmarshal(success, &successObject); err != nil {
		t.Fatal(err)
	}
	if _, ok := successObject["result"]; !ok || successObject["error"] != nil || successObject["error_type"] != nil {
		t.Fatalf("success = %s", success)
	}

	for _, category := range []ErrorCategory{ErrorValidation, ErrorConfiguration, ErrorAuthentication, ErrorProvider, ErrorTool, ErrorOutput, ErrorTimeout, ErrorLimit, ErrorCancelled, ErrorExecution, ErrorInternal} {
		failure, err := json.Marshal(RunResult{SchemaVersion: ResultSchemaVersion, RunID: "run-1", Status: RunStatusFailure, Runtime: runtime, ErrorType: category, Error: "safe"})
		if err != nil {
			t.Fatal(err)
		}
		var object map[string]any
		if err := json.Unmarshal(failure, &object); err != nil {
			t.Fatal(err)
		}
		if object["error_type"] != string(category) || object["error"] != "safe" {
			t.Errorf("failure %s = %s", category, failure)
		}
		if _, ok := object["result"]; ok {
			t.Errorf("failure %s includes result: %s", category, failure)
		}
	}
}
