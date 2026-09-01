package auth

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const canary = "subscription-secret-canary"

func TestStoreReplaceLogoutAndOpaqueHandle(t *testing.T) {
	root := filepath.Join(t.TempDir(), "config", "agentrun")
	store, err := NewStoreAt(root)
	if err != nil {
		t.Fatal(err)
	}
	present, err := store.Present()
	if err != nil || present {
		t.Fatalf("Present before login = %v, %v", present, err)
	}
	if err := store.Replace([]byte(`{"openai-codex":{"type":"oauth","access":"` + canary + `"},"anthropic":{"type":"oauth","access":"ignored"}}`)); err != nil {
		t.Fatal(err)
	}
	present, err = store.Present()
	if err != nil || !present {
		t.Fatalf("Present after login = %v, %v", present, err)
	}
	credentialPath := filepath.Join(root, "auth", "openai-subscription.json")
	contents, err := os.ReadFile(credentialPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(contents), "ignored") || !strings.Contains(string(contents), canary) {
		t.Fatalf("stored credential did not retain only OpenAI credential")
	}
	for _, path := range []string{root, filepath.Join(root, "auth"), credentialPath} {
		info, statErr := os.Stat(path)
		if statErr != nil {
			t.Fatal(statErr)
		}
		want := fs.FileMode(directoryMode)
		if path == credentialPath {
			want = fileMode
		}
		if info.Mode().Perm() != want {
			t.Errorf("%s permissions = %o, want %o", path, info.Mode().Perm(), want)
		}
	}
	handle, err := store.Open()
	if err != nil {
		t.Fatal(err)
	}
	if err := handle.WithPiAuth(func(document []byte) error {
		if !strings.Contains(string(document), canary) {
			t.Fatal("handle did not provide credential to transport")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.Replace([]byte(`{"openai-codex":{"type":"oauth","access":"replacement"}}`)); err != nil {
		t.Fatal(err)
	}
	if err := store.Logout(); err != nil {
		t.Fatal(err)
	}
	present, err = store.Present()
	if err != nil || present {
		t.Fatalf("Present after logout = %v, %v", present, err)
	}
	if _, err := store.Open(); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("Open after logout error = %v, want not exist", err)
	}
}

func TestStoreRejectsInsecureOrInvalidStorageWithoutLeakingCredential(t *testing.T) {
	root := filepath.Join(t.TempDir(), "agentrun")
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	store, err := NewStoreAt(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Replace([]byte(`{"openai-codex":{"access":"` + canary + `"}}`)); err != nil {
		t.Fatal(err)
	}
	credentialPath := filepath.Join(root, "auth", "openai-subscription.json")
	if err := os.Chmod(credentialPath, 0o644); err != nil {
		t.Fatal(err)
	}
	_, err = store.Open()
	if err == nil || strings.Contains(err.Error(), canary) {
		t.Fatalf("insecure Open error = %v", err)
	}
	if err := os.Chmod(credentialPath, fileMode); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(credentialPath, []byte("not json "+canary), fileMode); err != nil {
		t.Fatal(err)
	}
	_, err = store.Open()
	if err == nil || strings.Contains(err.Error(), canary) {
		t.Fatalf("invalid Open error = %v", err)
	}
}

func TestStoreRejectsMissingSelectedProvider(t *testing.T) {
	root := filepath.Join(t.TempDir(), "agentrun")
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	store, err := NewStoreAt(root)
	if err != nil {
		t.Fatal(err)
	}
	err = store.Replace([]byte(`{"anthropic":{"access":"` + canary + `"}}`))
	if err == nil || strings.Contains(err.Error(), canary) {
		t.Fatalf("Replace error = %v", err)
	}
}

func TestLogoutWithoutAnAccountIsIdempotent(t *testing.T) {
	store, err := NewStoreAt(filepath.Join(t.TempDir(), "agentrun"))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Logout(); err != nil {
		t.Fatalf("Logout without credential = %v", err)
	}
}
