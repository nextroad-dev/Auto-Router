package config

import (
	"strings"
	"time"
)

// maxRequestBytesLimit caps one buffered model request. The limit is shared by
// the HTTP configuration validator and the forwarding handler.
const maxRequestBytesLimit = 1 << 30

// maxRequestBytesDefault is the shipped 16 MiB request limit.
const maxRequestBytesDefault = 16 << 20

// maxBufferedResponseBytesDefault bounds a converted (buffered) upstream
// response. 32 MiB is far above any real completion body.
const maxBufferedResponseBytesDefault = 32 << 20

// upstreamBodyTimeoutDefault bounds how long a non-stream upstream body may stall
// between reads after its headers arrived. It is generous because a non-stream
// reasoning request may legitimately hold the body until generation finishes.
const upstreamBodyTimeoutDefault = 10 * time.Minute

// forbiddenAuthHeaders are headers managed by net/http or negotiated per
// connection. They cannot safely be configured as an authentication header.
var forbiddenAuthHeaders = map[string]struct{}{
	"connection":          {},
	"keep-alive":          {},
	"proxy-connection":    {},
	"te":                  {},
	"trailer":             {},
	"transfer-encoding":   {},
	"upgrade":             {},
	"proxy-authenticate":  {},
	"proxy-authorization": {},
	"host":                {},
	"content-length":      {},
	"content-type":        {},
	"content-encoding":    {},
	"expect":              {},
	"accept-encoding":     {},
}

// validHeaderName accepts the RFC 7230 token grammar without importing net/http
// internals. It is used by the inbound auth and Jev client configuration.
func validHeaderName(name string) bool {
	if name == "" {
		return false
	}
	for i := 0; i < len(name); i++ {
		c := name[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		case strings.IndexByte("!#$%&'*+-.^_`|~", c) >= 0:
		default:
			return false
		}
	}
	return true
}
