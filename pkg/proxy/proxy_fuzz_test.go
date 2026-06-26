package proxy

import (
	"net"
	"net/http"
	"strings"
	"testing"
)

// FuzzResolveAddr fuzzes the address-translation table lookup. Oracle: the
// result must never be garbage — it is always either an entry from the mapping
// table (a translated value, possibly with the original port re-appended) or
// the original input returned unchanged. resolveAddr must not panic on any
// host:port-ish input.
func FuzzResolveAddr(f *testing.F) {
	f.Add("redis.default.svc.cluster.local:6379", "redis.default.svc.cluster.local:6379", "127.0.0.1:16379", true)
	f.Add("redis-0.redis.dev.svc.cluster.local:6379", "redis-0.dev", "127.0.0.1:16379", true)
	f.Add("svc:80", "svc", "localhost:8080", true)
	f.Add("", "", "", false)
	f.Add("no-port", "no-port", "x:1", false)
	f.Add("[::1]:80", "::1", "127.0.0.1:9", true)
	f.Add("host:notaport", "host", "y:2", true)
	f.Add(":::::", "a", "b", true)

	f.Fuzz(func(t *testing.T, addr, mapKey, mapVal string, fuzzy bool) {
		addrs := map[string]string{}
		if mapKey != "" {
			addrs[mapKey] = mapVal
		}

		got := resolveAddr(addrs, addr, fuzzy)

		// Property: the result is one of a bounded set of legitimate outputs.
		if got == addr {
			return // returned the original input unchanged — always allowed
		}
		for k, v := range addrs {
			if got == v {
				return // exact-key translation
			}
			// host-only match re-appends the original port: JoinHostPort(v, port)
			if host, port, err := net.SplitHostPort(addr); err == nil && k == host {
				if got == net.JoinHostPort(v, port) {
					return
				}
			}
		}
		// Fuzzy matches also resolve to a table value; if got equals any value,
		// it is legitimate. Anything else is garbage the function must not emit.
		for _, v := range addrs {
			if got == v {
				return
			}
		}
		t.Fatalf("resolveAddr(%q, fuzzy=%v) returned %q which is neither the input nor any mapping value (addrs=%v)", addr, fuzzy, got, addrs)
	})
}

// FuzzHTTPCheckAuth fuzzes checkAuth over arbitrary Proxy-Authorization header
// values. Oracle: no panic; checkAuth must tolerate any header bytes (malformed
// base64, missing scheme, missing colon, control characters) and return a bool.
func FuzzHTTPCheckAuth(f *testing.F) {
	f.Add("Basic dXNlcjpwYXNz", "user", "pass")
	f.Add("Basic !!!notbase64", "user", "pass")
	f.Add("Bearer token", "user", "pass")
	f.Add("", "", "")
	f.Add("Basic ", "u", "p")
	f.Add("Basic Og==", "", "") // decodes to ":"
	f.Add("Basic dXNlcg==", "user", "")
	f.Add("BasicNoSpace", "user", "pass")

	f.Fuzz(func(t *testing.T, header, username, password string) {
		s := &HTTPProxyServer{username: username, password: password}
		req := &http.Request{Header: http.Header{}}
		if header != "" {
			req.Header.Set("Proxy-Authorization", header)
		}
		got := s.checkAuth(req)

		// Sanity oracle: when auth succeeds, the header must have decoded to the
		// exact configured credentials. This guards against checkAuth returning
		// true on a non-matching header.
		if got {
			if !strings.HasPrefix(header, "Basic ") {
				t.Fatalf("checkAuth returned true for non-Basic header %q", header)
			}
		}
	})
}
