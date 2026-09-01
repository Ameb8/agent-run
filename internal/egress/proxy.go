// Package egress implements the per-run tool HTTP/HTTPS egress boundary.
//
// It deliberately is not an http.RoundTripper supplied to runtime code. The
// runtime talks to this server using the ordinary HTTP proxy protocol; this
// package owns DNS resolution and the only outbound dialer.
package egress

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/Ameb8/agent-run/internal/contract"
)

// Resolver is intentionally narrower than net.Resolver. Implementations must
// be consulted for every outbound connection; Proxy never caches a result.
type Resolver interface {
	LookupNetIP(context.Context, string, string) ([]net.IP, error)
}

// Proxy enforces one immutable run-local policy. It has no package variables,
// shared authorization cache, or fallback dialer, so concurrently created
// proxies cannot influence one another.
type Proxy struct {
	mode        contract.NetworkMode
	hosts       map[string]struct{}
	resolver    Resolver
	dialer      net.Dialer
	dialConnect func(context.Context, string, string) (net.Conn, error)
}

// New constructs a proxy for exactly one effective network declaration. A
// missing resolver is rejected rather than silently using a host-controlled
// fallback. NetworkNone is valid and rejects every request.
func New(network contract.Network, resolver Resolver) (*Proxy, error) {
	if resolver == nil {
		return nil, errors.New("egress resolver is required")
	}
	if network.Mode != contract.NetworkNone && network.Mode != contract.NetworkAllowlist {
		return nil, errors.New("egress network mode is invalid")
	}
	if network.Mode == contract.NetworkNone && len(network.Hosts) != 0 {
		return nil, errors.New("egress hosts require allowlist mode")
	}
	p := &Proxy{mode: network.Mode, hosts: make(map[string]struct{}, len(network.Hosts)), resolver: resolver}
	for _, host := range network.Hosts {
		canonical, err := canonicalHost(host)
		if err != nil {
			return nil, fmt.Errorf("egress host %q: %w", host, err)
		}
		if _, duplicate := p.hosts[canonical]; duplicate {
			return nil, fmt.Errorf("egress host %q is duplicated", host)
		}
		p.hosts[canonical] = struct{}{}
	}
	return p, nil
}

// ServeHTTP implements only the HTTP proxy protocol. Regular origin-form
// requests are rejected, ensuring this server cannot accidentally become a
// general local web server.
func (p *Proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if p == nil {
		http.Error(w, "egress proxy unavailable", http.StatusBadGateway)
		return
	}
	if r.Method == http.MethodConnect {
		p.connect(w, r)
		return
	}
	p.forward(w, r)
}

func (p *Proxy) forward(w http.ResponseWriter, r *http.Request) {
	if r.URL == nil || !r.URL.IsAbs() {
		deny(w, "an absolute HTTP or HTTPS URL is required")
		return
	}
	target, err := p.authorizeURL(r.URL)
	if err != nil {
		deny(w, err.Error())
		return
	}

	out := r.Clone(r.Context())
	out.URL = target
	out.RequestURI = ""
	out.Host = target.Host
	out.Header = r.Header.Clone()
	out.Header.Del("Proxy-Authorization")
	removeHopHeaders(out.Header)

	transport := &http.Transport{
		Proxy:             nil,
		DialContext:       p.dialContext,
		DisableKeepAlives: true, // force a fresh DNS check for every connection
		ForceAttemptHTTP2: false,
	}
	client := &http.Client{
		Transport: transport,
		CheckRedirect: func(next *http.Request, _ []*http.Request) error {
			return p.authorizeRedirect(target, next.URL)
		},
	}
	response, err := client.Do(out)
	if err != nil {
		deny(w, "connection denied")
		return
	}
	defer response.Body.Close()
	removeHopHeaders(response.Header)
	copyHeader(w.Header(), response.Header)
	w.WriteHeader(response.StatusCode)
	_, _ = io.Copy(w, response.Body)
}

func (p *Proxy) authorizeRedirect(origin *url.URL, next *url.URL) error {
	checked, err := p.authorizeURL(next)
	if err != nil {
		return err
	}
	if !sameHost(origin, checked) {
		return errors.New("cross-host redirects are denied")
	}
	return nil
}

func (p *Proxy) connect(w http.ResponseWriter, r *http.Request) {
	if r.URL != nil && r.URL.User != nil {
		deny(w, "user information is denied")
		return
	}
	host, port, err := connectTarget(r.Host)
	if err == nil {
		err = p.authorize(host, "https", port)
	}
	if err != nil {
		deny(w, err.Error())
		return
	}
	connection, err := p.dial(r.Context(), "tcp", net.JoinHostPort(host, port))
	if err != nil {
		deny(w, "connection denied")
		return
	}
	defer connection.Close()

	hijacker, ok := w.(http.Hijacker)
	if !ok {
		deny(w, "proxy connection cannot be upgraded")
		return
	}
	client, buffered, err := hijacker.Hijack()
	if err != nil {
		return
	}
	defer client.Close()
	if _, err := buffered.WriteString("HTTP/1.1 200 Connection Established\r\n\r\n"); err != nil {
		return
	}
	if err := buffered.Flush(); err != nil {
		return
	}
	proxyTunnel(client, connection)
}

func (p *Proxy) authorizeURL(raw *url.URL) (*url.URL, error) {
	if raw == nil || raw.User != nil {
		return nil, errors.New("user information is denied")
	}
	scheme := strings.ToLower(raw.Scheme)
	if scheme != "http" && scheme != "https" {
		return nil, errors.New("only HTTP and HTTPS are supported")
	}
	host, err := canonicalHost(raw.Hostname())
	if err != nil {
		return nil, err
	}
	port := raw.Port()
	if err := p.authorize(host, scheme, port); err != nil {
		return nil, err
	}
	copy := *raw
	copy.Scheme = scheme
	copy.Host = host
	return &copy, nil
}

func (p *Proxy) authorize(host, scheme, port string) error {
	if p.mode != contract.NetworkAllowlist {
		return errors.New("tool network access is disabled")
	}
	if _, allowed := p.hosts[host]; !allowed {
		return errors.New("hostname is not allowed")
	}
	defaultPort := "80"
	if scheme == "https" {
		defaultPort = "443"
	}
	if port != "" && port != defaultPort {
		return errors.New("custom ports are denied")
	}
	return nil
}

func (p *Proxy) dialContext(ctx context.Context, network, address string) (net.Conn, error) {
	return p.dial(ctx, network, address)
}

func (p *Proxy) dial(ctx context.Context, network, address string) (net.Conn, error) {
	if network != "tcp" && network != "tcp4" && network != "tcp6" {
		return nil, errors.New("non-TCP connections are denied")
	}
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, errors.New("invalid connection target")
	}
	host, err = canonicalHost(host)
	if err != nil {
		return nil, err
	}
	addresses, err := p.resolver.LookupNetIP(ctx, "ip", host)
	if err != nil || len(addresses) == 0 {
		return nil, errors.New("destination resolution failed")
	}
	// A mixed DNS answer is unsafe: accepting its public member would let a
	// rebinding-capable name select a private address on a later connection.
	for _, address := range addresses {
		if !publicAddress(address) {
			return nil, errors.New("destination resolved to a non-public address")
		}
	}
	var failures []error
	for _, address := range addresses {
		connector := p.dialConnect
		if connector == nil {
			connector = p.dialer.DialContext
		}
		connection, dialErr := connector(ctx, "tcp", net.JoinHostPort(address.String(), port))
		if dialErr == nil {
			return connection, nil
		}
		failures = append(failures, dialErr)
	}
	return nil, fmt.Errorf("destination connection failed: %w", errors.Join(failures...))
}

func canonicalHost(value string) (string, error) {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" || net.ParseIP(value) != nil || len(value) > 253 || strings.ContainsAny(value, "*/:@[]") {
		return "", errors.New("hostname must be a DNS name")
	}
	for _, label := range strings.Split(value, ".") {
		if label == "" || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return "", errors.New("hostname must be a DNS name")
		}
		for _, character := range label {
			if !(character >= 'a' && character <= 'z' || character >= '0' && character <= '9' || character == '-') {
				return "", errors.New("hostname must be a DNS name")
			}
		}
	}
	return value, nil
}

func connectTarget(authority string) (string, string, error) {
	if strings.ContainsAny(authority, "@/") {
		return "", "", errors.New("CONNECT target is invalid")
	}
	u, err := url.Parse("https://" + authority)
	if err != nil || u.User != nil || u.Path != "" {
		return "", "", errors.New("CONNECT target is invalid")
	}
	host, err := canonicalHost(u.Hostname())
	if err != nil {
		return "", "", err
	}
	return host, u.Port(), nil
}

func sameHost(left, right *url.URL) bool {
	return left != nil && right != nil && strings.EqualFold(left.Hostname(), right.Hostname())
}

func publicAddress(value net.IP) bool {
	address, ok := netip.AddrFromSlice(value)
	if !ok {
		return false
	}
	address = address.Unmap()
	if !address.IsGlobalUnicast() || address.IsPrivate() || address.IsLoopback() || address.IsLinkLocalUnicast() || address.IsLinkLocalMulticast() || address.IsMulticast() || address.IsUnspecified() {
		return false
	}
	if address.Is4() {
		// CGNAT and benchmarking blocks are not public destinations either.
		return !netip.MustParsePrefix("100.64.0.0/10").Contains(address) && !netip.MustParsePrefix("198.18.0.0/15").Contains(address)
	}
	return true
}

func deny(w http.ResponseWriter, message string) {
	http.Error(w, "egress denied: "+message, http.StatusForbidden)
}

func removeHopHeaders(header http.Header) {
	for _, name := range []string{"Connection", "Proxy-Connection", "Keep-Alive", "Proxy-Authenticate", "Proxy-Authorization", "TE", "Trailer", "Transfer-Encoding", "Upgrade"} {
		header.Del(name)
	}
}

func copyHeader(destination, source http.Header) {
	for name, values := range source {
		for _, value := range values {
			destination.Add(name, value)
		}
	}
}

func proxyTunnel(client, destination net.Conn) {
	var group sync.WaitGroup
	group.Add(2)
	go func() { defer group.Done(); _, _ = io.Copy(destination, client); closeWrite(destination) }()
	go func() { defer group.Done(); _, _ = io.Copy(client, destination); closeWrite(client) }()
	group.Wait()
}

func closeWrite(connection net.Conn) {
	if closer, ok := connection.(interface{ CloseWrite() error }); ok {
		_ = closer.CloseWrite()
	}
}

// Listen starts a loopback-only listener suitable for one run's private proxy
// bridge. Callers own the returned listener and must close it during cleanup.
func (p *Proxy) Listen() (net.Listener, error) {
	if p == nil {
		return nil, errors.New("egress proxy is unavailable")
	}
	return net.Listen("tcp", "127.0.0.1:0")
}

// Serve accepts proxy clients until listener closure. It intentionally has no
// default server timeouts because tool request limits are owned by the run
// supervisor; it does cap header parsing to net/http's safe default.
func (p *Proxy) Serve(listener net.Listener) error {
	if listener == nil {
		return errors.New("egress listener is required")
	}
	server := &http.Server{Handler: p, ReadHeaderTimeout: 10 * time.Second}
	return server.Serve(listener)
}
