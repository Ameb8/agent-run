// Package agent resolves an agent definition to its package boundary.
package agent

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/Ameb8/agent-run/internal/contract"
	"gopkg.in/yaml.v3"
)

// ResolveOptions supplies the filesystem context for an agent selection.
// CurrentDir and UserHome may be supplied by callers that need deterministic
// resolution (including tests); empty values use the process values.
type ResolveOptions struct {
	Workspace           string
	Selection           string
	AllowWorkspaceAgent bool
	CurrentDir          string
	UserHome            string
}

// Resolution identifies a selected definition and the package that contains
// it. The resolver reads only a named definition's name field; package
// resources and full definition validation are deliberately deferred.
type Resolution struct {
	Workspace      string
	DefinitionPath string
	PackageRoot    string
	Origin         contract.PackageOrigin
}

// Resolve selects an agent according to the v1 workspace-local then user
// precedence rules, or resolves an explicit definition path.
func Resolve(options ResolveOptions) (Resolution, error) {
	if options.Workspace == "" {
		return Resolution{}, validation("--workspace is required")
	}
	if options.Selection == "" {
		return Resolution{}, validation("agent name or path is required")
	}

	workspace, err := canonicalDirectory(options.Workspace, options.CurrentDir)
	if err != nil {
		return Resolution{}, validation("workspace: %v", err)
	}
	if isPathSelection(options.Selection) {
		return resolvePath(workspace, options)
	}
	if err := validRequestedName(options.Selection); err != nil {
		return Resolution{}, validation("agent name: %v", err)
	}
	resolution, err := resolveNamed(workspace, options)
	if err != nil {
		return Resolution{}, err
	}
	resolution.Workspace = workspace
	return resolution, nil
}

func resolvePath(workspace string, options ResolveOptions) (Resolution, error) {
	path, err := canonicalFile(options.Selection, options.CurrentDir)
	if err != nil {
		return Resolution{}, validation("agent path: %v", err)
	}
	agentsDirectory := filepath.Dir(path)
	if filepath.Base(agentsDirectory) != "agents" {
		return Resolution{}, validation("agent path %q must reside in an agents directory", path)
	}
	return Resolution{
		Workspace: workspace, DefinitionPath: path, PackageRoot: filepath.Dir(agentsDirectory),
		Origin: contract.PackageOriginPath,
	}, nil
}

func resolveNamed(workspace string, options ResolveOptions) (Resolution, error) {
	local := filepath.Join(workspace, ".agentrun", "agents", options.Selection+".yaml")
	localExists, err := fileExists(local)
	if err != nil {
		return Resolution{}, validation("workspace agent %q: %v", local, err)
	}
	if localExists {
		if !options.AllowWorkspaceAgent {
			return Resolution{}, validation("workspace agent %q requires --allow-workspace-agent", local)
		}
		return resolveNamedAt(local, workspace, contract.PackageOriginWorkspace, options.Selection)
	}

	home, err := userHome(options)
	if err != nil {
		return Resolution{}, validation("user configuration: %v", err)
	}
	global := filepath.Join(home, ".agentrun", "agents", options.Selection+".yaml")
	globalExists, err := fileExists(global)
	if err != nil {
		return Resolution{}, validation("user agent %q: %v", global, err)
	}
	if !globalExists {
		return Resolution{}, validation("agent %q was not found in %q or %q", options.Selection, local, global)
	}
	return resolveNamedAt(global, home, contract.PackageOriginUser, options.Selection)
}

func resolveNamedAt(candidate, owner string, origin contract.PackageOrigin, requested string) (Resolution, error) {
	definition, err := canonicalFile(candidate, "")
	if err != nil {
		return Resolution{}, validation("agent definition %q: %v", candidate, err)
	}
	packageRoot, err := namedPackageRoot(definition, owner)
	if err != nil {
		return Resolution{}, validation("agent definition %q: %v", definition, err)
	}
	if filepath.Base(definition) != requested+".yaml" {
		return Resolution{}, validation("agent definition filename %q does not match requested name %q", filepath.Base(definition), requested)
	}
	name, err := definitionName(definition)
	if err != nil {
		return Resolution{}, validation("agent definition %q: %v", definition, err)
	}
	if name != requested {
		return Resolution{}, validation("agent definition name %q does not match requested name %q", name, requested)
	}
	return Resolution{DefinitionPath: definition, PackageRoot: packageRoot, Origin: origin}, nil
}

func namedPackageRoot(definition, owner string) (string, error) {
	owner, err := canonicalDirectory(owner, "")
	if err != nil {
		return "", err
	}
	configuration := filepath.Join(owner, ".agentrun")
	configuration, err = canonicalDirectory(configuration, "")
	if err != nil {
		return "", err
	}
	if filepath.Dir(configuration) != owner {
		return "", errors.New(".agentrun directory escapes its owner")
	}
	agentsDirectory := filepath.Join(configuration, "agents")
	agentsDirectory, err = canonicalDirectory(agentsDirectory, "")
	if err != nil {
		return "", err
	}
	if filepath.Dir(agentsDirectory) != configuration {
		return "", errors.New("agents directory escapes its package root")
	}
	if filepath.Dir(definition) != agentsDirectory {
		return "", errors.New("definition escapes its agents directory")
	}
	return configuration, nil
}

func definitionName(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer func() { _ = file.Close() }()

	decoder := yaml.NewDecoder(file)
	var document yaml.Node
	if err := decoder.Decode(&document); err != nil {
		return "", err
	}
	var extra yaml.Node
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return "", errors.New("contains more than one YAML document")
		}
		return "", err
	}
	if len(document.Content) != 1 || document.Content[0].Kind != yaml.MappingNode {
		return "", errors.New("must be a YAML mapping with a name field")
	}
	mapping := document.Content[0]
	for index := 0; index < len(mapping.Content); index += 2 {
		if mapping.Content[index].Value == "name" {
			value := mapping.Content[index+1]
			if value.Kind != yaml.ScalarNode || value.Tag != "!!str" || value.Value == "" {
				return "", errors.New("name must be a non-empty string")
			}
			return value.Value, nil
		}
	}
	return "", errors.New("name is required")
}

func isPathSelection(selection string) bool {
	return filepath.IsAbs(selection) || strings.ContainsRune(selection, filepath.Separator) || strings.HasSuffix(selection, ".yaml")
}

func validRequestedName(name string) error {
	if name == "." || name == ".." || strings.ContainsRune(name, filepath.Separator) {
		return errors.New("must be a simple name, not a path")
	}
	return nil
}

func canonicalDirectory(path, currentDir string) (string, error) {
	path, err := absolute(path, currentDir)
	if err != nil {
		return "", err
	}
	path, err = filepath.EvalSymlinks(path)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", errors.New("is not a directory")
	}
	return path, nil
}

func canonicalFile(path, currentDir string) (string, error) {
	path, err := absolute(path, currentDir)
	if err != nil {
		return "", err
	}
	path, err = filepath.EvalSymlinks(path)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() {
		return "", errors.New("is not a regular file")
	}
	return path, nil
}

func absolute(path, currentDir string) (string, error) {
	if filepath.IsAbs(path) {
		return filepath.Clean(path), nil
	}
	if currentDir == "" {
		var err error
		currentDir, err = os.Getwd()
		if err != nil {
			return "", err
		}
	}
	return filepath.Abs(filepath.Join(currentDir, path))
}

func userHome(options ResolveOptions) (string, error) {
	if options.UserHome != "" {
		return canonicalDirectory(options.UserHome, options.CurrentDir)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return canonicalDirectory(home, options.CurrentDir)
}

func fileExists(path string) (bool, error) {
	_, err := os.Lstat(path)
	if err == nil {
		return true, nil
	}
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	return false, err
}

func validation(format string, args ...any) error {
	return &contract.CommandError{Category: contract.ErrorValidation, Message: fmt.Sprintf(format, args...)}
}
