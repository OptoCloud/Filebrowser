package auth

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"sync"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"

	"filebrowser/internal/config"
)

var ErrOIDCDenied = errors.New("identity not allowed")

type OIDC struct {
	keys      *Keys
	secure    bool
	redirect  string
	providers map[string]*oidcProvider
	order     []string
}

type oidcProvider struct {
	cfg config.OIDC

	mu       sync.Mutex
	provider *oidc.Provider
	oauth    *oauth2.Config
}

type OIDCProviderInfo struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type OIDCIdentity struct {
	Provider   string
	Issuer     string
	Subject    string
	Username   string
	LinkUserID int64
	// LinkSession is the hash of the session that asked for the link.
	LinkSession []byte
	// MFA reports a login the provider is trusted to have asked a second
	// factor for (trust_mfa, and the token's amr or acr where configured).
	MFA bool
}

type oidcState struct {
	Provider string `json:"p"`
	State    string `json:"s"`
	Nonce    string `json:"n"`
	Verifier string `json:"v"`
	Link     int64  `json:"l,omitempty"`
	Session  []byte `json:"h,omitempty"`
	Expires  int64  `json:"e"`
}

func NewOIDC(cfgs []config.OIDC, appOrigin string, keys *Keys, secure bool) *OIDC {
	o := &OIDC{keys: keys, secure: secure, redirect: appOrigin + "/api/auth/oidc/callback", providers: map[string]*oidcProvider{}}
	for _, c := range cfgs {
		if c.TrustMFA && len(c.MFAAmr) == 0 && len(c.MFAAcr) == 0 {
			slog.Warn("oidc provider is trusted for two-factor authentication without mfa_amr or mfa_acr: "+
				"every login through it counts as one, whether or not the provider asked for a second factor", "provider", c.ID)
		}
		o.providers[c.ID] = &oidcProvider{cfg: c}
		o.order = append(o.order, c.ID)
	}
	return o
}

func (o *OIDC) Providers() []OIDCProviderInfo {
	out := []OIDCProviderInfo{}
	for _, id := range o.order {
		p := o.providers[id]
		name := p.cfg.Name
		if name == "" {
			name = id
		}
		out = append(out, OIDCProviderInfo{ID: id, Name: name})
	}
	return out
}

// usedMFA reports whether a login with these ID token claims satisfies the
// second-factor requirement by itself.
func usedMFA(cfg config.OIDC, claims map[string]any) bool {
	if !cfg.TrustMFA {
		return false
	}
	if len(cfg.MFAAmr) == 0 && len(cfg.MFAAcr) == 0 {
		return true
	}
	if acr, _ := claims["acr"].(string); acr != "" && slices.Contains(cfg.MFAAcr, acr) {
		return true
	}
	amr, _ := claims["amr"].([]any)
	for _, m := range amr {
		if s, _ := m.(string); slices.Contains(cfg.MFAAmr, s) {
			return true
		}
	}
	return false
}

func (p *oidcProvider) init(ctx context.Context, redirect string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.provider != nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	prov, err := oidc.NewProvider(ctx, p.cfg.Issuer)
	if err != nil {
		return fmt.Errorf("oidc discovery for %s: %w", p.cfg.ID, err)
	}
	p.provider = prov
	p.oauth = &oauth2.Config{
		ClientID:     p.cfg.ClientID,
		ClientSecret: p.cfg.ClientSecret,
		Endpoint:     prov.Endpoint(),
		RedirectURL:  redirect,
		Scopes:       p.cfg.Scopes,
	}
	return nil
}

func (o *OIDC) cookieName() string { return CookieName(o.secure, "oidc", "fb_oidc") }

// Start returns the provider authorization URL and sets the state cookie.
// linkUserID > 0 links the identity to an existing, logged-in user, whose
// session (linkSession, its hash) must still be valid when the provider
// returns: the callback cannot see the SameSite=Strict session cookie.
func (o *OIDC) Start(ctx context.Context, w http.ResponseWriter, providerID string, linkUserID int64, linkSession []byte) (string, error) {
	p := o.providers[providerID]
	if p == nil {
		return "", errors.New("unknown provider")
	}
	if err := p.init(ctx, o.redirect); err != nil {
		return "", err
	}
	st := oidcState{
		Provider: providerID,
		State:    NewToken(16),
		Nonce:    NewToken(16),
		Verifier: oauth2.GenerateVerifier(),
		Link:     linkUserID,
		Session:  linkSession,
		Expires:  time.Now().Add(10 * time.Minute).Unix(),
	}
	raw, _ := json.Marshal(st)
	ct, err := o.keys.Encrypt(raw, []byte("oidc-state"))
	if err != nil {
		return "", err
	}
	SetCookie(w, Cookie{
		Name: o.cookieName(), Value: base64.RawURLEncoding.EncodeToString(ct), MaxAge: 600,
		// Lax: the callback is a top-level cross-site navigation from the provider.
		SameSite: http.SameSiteLaxMode,
	}, o.secure)
	return p.oauth.AuthCodeURL(st.State, oidc.Nonce(st.Nonce), oauth2.S256ChallengeOption(st.Verifier)), nil
}

// Callback validates the provider response and returns the verified identity.
func (o *OIDC) Callback(ctx context.Context, w http.ResponseWriter, r *http.Request) (*OIDCIdentity, error) {
	c, err := r.Cookie(o.cookieName())
	if err != nil {
		return nil, errors.New("missing state cookie")
	}
	ClearCookie(w, o.cookieName(), o.secure, http.SameSiteLaxMode)
	ct, err := base64.RawURLEncoding.DecodeString(c.Value)
	if err != nil {
		return nil, errors.New("bad state cookie")
	}
	raw, err := o.keys.Decrypt(ct, []byte("oidc-state"))
	if err != nil {
		return nil, errors.New("bad state cookie")
	}
	var st oidcState
	if err := json.Unmarshal(raw, &st); err != nil || time.Now().Unix() > st.Expires {
		return nil, errors.New("state expired")
	}
	q := r.URL.Query()
	if e := q.Get("error"); e != "" {
		return nil, fmt.Errorf("provider error: %s", e)
	}
	if q.Get("state") == "" || q.Get("state") != st.State {
		return nil, errors.New("state mismatch")
	}
	p := o.providers[st.Provider]
	if p == nil {
		return nil, errors.New("unknown provider")
	}
	if err := p.init(ctx, o.redirect); err != nil {
		return nil, err
	}
	tok, err := p.oauth.Exchange(ctx, q.Get("code"), oauth2.VerifierOption(st.Verifier))
	if err != nil {
		return nil, fmt.Errorf("code exchange: %w", err)
	}
	rawID, ok := tok.Extra("id_token").(string)
	if !ok {
		return nil, errors.New("no id_token in response")
	}
	idt, err := p.provider.Verifier(&oidc.Config{ClientID: p.cfg.ClientID}).Verify(ctx, rawID)
	if err != nil {
		return nil, fmt.Errorf("verify id_token: %w", err)
	}
	if idt.Nonce != st.Nonce {
		return nil, errors.New("nonce mismatch")
	}
	var claims map[string]any
	if err := idt.Claims(&claims); err != nil {
		return nil, err
	}
	if len(p.cfg.AllowedSubjects) > 0 && !slices.Contains(p.cfg.AllowedSubjects, idt.Subject) {
		return nil, ErrOIDCDenied
	}
	if len(p.cfg.AllowedGroups) > 0 {
		groups, _ := claims[p.cfg.GroupsClaim].([]any)
		ok := false
		for _, g := range groups {
			if s, _ := g.(string); slices.Contains(p.cfg.AllowedGroups, s) {
				ok = true
				break
			}
		}
		if !ok {
			return nil, ErrOIDCDenied
		}
	}
	username, _ := claims["preferred_username"].(string)
	return &OIDCIdentity{Provider: st.Provider, Issuer: idt.Issuer, Subject: idt.Subject, Username: username, LinkUserID: st.Link, LinkSession: st.Session,
		MFA: usedMFA(p.cfg, claims)}, nil
}
