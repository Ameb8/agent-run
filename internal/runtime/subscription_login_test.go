package runtime

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestSubscriptionLoginUsesVerifiedPinnedRuntimeAndTemporaryPiHome(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("subscription login is Linux-only")
	}
	manifest, err := LoadManifest()
	if err != nil {
		t.Fatal(err)
	}
	var gotName string
	var gotArgs []string
	var stderr bytes.Buffer
	login := NewSubscriptionLogin(Verifier{Manifest: manifest, Inspector: loginImageInspector{digest: manifest.ImageDigest}}, strings.NewReader(""), &bytes.Buffer{}, &stderr)
	login.run = func(_ context.Context, name string, args []string, _ io.Reader, _ io.Writer, _ io.Writer) error {
		gotName, gotArgs = name, append([]string(nil), args...)
		var home string
		for index, arg := range args {
			if arg == "--mount" && index+1 < len(args) {
				for _, field := range strings.Split(args[index+1], ",") {
					if strings.HasPrefix(field, "src=") {
						home = strings.TrimPrefix(field, "src=")
					}
				}
			}
		}
		if err := os.MkdirAll(filepath.Join(home, ".pi", "agent"), 0o700); err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(home, ".pi", "agent", "auth.json"), []byte(`{"openai-codex":{"access":"canary"}}`), 0o600)
	}
	document, err := login.Login(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if gotName != "docker" || !containsArg(gotArgs, manifest.Image) || !containsArg(gotArgs, "openai-codex") || !containsArg(gotArgs, "--read-only") {
		t.Fatalf("interactive login command = %q %q", gotName, gotArgs)
	}
	if !strings.Contains(string(document), "canary") || strings.Contains(stderr.String(), "canary") {
		t.Fatalf("credential handling document=%q stderr=%q", document, stderr.String())
	}
}

type loginImageInspector struct{ digest string }

func (i loginImageInspector) LocalImageDigests(context.Context, string) ([]string, error) {
	return []string{i.digest}, nil
}

func containsArg(args []string, target string) bool {
	for _, arg := range args {
		if arg == target {
			return true
		}
	}
	return false
}
