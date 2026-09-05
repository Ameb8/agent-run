package agent

import (
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/Ameb8/agent-run/internal/contract"
	"gopkg.in/yaml.v3"
)

// Definition is a fully static, effective agent definition. All paths have
// been canonicalized and checked to be contained by the selected package.
// Extension code is intentionally not loaded here.
type Definition struct {
	Agent          contract.AgentDefinition
	PromptTemplate string
	PromptIncludes []string
	OutputSchema   string
	Skills         []string
	Extensions     []string
}

type definitionPresence struct{ networkMode, maxTurns, timeoutS bool }

var (
	identifier   = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
	simpleID     = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]*$`)
	builtInTools = map[string]struct{}{
		"read": {}, "grep": {}, "write": {}, "edit": {}, "shell": {},
	}
)

// ParseAndValidate strictly decodes and statically validates the definition
// selected by Resolve. It has no runtime side effects.
func ParseAndValidate(resolution Resolution) (Definition, error) {
	file, err := os.Open(resolution.DefinitionPath)
	if err != nil {
		return Definition{}, validation("agent definition: %v", err)
	}
	defer file.Close()

	decoder := yaml.NewDecoder(file)
	decoder.KnownFields(true)
	var definition contract.AgentDefinition
	if err := decoder.Decode(&definition); err != nil {
		return Definition{}, validation("agent definition: %v", err)
	}
	var extra yaml.Node
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return Definition{}, validation("agent definition contains more than one YAML document")
		}
		return Definition{}, validation("agent definition: %v", err)
	}
	presence, err := definitionFieldPresence(resolution.DefinitionPath)
	if err != nil {
		return Definition{}, validation("agent definition: %v", err)
	}

	if err := validateDefinition(&definition, presence); err != nil {
		return Definition{}, err
	}
	result := Definition{Agent: definition}
	result.PromptTemplate, err = packageFile(resolution.PackageRoot, definition.Prompt.Template)
	if err != nil {
		return Definition{}, validation("prompt.template: %v", err)
	}
	for _, include := range definition.Prompt.Includes {
		path, pathErr := packageFile(resolution.PackageRoot, include)
		if pathErr != nil {
			return Definition{}, validation("prompt.includes: %v", pathErr)
		}
		result.PromptIncludes = append(result.PromptIncludes, path)
	}
	if definition.Output.Schema != "" {
		result.OutputSchema, err = packageFile(resolution.PackageRoot, definition.Output.Schema)
		if err != nil {
			return Definition{}, validation("output.schema: %v", err)
		}
	}
	for _, skill := range definition.Skills {
		path, pathErr := packageDirectory(resolution.PackageRoot, "skills", skill, "SKILL.md")
		if pathErr != nil {
			return Definition{}, validation("skill %q: %v", skill, pathErr)
		}
		result.Skills = append(result.Skills, path)
	}
	for _, extension := range definition.Tools.Extensions {
		path, pathErr := packageDirectory(resolution.PackageRoot, "extensions", extension, "index.ts")
		if pathErr != nil {
			return Definition{}, validation("extension %q: %v", extension, pathErr)
		}
		result.Extensions = append(result.Extensions, path)
	}
	return result, nil
}

func validateDefinition(d *contract.AgentDefinition, presence definitionPresence) error {
	if d.SchemaVersion != contract.DefinitionSchemaVersion {
		return validation("schema_version must be %d", contract.DefinitionSchemaVersion)
	}
	if d.Name == "" || !simpleName(d.Name) {
		return validation("name must be a simple identifier")
	}
	if d.Model.Model == "" {
		return validation("model.model is required")
	}
	switch d.Model.Provider {
	case contract.ProviderOpenAICompatible:
		if d.Model.Endpoint == "" || d.Model.APIKeyEnv == "" {
			return validation("model.endpoint and model.api_key_env are required for openai-compatible")
		}
		if err := validEndpoint(d.Model.Endpoint); err != nil {
			return validation("model.endpoint: %v", err)
		}
		if !identifier.MatchString(d.Model.APIKeyEnv) {
			return validation("model.api_key_env must be an environment-variable name")
		}
	case contract.ProviderOpenAISubscription:
		if d.Model.Endpoint != "" || d.Model.APIKeyEnv != "" {
			return validation("model.endpoint and model.api_key_env are not supported for openai-subscription")
		}
	default:
		return validation("model.provider is unsupported")
	}
	if d.Permission != contract.PermissionReadOnly && d.Permission != contract.PermissionReadWrite {
		return validation("permission must be read-only or read-write")
	}
	if d.Prompt.Template == "" {
		return validation("prompt.template is required")
	}
	if err := uniqueIdentifiers("skills", d.Skills, simpleName); err != nil {
		return err
	}
	if err := uniqueIdentifiers("tools.extensions", d.Tools.Extensions, simpleName); err != nil {
		return err
	}
	if err := uniqueIdentifiers("tools.allow", d.Tools.Allow, simpleName); err != nil {
		return err
	}
	if err := uniqueIdentifiers("environment.allow", d.Environment.Allow, identifier.MatchString); err != nil {
		return err
	}
	if d.Model.Provider == contract.ProviderOpenAICompatible && contains(d.Environment.Allow, d.Model.APIKeyEnv) {
		return validation("environment.allow must not include model.api_key_env")
	}
	if err := uniqueIdentifiers("prompt.inputs.required", d.Prompt.Inputs.Required, identifier.MatchString); err != nil {
		return err
	}
	if err := uniqueIdentifiers("prompt.inputs.optional", d.Prompt.Inputs.Optional, identifier.MatchString); err != nil {
		return err
	}
	for _, name := range d.Prompt.Inputs.Required {
		if contains(d.Prompt.Inputs.Optional, name) {
			return validation("prompt input %q appears in both required and optional", name)
		}
	}
	if !presence.networkMode {
		d.Network.Mode = contract.NetworkNone
	}
	if d.Network.Mode != contract.NetworkNone && d.Network.Mode != contract.NetworkAllowlist {
		return validation("network.mode must be none or allowlist")
	}
	if d.Network.Mode == contract.NetworkNone && len(d.Network.Hosts) != 0 {
		return validation("network.hosts requires network.mode allowlist")
	}
	if err := uniqueIdentifiers("network.hosts", d.Network.Hosts, validHost); err != nil {
		return err
	}
	for _, tool := range d.Tools.Allow {
		// An extension's registrations can only be discovered after it is
		// loaded in the sandbox. Without an extension, every allowed name is
		// statically knowable and must be one of AgentRun's stable built-ins.
		if len(d.Tools.Extensions) == 0 && !isBuiltInTool(tool) {
			return validation("tools.allow %q is not a v1 built-in tool", tool)
		}
		if d.Permission == contract.PermissionReadOnly && (tool == "write" || tool == "edit") {
			return validation("tools.allow %q is not allowed with read-only permission", tool)
		}
	}
	if !presence.maxTurns {
		d.Limits.MaxTurns = contract.DefaultMaxTurns
	}
	if !presence.timeoutS {
		d.Limits.TimeoutS = contract.DefaultTimeoutSeconds
	}
	if d.Limits.MaxTurns <= 0 || d.Limits.TimeoutS <= 0 {
		return validation("limits must be positive integers")
	}
	return nil
}

// IsBuiltInTool reports membership in the closed v1 built-in tool set. It is
// exported for the runtime adapter, which must reject an attempted activation
// before passing an unknown name to Pi.
func IsBuiltInTool(name string) bool {
	return isBuiltInTool(name)
}

func isBuiltInTool(name string) bool {
	_, ok := builtInTools[name]
	return ok
}

func definitionFieldPresence(path string) (definitionPresence, error) {
	contents, err := os.ReadFile(path)
	if err != nil {
		return definitionPresence{}, err
	}
	var document yaml.Node
	if err := yaml.Unmarshal(contents, &document); err != nil {
		return definitionPresence{}, err
	}
	if len(document.Content) != 1 || document.Content[0].Kind != yaml.MappingNode {
		return definitionPresence{}, errors.New("must be a YAML mapping")
	}
	presence := definitionPresence{}
	root := document.Content[0]
	for i := 0; i < len(root.Content); i += 2 {
		key, value := root.Content[i].Value, root.Content[i+1]
		if key == "network" && value.Kind == yaml.MappingNode {
			presence.networkMode = mappingHas(value, "mode")
		}
		if key == "limits" && value.Kind == yaml.MappingNode {
			presence.maxTurns = mappingHas(value, "max_turns")
			presence.timeoutS = mappingHas(value, "timeout_s")
		}
	}
	return presence, nil
}

func mappingHas(mapping *yaml.Node, name string) bool {
	for i := 0; i < len(mapping.Content); i += 2 {
		if mapping.Content[i].Value == name {
			return true
		}
	}
	return false
}

func packageFile(root, name string) (string, error) {
	if name == "" {
		return "", errors.New("path is required")
	}
	path, err := canonicalFile(filepath.Join(root, name), "")
	if err != nil {
		return "", err
	}
	if !within(root, path) {
		return "", errors.New("resolves outside the package")
	}
	return path, nil
}

func packageDirectory(root, parent, name, entry string) (string, error) {
	if !simpleName(name) {
		return "", errors.New("must be a simple identifier")
	}
	parentPath, err := canonicalDirectory(filepath.Join(root, parent), "")
	if err != nil {
		return "", err
	}
	if !within(root, parentPath) {
		return "", errors.New("resolves outside the package")
	}
	path, err := canonicalDirectory(filepath.Join(parentPath, name), "")
	if err != nil {
		return "", err
	}
	if !within(parentPath, path) {
		return "", errors.New("resolves outside the package")
	}
	entryPath, err := canonicalFile(filepath.Join(path, entry), "")
	if err != nil {
		return "", fmt.Errorf("%s: %w", entry, err)
	}
	if !within(path, entryPath) {
		return "", fmt.Errorf("%s resolves outside the selected directory", entry)
	}
	return path, nil
}

func within(root, path string) bool {
	relative, err := filepath.Rel(root, path)
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

func simpleName(name string) bool {
	return simpleID.MatchString(name)
}

func contains(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func uniqueIdentifiers(field string, values []string, valid func(string) bool) error {
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		if !valid(value) {
			return validation("%s contains invalid name %q", field, value)
		}
		if _, ok := seen[value]; ok {
			return validation("%s contains duplicate declaration %q", field, value)
		}
		seen[value] = struct{}{}
	}
	return nil
}

func validEndpoint(value string) error {
	u, err := url.Parse(value)
	if err != nil || !u.IsAbs() || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil {
		return errors.New("must be an absolute HTTP or HTTPS URL without user information")
	}
	return nil
}

func validHost(value string) bool {
	if value == "" || net.ParseIP(value) != nil || strings.ContainsAny(value, "*/:@") || len(value) > 253 {
		return false
	}
	for _, label := range strings.Split(value, ".") {
		if label == "" || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, char := range label {
			if !(char >= 'a' && char <= 'z' || char >= 'A' && char <= 'Z' || char >= '0' && char <= '9' || char == '-') {
				return false
			}
		}
	}
	return true
}
