package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestValidateExtensionsAcceptsBundledAndRelativeImports(t *testing.T) {
	snapshot := extensionSnapshot(t, map[string]string{
		"index.ts": `import type { ExtensionAPI } from "@earendil-works/pi-coding-agent";
import { helper } from "./lib/helper";
import path from "node:path";
export default function (_pi: ExtensionAPI) { return helper(path.sep); }`,
		"lib/helper.ts": `export function helper(value: string) { return value; }`,
	})
	defer snapshot.Close()
	if err := ValidateExtensions(snapshot); err != nil {
		t.Fatal(err)
	}
}

func TestValidateExtensionsRejectsUnsupportedDependenciesAndEscapes(t *testing.T) {
	for name, files := range map[string]map[string]string{
		"bare":     {"index.ts": `import "left-pad";`},
		"missing":  {"index.ts": `import "./missing";`},
		"escape":   {"index.ts": `import "../../outside";`},
		"dynamic":  {"index.ts": `import(name);`},
		"manifest": {"index.ts": `export default {};`, "package.json": `{}`},
	} {
		t.Run(name, func(t *testing.T) {
			snapshot := extensionSnapshot(t, files)
			defer snapshot.Close()
			err := ValidateExtensions(snapshot)
			if err == nil || !strings.Contains(err.Error(), "VALIDATION") {
				t.Fatalf("error = %v, want validation failure", err)
			}
		})
	}
}

func extensionSnapshot(t *testing.T, files map[string]string) *Snapshot {
	t.Helper()
	f := newDefinitionFixture(t)
	f.write("prompts/main.tmpl", "prompt")
	for path, contents := range files {
		f.write(filepath.ToSlash(filepath.Join("extensions/search", path)), contents)
	}
	f.definition(strings.TrimSuffix(validDefinition(), "\n") + "\ntools:\n  extensions: [search]\n")
	snapshot, err := CreateSnapshot(f.resolution())
	if err != nil {
		t.Fatal(err)
	}
	return snapshot
}

func TestValidateExtensionsRejectsNodeModules(t *testing.T) {
	snapshot := extensionSnapshot(t, map[string]string{"index.ts": `export default {};`, "node_modules/x/index.js": ""})
	defer snapshot.Close()
	err := ValidateExtensions(snapshot)
	if err == nil || !strings.Contains(err.Error(), "dependency directories") {
		t.Fatalf("error = %v", err)
	}
	if _, err := os.Stat(snapshot.Root); err != nil {
		t.Fatal(err)
	}
}
