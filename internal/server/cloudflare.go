package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"time"
)

// With "cloudflare" in trusted_proxies, Cloudflare's published address
// ranges are trusted as proxies and kept current. That is for a server
// Cloudflare reaches directly; behind Cloudflare Tunnel the peer is
// cloudflared, on loopback or a private address, and these ranges never match.

// cloudflareIPsURL lists the ranges Cloudflare's edge connects from.
const cloudflareIPsURL = "https://api.cloudflare.com/client/v4/ips"

// cloudflareBuiltin is used until the list has been fetched once.
var cloudflareBuiltin = []string{
	"173.245.48.0/20", "103.21.244.0/22", "103.22.200.0/22", "103.31.4.0/22", "141.101.64.0/18",
	"108.162.192.0/18", "190.93.240.0/20", "188.114.96.0/20", "197.234.240.0/22", "198.41.128.0/17",
	"162.158.0.0/15", "104.16.0.0/13", "104.24.0.0/14", "172.64.0.0/13", "131.0.72.0/22",
	"2400:cb00::/32", "2606:4700::/32", "2803:f800::/32", "2405:b500::/32", "2405:8100::/32",
	"2a06:98c0::/29", "2c0f:f248::/32",
}

const cloudflareRefresh = 24 * time.Hour

// parseCloudflareRanges checks a list of ranges before it is trusted: a
// short or sweeping list (a truncated or tampered answer) is refused, since
// whoever is in these ranges may state any client address.
func parseCloudflareRanges(cidrs []string) ([]netip.Prefix, error) {
	if len(cidrs) < 5 || len(cidrs) > 500 {
		return nil, fmt.Errorf("%d ranges", len(cidrs))
	}
	out := make([]netip.Prefix, 0, len(cidrs))
	for _, c := range cidrs {
		p, err := netip.ParsePrefix(c)
		if err != nil {
			return nil, err
		}
		if a := p.Addr(); a.Is4() && p.Bits() < 12 || a.Is6() && p.Bits() < 24 || a.IsPrivate() || a.IsLoopback() {
			return nil, fmt.Errorf("implausible range %s", c)
		}
		out = append(out, p.Masked())
	}
	return out, nil
}

func (s *Server) cloudflareCache() string {
	return filepath.Join(s.cfg.DataDir, "cloudflare-ips.json")
}

// loadCloudflareRanges sets the ranges known at startup: the last list
// fetched, or the built-in one.
func (s *Server) loadCloudflareRanges() {
	cidrs := cloudflareBuiltin
	if raw, err := os.ReadFile(s.cloudflareCache()); err == nil {
		var cached []string
		if json.Unmarshal(raw, &cached) == nil {
			if _, err := parseCloudflareRanges(cached); err == nil {
				cidrs = cached
			}
		}
	}
	ranges, _ := parseCloudflareRanges(cidrs)
	s.setProxies(ranges)
}

func (s *Server) setProxies(cloudflare []netip.Prefix) {
	all := append(append([]netip.Prefix{}, s.staticProxies...), cloudflare...)
	s.proxies.Store(&all)
}

// refreshCloudflareRanges fetches the current list, once a day. A fetch that
// fails or looks wrong leaves the list in use as it is.
func (s *Server) refreshCloudflareRanges(ctx context.Context) {
	if !s.cfg.TrustsCloudflare() || time.Since(s.cloudflareAt) < cloudflareRefresh {
		return
	}
	cidrs, err := fetchCloudflareRanges(ctx, cloudflareIPsURL)
	if err != nil {
		slog.Warn("cloudflare address ranges not refreshed", "err", err)
		return
	}
	ranges, err := parseCloudflareRanges(cidrs)
	if err != nil {
		slog.Warn("cloudflare address ranges not refreshed", "err", err)
		return
	}
	s.cloudflareAt = time.Now()
	s.setProxies(ranges)
	if raw, err := json.Marshal(cidrs); err == nil {
		if err := os.WriteFile(s.cloudflareCache(), raw, 0o600); err != nil {
			slog.Warn("cloudflare address ranges not saved", "err", err)
		}
	}
}

func fetchCloudflareRanges(ctx context.Context, url string) ([]string, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return nil, errors.New(res.Status)
	}
	var body struct {
		Success bool `json:"success"`
		Result  struct {
			IPv4 []string `json:"ipv4_cidrs"`
			IPv6 []string `json:"ipv6_cidrs"`
		} `json:"result"`
	}
	if err := json.NewDecoder(io.LimitReader(res.Body, 1<<20)).Decode(&body); err != nil {
		return nil, err
	}
	if !body.Success {
		return nil, errors.New("unsuccessful answer")
	}
	return append(body.Result.IPv4, body.Result.IPv6...), nil
}
