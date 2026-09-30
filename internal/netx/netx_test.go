package netx

import (
	"crypto/tls"
	"net/http"
	"net/http/httptest"
	"testing"
)

func request(remote string, header map[string]string) *http.Request {
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.RemoteAddr = remote
	for name, value := range header {
		r.Header.Set(name, value)
	}
	return r
}

func TestParseTrustedProxies(t *testing.T) {
	if _, err := ParseTrustedProxies([]string{"10.0.0.0/8", " 192.168.1.1 ", "::1", "fd00::/8", ""}); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"10.0.0.0/33", "not-an-ip", "10.0.0.1:80"} {
		if _, err := ParseTrustedProxies([]string{bad}); err == nil {
			t.Fatalf("%q was accepted", bad)
		}
	}
}

func TestClientIP(t *testing.T) {
	trusted, _ := ParseTrustedProxies([]string{"10.0.0.0/8", "::1"})
	for _, tc := range []struct {
		name    string
		proxies *TrustedProxies
		remote  string
		xff     string
		want    string
	}{
		{"nil trusts nothing", nil, "203.0.113.1:1234", "1.1.1.1", "203.0.113.1"},
		{"untrusted peer", trusted, "203.0.113.1:1234", "1.1.1.1", "203.0.113.1"},
		{"trusted peer", trusted, "10.1.2.3:1234", "198.51.100.1", "198.51.100.1"},
		{"rightmost untrusted wins", trusted, "10.1.2.3:1234", "6.6.6.6, 198.51.100.1, 10.9.9.9", "198.51.100.1"},
		{"all hops trusted", trusted, "10.1.2.3:1234", "10.9.9.9", "10.9.9.9"},
		{"malformed hop stops the walk", trusted, "10.1.2.3:1234", "198.51.100.1, garbage", "10.1.2.3"},
		{"ipv6 peer", trusted, "[::1]:1234", "2001:db8::1", "2001:db8::1"},
		{"mapped ipv4 peer is unmapped", nil, "[::ffff:203.0.113.5]:1", "", "203.0.113.5"},
		{"port stripped without header", nil, "203.0.113.1:1234", "", "203.0.113.1"},
	} {
		header := map[string]string{}
		if tc.xff != "" {
			header["X-Forwarded-For"] = tc.xff
		}
		if got := tc.proxies.ClientIP(request(tc.remote, header)); got != tc.want {
			t.Fatalf("%s: ClientIP = %q; want %q", tc.name, got, tc.want)
		}
	}
}

func TestIsHTTPS(t *testing.T) {
	trusted, _ := ParseTrustedProxies([]string{"10.0.0.0/8"})
	if trusted.IsHTTPS(request("203.0.113.1:1", map[string]string{"X-Forwarded-Proto": "https"})) {
		t.Fatal("an untrusted client forced the HTTPS decision")
	}
	if !trusted.IsHTTPS(request("10.0.0.1:1", map[string]string{"X-Forwarded-Proto": "https, http"})) {
		t.Fatal("a trusted proxy's https was ignored")
	}
	if trusted.IsHTTPS(request("10.0.0.1:1", nil)) {
		t.Fatal("plain HTTP from a trusted proxy was reported as HTTPS")
	}
	direct := request("203.0.113.1:1", nil)
	direct.TLS = &tls.ConnectionState{}
	if !(*TrustedProxies)(nil).IsHTTPS(direct) {
		t.Fatal("a TLS connection terminated here was not HTTPS")
	}
}

func TestIsLocalRequest(t *testing.T) {
	trusted, _ := ParseTrustedProxies([]string{"127.0.0.1"})
	var none *TrustedProxies
	if !none.IsLocalRequest(request("127.0.0.1:1", nil)) {
		t.Fatal("a loopback client was not local")
	}
	if none.IsLocalRequest(request("127.0.0.1:1", map[string]string{"X-Forwarded-For": "127.0.0.1"})) {
		t.Fatal("a loopback peer relaying forwarded traffic was local")
	}
	if none.IsLocalRequest(request("203.0.113.1:1", nil)) {
		t.Fatal("a remote client was local")
	}
	if trusted.IsLocalRequest(request("127.0.0.1:1", map[string]string{"X-Forwarded-For": "203.0.113.1"})) {
		t.Fatal("a remote client behind a trusted local proxy was local")
	}
	if !trusted.IsLocalRequest(request("127.0.0.1:1", map[string]string{"X-Forwarded-For": "127.0.0.1"})) {
		t.Fatal("a local client behind a trusted local proxy was not local")
	}
}
