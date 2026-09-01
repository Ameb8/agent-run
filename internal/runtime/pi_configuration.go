package runtime

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/Ameb8/agent-run/internal/agent"
)

const (
	piAgentDirectory      = configurationMount + "/pi"
	piSessionDirectory    = temporaryMount + "/pi-sessions"
	piSettingsFile        = "settings.json"
	piCodingAgentDir      = "PI_CODING_AGENT_DIR"
	piCodingAgentSessions = "PI_CODING_AGENT_SESSION_DIR"
	piToolAdapterFile     = "agentrun-tools.ts"
	piExtensionLoaderFile = "agentrun-extensions.ts"
)

// stableToolAdapter adapts the one Pi built-in whose upstream name is not part
// of AgentRun's v1 contract. Keeping this extension generated and run-local
// prevents a workspace or user Pi configuration from adding tools or changing
// their names. The implementation delegates to Pi's pinned built-in, so its
// non-interactive shell behavior and error results remain intact.
const stableToolAdapter = `import type { ExtensionAPI } from "@earendil-works/pi-coding-agent";
import { createBashTool } from "@earendil-works/pi-coding-agent";

export default function (pi: ExtensionAPI) {
  const bash = createBashTool("/workspace");
  pi.registerTool({ ...bash, name: "shell", label: "shell" });
}
`

// PiConfiguration is the complete, run-local resource contract passed to the
// pinned Pi CLI.  It deliberately contains no model credential, caller
// environment value, package path, or discovery source.
type PiConfiguration struct {
	AgentDirectory  string
	SessionDir      string
	Settings        string
	Skills          []string
	PromptTemplate  string
	OutputSchema    string
	Extensions      []string
	ExtensionLoader string
	ActiveTools     []string
	ToolAdapter     string
}

// GeneratePiConfiguration writes the otherwise-empty Pi settings file and
// translates selected snapshot paths into their fixed container paths. Pi's
// command line disables every automatic resource discovery mechanism and
// adds selected skills explicitly, preserving definition order.
//
// resources must be the parent mounted at /agentrun/resources; snapshot must
// be a child of it.  The latter condition prevents a generated invocation
// from exposing a source-package path or arbitrary host file to the runtime.
func GeneratePiConfiguration(configuration, temporary, resources string, snapshot *agent.Snapshot) (PiConfiguration, error) {
	if snapshot == nil || snapshot.Root == "" {
		return PiConfiguration{}, configurationError("Pi configuration requires an agent snapshot")
	}
	configuration, err := privateDirectory(configuration, "generated configuration")
	if err != nil {
		return PiConfiguration{}, err
	}
	if _, err := privateDirectory(temporary, "private temporary storage"); err != nil {
		return PiConfiguration{}, err
	}
	resources, err = privateDirectory(resources, "selected resource snapshot parent")
	if err != nil {
		return PiConfiguration{}, err
	}
	snapshotRoot, err := filepath.EvalSymlinks(snapshot.Root)
	if err != nil || !withinDirectory(resources, snapshotRoot) || snapshotRoot == resources {
		return PiConfiguration{}, configurationError("agent snapshot is not contained by selected resources")
	}

	agentDirectory := filepath.Join(configuration, "pi")
	if err := os.Mkdir(agentDirectory, 0o700); err != nil && !os.IsExist(err) {
		return PiConfiguration{}, configurationError("create Pi configuration: %v", err)
	}
	settings := filepath.Join(agentDirectory, piSettingsFile)
	// Keep all persisted discovery lists empty. Explicit --skill arguments are
	// used below because --no-skills excludes settings-based skill sources.
	contents, err := json.Marshal(struct {
		Extensions []string `json:"extensions"`
		Packages   []string `json:"packages"`
		Prompts    []string `json:"prompts"`
		Skills     []string `json:"skills"`
		Themes     []string `json:"themes"`
	}{Extensions: []string{}, Packages: []string{}, Prompts: []string{}, Skills: []string{}, Themes: []string{}})
	if err != nil {
		return PiConfiguration{}, configurationError("encode Pi configuration: %v", err)
	}
	if err := os.WriteFile(settings, contents, 0o600); err != nil {
		return PiConfiguration{}, configurationError("write Pi configuration: %v", err)
	}

	toContainer := func(path string) (string, error) {
		path, err = filepath.EvalSymlinks(path)
		if err != nil || !withinDirectory(snapshotRoot, path) {
			return "", configurationError("selected snapshot resource is unavailable")
		}
		rel, err := filepath.Rel(resources, path)
		if err != nil {
			return "", configurationError("map selected snapshot resource: %v", err)
		}
		return filepath.ToSlash(filepath.Join(resourcesMount, rel)), nil
	}

	result := PiConfiguration{AgentDirectory: piAgentDirectory, SessionDir: piSessionDirectory, Settings: filepath.ToSlash(filepath.Join(configurationMount, "pi", piSettingsFile))}
	result.PromptTemplate, err = toContainer(snapshot.Definition.PromptTemplate)
	if err != nil {
		return PiConfiguration{}, err
	}
	if snapshot.Definition.OutputSchema != "" {
		result.OutputSchema, err = toContainer(snapshot.Definition.OutputSchema)
		if err != nil {
			return PiConfiguration{}, err
		}
	}
	for _, skill := range snapshot.Definition.Skills {
		containerPath, pathErr := toContainer(skill)
		if pathErr != nil {
			return PiConfiguration{}, pathErr
		}
		result.Skills = append(result.Skills, containerPath)
	}
	if err := agent.ValidateExtensions(snapshot); err != nil {
		return PiConfiguration{}, err
	}
	for _, extension := range snapshot.Definition.Extensions {
		// Definition.Extensions identifies the validated extension directory;
		// only its conventional entry point is executable. Passing the directory
		// would let Pi choose a manifest or other loader behavior instead.
		containerPath, pathErr := toContainer(filepath.Join(extension, "index.ts"))
		if pathErr != nil {
			return PiConfiguration{}, pathErr
		}
		result.Extensions = append(result.Extensions, containerPath)
	}
	for _, name := range snapshot.Definition.Agent.Tools.Allow {
		result.ActiveTools = append(result.ActiveTools, name)
	}
	if containsTool(result.ActiveTools, "shell") {
		adapter := filepath.Join(agentDirectory, piToolAdapterFile)
		if err := os.WriteFile(adapter, []byte(stableToolAdapter), 0o600); err != nil {
			return PiConfiguration{}, configurationError("write stable tool adapter: %v", err)
		}
		result.ToolAdapter = filepath.ToSlash(filepath.Join(configurationMount, "pi", piToolAdapterFile))
	}
	if len(result.Extensions) != 0 {
		loader := filepath.Join(agentDirectory, piExtensionLoaderFile)
		contents, loaderErr := extensionLoader(result.Extensions, result.ActiveTools)
		if loaderErr != nil {
			return PiConfiguration{}, configurationError("generate extension loader: %v", loaderErr)
		}
		if err := os.WriteFile(loader, contents, 0o600); err != nil {
			return PiConfiguration{}, configurationError("write extension loader: %v", err)
		}
		result.ExtensionLoader = filepath.ToSlash(filepath.Join(configurationMount, "pi", piExtensionLoaderFile))
	}
	return result, nil
}

// extensionLoader is the sole Pi extension that AgentRun passes for declared
// package extensions. It imports their immutable index.ts files in definition
// order, retains a run-local registration set, and rejects conflicts before
// Pi can offer a tool to the model. The generated file and its module state
// live under the private per-run configuration mount, so lifecycle state is
// never shared between runs.
func extensionLoader(extensions, allowed []string) ([]byte, error) {
	encode := func(value any) (string, error) {
		bytes, err := json.Marshal(value)
		return string(bytes), err
	}
	var source strings.Builder
	for i, extension := range extensions {
		path, err := encode(extension)
		if err != nil {
			return nil, err
		}
		_, _ = fmt.Fprintf(&source, "import extension%d from %s;\n", i, path)
	}
	allowedJSON, err := encode(allowed)
	if err != nil {
		return nil, err
	}
	builtInsJSON, err := encode([]string{"read", "grep", "write", "edit", "shell"})
	if err != nil {
		return nil, err
	}
	_, _ = fmt.Fprintf(&source, `
const allowed = new Set<string>(%s);
const builtIns = new Set<string>(%s);

export default function (pi: any) {
  const registered = new Set<string>();
  const guarded = new Proxy(pi, {
    get(target, property, receiver) {
      if (property !== "registerTool") return Reflect.get(target, property, receiver);
      return (tool: any, ...rest: any[]) => {
        const name = tool?.name;
        if (typeof name !== "string" || name.length === 0) throw new Error("extension registered an invalid tool name");
        if (builtIns.has(name)) throw new Error("extension cannot override built-in tool " + name);
        if (registered.has(name)) throw new Error("duplicate extension tool " + name);
        registered.add(name);
        return target.registerTool.call(target, tool, ...rest);
      };
    },
  });
`, allowedJSON, builtInsJSON)
	for i := range extensions {
		_, _ = fmt.Fprintf(&source, "  extension%d(guarded);\n", i)
	}
	source.WriteString(`  for (const name of allowed) {
    if (!builtIns.has(name) && !registered.has(name)) throw new Error("allowed extension tool was not registered: " + name);
  }
}
`)
	return []byte(source.String()), nil
}

// Environment is the fixed Pi environment in addition to the separately
// allowlisted caller variables. It replaces, rather than inherits, Pi's
// normal per-user configuration and session locations.
func (c PiConfiguration) Environment() []string {
	return []string{piCodingAgentDir + "=" + c.AgentDirectory, piCodingAgentSessions + "=" + c.SessionDir}
}

// Command returns only documented options from the pinned 0.74 Pi contract.
// The explicit paths remain effective with --no-skills; all global and project
// resource discovery (including AGENTS.md) is disabled.
func (c PiConfiguration) Command() []string {
	// Start with no tools, then use Pi's explicit allowlist. This means an empty
	// list stays empty and Pi additions such as find, ls, or bash never become
	// AgentRun capabilities by default.
	command := []string{"pi", "--mode", "rpc", "--no-session", "--no-tools", "--no-extensions", "--no-skills", "--no-prompt-templates", "--no-themes", "--no-context-files"}
	// Pi receives only the generated adapter and the immutable, declared
	// extension entry points. --no-extensions still disables all discovery
	// sources; explicit --extension is the per-run loading mechanism.
	if c.ToolAdapter != "" {
		command = append(command, "--extension", c.ToolAdapter)
	}
	if c.ExtensionLoader != "" {
		command = append(command, "--extension", c.ExtensionLoader)
	}
	if len(c.ActiveTools) != 0 {
		command = append(command, "--tools", strings.Join(c.ActiveTools, ","))
	}
	for _, skill := range c.Skills {
		command = append(command, "--skill", skill)
	}
	return command
}

func containsTool(tools []string, name string) bool {
	for _, tool := range tools {
		if tool == name {
			return true
		}
	}
	return false
}

func privateDirectory(path, label string) (string, error) {
	if path == "" {
		return "", configurationError("%s is required", label)
	}
	canonical, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", configurationError("%s: %v", label, err)
	}
	info, err := os.Stat(canonical)
	if err != nil || !info.IsDir() {
		if err != nil {
			return "", configurationError("%s: %v", label, err)
		}
		return "", configurationError("%s is not a directory", label)
	}
	return canonical, nil
}

func withinDirectory(root, path string) bool {
	return path != "" && (path == root || strings.HasPrefix(path, root+string(filepath.Separator)))
}
