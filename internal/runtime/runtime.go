// Package runtime owns AgentRun's private-runtime identity and its local-image
// verification boundary. It deliberately has no run or sandbox behavior.
package runtime

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"io"
	"runtime"
	"sort"
	"strings"

	"github.com/Ameb8/agent-run/internal/contract"
)

const ManifestSchemaVersion = 2

// BuildVersion is replaced by release builds with -ldflags. It is kept outside
// the runtime manifest because the manifest identifies the private image, not
// a particular host-binary build.
var BuildVersion = "dev"

//go:embed manifest.json
var embeddedManifest []byte

// Manifest is the complete, release-owned description of the private runtime.
// BuiltInTools is ordered deliberately: it is downstream contract metadata.
type Manifest struct {
	SchemaVersion     int              `json:"schema_version"`
	Images            map[string]Image `json:"images"`
	Pi                Pi               `json:"pi"`
	JavaScriptVersion string           `json:"javascript_version"`
	BuiltInTools      []BuiltInTool    `json:"built_in_tools"`
}

// Image is the release-owned identity for one native Linux architecture. The
// separate tags deliberately make an accidentally loaded image for another
// architecture unselectable, even before Docker reports its platform.
type Image struct {
	Image       string `json:"image"`
	ImageDigest string `json:"image_digest"`
}

type Pi struct {
	Package string `json:"package"`
	Version string `json:"version"`
}

type BuiltInTool struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

// LoadManifest returns the manifest compiled into this host binary. It never
// reads mutable project configuration or discovers host runtimes.
func LoadManifest() (Manifest, error) {
	return ParseManifest(embeddedManifest)
}

// ParseManifest is intentionally exported for release tooling and tests that
// need to verify a candidate release manifest before embedding it.
func ParseManifest(data []byte) (Manifest, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var manifest Manifest
	if err := decoder.Decode(&manifest); err != nil {
		return Manifest{}, fmt.Errorf("decode runtime manifest: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return Manifest{}, fmt.Errorf("decode runtime manifest: multiple JSON values")
		}
		return Manifest{}, fmt.Errorf("decode runtime manifest: %w", err)
	}
	if err := manifest.Validate(); err != nil {
		return Manifest{}, err
	}
	return manifest, nil
}

func (m Manifest) Validate() error {
	if m.SchemaVersion != ManifestSchemaVersion {
		return fmt.Errorf("runtime manifest schema_version must be %d", ManifestSchemaVersion)
	}
	if len(m.Images) == 0 {
		return fmt.Errorf("runtime manifest images are required")
	}
	for _, architecture := range []string{"amd64", "arm64"} {
		image, ok := m.Images[architecture]
		if !ok || strings.TrimSpace(image.Image) == "" || !validDigest(image.ImageDigest) {
			return fmt.Errorf("runtime manifest image for linux/%s is required and must have a sha256 digest", architecture)
		}
	}
	for architecture, image := range m.Images {
		if architecture != "amd64" && architecture != "arm64" {
			return fmt.Errorf("runtime manifest has unsupported architecture %q", architecture)
		}
		if strings.TrimSpace(image.Image) == "" || !validDigest(image.ImageDigest) {
			return fmt.Errorf("runtime manifest image for linux/%s is invalid", architecture)
		}
	}
	if strings.TrimSpace(m.Pi.Package) == "" || strings.TrimSpace(m.Pi.Version) == "" {
		return fmt.Errorf("runtime manifest pi package and version are required")
	}
	if strings.TrimSpace(m.JavaScriptVersion) == "" {
		return fmt.Errorf("runtime manifest javascript_version is required")
	}
	if len(m.BuiltInTools) == 0 {
		return fmt.Errorf("runtime manifest built_in_tools is required")
	}
	seen := make(map[string]struct{}, len(m.BuiltInTools))
	for _, tool := range m.BuiltInTools {
		if strings.TrimSpace(tool.Name) == "" || strings.TrimSpace(tool.Description) == "" {
			return fmt.Errorf("runtime manifest built-in tool name and description are required")
		}
		if _, duplicate := seen[tool.Name]; duplicate {
			return fmt.Errorf("runtime manifest has duplicate built-in tool %q", tool.Name)
		}
		seen[tool.Name] = struct{}{}
	}
	return nil
}

func validDigest(digest string) bool {
	if !strings.HasPrefix(digest, "sha256:") || len(digest) != len("sha256:")+64 {
		return false
	}
	for _, character := range digest[len("sha256:"):] {
		if !(character >= '0' && character <= '9') && !(character >= 'a' && character <= 'f') {
			return false
		}
	}
	return true
}

// Identity is the stable runtime portion of a v1 result object.
func (m Manifest) Identity(agentRunVersion, architecture string) (contract.RuntimeIdentity, error) {
	image, ok := m.Images[architecture]
	if !ok {
		return contract.RuntimeIdentity{}, configurationError("private runtime does not support linux/%s", architecture)
	}
	return contract.RuntimeIdentity{
		AgentRunVersion:   agentRunVersion,
		PiVersion:         m.Pi.Version,
		JavaScriptVersion: m.JavaScriptVersion,
		ImageDigest:       image.ImageDigest,
	}, nil
}

func (m Manifest) ImageFor(architecture string) (Image, error) {
	image, ok := m.Images[architecture]
	if !ok {
		return Image{}, configurationError("private runtime manifest omits linux/%s", architecture)
	}
	return image, nil
}

// HostArchitecture is kept small and explicit so a source-built binary fails
// closed rather than silently asking Docker to emulate an unsupported image.
func HostArchitecture() (string, error) {
	if runtime.GOOS != "linux" {
		return "", configurationError("AgentRun v1 requires a Linux host")
	}
	if runtime.GOARCH != "amd64" && runtime.GOARCH != "arm64" {
		return "", configurationError("private runtime does not support linux/%s", runtime.GOARCH)
	}
	return runtime.GOARCH, nil
}

// ImageInspector exposes the sole host capability this package needs. Its
// implementation must inspect local image metadata only; it must not pull.
type ImageInspector interface {
	LocalImage(context.Context, string) (LocalImage, error)
}

// LocalImage is metadata Docker reports without running or pulling an image.
type LocalImage struct {
	Digests      []string
	OS           string
	Architecture string
}

// Verifier is the narrow setup seam shared by future run, doctor, and version
// commands. A verified identity is safe to include in a result contract.
type Verifier struct {
	Manifest  Manifest
	Inspector ImageInspector
	Version   string
}

func NewVerifier(inspector ImageInspector, version string) (*Verifier, error) {
	manifest, err := LoadManifest()
	if err != nil {
		return nil, err
	}
	return &Verifier{Manifest: manifest, Inspector: inspector, Version: version}, nil
}

// Verify confirms that the exact local image selected by the manifest has the
// release manifest digest. Missing Docker access, missing images, malformed
// metadata, and mismatches are all configuration failures before a model call.
func (v Verifier) Verify(ctx context.Context) (contract.RuntimeIdentity, error) {
	architecture, err := HostArchitecture()
	if err != nil {
		return contract.RuntimeIdentity{}, err
	}
	return v.VerifyPlatform(ctx, "linux", architecture)
}

// VerifyPlatform is the testable selection boundary. Production callers use
// Verify, which obtains the platform from the host Go runtime.
func (v Verifier) VerifyPlatform(ctx context.Context, osName, architecture string) (contract.RuntimeIdentity, error) {
	if err := v.Manifest.Validate(); err != nil {
		return contract.RuntimeIdentity{}, configurationError("runtime manifest: %v", err)
	}
	if v.Inspector == nil {
		return contract.RuntimeIdentity{}, configurationError("private runtime image inspector is unavailable")
	}
	if osName != "linux" || (architecture != "amd64" && architecture != "arm64") {
		return contract.RuntimeIdentity{}, configurationError("private runtime does not support %s/%s", osName, architecture)
	}
	image, err := v.Manifest.ImageFor(architecture)
	if err != nil {
		return contract.RuntimeIdentity{}, err
	}
	local, err := v.Inspector.LocalImage(ctx, image.Image)
	if err != nil {
		return contract.RuntimeIdentity{}, configurationError("private runtime image %q is unavailable", image.Image)
	}
	if local.OS != "linux" || local.Architecture != architecture {
		return contract.RuntimeIdentity{}, configurationError("private runtime image %q platform is %s/%s, want linux/%s", image.Image, local.OS, local.Architecture, architecture)
	}
	matches := 0
	for _, digest := range local.Digests {
		if digest == image.ImageDigest {
			matches++
		}
	}
	if matches != 1 {
		return contract.RuntimeIdentity{}, configurationError("private runtime image %q does not match release digest", image.Image)
	}
	return v.Manifest.Identity(v.Version, architecture)
}

func (m Manifest) Architectures() []string {
	architectures := make([]string, 0, len(m.Images))
	for architecture := range m.Images {
		architectures = append(architectures, architecture)
	}
	sort.Strings(architectures)
	return architectures
}

func configurationError(format string, args ...any) error {
	return &contract.CommandError{Category: contract.ErrorConfiguration, Message: fmt.Sprintf(format, args...)}
}
