// Package config loads the filebrowser TOML configuration.
package config

import (
	"errors"
	"fmt"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
)

// Duration is a time.Duration that unmarshals from strings like "720h".
type Duration struct{ time.Duration }

func (d *Duration) UnmarshalText(b []byte) error {
	v, err := time.ParseDuration(string(b))
	if err != nil {
		return err
	}
	d.Duration = v
	return nil
}

func (d Duration) MarshalText() ([]byte, error) { return []byte(d.String()), nil }

type Config struct {
	Listen string `toml:"listen"`
	// AppOrigin is the authenticated UI/API origin, e.g. https://files.example.com.
	AppOrigin string `toml:"app_origin"`
	// ShareOrigin serves public share links and user content, e.g. https://share.example.com.
	ShareOrigin string `toml:"share_origin"`
	// DatabaseURL is a PostgreSQL connection string. FB_DATABASE_URL overrides it
	// (likewise FB_APP_ORIGIN and FB_SHARE_ORIGIN for the origins).
	DatabaseURL string `toml:"database_url"`
	// DataDir holds the secret key file.
	DataDir string `toml:"data_dir"`
	// CacheDir holds thumbnails and the media cache.
	CacheDir string `toml:"cache_dir"`
	// FilesRoot is the directory that is browsed. Grants are paths relative to it.
	FilesRoot string `toml:"files_root"`
	// TrustedIPHeader names a header carrying the client IP set by a trusted proxy
	// (e.g. "CF-Connecting-IP" behind Cloudflare Tunnel). Empty uses RemoteAddr.
	TrustedIPHeader string `toml:"trusted_ip_header"`
	// TrustedProxies are the peers (CIDRs) whose TrustedIPHeader is believed.
	TrustedProxies []string `toml:"trusted_proxies"`
	// Dev relaxes cookie and HSTS settings for http://localhost development.
	Dev bool `toml:"dev"`

	Session Session `toml:"session"`
	Trash   Trash   `toml:"trash"`
	Shares  Shares  `toml:"shares"`
	Uploads Uploads `toml:"uploads"`
	Media   Media   `toml:"media"`
	OIDC    []OIDC  `toml:"oidc"`
}

type Session struct {
	IdleTimeout     Duration `toml:"idle_timeout"`
	AbsoluteTimeout Duration `toml:"absolute_timeout"`
}

type Trash struct {
	RetentionDays int `toml:"retention_days"`
}

type Shares struct {
	DefaultExpiry Duration `toml:"default_expiry"`
	MaxExpiry     Duration `toml:"max_expiry"`
}

type Uploads struct {
	MaxSize int64 `toml:"max_size"`
}

type Media struct {
	FFmpeg  string `toml:"ffmpeg"`
	FFprobe string `toml:"ffprobe"`
	// Sandbox is "bwrap" (default on Linux), "appcontainer" (default on
	// Windows) or "none".
	Sandbox string `toml:"sandbox"`
	// AllowUnsandboxed permits sandbox = "none" outside dev mode.
	AllowUnsandboxed bool `toml:"allow_unsandboxed"`
	// Wrapper is an extra command prefix, e.g. ["sudo", "-n", "-u", "fbmedia"].
	Wrapper        []string `toml:"wrapper"`
	MaxJobs        int      `toml:"max_jobs"`
	MaxJobsPerUser int      `toml:"max_jobs_per_user"`
	JobTimeout     Duration `toml:"job_timeout"`
	ProbeTimeout   Duration `toml:"probe_timeout"`
	CacheMaxBytes  int64    `toml:"cache_max_bytes"`
	// MemoryLimitMB and CPUSeconds are enforced with prlimit when > 0 (Linux).
	MemoryLimitMB int `toml:"memory_limit_mb"`
	CPUSeconds    int `toml:"cpu_seconds"`
	// ExternalURLTTL is the lifetime of signed "Open in VLC" URLs.
	ExternalURLTTL Duration `toml:"external_url_ttl"`
}

type OIDC struct {
	ID           string   `toml:"id"`
	Name         string   `toml:"name"`
	Issuer       string   `toml:"issuer"`
	ClientID     string   `toml:"client_id"`
	ClientSecret string   `toml:"client_secret"`
	Scopes       []string `toml:"scopes"`
	// AllowedSubjects / AllowedGroups restrict who may log in. Empty means any
	// identity already linked to a user.
	AllowedSubjects []string `toml:"allowed_subjects"`
	AllowedGroups   []string `toml:"allowed_groups"`
	GroupsClaim     string   `toml:"groups_claim"`
	// TrustMFA treats OIDC logins as satisfying the TOTP requirement.
	TrustMFA bool `toml:"trust_mfa"`
}

func Default() Config {
	return Config{
		Listen:         "127.0.0.1:8080",
		AppOrigin:      "http://files.localhost:8080",
		ShareOrigin:    "http://share.localhost:8080",
		DataDir:        "data",
		CacheDir:       "cache",
		FilesRoot:      "files",
		TrustedProxies: []string{"127.0.0.1/32", "::1/128"},
		Session: Session{
			IdleTimeout:     Duration{7 * 24 * time.Hour},
			AbsoluteTimeout: Duration{30 * 24 * time.Hour},
		},
		Trash:   Trash{RetentionDays: 30},
		Shares:  Shares{DefaultExpiry: Duration{7 * 24 * time.Hour}, MaxExpiry: Duration{90 * 24 * time.Hour}},
		Uploads: Uploads{MaxSize: 100 << 30},
		Media: Media{
			FFmpeg:         "ffmpeg",
			FFprobe:        "ffprobe",
			Sandbox:        defaultSandbox(),
			MaxJobs:        2,
			MaxJobsPerUser: 1,
			JobTimeout:     Duration{2 * time.Hour},
			ProbeTimeout:   Duration{30 * time.Second},
			CacheMaxBytes:  200 << 30,
			CPUSeconds:     3600,
			ExternalURLTTL: Duration{6 * time.Hour},
		},
	}
}

func Load(path string) (Config, error) {
	cfg := Default()
	if path != "" {
		md, err := toml.DecodeFile(path, &cfg)
		if err != nil {
			return cfg, err
		}
		if und := md.Undecoded(); len(und) > 0 {
			return cfg, fmt.Errorf("unknown config keys: %v", und)
		}
		base := filepath.Dir(path)
		for _, p := range []*string{&cfg.DataDir, &cfg.CacheDir, &cfg.FilesRoot} {
			if *p != "" && !filepath.IsAbs(*p) {
				*p = filepath.Join(base, *p)
			}
		}
	}
	// Environment overrides, convenient for containers.
	for name, p := range map[string]*string{
		"FB_DATABASE_URL": &cfg.DatabaseURL, "FB_APP_ORIGIN": &cfg.AppOrigin, "FB_SHARE_ORIGIN": &cfg.ShareOrigin,
	} {
		if v := os.Getenv(name); v != "" {
			*p = v
		}
	}
	for i := range cfg.OIDC {
		if v := os.Getenv("FB_OIDC_" + strings.ToUpper(cfg.OIDC[i].ID) + "_CLIENT_SECRET"); v != "" {
			cfg.OIDC[i].ClientSecret = v
		}
		if len(cfg.OIDC[i].Scopes) == 0 {
			cfg.OIDC[i].Scopes = []string{"openid", "profile", "email"}
		}
		if cfg.OIDC[i].GroupsClaim == "" {
			cfg.OIDC[i].GroupsClaim = "groups"
		}
	}
	return cfg, cfg.Validate()
}

func (c Config) Validate() error {
	var errs []error
	if c.DatabaseURL == "" {
		errs = append(errs, errors.New("database_url (or FB_DATABASE_URL) is required"))
	}
	app, err := url.Parse(c.AppOrigin)
	if err != nil || app.Host == "" {
		errs = append(errs, errors.New("app_origin must be an absolute URL"))
	}
	share, err := url.Parse(c.ShareOrigin)
	if err != nil || share.Host == "" {
		errs = append(errs, errors.New("share_origin must be an absolute URL"))
	}
	if app != nil && share != nil && app.Host == share.Host {
		errs = append(errs, errors.New("app_origin and share_origin must use different hosts"))
	}
	if !c.Dev && (app != nil && app.Scheme != "https" || share != nil && share.Scheme != "https") {
		errs = append(errs, errors.New("origins must use https unless dev = true"))
	}
	if _, err := c.ProxyPrefixes(); err != nil {
		errs = append(errs, err)
	}
	if c.FilesRoot == "" {
		errs = append(errs, errors.New("files_root is required"))
	}
	if c.Shares.MaxExpiry.Duration <= 0 {
		errs = append(errs, errors.New("shares.max_expiry must be positive"))
	}
	switch c.Media.Sandbox {
	case "none":
	case "bwrap":
		if runtime.GOOS != "linux" {
			errs = append(errs, errors.New(`media.sandbox = "bwrap" requires Linux`))
		}
	case "appcontainer":
		if runtime.GOOS != "windows" {
			errs = append(errs, errors.New(`media.sandbox = "appcontainer" requires Windows`))
		}
	default:
		errs = append(errs, errors.New(`media.sandbox must be "bwrap", "appcontainer" or "none"`))
	}
	if c.Media.Sandbox == "none" && !c.Dev && !c.Media.AllowUnsandboxed {
		errs = append(errs, errors.New(`media.sandbox = "none" runs ffmpeg on untrusted files without isolation; use "bwrap" (Linux) or "appcontainer" (Windows), or set media.allow_unsandboxed = true`))
	}
	seen := map[string]bool{}
	for _, o := range c.OIDC {
		if o.ID == "" || o.Issuer == "" || o.ClientID == "" {
			errs = append(errs, errors.New("oidc providers need id, issuer and client_id"))
		}
		if seen[o.ID] {
			errs = append(errs, fmt.Errorf("duplicate oidc provider id %q", o.ID))
		}
		seen[o.ID] = true
	}
	return errors.Join(errs...)
}

// AppHost and ShareHost return the host[:port] of each origin.
func (c Config) AppHost() string   { u, _ := url.Parse(c.AppOrigin); return u.Host }
func (c Config) ShareHost() string { u, _ := url.Parse(c.ShareOrigin); return u.Host }

// defaultSandbox is bwrap on Linux and an AppContainer on Windows; elsewhere
// (development only) there is none.
func defaultSandbox() string {
	switch runtime.GOOS {
	case "linux":
		return "bwrap"
	case "windows":
		return "appcontainer"
	}
	return "none"
}

// ProxyPrefixes parses TrustedProxies.
func (c Config) ProxyPrefixes() ([]netip.Prefix, error) {
	var out []netip.Prefix
	for _, p := range c.TrustedProxies {
		pre, err := netip.ParsePrefix(p)
		if err != nil {
			return nil, fmt.Errorf("trusted_proxies: %w", err)
		}
		out = append(out, pre.Masked())
	}
	return out, nil
}
