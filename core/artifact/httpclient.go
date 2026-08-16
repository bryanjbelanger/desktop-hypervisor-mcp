// Shared HTTP client with DNS resilience for broken NAT DNS proxies.
//
// The primary defense against VMware's NAT DNS proxy — which corrupts EDNS0
// responses so Go's default resolver gets "no such host" — is the
// //go:debug netedns0=0 directive in main.go; see the comment there. This
// transport is the second layer for other resolver breakage: every download
// and metadata path shares one transport whose dialer retries name
// resolution over TCP DNS when the default lookup reports not-found. (The
// VMware Fusion proxy refuses TCP on port 53, so there the retry fails fast
// and the error steers the user to a public nameserver instead.)
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
		return nil, fmt.Errorf("%w (retry over TCP DNS also failed; if this host is a VMware NAT guest its DNS proxy is likely at fault — point /etc/resolv.conf at a public nameserver, e.g. 'nameserver 1.1.1.1', and make sure GODEBUG does not re-enable EDNS0: this binary defaults to netedns0=0)", err)
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
