// Package safehttp provides HTTP clients for user-supplied download URLs.
// It rejects redirects to unsafe destinations and pins each connection to a
// resolved public IP address to prevent SSRF and DNS-rebinding attacks.
package safehttp

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

var blockedPrefixes = mustParsePrefixes(
	"100.64.0.0/10",   // shared address space / CGNAT
	"192.0.0.0/24",    // IETF protocol assignments
	"192.0.2.0/24",    // documentation
	"192.88.99.0/24",  // deprecated 6to4 relay anycast
	"198.18.0.0/15",   // benchmarking
	"198.51.100.0/24", // documentation
	"203.0.113.0/24",  // documentation
	"240.0.0.0/4",     // reserved / broadcast
	"64:ff9b::/96",    // well-known NAT64 (can encode private IPv4)
	"64:ff9b:1::/48",  // local-use NAT64
	"100::/64",        // discard-only
	"2001::/23",       // IETF protocol assignments / transition mechanisms
	"2001:db8::/32",   // documentation
	"2002::/16",       // 6to4 (embeds IPv4)
	"3fff::/20",       // documentation
	"fec0::/10",       // deprecated site-local
)

// NewClient returns an HTTP client suitable for downloading files from a
// manifest. It intentionally does not use HTTP_PROXY/HTTPS_PROXY: a proxy
// would bypass the destination-IP checks performed by the dialer.
func NewClient(timeout time.Duration) *http.Client {
	transport := &http.Transport{
		Proxy:                 nil,
		DialContext:           safeDialContext,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: 30 * time.Second,
		IdleConnTimeout:       30 * time.Second,
		MaxIdleConns:          10,
		MaxIdleConnsPerHost:   2,
		MaxConnsPerHost:       4,
		DisableKeepAlives:     true,
		DisableCompression:    true,
		ForceAttemptHTTP2:     false,
		ExpectContinueTimeout: 1 * time.Second,
	}
	return &http.Client{
		Timeout:   timeout,
		Transport: validatingTransport{base: transport},
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 5 {
				return fmt.Errorf("stopped after 5 redirects")
			}
			return ValidateURL(req.URL)
		},
	}
}

// ValidateURL checks the URL syntax and rejects unsupported schemes, embedded
// credentials, malformed ports, and hostnames with IPv6 zone identifiers.
// Whether a hostname resolves to a permitted address is checked at dial time.
func ValidateURL(u *url.URL) error {
	if u == nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.User != nil {
		return fmt.Errorf("URL must be HTTP(S), include a hostname, and not contain embedded credentials")
	}
	host := u.Hostname()
	if strings.Contains(host, "%") {
		return fmt.Errorf("URL host must not contain an IPv6 zone identifier")
	}
	if port := u.Port(); port != "" {
		n, err := strconv.Atoi(port)
		if err != nil || n < 1 || n > 65535 {
			return fmt.Errorf("URL has an invalid port")
		}
	}
	return nil
}

type validatingTransport struct {
	base http.RoundTripper
}

func (t validatingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if err := ValidateURL(req.URL); err != nil {
		return nil, err
	}
	return t.base.RoundTrip(req)
}

func safeDialContext(ctx context.Context, network, address string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, fmt.Errorf("invalid download address: %w", err)
	}
	if strings.Contains(host, "%") {
		return nil, fmt.Errorf("IPv6 zone identifiers are not allowed")
	}

	var addresses []net.IPAddr
	if ip := net.ParseIP(host); ip != nil {
		addresses = []net.IPAddr{{IP: ip}}
	} else {
		addresses, err = net.DefaultResolver.LookupIPAddr(ctx, host)
		if err != nil {
			return nil, fmt.Errorf("resolve download host %q: %w", host, err)
		}
	}
	if len(addresses) == 0 {
		return nil, fmt.Errorf("download host %q has no addresses", host)
	}
	for _, address := range addresses {
		if !isPublicIP(address.IP) || address.Zone != "" {
			return nil, fmt.Errorf("download host %q resolves to a non-public address", host)
		}
	}

	dialer := &net.Dialer{Timeout: 15 * time.Second, KeepAlive: 30 * time.Second}
	var lastErr error
	for _, address := range addresses {
		// Dial the validated IP directly rather than resolving the hostname a
		// second time. This pins the connection and prevents DNS rebinding.
		conn, err := dialer.DialContext(ctx, network, net.JoinHostPort(address.IP.String(), port))
		if err == nil {
			return conn, nil
		}
		lastErr = err
	}
	return nil, fmt.Errorf("connect to download host %q: %w", host, lastErr)
}

func isPublicIP(ip net.IP) bool {
	if ip == nil {
		return false
	}
	if v4 := ip.To4(); v4 != nil {
		if !v4.IsGlobalUnicast() || v4.IsPrivate() || v4.IsLoopback() || v4.IsLinkLocalUnicast() || v4.IsUnspecified() {
			return false
		}
		for _, prefix := range blockedPrefixes {
			if prefix.Contains(v4) {
				return false
			}
		}
		return true
	}
	if !ip.IsGlobalUnicast() || ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() ||
		ip.IsLinkLocalMulticast() || ip.IsMulticast() || ip.IsUnspecified() {
		return false
	}
	for _, prefix := range blockedPrefixes {
		if prefix.Contains(ip) {
			return false
		}
	}
	return true
}

func mustParsePrefixes(values ...string) []*net.IPNet {
	prefixes := make([]*net.IPNet, 0, len(values))
	for _, value := range values {
		_, prefix, err := net.ParseCIDR(value)
		if err != nil {
			panic(err)
		}
		prefixes = append(prefixes, prefix)
	}
	return prefixes
}
