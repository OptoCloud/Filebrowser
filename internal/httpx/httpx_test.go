package httpx

import (
	"net/http/httptest"
	"net/netip"
	"testing"
)

func TestClientIPTrustsOnlyProxies(t *testing.T) {
	trusted := []netip.Prefix{netip.MustParsePrefix("127.0.0.1/32")}
	r := httptest.NewRequest("GET", "/", nil)
	r.Header.Set("CF-Connecting-IP", "1.2.3.4")
	r.RemoteAddr = "127.0.0.1:5000"
	if got := ClientIP(r, "CF-Connecting-IP", trusted); got != "1.2.3.4" {
		t.Errorf("from proxy: %s", got)
	}
	r.RemoteAddr = "203.0.113.5:5000"
	if got := ClientIP(r, "CF-Connecting-IP", trusted); got != "203.0.113.5" {
		t.Errorf("spoofed header believed: %s", got)
	}
}

func TestRedactPath(t *testing.T) {
	for in, want := range map[string]string{
		"/s/abc/file": "/s/***/file", "/x/tok/name": "/x/***/name", "/invite/tok": "/invite/***", "/api/files": "/api/files",
	} {
		if got := RedactPath(in); got != want {
			t.Errorf("RedactPath(%q) = %q", in, got)
		}
	}
}

func TestClientIPForwardedForList(t *testing.T) {
	trusted := []netip.Prefix{netip.MustParsePrefix("10.0.0.0/8")}
	cases := map[string]string{
		"198.51.100.7":                    "198.51.100.7",
		"198.51.100.7, 10.0.0.2":          "198.51.100.7", // our second proxy appended itself
		"6.6.6.6, 198.51.100.7":           "198.51.100.7", // left-most entry is client-controlled
		"6.6.6.6, 198.51.100.7, 10.0.0.2": "198.51.100.7",
		"garbage, 198.51.100.7":           "198.51.100.7",
		"198.51.100.7, garbage":           "10.0.0.1", // unparseable: trust nothing past it
		"::ffff:198.51.100.7":             "198.51.100.7",
		"2001:db8::1, 10.0.0.3":           "2001:db8::1",
		"10.0.0.9, 10.0.0.8":              "10.0.0.9", // all proxies: the first one
	}
	for hdr, want := range cases {
		r := httptest.NewRequest("GET", "/", nil)
		r.RemoteAddr = "10.0.0.1:5000"
		r.Header.Set("X-Forwarded-For", hdr)
		if got := ClientIP(r, "X-Forwarded-For", trusted); got != want {
			t.Errorf("X-Forwarded-For %q: got %s, want %s", hdr, got, want)
		}
	}
}

func TestLimitKeyGroupsIPv6Subnets(t *testing.T) {
	key := func(ip string) string { return LimitKey(WithIP(t.Context(), ip)) }
	if key("2001:db8:1:2::1") != key("2001:db8:1:2:ffff::9") {
		t.Error("addresses in one /64 got different keys")
	}
	if key("2001:db8:1:2::1") == key("2001:db8:1:3::1") {
		t.Error("different /64s share a key")
	}
	if key("198.51.100.7") != "198.51.100.7" {
		t.Errorf("IPv4 key = %q", key("198.51.100.7"))
	}
}
