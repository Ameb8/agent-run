package runtime

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// RunScope owns the host files created for exactly one invocation.  Its
// directory is deliberately private to the invoking user; no global pi or
// AgentRun state is consulted or modified. Resources are staged below Root by
// agent.CreateSnapshotIn, while Configuration and Temporary are reserved for
// the execution layer.
//
// Call Close exactly once when the invocation finishes, including setup
// failures. It removes all three locations and any staged resources.
type RunScope struct {
	Root          string
	Resources     string
	Configuration string
	Temporary     string
}

func NewRunScope() (*RunScope, error) {
	root, err := os.MkdirTemp("", "agentrun-run-")
	if err != nil {
		return nil, fmt.Errorf("create private run storage: %w", err)
	}
	fail := func(err error) (*RunScope, error) {
		_ = os.RemoveAll(root)
		return nil, err
	}
	resources := filepath.Join(root, "resources")
	configuration := filepath.Join(root, "config")
	temporary := filepath.Join(root, "tmp")
	for _, path := range []string{resources, configuration, temporary} {
		if err := os.Mkdir(path, 0o700); err != nil {
			return fail(fmt.Errorf("create private run storage: %w", err))
		}
	}
	return &RunScope{Root: root, Resources: resources, Configuration: configuration, Temporary: temporary}, nil
}

// Close deterministically removes all run-scoped host state. It is safe to
// call repeatedly, which lets every setup path defer it immediately.
func (s *RunScope) Close() error {
	if s == nil || s.Root == "" {
		return nil
	}
	if err := makeScopeRemovable(s.Root); err != nil {
		return fmt.Errorf("prepare private run storage cleanup: %w", err)
	}
	err := os.RemoveAll(s.Root)
	s.Root = ""
	s.Resources = ""
	s.Configuration = ""
	s.Temporary = ""
	return err
}

// makeScopeRemovable permits cleanup of a staged read-only resource snapshot.
// Removing a directory tree requires write permission on each directory,
// including directories intentionally made immutable during staging.
func makeScopeRemovable(root string) error {
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
