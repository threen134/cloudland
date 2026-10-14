package services

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"sync"
	"syscall"
	"time"
)

// Notification channels are configured by organization admins and the requests are sent from
// inside the region's control plane, which can reach clapi, alarm-rules-manager, Prometheus and
// the management network. The gateway rejects private IP literals when a channel is saved, but a
// host name resolving to an internal address (or re-resolving to one later) passes that check, so
// the address is checked again here, on the resolved IP.

// nonPublicPrefixes are ranges a notification must never be sent to, besides what netip already
// classifies as private, loopback, link-local, multicast or unspecified.
// Keep in sync with nonPublicPrefixes in cpgateway/src/apis/notification_channels.go, which checks
// IP literals when a channel is saved.
var nonPublicPrefixes = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),      // "this network"
	netip.MustParsePrefix("100.64.0.0/10"),  // carrier-grade NAT
	netip.MustParsePrefix("192.0.0.0/24"),   // IETF protocol assignments
	netip.MustParsePrefix("198.18.0.0/15"),  // benchmarking
	netip.MustParsePrefix("240.0.0.0/4"),    // reserved, includes broadcast
	netip.MustParsePrefix("64:ff9b::/96"),   // well-known NAT64, maps to any IPv4
	netip.MustParsePrefix("64:ff9b:1::/48"), // local-use NAT64 (RFC 8215)
	netip.MustParsePrefix("2002::/16"),      // 6to4, embeds an IPv4 address
}

// isPublicAddr reports whether ip is a public unicast address a notification may be sent to.
func isPublicAddr(ip netip.Addr) bool {
	ip = ip.Unmap()
	if !ip.IsValid() || !ip.IsGlobalUnicast() || ip.IsPrivate() || ip.IsLoopback() ||
		ip.IsLinkLocalUnicast() || ip.IsUnspecified() {
		return false
	}
	for _, p := range nonPublicPrefixes {
		if p.Contains(ip) {
			return false
		}
	}
	return true
}

var errNonPublicAddr = errors.New("notification target resolves to a non-public address")

// publicOnlyControl refuses connections to non-public addresses. It runs after name resolution,
// for every address tried, so a name that resolves (or re-resolves) to an internal IP is refused.
func publicOnlyControl(_, address string, _ syscall.RawConn) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return err
	}
	ip, err := netip.ParseAddr(host)
	if err != nil {
		return fmt.Errorf("%w: %s", errNonPublicAddr, host)
	}
	if !isPublicAddr(ip) {
		return fmt.Errorf("%w: %s", errNonPublicAddr, ip)
	}
	return nil
}

// checkHostPublic resolves host and requires every address to be public. It is used when the
// request goes through a proxy: the proxy dials the target, so the connect-time check does not see
// it. The proxy resolves the name again, so a rebinding DNS server could still win that race; the
// check stops plain names and literals that point inside.
func checkHostPublic(ctx context.Context, resolver *net.Resolver, host string) error {
	if ip, err := netip.ParseAddr(host); err == nil {
		if !isPublicAddr(ip) {
			return fmt.Errorf("%w: %s", errNonPublicAddr, ip)
		}
		return nil
	}
	ips, err := resolver.LookupNetIP(ctx, "ip", host)
	if err != nil {
		return fmt.Errorf("resolve %s: %w", host, err)
	}
	for _, ip := range ips {
		if !isPublicAddr(ip) {
			return fmt.Errorf("%w: %s resolves to %s", errNonPublicAddr, host, ip)
		}
	}
	return nil
}

// newNotifierClient returns the HTTP client for webhook and Feishu notifications and other
// user-configured callbacks: public targets only, no redirects (a public endpoint could otherwise
// redirect to an internal one).
//
// Deployments that reach the internet through HTTP(S)_PROXY keep working: the proxy itself may be
// on an internal address, so a connection to the proxy is allowed, and the target is checked by
// resolving it before the request is handed to the proxy.
func newNotifierClient() *http.Client {
	return newNotifierClientWith(http.ProxyFromEnvironment, net.DefaultResolver)
}

func newNotifierClientWith(proxy func(*http.Request) (*url.URL, error), resolver *net.Resolver) *http.Client {
	guarded := &net.Dialer{Timeout: 5 * time.Second, Control: publicOnlyControl}
	plain := &net.Dialer{Timeout: 5 * time.Second}
	var proxies sync.Map // host:port of the proxies in use, which may be internal

	return &http.Client{
		Timeout: 10 * time.Second,
		Transport: &http.Transport{
			Proxy: func(req *http.Request) (*url.URL, error) {
				u, err := proxy(req)
				if err != nil || u == nil {
					return u, err
				}
				if err := checkHostPublic(req.Context(), resolver, req.URL.Hostname()); err != nil {
					return nil, err
				}
				proxies.Store(proxyAddr(u), true)
				return u, nil
			},
			DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
				if _, isProxy := proxies.Load(addr); isProxy {
					return plain.DialContext(ctx, network, addr)
				}
				return guarded.DialContext(ctx, network, addr)
			},
			TLSHandshakeTimeout: 5 * time.Second,
			MaxIdleConns:        20,
			IdleConnTimeout:     90 * time.Second,
		},
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return errors.New("notification endpoints must not redirect")
		},
	}
}

// proxyAddr is the host:port the transport dials for proxy u.
func proxyAddr(u *url.URL) string {
	if port := u.Port(); port != "" {
		return net.JoinHostPort(u.Hostname(), port)
	}
	switch u.Scheme {
	case "https":
		return net.JoinHostPort(u.Hostname(), "443")
	case "socks5", "socks5h":
		return net.JoinHostPort(u.Hostname(), "1080")
	}
	return net.JoinHostPort(u.Hostname(), "80")
}
