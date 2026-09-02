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
	if len(encoded) == 0 || len(manifest.Images) != 2 {
		t.Fatalf("manifest serialization or architectures = %s %#v", encoded, manifest.Images)
	}
	identity, err := manifest.Identity("1.2.3", "amd64")
	if err != nil {
		t.Fatal(err)
	}
	if identity != (contract.RuntimeIdentity{AgentRunVersion: "1.2.3", PiVersion: "0.74.0", JavaScriptVersion: "v22.14.0", ImageDigest: manifest.Images["amd64"].ImageDigest}) {
		t.Fatalf("identity = %#v", identity)
	}
}

func TestParseManifestRejectsMalformedOrIncompleteContent(t *testing.T) {
	t.Parallel()

	for _, input := range []string{
		`{}`,
		`{"schema_version":2,"images":{},"pi":{"package":"p","version":"v"},"javascript_version":"v","built_in_tools":[{"name":"read","description":"x"}]}`,
		`{"schema_version":2,"images":{"amd64":{"image":"x","image_digest":"` + testDigest + `"},"arm64":{"image":"y","image_digest":"` + testDigest + `"}},"pi":{"package":"p","version":"v"},"javascript_version":"v","built_in_tools":[{"name":"read","description":"x"}],"unknown":true}`,
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
		{name: "known good", inspector: fakeInspector{image: LocalImage{Digests: []string{"sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", testDigest}, OS: "linux", Architecture: "amd64"}}},
		{name: "missing image", inspector: fakeInspector{err: errors.New("No such image")}, wantError: true},
		{name: "wrong tag has no digest", inspector: fakeInspector{}, wantError: true},
		{name: "tampered image", inspector: fakeInspector{image: LocalImage{Digests: []string{"sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"}, OS: "linux", Architecture: "amd64"}}, wantError: true},
		{name: "wrong platform", inspector: fakeInspector{image: LocalImage{Digests: []string{testDigest}, OS: "linux", Architecture: "arm64"}}, wantError: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			verifier := Verifier{Manifest: manifest, Inspector: test.inspector, Version: "1.2.3"}
			identity, err := verifier.VerifyPlatform(context.Background(), "linux", "amd64")
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

func TestVerifierSelectsOnlyRequestedArchitecture(t *testing.T) {
	t.Parallel()
	manifest := testManifest()
	arm := manifest.Images["arm64"]
	for _, test := range []struct {
		name             string
		os, architecture string
		image            LocalImage
		wantError        bool
	}{
		{"arm64", "linux", "arm64", LocalImage{Digests: []string{arm.ImageDigest}, OS: "linux", Architecture: "arm64"}, false},
		{"missing arm64 entry", "linux", "arm64", LocalImage{Digests: []string{testDigest}, OS: "linux", Architecture: "arm64"}, true},
		{"unsupported architecture", "linux", "riscv64", LocalImage{}, true},
		{"wrong operating system", "darwin", "arm64", LocalImage{}, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			candidate := manifest
			if test.name == "missing arm64 entry" {
				delete(candidate.Images, "arm64")
			}
			_, err := (Verifier{Manifest: candidate, Inspector: fakeInspector{image: test.image}}).VerifyPlatform(context.Background(), test.os, test.architecture)
			if (err != nil) != test.wantError {
				t.Fatalf("VerifyPlatform() error = %v, want error %t", err, test.wantError)
			}
		})
	}
}

type fakeInspector struct {
	image LocalImage
	err   error
}

func goodLocalImage() LocalImage {
	return LocalImage{Digests: []string{testDigest}, OS: "linux", Architecture: "amd64"}
}

func (f fakeInspector) LocalImage(_ context.Context, _ string) (LocalImage, error) {
	return f.image, f.err
}

func testManifest() Manifest {
	return Manifest{
		SchemaVersion: ManifestSchemaVersion,
		Images: map[string]Image{
			"amd64": {Image: "agentrun-runtime:test-amd64", ImageDigest: testDigest},
			"arm64": {Image: "agentrun-runtime:test-arm64", ImageDigest: "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"},
		},
		Pi:                Pi{Package: "pi", Version: "1.0.0"},
		JavaScriptVersion: "v22.0.0",
		BuiltInTools:      []BuiltInTool{{Name: "read", Description: "read"}},
	}
}
