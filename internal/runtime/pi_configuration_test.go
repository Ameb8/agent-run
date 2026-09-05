package runtime

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/Ameb8/agent-run/internal/agent"
	"github.com/Ameb8/agent-run/internal/contract"
)

func TestGeneratePiConfigurationExposesOnlyOrderedSelectedSkills(t *testing.T) {
	scope, snapshot := piConfigurationFixture(t, []string{"second", "first"}, true)
	defer func() { _ = scope.Close() }()
	defer func() { _ = snapshot.Close() }()

	configuration, err := GeneratePiConfiguration(scope.Configuration, scope.Temporary, scope.Resources, snapshot)
	if err != nil {
		t.Fatal(err)
	}
	resourceRoot := "/agentrun/resources/" + filepath.Base(snapshot.Root)
	wantSkills := []string{resourceRoot + "/skills/second", resourceRoot + "/skills/first"}
	if !reflect.DeepEqual(configuration.Skills, wantSkills) {
		t.Fatalf("skills = %q, want %q", configuration.Skills, wantSkills)
	}
	if _, err := os.Stat(filepath.Join(snapshot.Root, "skills", "second", "assets", "data.txt")); err != nil {
		t.Fatalf("selected skill supporting resource is missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(snapshot.Root, "skills", "unselected")); !os.IsNotExist(err) {
		t.Fatalf("unselected skill entered snapshot: %v", err)
	}
	if configuration.PromptTemplate != resourceRoot+"/prompts/main.tmpl" || configuration.OutputSchema != resourceRoot+"/schema.json" {
		t.Fatalf("adapter resources = %#v", configuration)
	}
	contents, err := os.ReadFile(filepath.Join(scope.Configuration, "pi", piSettingsFile))
	if err != nil {
		t.Fatal(err)
	}
	var settings map[string][]string
	if err := json.Unmarshal(contents, &settings); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"extensions", "packages", "prompts", "skills", "themes"} {
		if len(settings[key]) != 0 {
			t.Errorf("settings %s = %q, want empty", key, settings[key])
		}
	}
	command := strings.Join(configuration.Command(), "\x00")
	for _, required := range []string{"--no-tools", "--no-extensions", "--no-skills", "--no-prompt-templates", "--no-themes", "--no-context-files", "--skill\x00" + resourceRoot + "/skills/second", "--skill\x00" + resourceRoot + "/skills/first"} {
		if !strings.Contains(command, required) {
			t.Errorf("command missing %q: %q", required, configuration.Command())
		}
	}
	if strings.Contains(command, "unselected") || strings.Contains(command, ".pi") {
		t.Fatalf("command exposes an unselected or discovered resource: %q", configuration.Command())
	}
	if got := configuration.Environment(); !reflect.DeepEqual(got, []string{"PI_CODING_AGENT_DIR=/agentrun/tmp/pi-home", "PI_CODING_AGENT_SESSION_DIR=/agentrun/tmp/pi-sessions"}) {
		t.Fatalf("environment = %q", got)
	}
}

func TestGeneratePiConfigurationIsPrivatePerRun(t *testing.T) {
	firstScope, firstSnapshot := piConfigurationFixture(t, []string{"first"}, false)
	defer func() { _ = firstScope.Close() }()
	defer func() { _ = firstSnapshot.Close() }()
	secondScope, secondSnapshot := piConfigurationFixture(t, []string{"second"}, false)
	defer func() { _ = secondScope.Close() }()
	defer func() { _ = secondSnapshot.Close() }()
	first, err := GeneratePiConfiguration(firstScope.Configuration, firstScope.Temporary, firstScope.Resources, firstSnapshot)
	if err != nil {
		t.Fatal(err)
	}
	second, err := GeneratePiConfiguration(secondScope.Configuration, secondScope.Temporary, secondScope.Resources, secondSnapshot)
	if err != nil {
		t.Fatal(err)
	}
	if first.Skills[0] == second.Skills[0] || firstScope.Root == secondScope.Root {
		t.Fatalf("concurrent configurations overlap: %q, %q", first, second)
	}
	if _, err := os.Stat(filepath.Join(firstScope.Configuration, "pi", piSettingsFile)); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(secondScope.Configuration, "pi", piSettingsFile)); err != nil {
		t.Fatal(err)
	}
}

func TestPiConfigurationActivatesExactlyDeclaredBuiltIns(t *testing.T) {
	scope, snapshot := piConfigurationFixture(t, nil, false, "read", "grep", "shell")
	defer func() { _ = scope.Close() }()
	defer func() { _ = snapshot.Close() }()

	configuration, err := GeneratePiConfiguration(scope.Configuration, scope.Temporary, scope.Resources, snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := configuration.ActiveTools, []string{"read", "grep", "shell"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("active tools = %q, want %q", got, want)
	}
	if configuration.ToolAdapter != "/agentrun/config/pi/agentrun-tools.ts" {
		t.Fatalf("tool adapter = %q", configuration.ToolAdapter)
	}
	adapter, err := os.ReadFile(filepath.Join(scope.Configuration, "pi", piToolAdapterFile))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(adapter), "name: \"shell\"") || !strings.Contains(string(adapter), "createBashTool") {
		t.Fatalf("adapter does not adapt Pi bash to stable shell: %s", adapter)
	}
	command := strings.Join(configuration.Command(), "\x00")
	for _, required := range []string{"--no-tools", "--no-extensions", "--extension\x00/agentrun/config/pi/agentrun-tools.ts", "--tools\x00read,grep,shell"} {
		if !strings.Contains(command, required) {
			t.Errorf("command missing %q: %q", required, configuration.Command())
		}
	}
	for _, argument := range configuration.Command() {
		if argument == "find" || argument == "ls" || argument == "bash" || argument == "write" || argument == "edit" {
			t.Errorf("command exposes undeclared tool %q: %q", argument, configuration.Command())
		}
	}
}

func TestPiConfigurationEmptyAllowlistExposesNoTools(t *testing.T) {
	scope, snapshot := piConfigurationFixture(t, nil, false)
	defer func() { _ = scope.Close() }()
	defer func() { _ = snapshot.Close() }()

	configuration, err := GeneratePiConfiguration(scope.Configuration, scope.Temporary, scope.Resources, snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if len(configuration.ActiveTools) != 0 || configuration.ToolAdapter != "" {
		t.Fatalf("empty allowlist configuration = %#v", configuration)
	}
	command := strings.Join(configuration.Command(), "\x00")
	if strings.Contains(command, "--tools") || strings.Contains(command, "--extension") || !strings.Contains(command, "--no-tools") {
		t.Fatalf("empty allowlist command = %q", configuration.Command())
	}
}

func TestGeneratePiConfigurationRejectsSnapshotOutsideMountedResources(t *testing.T) {
	scope, snapshot := piConfigurationFixture(t, []string{"first"}, false)
	defer func() { _ = scope.Close() }()
	defer func() { _ = snapshot.Close() }()
	_, err := GeneratePiConfiguration(scope.Configuration, scope.Temporary, t.TempDir(), snapshot)
	if err == nil || !strings.Contains(err.Error(), "CONFIGURATION") {
		t.Fatalf("error = %v, want CONFIGURATION", err)
	}
}

func TestPiConfigurationLoadsDeclaredExtensionsAndActivatesOnlyAllowlist(t *testing.T) {
	scope, snapshot := piConfigurationExtensionFixture(t)
	defer func() { _ = scope.Close() }()
	defer func() { _ = snapshot.Close() }()

	configuration, err := GeneratePiConfiguration(scope.Configuration, scope.Temporary, scope.Resources, snapshot)
	if err != nil {
		t.Fatal(err)
	}
	resourceRoot := "/agentrun/resources/" + filepath.Base(snapshot.Root)
	if got, want := configuration.Extensions, []string{resourceRoot + "/extensions/search/index.ts"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("extensions = %q, want %q", got, want)
	}
	if got, want := configuration.ActiveTools, []string{"read", "search_web"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("active tools = %q, want %q", got, want)
	}
	command := strings.Join(configuration.Command(), "\x00")
	if configuration.ExtensionLoader != "/agentrun/config/pi/agentrun-extensions.ts" {
		t.Fatalf("extension loader = %q", configuration.ExtensionLoader)
	}
	loader, err := os.ReadFile(filepath.Join(scope.Configuration, "pi", piExtensionLoaderFile))
	if err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{resourceRoot + "/extensions/search/index.ts", "duplicate extension tool", "cannot override built-in", "was not registered"} {
		if !strings.Contains(string(loader), required) {
			t.Errorf("loader missing %q: %s", required, loader)
		}
	}
	if !strings.Contains(command, "--no-extensions") || !strings.Contains(command, "--extension\x00/agentrun/config/pi/agentrun-extensions.ts") || strings.Contains(command, "--extension\x00"+resourceRoot+"/extensions/search/index.ts") || !strings.Contains(command, "--tools\x00read,search_web") {
		t.Fatalf("extension command = %q", configuration.Command())
	}
}

func piConfigurationExtensionFixture(t *testing.T) (*RunScope, *agent.Snapshot) {
	t.Helper()
	scope, err := NewRunScope()
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(t.TempDir(), "source")
	for _, directory := range []string{"agents", "prompts", "extensions/search"} {
		if err := os.MkdirAll(filepath.Join(root, directory), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "prompts", "main.tmpl"), []byte("prompt"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "extensions", "search", "index.ts"), []byte(`import type { ExtensionAPI } from "@earendil-works/pi-coding-agent";
export default function (pi: ExtensionAPI) { pi.registerTool({ name: "search_web" }); }`), 0o600); err != nil {
		t.Fatal(err)
	}
	definition := "schema_version: 1\nname: reviewer\nmodel:\n  provider: openai-subscription\n  model: test\npermission: read-only\nprompt:\n  template: prompts/main.tmpl\ntools:\n  extensions: [search]\n  allow: [read, search_web]\n"
	definitionPath := filepath.Join(root, "agents", "reviewer.yaml")
	if err := os.WriteFile(definitionPath, []byte(definition), 0o600); err != nil {
		t.Fatal(err)
	}
	resolution, err := agent.Resolve(agent.ResolveOptions{Workspace: root, Selection: definitionPath})
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := agent.CreateSnapshotIn(resolution, scope.Resources)
	if err != nil {
		t.Fatal(err)
	}
	return scope, snapshot
}

func TestGenerateProviderAdapterUsesOnlyPrivateBridge(t *testing.T) {
	configuration := t.TempDir()
	if err := os.Mkdir(filepath.Join(configuration, "pi"), 0o700); err != nil {
		t.Fatal(err)
	}
	adapter, err := GenerateProviderAdapter(configuration, contract.Model{Provider: contract.ProviderOpenAICompatible, Model: "gpt-test"})
	if err != nil {
		t.Fatal(err)
	}
	if adapter != "/agentrun/config/pi/agentrun-provider.ts" {
		t.Fatalf("adapter = %q", adapter)
	}
	contents, err := os.ReadFile(filepath.Join(configuration, "pi", "agentrun-provider.ts"))
	if err != nil {
		t.Fatal(err)
	}
	source := string(contents)
	for _, required := range []string{`pi.registerProvider("agentrun"`, "streamSimpleOpenAIResponses", "/agentrun/tmp/provider.sock", "/agentrun/tmp/egress.sock", "gpt-test", "127.0.0.1"} {
		if !strings.Contains(source, required) {
			t.Errorf("adapter missing %q", required)
		}
	}
	for _, forbidden := range []string{"Authorization", "api.openai", "OPENAI_API_KEY"} {
		if strings.Contains(source, forbidden) {
			t.Errorf("adapter leaks provider configuration %q", forbidden)
		}
	}
}

func TestPiConfigurationSelectsOnlyAgentRunProviderAndModel(t *testing.T) {
	configuration := PiConfiguration{ProviderAdapter: "/agentrun/config/pi/agentrun-provider.ts", Provider: "agentrun", Model: "gpt-test"}
	command := strings.Join(configuration.Command(), "\x00")
	for _, required := range []string{"--extension\x00/agentrun/config/pi/agentrun-provider.ts", "--provider\x00agentrun", "--model\x00gpt-test"} {
		if !strings.Contains(command, required) {
			t.Fatalf("command %q missing %q", configuration.Command(), required)
		}
	}
}

func piConfigurationFixture(t *testing.T, skills []string, schema bool, tools ...string) (*RunScope, *agent.Snapshot) {
	t.Helper()
	scope, err := NewRunScope()
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(t.TempDir(), "source")
	for _, directory := range []string{"agents", "prompts", "skills/first/references", "skills/second/assets", "skills/unselected"} {
		if err := os.MkdirAll(filepath.Join(root, directory), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "prompts", "main.tmpl"), []byte("prompt"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, file := range []string{"skills/first/SKILL.md", "skills/first/references/guide.md", "skills/second/SKILL.md", "skills/second/assets/data.txt", "skills/unselected/SKILL.md"} {
		if err := os.WriteFile(filepath.Join(root, file), []byte(file), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	definition := "schema_version: 1\nname: reviewer\nmodel:\n  provider: openai-subscription\n  model: test\nskills: [" + strings.Join(skills, ", ") + "]\npermission: read-only\nprompt:\n  template: prompts/main.tmpl\n"
	if len(tools) != 0 {
		definition += "tools:\n  allow: [" + strings.Join(tools, ", ") + "]\n"
	}
	if schema {
		if err := os.WriteFile(filepath.Join(root, "schema.json"), []byte(`{"type":"object"}`), 0o600); err != nil {
			t.Fatal(err)
		}
		definition += "output:\n  schema: schema.json\n"
	}
	definitionPath := filepath.Join(root, "agents", "reviewer.yaml")
	if err := os.WriteFile(definitionPath, []byte(definition), 0o600); err != nil {
		t.Fatal(err)
	}
	resolution, err := agent.Resolve(agent.ResolveOptions{Workspace: root, Selection: definitionPath})
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := agent.CreateSnapshotIn(resolution, scope.Resources)
	if err != nil {
		t.Fatal(err)
	}
	return scope, snapshot
}
