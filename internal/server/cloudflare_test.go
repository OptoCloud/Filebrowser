package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestCloudflareRanges(t *testing.T) {
	if _, err := parseCloudflareRanges(cloudflareBuiltin); err != nil {
		t.Fatalf("built-in list: %v", err)
	}
	for name, list := range map[string][]string{
		"too short":  {"173.245.48.0/20"},
		"everything": {"0.0.0.0/0", "103.21.244.0/22", "103.22.200.0/22", "103.31.4.0/22", "141.101.64.0/18"},
		"private":    {"10.0.0.0/16", "103.21.244.0/22", "103.22.200.0/22", "103.31.4.0/22", "141.101.64.0/18"},
		"not a cidr": {"cloudflare", "103.21.244.0/22", "103.22.200.0/22", "103.31.4.0/22", "141.101.64.0/18"},
	} {
		if _, err := parseCloudflareRanges(list); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"result":{"ipv4_cidrs":["173.245.48.0/20"],"ipv6_cidrs":["2400:cb00::/32"]},"success":true}`))
	}))
	defer srv.Close()
	got, err := fetchCloudflareRanges(context.Background(), srv.URL)
	if err != nil || len(got) != 2 || got[1] != "2400:cb00::/32" {
		t.Fatalf("fetch = %v, %v", got, err)
	}
}
