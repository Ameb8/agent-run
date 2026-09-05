package runtime

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRunScopesArePrivateIndependentAndCleanedUp(t *testing.T) {
	first, err := NewRunScope()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = first.Close() }()
	second, err := NewRunScope()
	if err != nil {
		t.Fatal(err)
	}

	if first.Root == second.Root {
		t.Fatal("concurrent scopes share a root")
	}
	for _, path := range []string{first.Root, first.Resources, first.Configuration, first.Temporary, second.Root, second.Resources, second.Configuration, second.Temporary} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatalf("stat %q: %v", path, err)
		}
		if info.Mode().Perm() != 0o700 {
			t.Errorf("mode for %q = %o, want 0700", path, info.Mode().Perm())
		}
	}
	firstFile := filepath.Join(first.Temporary, "canary")
	if err := os.WriteFile(firstFile, []byte("first"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(second.Temporary, "canary")); !os.IsNotExist(err) {
		t.Fatalf("second scope observed first temporary file: %v", err)
	}
	staged := filepath.Join(second.Resources, "staged")
	if err := os.Mkdir(staged, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(staged, "resource"), []byte("immutable"), 0o400); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(staged, 0o500); err != nil {
		t.Fatal(err)
	}
	root := second.Root
	if err := second.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Fatalf("closed scope remains on disk: %v", err)
	}
}
