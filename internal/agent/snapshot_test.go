package agent

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Ameb8/agent-run/internal/contract"
)

func TestDigestGoldenVectorUsesSortedPathsAndByteLengths(t *testing.T) {
	files := []snapshotFile{{path: "b", target: "one", bytes: []byte("A")}, {path: "a", target: "two", bytes: []byte("hi")}}
	const want = "sha256:4fef6e745237e4c0013da50b502e5b6d807e4577556894e73261d6f455ca342d"
	if got := digest([]snapshotFile{files[1], files[0]}); got != want {
		t.Fatalf("digest = %s, want %s", got, want)
	}
	if digest([]snapshotFile{{path: "a", target: "two", bytes: []byte("h!")}, files[0]}) == want {
		t.Fatal("digest ignored changed bytes of the same byte length")
	}
}

func TestSnapshotIsImmutableAndSelectsOnlyPackageResources(t *testing.T) {
	f := newDefinitionFixture(t)
	f.write("prompts/main.tmpl", "original")
	f.write("skills/review/SKILL.md", "skill")
	f.write("skills/review/references/guide.md", "guide")
	f.write("extensions/search/index.ts", "extension")
	f.write("unselected/private.txt", "not selected")
	f.definition(strings.TrimSuffix(validDefinition(), "\n") + "\nskills: [review]\ntools:\n  extensions: [search]\n")
	snapshot, err := CreateSnapshot(f.resolution())
	if err != nil {
		t.Fatal(err)
	}
	defer snapshot.Close()
	if _, err := os.Stat(filepath.Join(snapshot.Root, "unselected", "private.txt")); !os.IsNotExist(err) {
		t.Fatalf("unselected file stat error = %v", err)
	}
	if err := os.WriteFile(f.path("prompts/main.tmpl"), []byte("mutated"), 0o600); err != nil {
		t.Fatal(err)
	}
	contents, err := os.ReadFile(snapshot.Definition.PromptTemplate)
	if err != nil || string(contents) != "original" {
		t.Fatalf("snapshot prompt = %q, %v", contents, err)
	}
	if _, err := os.Stat(filepath.Join(snapshot.Root, "skills", "review", "references", "guide.md")); err != nil {
		t.Fatalf("selected supporting skill file missing: %v", err)
	}
}

func TestSnapshotCanBeStagedInItsRunPrivateParent(t *testing.T) {
	f := newDefinitionFixture(t)
	f.write("prompts/main.tmpl", "original")
	f.definition(validDefinition())
	parent := t.TempDir()
	snapshot, err := CreateSnapshotIn(f.resolution(), parent)
	if err != nil {
		t.Fatal(err)
	}
	defer snapshot.Close()
	if filepath.Dir(snapshot.Root) != parent {
		t.Fatalf("snapshot root %q is not below private parent %q", snapshot.Root, parent)
	}
	if err := os.WriteFile(f.path("prompts/main.tmpl"), []byte("mutated"), 0o600); err != nil {
		t.Fatal(err)
	}
	contents, err := os.ReadFile(snapshot.Definition.PromptTemplate)
	if err != nil || string(contents) != "original" {
		t.Fatalf("staged snapshot prompt = %q, %v", contents, err)
	}
}

func TestSnapshotDigestIncludesSelectedResourcesButNotUnselectedFiles(t *testing.T) {
	makeSnapshot := func(prompt, ignored string) *Snapshot {
		t.Helper()
		f := newDefinitionFixture(t)
		f.write("prompts/main.tmpl", prompt)
		f.write("unselected/notes.txt", ignored)
		f.definition(validDefinition())
		snapshot, err := CreateSnapshot(f.resolution())
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = snapshot.Close() })
		return snapshot
	}
	baseline := makeSnapshot("prompt", "one")
	if got := makeSnapshot("prompt", "two").Digest; got != baseline.Digest {
		t.Fatalf("unselected content changed digest: %s != %s", got, baseline.Digest)
	}
	if got := makeSnapshot("changed prompt", "one").Digest; got == baseline.Digest {
		t.Fatal("selected prompt did not change digest")
	}
}

func TestSnapshotRejectsAliasedSelectedTargetsAndDigestPinning(t *testing.T) {
	f := newDefinitionFixture(t)
	f.write("prompts/main.tmpl", "prompt")
	if err := os.Symlink(f.path("prompts/main.tmpl"), f.path("prompts/alias.tmpl")); err != nil {
		t.Fatal(err)
	}
	f.definition(replaceTopLevel(validDefinition(), "prompt:\n  template: prompts/main.tmpl\n  includes: [prompts/alias.tmpl]\n"))
	_, err := CreateSnapshot(f.resolution())
	assertDefinitionValidation(t, err, "duplicate resolved path")

	f = newDefinitionFixture(t)
	f.write("prompts/main.tmpl", "prompt")
	f.definition(validDefinition())
	snapshot, err := CreateSnapshot(f.resolution())
	if err != nil {
		t.Fatal(err)
	}
	defer snapshot.Close()
	if err := VerifyExpectedDigest(snapshot, snapshot.Digest); err != nil {
		t.Fatal(err)
	}
	err = VerifyExpectedDigest(snapshot, "sha256:wrong")
	var commandErr *contract.CommandError
	if err == nil || !strings.Contains(err.Error(), "VALIDATION") || !strings.Contains(err.Error(), "expect-agent-digest") || !errors.As(err, &commandErr) {
		t.Fatalf("pin error = %v", err)
	}
}

func TestSnapshotValidatesSelectedOutputSchema(t *testing.T) {
	for _, schema := range []string{
		`{"$schema":"https://json-schema.org/draft/2019-09/schema"}`,
		`{"$ref":"https://example.test/result.json"}`,
		`{"type":"object",`,
	} {
		f := newDefinitionFixture(t)
		f.write("prompts/main.tmpl", "prompt")
		f.write("schemas/result.json", schema)
		f.definition(strings.TrimSuffix(validDefinition(), "\n") + "\noutput:\n  schema: schemas/result.json\n")
		if _, err := CreateSnapshot(f.resolution()); err == nil || !strings.Contains(err.Error(), "output.schema") {
			t.Errorf("CreateSnapshot(%q) = %v, want output.schema validation failure", schema, err)
		}
	}
}

func TestSnapshotRetainsCompiledOutputValidator(t *testing.T) {
	f := newDefinitionFixture(t)
	f.write("prompts/main.tmpl", "prompt")
	f.write("schemas/result.json", `{"type":"boolean"}`)
	f.definition(strings.TrimSuffix(validDefinition(), "\n") + "\noutput:\n  schema: schemas/result.json\n")
	snapshot, err := CreateSnapshot(f.resolution())
	if err != nil {
		t.Fatal(err)
	}
	defer snapshot.Close()
	if snapshot.OutputValidator == nil {
		t.Fatal("OutputValidator is nil")
	}
	if _, err := snapshot.OutputValidator.Parse("true"); err != nil {
		t.Fatalf("compiled validator rejected valid output: %v", err)
	}
}
