// Package auth owns AgentRun-managed provider authentication. Credentials stay
// outside agent packages and run sandboxes; callers can observe only presence
// or pass an opaque handle to the selected provider transport.
package auth

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"
)

const (
	openAISubscriptionProvider = "openai-codex"
	directoryMode              = 0o700
	fileMode                   = 0o600
)

// Store contains the single subscription credential for one operating-system
// user. Root is an AgentRun-specific user configuration directory.
type Store struct{ root string }

// NewStore uses the platform user configuration directory rather than a Pi
// directory so AgentRun never adopts global Pi credentials implicitly.
func NewStore() (Store, error) {
	root, err := os.UserConfigDir()
	if err != nil {
		return Store{}, fmt.Errorf("user configuration directory: %w", err)
	}
	return NewStoreAt(filepath.Join(root, "agentrun"))
}

// NewStoreAt is primarily useful to embedding callers and isolated tests.
func NewStoreAt(root string) (Store, error) {
	if root == "" || !filepath.IsAbs(root) {
		return Store{}, errors.New("credential storage directory must be absolute")
	}
	return Store{root: filepath.Clean(root)}, nil
}

func (s Store) credentialPath() string {
	return filepath.Join(s.root, "auth", "openai-subscription.json")
}

// Present reports only whether a usable managed credential exists. It never
// returns authentication material.
func (s Store) Present() (bool, error) {
	_, err := s.open()
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

// Replace atomically makes credential the account used by subsequent runs.
// The supplied Pi auth document is reduced to the one supported provider.
func (s Store) Replace(piAuthDocument []byte) error {
	credential, err := subscriptionCredential(piAuthDocument)
	if err != nil {
		return err
	}
	if err := secureDirectory(s.root); err != nil {
		return err
	}
	directory := filepath.Join(s.root, "auth")
	if err := secureDirectory(directory); err != nil {
		return err
	}
	encoded, err := json.Marshal(map[string]json.RawMessage{openAISubscriptionProvider: credential})
	if err != nil {
		return fmt.Errorf("encode subscription credential: %w", err)
	}
	temporary, err := os.CreateTemp(directory, ".openai-subscription-*")
	if err != nil {
		return fmt.Errorf("create credential storage: %w", err)
	}
	temporaryName := temporary.Name()
	defer func() { _ = os.Remove(temporaryName) }()
	if err := temporary.Chmod(fileMode); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("secure credential storage: %w", err)
	}
	if _, err := temporary.Write(encoded); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("write credential storage: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("sync credential storage: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close credential storage: %w", err)
	}
	if err := os.Rename(temporaryName, s.credentialPath()); err != nil {
		return fmt.Errorf("replace credential storage: %w", err)
	}
	return nil
}

// Logout removes the single active account. Existing runs retain any handle
// they already opened; subsequent runs observe the logout.
func (s Store) Logout() error {
	if err := s.secureExistingParent(); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return err
	}
	err := os.Remove(s.credentialPath())
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("remove credential storage: %w", err)
	}
	return nil
}

// Handle is intentionally opaque. Only the provider transport should use its
// WithPiAuth callback, which avoids exposing a raw credential to definitions,
// diagnostics, or generated sandbox configuration.
type Handle struct{ piAuthDocument []byte }

// Open returns a narrow credential handle or fs.ErrNotExist when no account is
// logged in. Its error text never incorporates file contents.
func (s Store) Open() (Handle, error) {
	document, err := s.open()
	if err != nil {
		return Handle{}, err
	}
	return Handle{piAuthDocument: document}, nil
}

// WithPiAuth provides the provider-native document only for the duration of
// callback. It is deliberately not marshaled into agent configuration.
func (h Handle) WithPiAuth(callback func([]byte) error) error {
	if len(h.piAuthDocument) == 0 {
		return errors.New("subscription credential handle is unavailable")
	}
	return callback(bytes.Clone(h.piAuthDocument))
}

func (s Store) open() ([]byte, error) {
	if err := s.secureExistingParent(); err != nil {
		return nil, err
	}
	path := s.credentialPath()
	info, err := os.Lstat(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, fs.ErrNotExist
		}
		return nil, fmt.Errorf("credential storage: %w", err)
	}
	if !ownedByCurrentUser(info) || !info.Mode().IsRegular() || info.Mode().Perm() != fileMode {
		return nil, errors.New("credential storage must be a private regular file")
	}
	document, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("credential storage: %w", err)
	}
	if _, err := subscriptionCredential(document); err != nil {
		return nil, errors.New("credential storage is invalid")
	}
	return document, nil
}

func (s Store) secureExistingParent() error {
	for _, directory := range []string{s.root, filepath.Join(s.root, "auth")} {
		info, err := os.Lstat(directory)
		if errors.Is(err, fs.ErrNotExist) {
			return fs.ErrNotExist
		}
		if err != nil {
			return fmt.Errorf("credential storage directory: %w", err)
		}
		if !ownedByCurrentUser(info) || !info.IsDir() || info.Mode().Perm() != directoryMode {
			return errors.New("credential storage directory must be private")
		}
	}
	return nil
}

func secureDirectory(path string) error {
	info, err := os.Lstat(path)
	if errors.Is(err, fs.ErrNotExist) {
		if err := os.MkdirAll(path, directoryMode); err != nil {
			return fmt.Errorf("create credential storage directory: %w", err)
		}
		info, err = os.Lstat(path)
	}
	if err != nil {
		return fmt.Errorf("credential storage directory: %w", err)
	}
	if !ownedByCurrentUser(info) || !info.IsDir() || info.Mode().Perm() != directoryMode {
		return errors.New("credential storage directory must be private")
	}
	return nil
}

func ownedByCurrentUser(info fs.FileInfo) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && stat.Uid == uint32(os.Geteuid())
}

func subscriptionCredential(document []byte) (json.RawMessage, error) {
	var providers map[string]json.RawMessage
	decoder := json.NewDecoder(bytes.NewReader(document))
	if err := decoder.Decode(&providers); err != nil {
		return nil, errors.New("interactive login did not produce a valid credential")
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return nil, errors.New("interactive login did not produce a valid credential")
	}
	credential, ok := providers[openAISubscriptionProvider]
	if !ok || len(credential) == 0 || !json.Valid(credential) || bytes.Equal(bytes.TrimSpace(credential), []byte("null")) {
		return nil, errors.New("interactive login did not produce an OpenAI subscription credential")
	}
	return credential, nil
}
