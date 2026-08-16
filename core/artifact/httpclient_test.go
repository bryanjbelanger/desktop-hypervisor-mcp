package artifact

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"testing"
)

func notFoundErr(host string) error {
	// Shaped like the pure-Go resolver's failure against VMware's NAT DNS
	// proxy, wrapped the way net.Dialer surfaces it.
	return fmt.Errorf("dial tcp: %w", &net.DNSError{Err: "no such host", Name: host, IsNotFound: true})
}

func TestIsDNSNotFound(t *testing.T) {
	if !isDNSNotFound(notFoundErr("api.github.com")) {
		t.Error("wrapped IsNotFound DNSError should match")
	}
	if isDNSNotFound(&net.DNSError{Err: "server misbehaving", IsTemporary: true}) {
		t.Error("non-not-found DNSError should not match")
	}
	if isDNSNotFound(errors.New("connection refused")) {
		t.Error("plain error should not match")
	}
}

func TestDialWithFallbackRetriesOverTCPDNS(t *testing.T) {
	dialed := []string{}
	client, server := net.Pipe()
	defer client.Close()
	defer server.Close()
	dial := func(_ context.Context, _, addr string) (net.Conn, error) {
		dialed = append(dialed, addr)
		if addr == "api.github.com:443" {
			return nil, notFoundErr("api.github.com")
		}
		return client, nil
	}
	lookup := func(_ context.Context, host string) ([]net.IPAddr, error) {
		if host != "api.github.com" {
			t.Fatalf("looked up %q", host)
		}
		return []net.IPAddr{{IP: net.ParseIP("140.82.112.6")}}, nil
	}
	conn, err := dialWithFallback(context.Background(), "tcp", "api.github.com:443", dial, lookup)
	if err != nil {
		t.Fatalf("fallback dial failed: %v", err)
	}
	if conn == nil {
		t.Fatal("no conn returned")
	}
	want := []string{"api.github.com:443", "140.82.112.6:443"}
	if len(dialed) != 2 || dialed[0] != want[0] || dialed[1] != want[1] {
		t.Errorf("dialed %v, want %v", dialed, want)
	}
}

func TestDialWithFallbackNonDNSErrorPassesThrough(t *testing.T) {
	refused := errors.New("connection refused")
	lookedUp := false
	_, err := dialWithFallback(context.Background(), "tcp", "10.0.0.1:443",
		func(context.Context, string, string) (net.Conn, error) { return nil, refused },
		func(context.Context, string) ([]net.IPAddr, error) { lookedUp = true; return nil, nil })
	if !errors.Is(err, refused) {
		t.Errorf("got %v, want the original dial error", err)
	}
	if lookedUp {
		t.Error("non-DNS failure must not trigger a TCP DNS lookup")
	}
}

func TestDialWithFallbackDoubleFailureExplains(t *testing.T) {
	_, err := dialWithFallback(context.Background(), "tcp", "api.github.com:443",
		func(_ context.Context, _, addr string) (net.Conn, error) { return nil, notFoundErr("api.github.com") },
		func(context.Context, string) ([]net.IPAddr, error) { return nil, errors.New("i/o timeout") })
	if err == nil {
		t.Fatal("expected an error")
	}
	if !isDNSNotFound(err) {
		t.Errorf("original not-found error should stay unwrappable: %v", err)
	}
	if !strings.Contains(err.Error(), "public nameserver") || !strings.Contains(err.Error(), "VMware NAT") {
		t.Errorf("double failure should suggest the resolv.conf workaround, got: %v", err)
	}
}

func TestHTTPClientSharesTransport(t *testing.T) {
	a, b := httpClient(1), httpClient(2)
	if a.Transport != b.Transport {
		t.Error("clients should share one transport for connection pooling")
	}
	if sharedTransport.DialContext == nil {
		t.Error("shared transport lost its resilient dialer")
	}
}
