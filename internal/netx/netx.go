// Package netx resolves the network identity of an inbound request: the client
// address and whether the browser-facing connection used HTTPS.
//
// Forwarding headers are client-controlled unless they were written by a proxy
// this process trusts, so they are consulted only when the directly connected
// peer is inside an explicitly configured trusted-proxy range. With no range
// configured (the default), RemoteAddr is the only source of truth and every
// forwarding header is ignored.
package netx

import (
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"strings"
)

// TrustedProxies is an immutable set of address ranges whose forwarding headers
// are believed. The zero value and nil trust nothing.
type TrustedProxies struct {
	prefixes []netip.Prefix
}

// ParseTrustedProxies accepts CIDR ranges ("10.0.0.0/8") and single addresses
// ("127.0.0.1", "::1"). An empty list yields a set that trusts nothing.
func ParseTrustedProxies(values []string) (*TrustedProxies, error) {
	proxies := &TrustedProxies{}
	for _, raw := range values {
		value := strings.TrimSpace(raw)
		if value == "" {
			continue
		}
		if strings.Contains(value, "/") {
			prefix, err := netip.ParsePrefix(value)
			if err != nil {
				return nil, fmt.Errorf("trusted proxy %q is not a valid CIDR range", value)
			}
			proxies.prefixes = append(proxies.prefixes, prefix.Masked())
			continue
		}
		addr, err := netip.ParseAddr(value)
		if err != nil {
			return nil, fmt.Errorf("trusted proxy %q is not a valid IP address or CIDR range", value)
		}
		addr = addr.Unmap()
		proxies.prefixes = append(proxies.prefixes, netip.PrefixFrom(addr, addr.BitLen()))
	}
	return proxies, nil
}

// ValidateTrustedProxies reports whether every value parses.
func ValidateTrustedProxies(values []string) error {
	_, err := ParseTrustedProxies(values)
	return err
}

// Empty reports whether no proxy is trusted.
func (t *TrustedProxies) Empty() bool {
	return t == nil || len(t.prefixes) == 0
}

// Trusted reports whether addr is inside a trusted range.
func (t *TrustedProxies) Trusted(addr netip.Addr) bool {
	if t == nil || !addr.IsValid() {
		return false
	}
	addr = addr.Unmap()
	for _, prefix := range t.prefixes {
		if prefix.Contains(addr) {
			return true
		}
	}
	return false
}

// PeerAddr parses the directly connected peer from RemoteAddr, accepting both the
// host:port form net/http produces and a bare address.
func PeerAddr(r *http.Request) (netip.Addr, bool) {
	if r == nil {
		return netip.Addr{}, false
	}
	return parseHostAddr(r.RemoteAddr)
}

// ClientAddr returns the originating client address. When the peer is a trusted
// proxy, X-Forwarded-For is walked from the right, skipping trusted hops, and the
// first untrusted address is the client; a malformed entry stops the walk at the
// last address that was established. Otherwise the peer itself is the client.
func (t *TrustedProxies) ClientAddr(r *http.Request) (netip.Addr, bool) {
	peer, ok := PeerAddr(r)
	if !ok {
		return netip.Addr{}, false
	}
	if !t.Trusted(peer) {
		return peer, true
	}
	hops := forwardedFor(r.Header)
	client := peer
	for i := len(hops) - 1; i >= 0; i-- {
		addr, ok := parseHostAddr(hops[i])
		if !ok {
			break
		}
		client = addr
		if !t.Trusted(addr) {
			break
		}
	}
	return client, true
}

// ClientIP renders ClientAddr as a bare IP string. An unparsable RemoteAddr is
// returned with any port removed so a stored value never carries one.
func (t *TrustedProxies) ClientIP(r *http.Request) string {
	if addr, ok := t.ClientAddr(r); ok {
		return addr.String()
	}
	if r == nil {
		return ""
	}
	if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		return host
	}
	return r.RemoteAddr
}

// IsHTTPS reports whether the browser-facing connection was HTTPS: either this
// process terminated TLS itself, or a trusted proxy said so in X-Forwarded-Proto
// (the first, client-facing value).
func (t *TrustedProxies) IsHTTPS(r *http.Request) bool {
	if r == nil {
		return false
	}
	if r.TLS != nil {
		return true
	}
	peer, ok := PeerAddr(r)
	if !ok || !t.Trusted(peer) {
		return false
	}
	proto := r.Header.Get("X-Forwarded-Proto")
	if index := strings.IndexByte(proto, ','); index >= 0 {
		proto = proto[:index]
	}
	return strings.EqualFold(strings.TrimSpace(proto), "https")
}

// ProxyIdentityHeaders are the request headers that carry a claimed network
// identity or scheme. They are never forwarded upstream and, when the peer is not
// trusted, never believed.
var ProxyIdentityHeaders = []string{
	"Forwarded",
	"X-Forwarded-For",
	"X-Forwarded-Host",
	"X-Forwarded-Port",
	"X-Forwarded-Proto",
	"X-Real-Ip",
}

// HasForwardingHeaders reports whether a request carries any proxy identity header.
func HasForwardingHeaders(header http.Header) bool {
	for _, name := range ProxyIdentityHeaders {
		if len(header.Values(name)) > 0 {
			return true
		}
	}
	return false
}

// IsLocalRequest reports whether the originating client is on this machine. A
// loopback peer that carries forwarding headers from an untrusted source is a
// local reverse proxy relaying someone else, so it is not local.
func (t *TrustedProxies) IsLocalRequest(r *http.Request) bool {
	peer, ok := PeerAddr(r)
	if !ok {
		return false
	}
	if !t.Trusted(peer) {
		return peer.IsLoopback() && !HasForwardingHeaders(r.Header)
	}
	client, ok := t.ClientAddr(r)
	return ok && client.IsLoopback()
}

func forwardedFor(header http.Header) []string {
	var hops []string
	for _, value := range header.Values("X-Forwarded-For") {
		for _, part := range strings.Split(value, ",") {
			hops = append(hops, strings.TrimSpace(part))
		}
	}
	return hops
}

func parseHostAddr(value string) (netip.Addr, bool) {
	value = strings.TrimSpace(value)
	if value == "" {
		return netip.Addr{}, false
	}
	if addrPort, err := netip.ParseAddrPort(value); err == nil {
		return addrPort.Addr().Unmap(), true
	}
	addr, err := netip.ParseAddr(strings.Trim(value, "[]"))
	if err != nil {
		return netip.Addr{}, false
	}
	return addr.WithZone("").Unmap(), true
}
