// Package provider implements AgentRun's host-side model-provider boundary.
//
// A Transport is intentionally not an http.RoundTripper handed to the agent
// runtime: doing so would let runtime code choose arbitrary destinations.  It
// owns the configured origin and the credential, and exposes only requests
// constrained to that origin.
package provider

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/Ameb8/agent-run/internal/auth"
	"github.com/Ameb8/agent-run/internal/contract"
)

const subscriptionEndpoint = "https://chatgpt.com/backend-api/codex"

// Transport sends requests for exactly one selected provider. Credentials are
// retained only in this host-side value; neither Endpoint nor Do exposes them.
type Transport struct {
	endpoint *url.URL
	origin   origin
	client   *http.Client
	apiKey   string
	model    contract.ModelIdentity
}

// Prepare selects the host-side transport from a validated agent declaration.
// It preserves only the requested provider/model identity; the endpoint and
// credential never become runtime-visible provider configuration.
func Prepare(model contract.Model, lookup func(string) (string, bool), subscription auth.Handle) (*Transport, error) {
	switch model.Provider {
	case contract.ProviderOpenAICompatible:
		transport, err := NewOpenAICompatibleFromEnvironment(model.Endpoint, model.APIKeyEnv, lookup)
		if err != nil {
			return nil, err
		}
		transport.model = contract.ModelIdentity{Provider: model.Provider, Requested: model.Model}
		return transport, nil
	case contract.ProviderOpenAISubscription:
		transport, err := NewOpenAISubscription(subscription)
		if err != nil {
			return nil, err
		}
		transport.model = contract.ModelIdentity{Provider: model.Provider, Requested: model.Model}
		return transport, nil
	default:
		return nil, configuration("model provider is unsupported")
	}
}

// NewOpenAICompatible creates a transport for a caller-managed API key. The
// endpoint is parsed again here even though definitions validate it, because a
// provider transport is a security boundary in its own right.
func NewOpenAICompatible(endpoint, apiKey string) (*Transport, error) {
	if strings.TrimSpace(apiKey) == "" {
		return nil, configuration("model API key is required")
	}
	return newTransport(endpoint, apiKey)
}

// NewOpenAICompatibleFromEnvironment obtains the named credential immediately
// before a run. lookup is injectable so callers never need to copy secrets into
// generated runtime configuration merely to construct a transport.
func NewOpenAICompatibleFromEnvironment(endpoint, variable string, lookup func(string) (string, bool)) (*Transport, error) {
	if lookup == nil {
		return nil, configuration("model API key environment is unavailable")
	}
	apiKey, ok := lookup(variable)
	if !ok || strings.TrimSpace(apiKey) == "" {
		return nil, configuration("model API key environment variable is missing")
	}
	return NewOpenAICompatible(endpoint, apiKey)
}

// NewOpenAISubscription creates the fixed-origin Codex transport from the
// opaque managed credential handle. The document never leaves this package and
// is not written to a Pi config file or the sandbox environment.
func NewOpenAISubscription(handle auth.Handle) (*Transport, error) {
	return newOpenAISubscription(subscriptionEndpoint, handle)
}

func newOpenAISubscription(endpoint string, handle auth.Handle) (*Transport, error) {
	var token string
	err := handle.WithPiAuth(func(document []byte) error {
		credential, err := subscriptionAccessToken(document)
		if err != nil {
			return err
		}
		token = credential
		return nil
	})
	if err != nil || token == "" {
		return nil, authentication("OpenAI subscription authentication is required")
	}
	return newTransport(endpoint, token)
}

func newTransport(endpoint, apiKey string) (*Transport, error) {
	u, err := parseEndpoint(endpoint)
	if err != nil {
		return nil, err
	}
	// Do not inherit HTTP(S)_PROXY from the AgentRun host. Provider traffic has
	// one explicitly selected endpoint and must not be silently routed through
	// caller-controlled proxy configuration.
	httpTransport := http.DefaultTransport.(*http.Transport).Clone()
	httpTransport.Proxy = nil
	t := &Transport{endpoint: u, origin: makeOrigin(u), apiKey: apiKey}
	t.client = &http.Client{
		Transport: httpTransport,
		CheckRedirect: func(request *http.Request, _ []*http.Request) error {
			if request.URL == nil || !t.origin.matches(request.URL) {
				return providerFailure("model provider redirect left configured origin")
			}
			return nil
		},
	}
	return t, nil
}

// Endpoint returns the non-secret configured endpoint for provider metadata
// and diagnostics. It must not be used to construct an unconstrained client.
func (t *Transport) Endpoint() string {
	if t == nil || t.endpoint == nil {
		return ""
	}
	return t.endpoint.String()
}

// Model identifies the provider and model requested by the declaration. It is
// suitable for a result object and deliberately excludes endpoint and secrets.
func (t *Transport) Model() contract.ModelIdentity {
	if t == nil {
		return contract.ModelIdentity{}
	}
	return t.model
}

// Do sends a request through the isolated provider boundary. target may be a
// relative endpoint path or an absolute URL at the selected origin. Request
// headers supplied by the runtime cannot override the provider credential.
// Non-success provider responses are mapped without reading their bodies.
func (t *Transport) Do(ctx context.Context, method, target string, body io.Reader, headers http.Header) (*http.Response, error) {
	if t == nil || t.endpoint == nil || t.client == nil || t.apiKey == "" {
		return nil, configuration("model provider transport is unavailable")
	}
	u, err := t.resolve(target)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, method, u.String(), body)
	if err != nil {
		return nil, providerFailure("model provider request is invalid")
	}
	for name, values := range headers {
		if strings.EqualFold(name, "Authorization") {
			continue
		}
		for _, value := range values {
			req.Header.Add(name, value)
		}
	}
	req.Header.Set("Authorization", "Bearer "+t.apiKey)
	response, err := t.client.Do(req)
	if err != nil {
		var category *contract.CommandError
		if errors.As(err, &category) {
			return nil, category
		}
		return nil, providerFailure("model provider request failed")
	}
	if response.StatusCode == http.StatusUnauthorized || response.StatusCode == http.StatusForbidden {
		response.Body.Close()
		return nil, authentication("model provider rejected credentials")
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		response.Body.Close()
		return nil, providerFailure("model provider returned an unsuccessful response")
	}
	return response, nil
}

func (t *Transport) resolve(target string) (*url.URL, error) {
	u, err := url.Parse(target)
	if err != nil {
		return nil, providerFailure("model provider request target is invalid")
	}
	if u.User != nil {
		return nil, providerFailure("model provider request target contains user information")
	}
	if !u.IsAbs() {
		// Provider declarations name an API base URL (typically ending in /v1),
		// not merely an origin. Keep that base path when the runtime supplies an
		// operation name such as "responses".
		base := *t.endpoint
		base.Path = strings.TrimRight(base.Path, "/") + "/"
		base.RawPath = ""
		u = base.ResolveReference(u)
	}
	if !t.origin.matches(u) {
		return nil, providerFailure("model provider request left configured origin")
	}
	return u, nil
}

type origin struct{ scheme, host, port string }

func makeOrigin(u *url.URL) origin {
	port := u.Port()
	if port == "" {
		if strings.EqualFold(u.Scheme, "https") {
			port = "443"
		} else {
			port = "80"
		}
	}
	return origin{scheme: strings.ToLower(u.Scheme), host: strings.ToLower(u.Hostname()), port: port}
}

func (o origin) matches(u *url.URL) bool {
	return u != nil && u.User == nil && makeOrigin(u) == o
}

func parseEndpoint(value string) (*url.URL, error) {
	u, err := url.Parse(value)
	if err != nil || !u.IsAbs() || u.Host == "" || u.User != nil || (u.Scheme != "http" && u.Scheme != "https") {
		return nil, configuration("model endpoint must be an absolute HTTP or HTTPS URL without user information")
	}
	return u, nil
}

func subscriptionAccessToken(document []byte) (string, error) {
	var providers map[string]json.RawMessage
	if err := json.Unmarshal(document, &providers); err != nil {
		return "", errors.New("invalid subscription credential")
	}
	credential, ok := providers["openai-codex"]
	if !ok {
		return "", errors.New("missing subscription credential")
	}
	var values map[string]any
	if err := json.Unmarshal(credential, &values); err != nil {
		return "", errors.New("invalid subscription credential")
	}
	for _, name := range []string{"access", "access_token"} {
		if value, ok := values[name].(string); ok && strings.TrimSpace(value) != "" {
			return value, nil
		}
	}
	return "", errors.New("missing subscription access token")
}

func configuration(message string) error {
	return &contract.CommandError{Category: contract.ErrorConfiguration, Message: message}
}

func authentication(message string) error {
	return &contract.CommandError{Category: contract.ErrorAuthentication, Message: message}
}

func providerFailure(message string) error {
	return &contract.CommandError{Category: contract.ErrorProvider, Message: message}
}
