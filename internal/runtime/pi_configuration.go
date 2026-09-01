package runtime

import (
	"encoding/json"
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
)

// PiConfiguration is the complete, run-local resource contract passed to the
// pinned Pi CLI.  It deliberately contains no model credential, caller
// environment value, package path, or discovery source.
type PiConfiguration struct {
	AgentDirectory string
	SessionDir     string
	Settings       string
	Skills         []string
	PromptTemplate string
	OutputSchema   string
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
	return result, nil
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
	// Start with no tools. The later tool adapter may add only the definition's
	// validated allowlist; a skill is never a source of tool activation.
	command := []string{"pi", "--mode", "rpc", "--no-session", "--no-tools", "--no-extensions", "--no-skills", "--no-prompt-templates", "--no-themes", "--no-context-files"}
	for _, skill := range c.Skills {
		command = append(command, "--skill", skill)
	}
	return command
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
