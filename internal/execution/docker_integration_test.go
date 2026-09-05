//go:build linux

package execution

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Ameb8/agent-run/internal/contract"
	"github.com/Ameb8/agent-run/internal/provider"
	agentruntime "github.com/Ameb8/agent-run/internal/runtime"
)

func TestDockerPiRPCFinalAndToolRound(t *testing.T) {
	if os.Getenv("AGENTRUN_DOCKER_E2E") != "1" {
		t.Skip("set AGENTRUN_DOCKER_E2E=1 after building the pinned runtime image")
	}
	manifest, err := agentruntime.LoadManifest()
	if err != nil {
		t.Fatal(err)
	}
	image, err := manifest.ImageFor(runtime.GOARCH)
	if err != nil {
		t.Fatal(err)
	}
	if err := exec.Command("docker", "image", "inspect", image.Image).Run(); err != nil {
		t.Skipf("pinned runtime image unavailable: %v", err)
	}
	verifier := agentruntime.Verifier{Manifest: manifest, Inspector: integrationImageInspector{image.ImageDigest, runtime.GOARCH}, Version: "integration"}
	adapter := Adapter{CreateProcess: DockerProcessFactory(agentruntime.NewDockerSandbox(verifier)), Resolver: integrationResolver{}}

	for _, test := range []struct {
		name       string
		tools      []string
		responses  []string
		wantTurns  int
		wantResult string
	}{
		{name: "final", responses: []string{responseTextSSE("done")}, wantTurns: 1, wantResult: "done"},
		{name: "read tool", tools: []string{"read"}, responses: []string{responseToolSSE("read", `{"path":"README.md"}`), responseTextSSE("read complete")}, wantTurns: 2, wantResult: "read complete"},
	} {
		t.Run(test.name, func(t *testing.T) {
			var requests atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				index := int(requests.Add(1)) - 1
				if request.URL.Path != "/v1/responses" || index >= len(test.responses) {
					http.Error(writer, "unexpected request", http.StatusBadRequest)
					return
				}
				if index == 1 {
					var body map[string]any
					if json.NewDecoder(request.Body).Decode(&body) != nil || !strings.Contains(fmt.Sprint(body["input"]), "README from sandbox") {
						http.Error(writer, "tool result missing", http.StatusBadRequest)
						return
					}
				}
				writer.Header().Set("Content-Type", "text/event-stream")
				_, _ = writer.Write([]byte(test.responses[index]))
			}))
			defer server.Close()
			transport, err := provider.NewOpenAICompatibleWithClient(server.URL+"/v1", "provider-secret-canary", server.Client())
			if err != nil {
				t.Fatal(err)
			}
			root := t.TempDir()
			workspace, resources, configuration, temporary := filepath.Join(root, "workspace"), filepath.Join(root, "resources"), filepath.Join(root, "config"), filepath.Join(root, "tmp")
			for _, directory := range []string{workspace, resources, configuration, temporary, filepath.Join(configuration, "pi")} {
				if err := os.Mkdir(directory, 0o700); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.WriteFile(filepath.Join(workspace, "README.md"), []byte("README from sandbox"), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(configuration, "pi", "settings.json"), []byte(`{"extensions":[],"packages":[],"prompts":[],"skills":[],"themes":[]}`), 0o600); err != nil {
				t.Fatal(err)
			}
			pi := agentruntime.PiConfiguration{AgentDirectory: "/agentrun/tmp/pi-home", SessionDir: "/agentrun/tmp/pi-sessions", Settings: "/agentrun/config/pi/settings.json", ActiveTools: test.tools, Model: "gpt-test"}
			outcome := adapter.Execute(context.Background(), AdapterRequest{
				Workspace: workspace, Resources: resources, Configuration: configuration, Temporary: temporary,
				Permission: contract.PermissionReadOnly, Pi: pi, Prompt: "perform the requested task",
				Network: contract.Network{Mode: contract.NetworkNone}, MaxTurns: 3, Timeout: 30 * time.Second,
				Transport: transport, SelectedProvider: contract.ProviderOpenAICompatible,
			})
			if !outcome.Success() || outcome.Result != test.wantResult || outcome.TurnsUsed != test.wantTurns || requests.Load() != int32(test.wantTurns) {
				t.Fatalf("outcome=%#v requests=%d", outcome, requests.Load())
			}
		})
	}
}

type integrationImageInspector struct{ digest, architecture string }

func (i integrationImageInspector) LocalImage(context.Context, string) (agentruntime.LocalImage, error) {
	return agentruntime.LocalImage{Digests: []string{i.digest}, OS: "linux", Architecture: i.architecture}, nil
}

type integrationResolver struct{}

func (integrationResolver) LookupNetIP(ctx context.Context, network, host string) ([]net.IP, error) {
	return net.DefaultResolver.LookupIP(ctx, network, host)
}

func responseTextSSE(text string) string {
	item := fmt.Sprintf(`{"id":"msg_1","type":"message","role":"assistant","status":"completed","content":[{"type":"output_text","text":%q,"annotations":[]}]}`, text)
	return responseSSE(
		`{"type":"response.created","response":{"id":"resp_1"}}`,
		`{"type":"response.output_item.added","output_index":0,"item":{"id":"msg_1","type":"message","role":"assistant","status":"in_progress","content":[]}}`,
		`{"type":"response.content_part.added","item_id":"msg_1","output_index":0,"content_index":0,"part":{"type":"output_text","text":"","annotations":[]}}`,
		fmt.Sprintf(`{"type":"response.output_text.delta","item_id":"msg_1","output_index":0,"content_index":0,"delta":%q}`, text),
		`{"type":"response.output_item.done","output_index":0,"item":`+item+`}`,
		`{"type":"response.completed","response":{"id":"resp_1","status":"completed","usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}}`,
	)
}

func responseToolSSE(name, arguments string) string {
	added := fmt.Sprintf(`{"id":"fc_1","type":"function_call","call_id":"call_1","name":%q,"arguments":"","status":"in_progress"}`, name)
	done := fmt.Sprintf(`{"id":"fc_1","type":"function_call","call_id":"call_1","name":%q,"arguments":%q,"status":"completed"}`, name, arguments)
	return responseSSE(
		`{"type":"response.created","response":{"id":"resp_tool"}}`,
		`{"type":"response.output_item.added","output_index":0,"item":`+added+`}`,
		fmt.Sprintf(`{"type":"response.function_call_arguments.delta","item_id":"fc_1","output_index":0,"delta":%q}`, arguments),
		fmt.Sprintf(`{"type":"response.function_call_arguments.done","item_id":"fc_1","output_index":0,"arguments":%q}`, arguments),
		`{"type":"response.output_item.done","output_index":0,"item":`+done+`}`,
		`{"type":"response.completed","response":{"id":"resp_tool","status":"completed","usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}}`,
	)
}

func responseSSE(events ...string) string {
	var result strings.Builder
	for _, event := range events {
		var envelope struct {
			Type string `json:"type"`
		}
		if json.Unmarshal([]byte(event), &envelope) != nil || envelope.Type == "" {
			panic("invalid test response event")
		}
		fmt.Fprintf(&result, "event: %s\ndata: %s\n\n", envelope.Type, event)
	}
	result.WriteString("data: [DONE]\n\n")
	return result.String()
}
