package agent

import (
	"crypto/sha256"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/Ameb8/agent-run/internal/output"
)

// Snapshot is a private, immutable copy of the resources selected by a
// definition.  Its Definition is reparsed from the copy, so callers never
// need to consult the mutable source package after Snapshot succeeds.
type Snapshot struct {
	Root       string
	Resolution Resolution
	Definition Definition
	Digest     string
	// OutputValidator is nil when output.schema is omitted. Otherwise it was
	// compiled from the immutable snapshot before sandbox preparation.
	OutputValidator *output.Validator
}

// Close removes the private snapshot when it is no longer needed.
func (s *Snapshot) Close() error {
	if s == nil || s.Root == "" {
		return nil
	}
	if err := makeRemovable(s.Root); err != nil {
		return fmt.Errorf("prepare agent snapshot cleanup: %w", err)
	}
	err := os.RemoveAll(s.Root)
	s.Root = ""
	return err
}

type snapshotFile struct {
	path   string
	target string
	bytes  []byte
}

// CreateSnapshot copies exactly the selected resources. Symlink targets are
// read under their selected package-relative names and must remain contained.
func CreateSnapshot(resolution Resolution) (*Snapshot, error) {
	return createSnapshot(resolution, "")
}

// CreateSnapshotIn stages a snapshot below parent. Parent is expected to be a
// private per-run directory owned by the caller. This lets an execution scope
// own the snapshot and all of its other ephemeral state with one cleanup.
func CreateSnapshotIn(resolution Resolution, parent string) (*Snapshot, error) {
	if parent == "" {
		return nil, fmt.Errorf("create agent snapshot: parent directory is required")
	}
	return createSnapshot(resolution, parent)
}

func createSnapshot(resolution Resolution, parent string) (*Snapshot, error) {
	definition, err := ParseAndValidate(resolution)
	if err != nil {
		return nil, err
	}
	files, err := selectedFiles(resolution, definition)
	if err != nil {
		return nil, err
	}
	root, err := os.MkdirTemp(parent, "agentrun-agent-")
	if err != nil {
		return nil, fmt.Errorf("create agent snapshot: %w", err)
	}
	fail := func(err error) (*Snapshot, error) {
		_ = makeRemovable(root)
		_ = os.RemoveAll(root)
		return nil, err
	}
	for _, file := range files {
		path := filepath.Join(root, filepath.FromSlash(file.path))
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			return fail(fmt.Errorf("create agent snapshot: %w", err))
		}
		if err := os.WriteFile(path, file.bytes, 0o400); err != nil {
			return fail(fmt.Errorf("create agent snapshot: %w", err))
		}
	}
	if err := makeReadOnly(root); err != nil {
		return fail(fmt.Errorf("protect agent snapshot: %w", err))
	}
	relDefinition, err := filepath.Rel(resolution.PackageRoot, resolution.DefinitionPath)
	if err != nil || relDefinition == ".." || strings.HasPrefix(relDefinition, ".."+string(filepath.Separator)) {
		return fail(validation("agent definition resolves outside the package"))
	}
	snapshotResolution := resolution
	snapshotResolution.PackageRoot = root
	snapshotResolution.DefinitionPath = filepath.Join(root, relDefinition)
	snapshotDefinition, err := ParseAndValidate(snapshotResolution)
	if err != nil {
		return fail(err)
	}
	var outputValidator *output.Validator
	if snapshotDefinition.OutputSchema != "" {
		outputValidator, err = output.CompileFile(snapshotDefinition.OutputSchema)
		if err != nil {
			return fail(validation("output.schema: %v", err))
		}
	}
	return &Snapshot{Root: root, Resolution: snapshotResolution, Definition: snapshotDefinition, Digest: digest(files), OutputValidator: outputValidator}, nil
}

func selectedFiles(resolution Resolution, definition Definition) ([]snapshotFile, error) {
	var files []snapshotFile
	seenDirectories := make(map[string]bool)
	definitionRel, err := relativePackagePath(resolution.PackageRoot, resolution.DefinitionPath)
	if err != nil {
		return nil, validation("agent definition: %v", err)
	}
	if err := appendSelectedFile(&files, resolution.PackageRoot, definitionRel, resolution.DefinitionPath); err != nil {
		return nil, err
	}
	for i, source := range definition.PromptIncludes {
		if err := appendSelectedFile(&files, resolution.PackageRoot, definition.Agent.Prompt.Includes[i], source); err != nil {
			return nil, validation("prompt.includes: %v", err)
		}
	}
	if err := appendSelectedFile(&files, resolution.PackageRoot, definition.Agent.Prompt.Template, definition.PromptTemplate); err != nil {
		return nil, validation("prompt.template: %v", err)
	}
	if definition.OutputSchema != "" {
		if err := appendSelectedFile(&files, resolution.PackageRoot, definition.Agent.Output.Schema, definition.OutputSchema); err != nil {
			return nil, validation("output.schema: %v", err)
		}
	}
	for i, source := range definition.Skills {
		if err := appendSelectedDirectory(&files, resolution.PackageRoot, filepath.ToSlash(filepath.Join("skills", definition.Agent.Skills[i])), source, seenDirectories); err != nil {
			return nil, validation("skill %q: %v", definition.Agent.Skills[i], err)
		}
	}
	for i, source := range definition.Extensions {
		if err := appendSelectedDirectory(&files, resolution.PackageRoot, filepath.ToSlash(filepath.Join("extensions", definition.Agent.Tools.Extensions[i])), source, seenDirectories); err != nil {
			return nil, validation("extension %q: %v", definition.Agent.Tools.Extensions[i], err)
		}
	}
	seenPath, seenTarget := map[string]bool{}, map[string]bool{}
	for _, file := range files {
		if seenPath[file.path] {
			return nil, validation("selected resources contain duplicate path %q", file.path)
		}
		seenPath[file.path] = true
		if seenTarget[file.target] {
			return nil, validation("selected resources contain duplicate resolved path %q", file.target)
		}
		seenTarget[file.target] = true
	}
	sort.Slice(files, func(i, j int) bool { return files[i].path < files[j].path })
	return files, nil
}

func appendSelectedFile(files *[]snapshotFile, root, logical, source string) error {
	logical, err := cleanRelative(logical)
	if err != nil {
		return err
	}
	target, err := canonicalFile(source, "")
	if err != nil {
		return err
	}
	if !within(root, target) {
		return fmt.Errorf("resolves outside the package")
	}
	bytes, err := os.ReadFile(target)
	if err != nil {
		return err
	}
	*files = append(*files, snapshotFile{path: logical, target: target, bytes: bytes})
	return nil
}

func appendSelectedDirectory(files *[]snapshotFile, root, logical, source string, seen map[string]bool) error {
	logical, err := cleanRelative(logical)
	if err != nil {
		return err
	}
	source, err = canonicalDirectory(source, "")
	if err != nil {
		return err
	}
	if !within(root, source) {
		return fmt.Errorf("resolves outside the package")
	}
	if seen[source] {
		return fmt.Errorf("contains duplicate resolved directory %q", source)
	}
	seen[source] = true
	return filepath.WalkDir(source, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		if entry.Type()&fs.ModeSymlink != 0 {
			target, err := filepath.EvalSymlinks(path)
			if err != nil {
				return err
			}
			info, err := os.Stat(target)
			if err != nil {
				return err
			}
			if info.IsDir() {
				return appendSelectedDirectory(files, root, filepath.ToSlash(filepath.Join(logical, rel)), target, seen)
			}
		}
		return appendSelectedFile(files, root, filepath.ToSlash(filepath.Join(logical, rel)), path)
	})
}

func cleanRelative(path string) (string, error) {
	path = filepath.Clean(path)
	if path == "." || filepath.IsAbs(path) || path == ".." || strings.HasPrefix(path, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("path escapes selected resource")
	}
	return filepath.ToSlash(path), nil
}

func relativePackagePath(root, path string) (string, error) {
	if !within(root, path) {
		return "", fmt.Errorf("resolves outside the package")
	}
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return "", err
	}
	return filepath.ToSlash(rel), nil
}

func digest(files []snapshotFile) string {
	hash := sha256.New()
	for _, file := range files {
		_, _ = hash.Write([]byte(file.path))
		_, _ = hash.Write([]byte{0})
		_, _ = hash.Write([]byte(strconv.Itoa(len(file.bytes))))
		_, _ = hash.Write([]byte{0})
		_, _ = hash.Write(file.bytes)
	}
	return "sha256:" + fmt.Sprintf("%x", hash.Sum(nil))
}

func makeReadOnly(root string) error {
	return filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return os.Chmod(path, 0o500)
		}
		return os.Chmod(path, 0o400)
	})
}

// makeRemovable restores directory write bits before cleanup. A read-only
// snapshot intentionally prevents mutation while in use, but POSIX requires
// write permission on every containing directory to unlink its entries.
func makeRemovable(root string) error {
	return filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return os.Chmod(path, 0o700)
		}
		return nil
	})
}

// VerifyExpectedDigest implements package pinning before any sandbox or
// extension work is allowed to begin.
func VerifyExpectedDigest(snapshot *Snapshot, expected string) error {
	if expected == "" {
		return nil
	}
	if snapshot == nil || snapshot.Digest != expected {
		return validation("agent digest does not match --expect-agent-digest")
	}
	return nil
}
