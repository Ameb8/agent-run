package provider

import (
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Ameb8/agent-run/internal/auth"
	"github.com/Ameb8/agent-run/internal/contract"
)

func TestOpenAICompatibleSendsCredentialOnlyThroughConstrainedTransport(t *testing.T) {
	t.Parallel()
	const canary = "compatible-secret-canary"
	transport, err := NewOpenAICompatibleFromEnvironment("https://provider.test/v1", "MODEL_KEY", func(name string) (string, bool) {
		if name != "MODEL_KEY" {
			t.Fatalf("lookup name = %q", name)
		}
		return canary, true
	})
	if err != nil {
		t.Fatal(err)
	}
	transport.client.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path != "/v1/responses" {
			t.Errorf("path = %q", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer "+canary {
			t.Errorf("authorization = %q", got)
		}
		return response(http.StatusOK, "ok"), nil
	})
	response, err := transport.Do(context.Background(), http.MethodPost, "responses", strings.NewReader("request"), http.Header{"Authorization": {"Bearer untrusted"}})
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if got := transport.Endpoint(); strings.Contains(got, canary) {
		t.Fatalf("endpoint exposed credential: %q", got)
	}
}

func TestPreparePreservesRequestedProviderAndModel(t *testing.T) {
	t.Parallel()
	transport, err := Prepare(contract.Model{
		Provider:  contract.ProviderOpenAICompatible,
		Endpoint:  "https://provider.test/v1",
		Model:     "controlled-model",
		APIKeyEnv: "MODEL_KEY",
	}, func(string) (string, bool) { return "key", true }, auth.Handle{})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := transport.Model(), (contract.ModelIdentity{Provider: contract.ProviderOpenAICompatible, Requested: "controlled-model"}); got != want {
		t.Fatalf("model = %#v, want %#v", got, want)
	}
}

func TestTransportRejectsCrossOriginRequestsAndRedirects(t *testing.T) {
	t.Parallel()
	transport, err := NewOpenAICompatible("https://provider.test", "secret")
	if err != nil {
		t.Fatal(err)
	}
	transport.client.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Host == "other.test" {
			t.Error("cross-origin provider request reached transport")
		}
		result := response(http.StatusFound, "")
		result.Header.Set("Location", "https://other.test/stolen")
		return result, nil
	})
	for _, target := range []string{"https://other.test/direct", "redirect"} {
		_, err := transport.Do(context.Background(), http.MethodGet, target, nil, nil)
		assertCategory(t, err, contract.ErrorProvider)
		if strings.Contains(err.Error(), "secret") {
			t.Fatalf("error exposed credential: %q", err)
		}
	}
}

func TestTransportAllowsSameOriginRedirectAndRejectsUserInfo(t *testing.T) {
	t.Parallel()
	transport, err := NewOpenAICompatible("https://provider.test", "secret")
	if err != nil {
		t.Fatal(err)
	}
	transport.client.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path == "/first" {
			result := response(http.StatusFound, "")
			result.Header.Set("Location", "/second")
			return result, nil
		}
		return response(http.StatusOK, ""), nil
	})
	response, err := transport.Do(context.Background(), http.MethodGet, "first", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	_, err = transport.Do(context.Background(), http.MethodGet, "http://user:password@example.test/v1", nil, nil)
	assertCategory(t, err, contract.ErrorProvider)
}

func TestProviderAuthenticationAndFailuresAreCategorizedWithoutBodies(t *testing.T) {
	t.Parallel()
	const bodyCanary = "raw-provider-body-secret"
	transport, err := NewOpenAICompatible("https://provider.test", "credential-canary")
	if err != nil {
		t.Fatal(err)
	}
	transport.client.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		switch r.URL.Path {
		case "/unauthorized":
			return response(http.StatusUnauthorized, bodyCanary), nil
		case "/failure":
			return response(http.StatusBadGateway, bodyCanary), nil
		}
		return response(http.StatusOK, bodyCanary), nil
	})
	for target, category := range map[string]contract.ErrorCategory{"unauthorized": contract.ErrorAuthentication, "failure": contract.ErrorProvider} {
		_, err := transport.Do(context.Background(), http.MethodGet, target, nil, nil)
		assertCategory(t, err, category)
		if strings.Contains(err.Error(), bodyCanary) || strings.Contains(err.Error(), "credential-canary") {
			t.Fatalf("error leaked secret: %q", err)
		}
	}
	_, err = NewOpenAICompatibleFromEnvironment("https://provider.test", "MISSING", func(string) (string, bool) { return "", false })
	assertCategory(t, err, contract.ErrorConfiguration)
}

func TestSubscriptionTransportUsesManagedHandleWithoutExposingCredential(t *testing.T) {
	root := t.TempDir()
	store, err := auth.NewStoreAt(filepath.Join(root, "agentrun"))
	if err != nil {
		t.Fatal(err)
	}
	const canary = "subscription-secret-canary"
	if err := store.Replace([]byte(`{"openai-codex":{"access":"` + canary + `"}}`)); err != nil {
		t.Fatal(err)
	}
	handle, err := store.Open()
	if err != nil {
		t.Fatal(err)
	}
	transport, err := newOpenAISubscription("https://subscription.test", handle)
	if err != nil {
		t.Fatal(err)
	}
	transport.client.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if got := r.Header.Get("Authorization"); got != "Bearer "+canary {
			t.Errorf("authorization = %q", got)
		}
		return response(http.StatusOK, ""), nil
	})
	if strings.Contains(transport.Endpoint(), canary) {
		t.Fatalf("endpoint exposed credential: %q", transport.Endpoint())
	}
	response, err := transport.Do(context.Background(), http.MethodPost, "responses", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	// The credential is not copied into an environment variable while building
	// the transport, the only process-visible channel extensions could inspect.
	for _, entry := range os.Environ() {
		if strings.Contains(entry, canary) {
			t.Fatalf("environment exposed credential: %q", entry)
		}
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) { return f(request) }

func response(status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}
}

func TestTransportRejectsInvalidEndpoints(t *testing.T) {
	t.Parallel()
	for _, endpoint := range []string{"", "relative", "ftp://example.test", "https://user:password@example.test"} {
		_, err := NewOpenAICompatible(endpoint, "key")
		assertCategory(t, err, contract.ErrorConfiguration)
	}
}

func assertCategory(t *testing.T, err error, want contract.ErrorCategory) {
	t.Helper()
	if err == nil {
		t.Fatalf("error = nil, want %s", want)
	}
	var command *contract.CommandError
	if !errors.As(err, &command) || command.Category != want {
		t.Fatalf("error = %v, want category %s", err, want)
	}
}
