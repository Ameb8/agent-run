package egress

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/Ameb8/agent-run/internal/contract"
)

type resolver map[string][]net.IP

func (r resolver) LookupNetIP(_ context.Context, _ string, host string) ([]net.IP, error) {
	return r[host], nil
}

func TestNoneRejectsAllToolRequests(t *testing.T) {
	t.Parallel()
	proxy := mustProxy(t, contract.Network{Mode: contract.NetworkNone}, resolver{})
	request := httptest.NewRequest(http.MethodGet, "http://allowed.example/path", nil)
	request.RequestURI = ""
	response := httptest.NewRecorder()
	proxy.ServeHTTP(response, request)
	if response.Code != http.StatusForbidden || !strings.Contains(response.Body.String(), "disabled") {
		t.Fatalf("none response = %d %q", response.Code, response.Body.String())
	}
}

func TestAllowlistRequiresExactHostAndSchemeDefaultPort(t *testing.T) {
	t.Parallel()
	proxy := mustProxy(t, contract.Network{Mode: contract.NetworkAllowlist, Hosts: []string{"api.example"}}, resolver{})
	for _, raw := range []string{
		"http://api.example:81/", "https://api.example:444/", "http://api.example.evil/",
		"http://127.0.0.1/", "http://user@api.example/", "ftp://api.example/",
	} {
		u, err := url.Parse(raw)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := proxy.authorizeURL(u); err == nil {
			t.Errorf("authorizeURL(%q) succeeded", raw)
		}
	}
	for _, raw := range []string{"http://api.example/", "https://api.example/", "http://API.EXAMPLE:80/", "https://api.example:443/"} {
		u, _ := url.Parse(raw)
		if _, err := proxy.authorizeURL(u); err != nil {
			t.Errorf("authorizeURL(%q) = %v", raw, err)
		}
	}
}

func TestResolutionFailsClosedForPrivateAndRebindingAnswers(t *testing.T) {
	t.Parallel()
	for _, addresses := range [][]net.IP{
		{net.ParseIP("127.0.0.1")},
		{net.ParseIP("10.0.0.8")},
		{net.ParseIP("169.254.1.2")},
		{net.ParseIP("172.17.0.1")},
		{net.ParseIP("100.64.1.2")},
		{net.ParseIP("8.8.8.8"), net.ParseIP("127.0.0.1")},
	} {
		proxy := mustProxy(t, contract.Network{Mode: contract.NetworkAllowlist, Hosts: []string{"api.example"}}, resolver{"api.example": addresses})
		called := false
		proxy.dialConnect = func(context.Context, string, string) (net.Conn, error) { called = true; return nil, nil }
		if _, err := proxy.dial(context.Background(), "tcp", "api.example:443"); err == nil {
			t.Errorf("dial with %v succeeded", addresses)
		}
		if called {
			t.Errorf("dial attempted unsafe address %v", addresses)
		}
	}
}

func TestHTTPRedirectCannotLeaveOriginalHost(t *testing.T) {
	t.Parallel()
	proxy := mustProxy(t, contract.Network{Mode: contract.NetworkAllowlist, Hosts: []string{"api.example", "other.example"}}, resolver{
		"api.example":   {net.ParseIP("8.8.8.8")},
		"other.example": {net.ParseIP("8.8.4.4")},
	})
	origin, _ := url.Parse("http://api.example/start")
	redirect, _ := url.Parse("http://other.example/next")
	if err := proxy.authorizeRedirect(origin, redirect); err == nil {
		t.Fatal("cross-host redirect was allowed")
	}
}

func TestConcurrentPoliciesCannotAuthorizeEachOther(t *testing.T) {
	t.Parallel()
	first := mustProxy(t, contract.Network{Mode: contract.NetworkAllowlist, Hosts: []string{"first.example"}}, resolver{})
	second := mustProxy(t, contract.Network{Mode: contract.NetworkAllowlist, Hosts: []string{"second.example"}}, resolver{})
	for _, test := range []struct {
		proxy *Proxy
		host  string
		allow bool
	}{{first, "first.example", true}, {first, "second.example", false}, {second, "second.example", true}, {second, "first.example", false}} {
		_, err := test.proxy.authorizeURL(&url.URL{Scheme: "https", Host: test.host})
		if (err == nil) != test.allow {
			t.Errorf("policy for %s at %s: err=%v", test.host, test.proxy.hosts, err)
		}
	}
}

func mustProxy(t *testing.T, network contract.Network, lookup Resolver) *Proxy {
	t.Helper()
	proxy, err := New(network, lookup)
	if err != nil {
		t.Fatal(err)
	}
	return proxy
}
