package agent

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// ValidateExtensions validates the import boundary of the immutable extension
// resources selected for one run. It deliberately does not execute code:
// registration is discovered later, inside the sandbox. Keeping this check at
// the snapshot boundary means the eventual loader cannot resolve a dependency
// from a mutable package directory or a host package cache.
func ValidateExtensions(snapshot *Snapshot) error {
	if snapshot == nil || snapshot.Root == "" {
		return validation("extension validation requires an agent snapshot")
	}
	for _, entry := range snapshot.Definition.Extensions {
		directory := filepath.Dir(entry)
		if err := validateExtensionDirectory(directory); err != nil {
			return validation("extension %q: %v", filepath.Base(directory), err)
		}
	}
	return nil
}

var extensionImport = regexp.MustCompile(`(?m)(?:\bimport\s*(?:[^'";]*?\s+from\s*)?|\bexport\s+(?:[^'";]*?\s+from\s*)?|\bimport\s*\(|\brequire\s*\()\s*['"]([^'"]+)['"]`)
var extensionDynamicImport = regexp.MustCompile(`(?m)\b(?:import|require)\s*\(\s*([^)]*)\)`)

func validateExtensionDirectory(root string) error {
	root, err := filepath.EvalSymlinks(root)
	if err != nil {
		return fmt.Errorf("extension directory is unavailable")
	}
	return filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			if entry.Name() == "node_modules" {
				return fmt.Errorf("dependency directories are not supported")
			}
			return nil
		}
		if unsupportedExtensionFile(entry.Name()) {
			return fmt.Errorf("package manifests and dependency installation files are not supported (%s)", entry.Name())
		}
		if !extensionSource(path) {
			return nil
		}
		contents, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, match := range extensionImport.FindAllStringSubmatch(string(contents), -1) {
			if err := validateExtensionImport(root, path, match[1]); err != nil {
				return err
			}
		}
		for _, match := range extensionDynamicImport.FindAllStringSubmatch(string(contents), -1) {
			value := strings.TrimSpace(match[1])
			if len(value) < 2 || (value[0] != '\'' && value[0] != '"') || value[len(value)-1] != value[0] {
				return fmt.Errorf("dynamic imports and requires must use a string literal")
			}
		}
		return nil
	})
}

func unsupportedExtensionFile(name string) bool {
	switch name {
	case "package.json", "package-lock.json", "npm-shrinkwrap.json", "pnpm-lock.yaml", "yarn.lock", ".npmrc", "bun.lock", "bun.lockb", "deno.json", "deno.jsonc":
		return true
	default:
		return false
	}
}

func extensionSource(path string) bool {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".ts", ".tsx", ".mts", ".cts", ".js", ".jsx", ".mjs", ".cjs":
		return true
	default:
		return false
	}
}

func validateExtensionImport(root, source, specifier string) error {
	if strings.HasPrefix(specifier, ".") {
		if !resolvesExtensionFile(root, filepath.Join(filepath.Dir(source), specifier)) {
			return fmt.Errorf("relative import %q is missing or escapes the extension", specifier)
		}
		return nil
	}
	// Pi's extension interface and Node's node:-qualified runtime modules are
	// the only non-relative modules bundled into the pinned runtime. In
	// particular, do not let Node's bare-module compatibility lookup reach a
	// package cache or node_modules directory.
	if specifier == "@earendil-works/pi-coding-agent" || strings.HasPrefix(specifier, "node:") {
		return nil
	}
	return fmt.Errorf("bare import %q is not supported", specifier)
}

func resolvesExtensionFile(root, candidate string) bool {
	candidates := []string{candidate}
	if filepath.Ext(candidate) == "" {
		for _, suffix := range []string{".ts", ".tsx", ".mts", ".cts", ".js", ".jsx", ".mjs", ".cjs"} {
			candidates = append(candidates, candidate+suffix)
		}
		for _, suffix := range []string{"index.ts", "index.tsx", "index.mts", "index.cts", "index.js", "index.jsx", "index.mjs", "index.cjs"} {
			candidates = append(candidates, filepath.Join(candidate, suffix))
		}
	}
	for _, path := range candidates {
		canonical, err := filepath.EvalSymlinks(path)
		if err != nil || !within(root, canonical) {
			continue
		}
		info, err := os.Stat(canonical)
		if err == nil && !info.IsDir() {
			return true
		}
	}
	return false
}
