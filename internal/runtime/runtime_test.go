package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/Ameb8/agent-run/internal/contract"
)

const testDigest = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

func TestEmbeddedManifestIsStableAndComplete(t *testing.T) {
	t.Parallel()

	manifest, err := LoadManifest()
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	const want = `{"schema_version":1,"image":"agentrun-runtime:private","image_digest":"sha256:68743e5dc0aed0db80ca1d5275d68919b45c1f2ad8bf85f7823f4d47fbaea659","pi":{"package":"@earendil-works/pi-coding-agent","version":"0.74.0"},"javascript_version":"v22.14.0","built_in_tools":[{"name":"read","description":"read a file or list a directory inside the workspace"},{"name":"grep","description":"search file contents inside the workspace"},{"name":"write","description":"create or replace a workspace file"},{"name":"edit","description":"apply a targeted change to a workspace file"},{"name":"shell","description":"execute a non-interactive command with the workspace as its working directory"}]}`
	if got := string(encoded); got != want {
		t.Fatalf("manifest serialization = %s\nwant %s", got, want)
	}
	identity := manifest.Identity("1.2.3")
	if identity != (contract.RuntimeIdentity{AgentRunVersion: "1.2.3", PiVersion: "0.74.0", JavaScriptVersion: "v22.14.0", ImageDigest: manifest.ImageDigest}) {
		t.Fatalf("identity = %#v", identity)
	}
}

func TestParseManifestRejectsMalformedOrIncompleteContent(t *testing.T) {
	t.Parallel()

	for _, input := range []string{
		`{}`,
		`{"schema_version":1,"image":"x","image_digest":"sha256:no","pi":{"package":"p","version":"v"},"javascript_version":"v","built_in_tools":[{"name":"read","description":"x"}]}`,
		`{"schema_version":1,"image":"x","image_digest":"` + testDigest + `","pi":{"package":"p","version":"v"},"javascript_version":"v","built_in_tools":[{"name":"read","description":"x"}],"unknown":true}`,
		`{} {}`,
	} {
		if _, err := ParseManifest([]byte(input)); err == nil {
			t.Errorf("ParseManifest(%q) succeeded", input)
		}
	}
}

func TestVerifierAcceptsOnlyTheDeclaredLocalImageDigest(t *testing.T) {
	t.Parallel()

	manifest := testManifest()
	for _, test := range []struct {
		name      string
		inspector ImageInspector
		wantError bool
	}{
		{name: "known good", inspector: fakeInspector{digests: []string{"sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", testDigest}}},
		{name: "missing image", inspector: fakeInspector{err: errors.New("No such image")}, wantError: true},
		{name: "wrong tag has no digest", inspector: fakeInspector{}, wantError: true},
		{name: "tampered image", inspector: fakeInspector{digests: []string{"sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"}}, wantError: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			verifier := Verifier{Manifest: manifest, Inspector: test.inspector, Version: "1.2.3"}
			identity, err := verifier.Verify(context.Background())
			if test.wantError {
				var commandError *contract.CommandError
				if !errors.As(err, &commandError) || commandError.Category != contract.ErrorConfiguration {
					t.Fatalf("Verify() error = %v, want CONFIGURATION", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if identity.ImageDigest != testDigest || identity.AgentRunVersion != "1.2.3" {
				t.Fatalf("identity = %#v", identity)
			}
		})
	}
}

type fakeInspector struct {
	digests []string
	err     error
}

func (f fakeInspector) LocalImageDigests(_ context.Context, _ string) ([]string, error) {
	return f.digests, f.err
}

func testManifest() Manifest {
	return Manifest{
		SchemaVersion:     ManifestSchemaVersion,
		Image:             "agentrun-runtime:test",
		ImageDigest:       testDigest,
		Pi:                Pi{Package: "pi", Version: "1.0.0"},
		JavaScriptVersion: "v22.0.0",
		BuiltInTools:      []BuiltInTool{{Name: "read", Description: "read"}},
	}
}
