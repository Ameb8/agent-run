package runtime

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/Ameb8/agent-run/internal/agent"
)

func TestGeneratePiConfigurationExposesOnlyOrderedSelectedSkills(t *testing.T) {
	scope, snapshot := piConfigurationFixture(t, []string{"second", "first"}, true)
	defer scope.Close()
	defer snapshot.Close()

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
	if got := configuration.Environment(); !reflect.DeepEqual(got, []string{"PI_CODING_AGENT_DIR=/agentrun/config/pi", "PI_CODING_AGENT_SESSION_DIR=/agentrun/tmp/pi-sessions"}) {
		t.Fatalf("environment = %q", got)
	}
}

func TestGeneratePiConfigurationIsPrivatePerRun(t *testing.T) {
	firstScope, firstSnapshot := piConfigurationFixture(t, []string{"first"}, false)
	defer firstScope.Close()
	defer firstSnapshot.Close()
	secondScope, secondSnapshot := piConfigurationFixture(t, []string{"second"}, false)
	defer secondScope.Close()
	defer secondSnapshot.Close()
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

func TestGeneratePiConfigurationRejectsSnapshotOutsideMountedResources(t *testing.T) {
	scope, snapshot := piConfigurationFixture(t, []string{"first"}, false)
	defer scope.Close()
	defer snapshot.Close()
	_, err := GeneratePiConfiguration(scope.Configuration, scope.Temporary, t.TempDir(), snapshot)
	if err == nil || !strings.Contains(err.Error(), "CONFIGURATION") {
		t.Fatalf("error = %v, want CONFIGURATION", err)
	}
}

func piConfigurationFixture(t *testing.T, skills []string, schema bool) (*RunScope, *agent.Snapshot) {
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
