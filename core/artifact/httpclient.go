// Shared HTTP client with DNS resilience for VMware NAT guests.
//
// Go's pure-Go resolver (the default in CGO_ENABLED=0 release binaries) fails
// with "no such host" against VMware's NAT DNS proxy, which mishandles some
// UDP queries that glibc's resolver tolerates — the proxy answers the same
// queries correctly over TCP. Every download and metadata path here shares
// one transport whose dialer retries name resolution over TCP DNS when the
// default lookup reports not-found, so the server works unmodified inside a
// VMware NAT guest.
package artifact

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"time"
)

// tcpResolver forces DNS over TCP regardless of what /etc/resolv.conf asks
// for. PreferGo is required: only the pure-Go resolver honors a custom Dial.
var tcpResolver = &net.Resolver{
	PreferGo: true,
	Dial: func(ctx context.Context, network, address string) (net.Conn, error) {
		d := net.Dialer{Timeout: 10 * time.Second}
		return d.DialContext(ctx, "tcp", address)
	},
}

// dialContext dials normally first; a not-found DNS error triggers one retry
// resolving the host over TCP DNS and dialing the returned addresses. When
// both attempts fail the error carries the resolv.conf workaround so a user
// in a broken NAT guest is not left with a bare "no such host".
func dialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	// Timeout/KeepAlive match http.DefaultTransport's dialer.
	d := net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second}
	return dialWithFallback(ctx, network, addr, d.DialContext, tcpResolver.LookupIPAddr)
}

type dialFunc func(ctx context.Context, network, addr string) (net.Conn, error)
type lookupFunc func(ctx context.Context, host string) ([]net.IPAddr, error)

func dialWithFallback(ctx context.Context, network, addr string, dial dialFunc, lookup lookupFunc) (net.Conn, error) {
	conn, err := dial(ctx, network, addr)
	if err == nil || !isDNSNotFound(err) {
		return conn, err
	}
	host, port, splitErr := net.SplitHostPort(addr)
	if splitErr != nil {
		return nil, err
	}
	addrs, lookupErr := lookup(ctx, host)
	if lookupErr != nil || len(addrs) == 0 {
		return nil, fmt.Errorf("%w (retry over TCP DNS also failed; if this host is a VMware NAT guest its DNS proxy may be mishandling UDP — add 'options use-vc' to /etc/resolv.conf or point it at a public nameserver)", err)
	}
	for _, a := range addrs {
		c, dialErr := dial(ctx, network, net.JoinHostPort(a.IP.String(), port))
		if dialErr == nil {
			return c, nil
		}
		err = dialErr
	}
	return nil, err
}

func isDNSNotFound(err error) bool {
	var dnsErr *net.DNSError
	return errors.As(err, &dnsErr) && dnsErr.IsNotFound
}

// sharedTransport is DefaultTransport (proxy support, HTTP/2, sane timeouts)
// with the resilient dialer, shared so connections pool across requests.
var sharedTransport = func() *http.Transport {
	t := http.DefaultTransport.(*http.Transport).Clone()
	t.DialContext = dialContext
	return t
}()

// httpClient returns a client on the shared transport. Timeout is per call
// site: minutes for artifact downloads, seconds for metadata lookups.
func httpClient(timeout time.Duration) *http.Client {
	return &http.Client{Timeout: timeout, Transport: sharedTransport}
}
