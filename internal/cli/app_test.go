package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Ameb8/agent-run/internal/auth"
	"github.com/Ameb8/agent-run/internal/contract"
	"github.com/Ameb8/agent-run/internal/provider"
	agentruntime "github.com/Ameb8/agent-run/internal/runtime"
)

func TestAllV1CommandsAreRegistered(t *testing.T) {
	t.Parallel()

	commands := registeredCommands()
	if len(commands) != 8 {
		t.Fatalf("registered command count = %d, want 8", len(commands))
	}

	var stderr bytes.Buffer
	app := New(&stderr)
	app.runtimeDoctor = doctorFunc(func(context.Context) agentruntime.DoctorReport {
		return agentruntime.DoctorReport{Runtime: app.runtimeIdentity, Checks: []agentruntime.DoctorCheck{{Name: "probe", Status: agentruntime.DoctorPass}}}
	})
	for _, test := range []struct {
		args []string
		code int
	}{
		{[]string{"run"}, 1}, {[]string{"validate"}, 1}, {[]string{"inspect"}, 1},
		{[]string{"auth", "login", "openai-subscription", "unexpected"}, 1},
		{[]string{"auth", "logout", "openai-subscription", "unexpected"}, 1}, {[]string{"version"}, 0}, {[]string{"doctor"}, 0},
	} {
		if exitCode := app.Run(test.args); exitCode != test.code {
			t.Errorf("Run(%q) exit code = %d, want %d", test.args, exitCode, test.code)
		}
	}
}

func TestDiagnosticsDoNotUseStdout(t *testing.T) {
	t.Parallel()

	var stderr bytes.Buffer
	app := New(&stderr)
	if exitCode := app.Run([]string{"unknown"}); exitCode != 1 {
		t.Fatalf("exit code = %d, want 1", exitCode)
	}
	if got := stderr.String(); !strings.Contains(got, "unknown command") {
		t.Fatalf("stderr = %q", got)
	}
}

func TestVersionEmitsOnlyStableReleaseIdentity(t *testing.T) {
	t.Parallel()

	var stdout, stderr bytes.Buffer
	app := NewWithWriters(&stdout, &stderr)
	want := contract.RuntimeIdentity{
		AgentRunVersion:   "1.2.3",
		PiVersion:         "0.74.0",
		JavaScriptVersion: "v22.14.0",
		ImageDigest:       "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
	}
	app.runtimeIdentity = want
	if code := app.Run([]string{"version"}); code != 0 {
		t.Fatalf("version exit = %d, stderr = %q", code, stderr.String())
	}
	var got contract.RuntimeIdentity
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil || got != want {
		t.Fatalf("version = %q, decoded = %#v, error = %v", stdout.String(), got, err)
	}
}

func TestDoctorReportsOnlyCredentialPresenceAndNeverPreparesAProvider(t *testing.T) {
	root := t.TempDir()
	store, err := auth.NewStoreAt(filepath.Join(root, "agentrun"))
	if err != nil {
		t.Fatal(err)
	}
	const canary = "doctor-subscription-secret-canary"
	if err := store.Replace([]byte(`{"openai-codex":{"type":"oauth","access":"` + canary + `"}}`)); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	app := NewWithWriters(&stdout, &stderr)
	app.subscriptionStore = store
	app.authSetupError = nil
	app.runtimeDoctor = doctorFunc(func(context.Context) agentruntime.DoctorReport {
		return agentruntime.DoctorReport{Runtime: app.runtimeIdentity, Checks: []agentruntime.DoctorCheck{{Name: "probe", Status: agentruntime.DoctorPass}}}
	})
	app.prepareProvider = func(contract.Model, func(string) (string, bool), auth.Handle) (*provider.Transport, error) {
		t.Fatal("doctor must not prepare a provider or make a model request")
		return nil, nil
	}
	if code := app.Run([]string{"doctor"}); code != 0 {
		t.Fatalf("doctor exit = %d, stderr = %q", code, stderr.String())
	}
	if strings.Contains(stdout.String(), canary) || !strings.Contains(stdout.String(), "credentials were not validated") {
		t.Fatalf("doctor output = %q", stdout.String())
	}
}

func TestDoctorReportsMissingSubscriptionWithoutCheckingAnyProvider(t *testing.T) {
	root := t.TempDir()
	store, err := auth.NewStoreAt(filepath.Join(root, "agentrun"))
	if err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	app := NewWithWriters(&stdout, &stderr)
	app.subscriptionStore = store
	app.authSetupError = nil
	app.runtimeDoctor = doctorFunc(func(context.Context) agentruntime.DoctorReport {
		return agentruntime.DoctorReport{Runtime: app.runtimeIdentity, Checks: []agentruntime.DoctorCheck{{Name: "probe", Status: agentruntime.DoctorPass}}}
	})
	app.prepareProvider = func(contract.Model, func(string) (string, bool), auth.Handle) (*provider.Transport, error) {
		t.Fatal("doctor must not validate arbitrary provider credentials")
		return nil, nil
	}
	if code := app.Run([]string{"doctor"}); code != 0 {
		t.Fatalf("doctor exit = %d, stderr = %q", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), `"name":"subscription_auth","status":"MISSING"`) || !strings.Contains(stdout.String(), `"optional":true`) || !strings.Contains(stdout.String(), "agentrun auth login openai-subscription") {
		t.Fatalf("doctor output = %q", stdout.String())
	}
}

func TestDoctorFailsWhenSubscriptionStorageCannotBeChecked(t *testing.T) {
	var stdout, stderr bytes.Buffer
	app := NewWithWriters(&stdout, &stderr)
	app.authSetupError = os.ErrPermission
	app.runtimeDoctor = doctorFunc(func(context.Context) agentruntime.DoctorReport {
		return agentruntime.DoctorReport{Runtime: app.runtimeIdentity, Checks: []agentruntime.DoctorCheck{{Name: "probe", Status: agentruntime.DoctorPass}}}
	})
	if code := app.Run([]string{"doctor"}); code != 1 {
		t.Fatalf("doctor exit = %d, stderr = %q", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), `"name":"subscription_auth","status":"FAIL"`) || strings.Contains(stdout.String(), `"optional":true`) {
		t.Fatalf("doctor output = %q", stdout.String())
	}
}

func TestDoctorFailsForARequiredCheckEvenWhenSubscriptionIsOptional(t *testing.T) {
	root := t.TempDir()
	store, err := auth.NewStoreAt(filepath.Join(root, "agentrun"))
	if err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	app := NewWithWriters(&stdout, &stderr)
	app.subscriptionStore = store
	app.authSetupError = nil
	app.runtimeDoctor = doctorFunc(func(context.Context) agentruntime.DoctorReport {
		return agentruntime.DoctorReport{Runtime: app.runtimeIdentity, Checks: []agentruntime.DoctorCheck{{Name: "sandbox", Status: agentruntime.DoctorFail, Detail: "isolation failed"}}}
	})
	if code := app.Run([]string{"doctor"}); code != 1 {
		t.Fatalf("doctor exit = %d, stderr = %q", code, stderr.String())
	}
}

func TestRunAlwaysEmitsOneRedactedFailureObject(t *testing.T) {
	t.Parallel()

	const canary = "prompt-and-credential-canary"
	var stdout, stderr bytes.Buffer
	app := NewWithWriters(&stdout, &stderr)
	if code := app.Run([]string{"run", canary}); code != 1 {
		t.Fatalf("exit code = %d, want 1", code)
	}
	got := decodeRunResult(t, stdout.Bytes())
	if got["error_type"] != string(contract.ErrorValidation) || strings.Contains(stdout.String(), canary) || strings.Contains(stderr.String(), canary) {
		t.Fatalf("stdout = %q, stderr = %q", stdout.String(), stderr.String())
	}
	decoder := json.NewDecoder(strings.NewReader(stdout.String()))
	var one, extra any
	if err := decoder.Decode(&one); err != nil {
		t.Fatal(err)
	}
	if err := decoder.Decode(&extra); err == nil {
		t.Fatalf("stdout contains more than one JSON object: %q", stdout.String())
	}
}

func TestValidateAndInspectUseStaticSnapshot(t *testing.T) {
	root := t.TempDir()
	workspace := filepath.Join(root, "workspace")
	definition := filepath.Join(root, "package", "agents", "reviewer.yaml")
	if err := os.MkdirAll(filepath.Join(root, "package", "prompts"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(workspace, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "package", "prompts", "main.tmpl"), []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}
	contents := "schema_version: 1\nname: reviewer\nmodel:\n  provider: openai-subscription\n  model: test\npermission: read-only\nprompt:\n  template: prompts/main.tmpl\n"
	if err := os.MkdirAll(filepath.Dir(definition), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(definition, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	args := []string{definition, "--workspace", workspace}
	var stdout, stderr bytes.Buffer
	app := NewWithWriters(&stdout, &stderr)
	app.runtimeVerifier = successfulRuntimeVerifier{}
	if code := app.Run(append([]string{"validate"}, args...)); code != 0 {
		t.Fatalf("validate exit = %d, stderr = %s", code, stderr.String())
	}
	if code := app.Run(append([]string{"inspect"}, args...)); code != 0 {
		t.Fatalf("inspect exit = %d, stderr = %s", code, stderr.String())
	}
	var got map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got["origin"] != "path" || got["digest"] == "" || got["resources"] == nil || got["capabilities"] == nil || got["defaults"] == nil || got["paths"] == nil {
		t.Fatalf("inspect output = %#v", got)
	}
	stdout.Reset()
	stderr.Reset()
	if code := app.Run(append([]string{"run"}, append(args, "--expect-agent-digest", "sha256:not-the-package")...)); code != 1 {
		t.Fatalf("run exit = %d", code)
	}
	gotRun := decodeRunResult(t, stdout.Bytes())
	if gotRun["error_type"] != string(contract.ErrorValidation) || gotRun["agent"] == nil || gotRun["model"] == nil {
		t.Fatalf("digest mismatch result = %#v", gotRun)
	}
}

func TestRunReportsConfigurationWhenPrivateRuntimeIsUnavailable(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	workspace := filepath.Join(root, "workspace")
	definition := filepath.Join(root, "package", "agents", "reviewer.yaml")
	if err := os.MkdirAll(filepath.Join(root, "package", "prompts"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(workspace, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "package", "prompts", "main.tmpl"), []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}
	contents := "schema_version: 1\nname: reviewer\nmodel:\n  provider: openai-subscription\n  model: test\npermission: read-only\nprompt:\n  template: prompts/main.tmpl\n"
	if err := os.MkdirAll(filepath.Dir(definition), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(definition, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	app := NewWithWriters(&stdout, &stderr)
	app.runtimeVerifier = unavailableRuntimeVerifier{}
	if code := app.Run([]string{"run", definition, "--workspace", workspace}); code != 1 {
		t.Fatalf("run exit = %d, want 1", code)
	}
	got := decodeRunResult(t, stdout.Bytes())
	if got["error_type"] != string(contract.ErrorConfiguration) || stderr.Len() != 0 {
		t.Fatalf("stdout = %q, stderr = %q", stdout.String(), stderr.String())
	}
}

func TestSubscriptionLoginReplacementAndLogoutKeepCredentialOffOutput(t *testing.T) {
	root := t.TempDir()
	store, err := auth.NewStoreAt(filepath.Join(root, "agentrun"))
	if err != nil {
		t.Fatal(err)
	}
	const canary = "openai-subscription-secret-canary"
	var stdout, stderr bytes.Buffer
	app := NewWithWriters(&stdout, &stderr)
	app.subscriptionStore = store
	app.authSetupError = nil
	app.subscriptionLogin = subscriptionLoginFunc(func(context.Context) ([]byte, error) {
		return []byte(`{"openai-codex":{"type":"oauth","access":"` + canary + `"}}`), nil
	})
	if code := app.Run([]string{"auth", "login", "openai-subscription"}); code != 0 {
		t.Fatalf("login exit = %d, stderr = %q", code, stderr.String())
	}
	if stdout.Len() != 0 || strings.Contains(stderr.String(), canary) {
		t.Fatalf("login output exposed credential: stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
	app.subscriptionLogin = subscriptionLoginFunc(func(context.Context) ([]byte, error) {
		return []byte(`{"openai-codex":{"type":"oauth","access":"replacement"}}`), nil
	})
	if code := app.Run([]string{"auth", "login", "openai-subscription"}); code != 0 {
		t.Fatalf("replacement login exit = %d, stderr = %q", code, stderr.String())
	}
	handle, err := store.Open()
	if err != nil {
		t.Fatal(err)
	}
	if err := handle.WithPiAuth(func(document []byte) error {
		if strings.Contains(string(document), canary) || !strings.Contains(string(document), "replacement") {
			t.Fatalf("stored replacement = %q", document)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if code := app.Run([]string{"auth", "logout", "openai-subscription"}); code != 0 {
		t.Fatalf("logout exit = %d, stderr = %q", code, stderr.String())
	}
	present, err := store.Present()
	if err != nil || present {
		t.Fatalf("credential after logout: present=%v err=%v", present, err)
	}
}

func TestSubscriptionRunReportsMissingCredentialAsAuthentication(t *testing.T) {
	root := t.TempDir()
	workspace := filepath.Join(root, "workspace")
	definition := filepath.Join(root, "package", "agents", "reviewer.yaml")
	if err := os.MkdirAll(filepath.Join(root, "package", "prompts"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(workspace, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "package", "prompts", "main.tmpl"), []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(definition), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(definition, []byte("schema_version: 1\nname: reviewer\nmodel:\n  provider: openai-subscription\n  model: test\npermission: read-only\nprompt:\n  template: prompts/main.tmpl\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := auth.NewStoreAt(filepath.Join(root, "agentrun"))
	if err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	app := NewWithWriters(&stdout, &stderr)
	app.runtimeVerifier = successfulRuntimeVerifier{}
	app.subscriptionStore = store
	app.prepareProvider = func(contract.Model, func(string) (string, bool), auth.Handle) (*provider.Transport, error) {
		t.Fatal("missing subscription authentication must fail before provider access")
		return nil, nil
	}
	if code := app.Run([]string{"run", definition, "--workspace", workspace}); code != 1 {
		t.Fatalf("run exit = %d", code)
	}
	got := decodeRunResult(t, stdout.Bytes())
	if got["error_type"] != string(contract.ErrorAuthentication) || stderr.Len() != 0 {
		t.Fatalf("stdout = %q, stderr = %q", stdout.String(), stderr.String())
	}
}

func TestOpenAICompatibleRunDoesNotRequireSubscriptionAuthentication(t *testing.T) {
	root := t.TempDir()
	workspace := filepath.Join(root, "workspace")
	definition := filepath.Join(root, "package", "agents", "reviewer.yaml")
	if err := os.MkdirAll(filepath.Join(root, "package", "prompts"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(workspace, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "package", "prompts", "main.tmpl"), []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}
	definitionContents := "schema_version: 1\nname: reviewer\nmodel:\n  provider: openai-compatible\n  endpoint: https://models.example/v1\n  model: test\n  api_key_env: MODEL_KEY\npermission: read-only\nprompt:\n  template: prompts/main.tmpl\n"
	if err := os.MkdirAll(filepath.Dir(definition), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(definition, []byte(definitionContents), 0o600); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	app := NewWithWriters(&stdout, &stderr)
	app.runtimeVerifier = successfulRuntimeVerifier{}
	app.authSetupError = os.ErrPermission // An unrelated subscription store must not block this provider.
	app.lookupEnv = func(name string) (string, bool) { return "compatible-key", name == "MODEL_KEY" }
	providerPrepared := false
	app.prepareProvider = func(contract.Model, func(string) (string, bool), auth.Handle) (*provider.Transport, error) {
		providerPrepared = true
		return nil, nil
	}
	if code := app.Run([]string{"run", definition, "--workspace", workspace}); code != 1 {
		t.Fatalf("run exit = %d", code)
	}
	got := decodeRunResult(t, stdout.Bytes())
	if !providerPrepared || got["error_type"] == string(contract.ErrorAuthentication) || stderr.Len() != 0 {
		t.Fatalf("providerPrepared=%v stdout=%q stderr=%q", providerPrepared, stdout.String(), stderr.String())
	}
}

func TestRunRejectsMissingDeclaredEnvironmentBeforeProviderAccess(t *testing.T) {
	root := t.TempDir()
	workspace := filepath.Join(root, "workspace")
	definition := filepath.Join(root, "package", "agents", "reviewer.yaml")
	if err := os.MkdirAll(filepath.Join(root, "package", "prompts"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(definition), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(workspace, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "package", "prompts", "main.tmpl"), []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}
	contents := "schema_version: 1\nname: reviewer\nmodel:\n  provider: openai-compatible\n  endpoint: https://models.example/v1\n  model: test\n  api_key_env: MODEL_KEY\nenvironment:\n  allow: [EXTENSION_KEY]\npermission: read-only\nprompt:\n  template: prompts/main.tmpl\n"
	if err := os.WriteFile(definition, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	app := NewWithWriters(&stdout, &stderr)
	app.runtimeVerifier = successfulRuntimeVerifier{}
	app.lookupEnv = func(name string) (string, bool) {
		if name == "MODEL_KEY" {
			return "model-secret", true
		}
		return "", false
	}
	providerCalled := false
	app.prepareProvider = func(contract.Model, func(string) (string, bool), auth.Handle) (*provider.Transport, error) {
		providerCalled = true
		return nil, nil
	}
	if code := app.Run([]string{"run", definition, "--workspace", workspace}); code != 1 {
		t.Fatalf("run exit = %d", code)
	}
	got := decodeRunResult(t, stdout.Bytes())
	if providerCalled || got["error_type"] != string(contract.ErrorConfiguration) || stderr.Len() != 0 {
		t.Fatalf("providerCalled=%v stdout=%q stderr=%q", providerCalled, stdout.String(), stderr.String())
	}
}

func TestParseRunArgsKeepsRunOnlyFlagsOutOfStaticResolution(t *testing.T) {
	t.Parallel()

	got, err := parseRunArgs([]string{"reviewer", "--workspace", "/work", "--allow-workspace-agent", "--expect-agent-digest", "sha256:abc", "--input", "request=one", "--input-file", "notes=-", "--inputs-json", "inputs.json", "--output-format", "json"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(got.definitionArgs, " ") != "reviewer --workspace /work --allow-workspace-agent" || got.expectedDigest != "sha256:abc" || strings.Join(got.inputs, ",") != "request=one" || strings.Join(got.inputFiles, ",") != "notes=-" || strings.Join(got.inputsJSON, ",") != "inputs.json" {
		t.Fatalf("parseRunArgs() = %#v", got)
	}
}

func TestParseRunArgsRejectsDuplicateAndUnknownNonRepeatableFlags(t *testing.T) {
	t.Parallel()

	for _, args := range [][]string{
		{"reviewer", "--workspace", "/one", "--workspace", "/two"},
		{"reviewer", "--workspace", "/work", "--allow-workspace-agent", "--allow-workspace-agent"},
		{"reviewer", "--workspace", "/work", "--expect-agent-digest", "one", "--expect-agent-digest", "two"},
		{"reviewer", "--workspace", "/work", "--output-format", "text"},
		{"reviewer", "--workspace", "/work", "--not-a-run-flag"},
	} {
		if _, err := parseRunArgs(args); err == nil {
			t.Errorf("parseRunArgs(%q) succeeded", args)
		}
	}
}

func TestRunReadsAndValidatesInputsBeforeRuntimeAccess(t *testing.T) {
	root := t.TempDir()
	workspace := filepath.Join(root, "workspace")
	definition := filepath.Join(root, "package", "agents", "reviewer.yaml")
	if err := os.MkdirAll(filepath.Join(root, "package", "prompts"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(workspace, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "package", "prompts", "main.tmpl"), []byte("{{.request}}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(definition), 0o700); err != nil {
		t.Fatal(err)
	}
	contents := "schema_version: 1\nname: reviewer\nmodel:\n  provider: openai-subscription\n  model: test\npermission: read-only\nprompt:\n  template: prompts/main.tmpl\n  inputs:\n    required: [request]\n"
	if err := os.WriteFile(definition, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	app := NewWithWriters(&stdout, &stderr)
	app.stdin = strings.NewReader("from stdin")
	app.runtimeVerifier = unavailableRuntimeVerifier{}
	if code := app.Run([]string{"run", definition, "--workspace", workspace, "--input-file", "request=-"}); code != 1 {
		t.Fatalf("run exit = %d", code)
	}
	if got := decodeRunResult(t, stdout.Bytes()); got["error_type"] != string(contract.ErrorConfiguration) {
		t.Fatalf("run result = %#v", got)
	}

	stdout.Reset()
	stderr.Reset()
	if code := app.Run([]string{"run", definition, "--workspace", workspace}); code != 1 {
		t.Fatalf("run exit = %d", code)
	}
	if got := decodeRunResult(t, stdout.Bytes()); got["error_type"] != string(contract.ErrorValidation) {
		t.Fatalf("missing input result = %#v", got)
	}
}

func decodeRunResult(t *testing.T, contents []byte) map[string]any {
	t.Helper()
	var got map[string]any
	if err := json.Unmarshal(contents, &got); err != nil {
		t.Fatalf("invalid run JSON %q: %v", contents, err)
	}
	if got["schema_version"] != float64(contract.ResultSchemaVersion) || got["run_id"] == "" || got["status"] != string(contract.RunStatusFailure) {
		t.Fatalf("run result = %#v", got)
	}
	return got
}

type successfulRuntimeVerifier struct{}

func (successfulRuntimeVerifier) Verify(context.Context) (contract.RuntimeIdentity, error) {
	return contract.RuntimeIdentity{}, nil
}

type unavailableRuntimeVerifier struct{}

func (unavailableRuntimeVerifier) Verify(context.Context) (contract.RuntimeIdentity, error) {
	return contract.RuntimeIdentity{}, &contract.CommandError{Category: contract.ErrorConfiguration, Message: "private runtime image is unavailable"}
}

type doctorFunc func(context.Context) agentruntime.DoctorReport

func (f doctorFunc) Run(ctx context.Context) agentruntime.DoctorReport { return f(ctx) }

type subscriptionLoginFunc func(context.Context) ([]byte, error)

func (f subscriptionLoginFunc) Login(ctx context.Context) ([]byte, error) { return f(ctx) }
