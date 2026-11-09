// Package localonly enforces this fork's private-network runtime policy.
package localonly

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"time"
)

const Enabled = true

var ErrDisabled = errors.New("external service disabled in this local-only build")

func PrivateIP(address string) bool {
	ip, err := netip.ParseAddr(address)
	if err != nil {
		return false
	}
	ip = ip.Unmap()
	return ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast()
}

// Single-label names and .local are local resolver/mDNS names. Public domain
// names are not queried as a side effect of rejecting an external URL.
func LocalHost(host string) bool {
	host = strings.TrimSuffix(strings.ToLower(host), ".")
	if PrivateIP(host) {
		return true
	}
	if strings.ContainsAny(host, ":/@\\; \t\r\n") {
		return false
	}
	if host == "localhost" {
		return true
	}
	if strings.Contains(host, ".") && !strings.HasSuffix(host, ".local") {
		return false
	}
	for _, label := range strings.Split(host, ".") {
		if label == "" || len(label) > 63 || strings.HasPrefix(label, "-") || strings.HasSuffix(label, "-") {
			return false
		}
		for _, c := range label {
			if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-') {
				return false
			}
		}
	}
	return host != ""
}

func CheckURL(u *url.URL) error {
	if u == nil || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || !LocalHost(u.Hostname()) {
		return ErrDisabled
	}
	return nil
}
func Proxy(req *http.Request) (*url.URL, error) { return nil, CheckURL(req.URL) }

// Resolve once, validate, and dial the numeric address. This prevents DNS
// rebinding between validation and connection and does not use environment proxies.
func DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, err
	}
	if !LocalHost(host) {
		return nil, ErrDisabled
	}
	addresses, err := net.DefaultResolver.LookupNetIP(ctx, "ip", host)
	if err != nil {
		return nil, err
	}
	var last error
	for _, ip := range addresses {
		if !PrivateIP(ip.String()) {
			last = ErrDisabled
			continue
		}
		conn, err := (&net.Dialer{Timeout: 15 * time.Second, KeepAlive: 30 * time.Second}).DialContext(ctx, network, net.JoinHostPort(ip.String(), port))
		if err == nil {
			return conn, nil
		}
		last = err
	}
	if last == nil {
		last = fmt.Errorf("no private address for local endpoint")
	}
	return nil, last
}

func ConfigureTransport(transport *http.Transport) *http.Transport {
	transport.Proxy = Proxy
	transport.DialContext = DialContext
	transport.DialTLS = nil
	transport.DialTLSContext = nil
	return transport
}

func Install() {
	// Resolver destinations are numeric IPs from resolv.conf. Reject public DNS
	// without recursively invoking this resolver from its own dial callback.
	net.DefaultResolver = &net.Resolver{PreferGo: true, Dial: func(ctx context.Context, network, address string) (net.Conn, error) {
		host, _, err := net.SplitHostPort(address)
		if err != nil || !PrivateIP(host) {
			return nil, ErrDisabled
		}
		return (&net.Dialer{Timeout: 5 * time.Second}).DialContext(ctx, network, address)
	}}
	http.DefaultTransport = ConfigureTransport(http.DefaultTransport.(*http.Transport).Clone())
	http.DefaultClient.Transport = http.DefaultTransport
}

func CandidateAllowed(candidate string) bool {
	fields := strings.Fields(strings.TrimPrefix(candidate, "candidate:"))
	return len(fields) >= 8 && fields[6] == "typ" && fields[7] == "host" && LocalHost(fields[4])
}
func FilterSDP(sdp string) string {
	lines := strings.Split(sdp, "\n")
	out := lines[:0]
	for _, line := range lines {
		if strings.HasPrefix(line, "a=candidate:") && !CandidateAllowed(strings.TrimPrefix(strings.TrimSpace(line), "a=")) {
			continue
		}
		out = append(out, line)
	}
	return strings.Join(out, "\n")
}
