package auth

import (
	"context"
	"filebrowser/internal/config"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestPasswordHash(t *testing.T) {
	h, err := HashPassword("correct horse battery")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(h, "$argon2id$v=19$m=65536,t=3,p=4$") {
		t.Fatalf("unexpected encoding %q", h)
	}
	if !VerifyPassword(h, "correct horse battery") || VerifyPassword(h, "correct horse batterY") {
		t.Fatal("verify mismatch")
	}
	for _, bad := range []string{"", "$argon2id$", "$argon2i$v=19$m=65536,t=3,p=4$AAAA$AAAA", "$argon2id$v=19$m=99999999,t=3,p=4$AAAA$AAAA"} {
		if VerifyPassword(bad, "x") {
			t.Errorf("verified against %q", bad)
		}
	}
	if ValidatePassword("short") == nil || ValidatePassword(strings.Repeat("a", 300)) == nil || ValidatePassword("long enough pw") != nil {
		t.Error("ValidatePassword bounds")
	}
}

func TestTOTPRFC6238(t *testing.T) {
	secret := []byte("12345678901234567890")
	// RFC 6238 appendix B (SHA-1), truncated to 6 digits.
	vectors := map[int64]string{59: "287082", 1111111109: "081804", 1234567890: "005924", 2000000000: "279037"}
	for ts, want := range vectors {
		if got := TOTPCode(secret, time.Unix(ts, 0)); got != want {
			t.Errorf("t=%d: %s, want %s", ts, got, want)
		}
	}
	now := time.Unix(1234567890, 0)
	if CheckTOTP(secret, "005924", now) == 0 || CheckTOTP(secret, " 005 924 ", now) == 0 {
		t.Error("valid code rejected")
	}
	if CheckTOTP(secret, TOTPCode(secret, now.Add(-30*time.Second)), now) == 0 {
		t.Error("previous step rejected")
	}
	if CheckTOTP(secret, TOTPCode(secret, now.Add(-90*time.Second)), now) != 0 {
		t.Error("stale code accepted")
	}
	if CheckTOTP(secret, "12345", now) != 0 || CheckTOTP(secret, "abcdef", now) != 0 {
		t.Error("malformed code accepted")
	}
}

func TestBackoff(t *testing.T) {
	b := NewBackoff(3, time.Second, 10*time.Second, time.Hour)
	now := time.Unix(1000, 0)
	for range 3 {
		b.Fail("k", now)
	}
	if b.Wait("k", now) != 0 {
		t.Fatal("free attempts should not wait")
	}
	b.Fail("k", now)
	if w := b.Wait("k", now); w != time.Second {
		t.Fatalf("wait = %v", w)
	}
	for range 20 {
		b.Fail("k", now)
	}
	if w := b.Wait("k", now); w != 10*time.Second {
		t.Fatalf("wait not capped: %v", w)
	}
	b.Reset("k")
	if b.Wait("k", now) != 0 {
		t.Fatal("reset failed")
	}
}

func TestRateLimiter(t *testing.T) {
	r := NewRateLimiter(60, 2)
	now := time.Unix(0, 0)
	for i, want := range []bool{true, true, false} {
		if got := r.Allow("ip", now); got != want {
			t.Fatalf("burst request %d: allowed=%v", i, got)
		}
	}
	if !r.Allow("ip", now.Add(time.Second)) {
		t.Fatal("refill")
	}
}

func TestKeys(t *testing.T) {
	k, _ := NewKeys([]byte(strings.Repeat("k", 32)))
	ct, _ := k.Encrypt([]byte("secret"), []byte("ad"))
	if pt, err := k.Decrypt(ct, []byte("ad")); err != nil || string(pt) != "secret" {
		t.Fatal("roundtrip")
	}
	if _, err := k.Decrypt(ct, []byte("other")); err == nil {
		t.Fatal("wrong AD accepted")
	}
	if !k.Verify("a", []byte("m"), k.Sign("a", []byte("m"))) || k.Verify("b", []byte("m"), k.Sign("a", []byte("m"))) {
		t.Fatal("sign purpose separation")
	}
}

func TestReserveConcurrent(t *testing.T) {
	b := NewBackoff(3, time.Second, 10*time.Second, time.Hour)
	other := NewBackoff(100, time.Second, 10*time.Second, time.Hour)
	now := time.Now()
	var passed atomic.Int32
	var wg sync.WaitGroup
	for range 50 {
		wg.Go(func() {
			if Reserve(now, b.Slot("k"), other.Slot("k")) == 0 {
				passed.Add(1)
			}
		})
	}
	wg.Wait()
	// The free attempts plus the one that starts the backoff; the rest wait
	// even though none of the guesses has been checked yet.
	if n := passed.Load(); n != 4 {
		t.Fatalf("%d concurrent attempts passed, want 4", n)
	}
	// A refused attempt counts on no slot.
	if n := other.Slot("k").Failures(); n != 4 {
		t.Fatalf("other slot has %d failures, want 4", n)
	}
}

func TestReserveRefund(t *testing.T) {
	b := NewBackoff(1, time.Second, 10*time.Second, time.Hour)
	s := b.Slot("k")
	now := time.Now()
	for range 5 {
		if Reserve(now, s) != 0 {
			t.Fatal("successful attempts must not build up a backoff")
		}
		s.Refund()
	}
	Reserve(now, s)
	Reserve(now, s) // second failure: backoff starts
	if Reserve(now, s) == 0 {
		t.Fatal("want a wait after two failures")
	}
	s.Refund() // the second one was right after all
	if Reserve(now, s) != 0 {
		t.Fatal("a refund must lift the wait it caused")
	}
}

// Anyone can start passkey logins; a flood of them must not crowd out the
// ceremonies of known accounts (security-key second factor, registration).
func TestPasskeyFloodKeepsOtherCeremonies(t *testing.T) {
	w, err := NewWebAuthn("https://files.example.com")
	if err != nil {
		t.Fatal(err)
	}
	login, err := w.put(&Ceremony{Kind: CeremonyLogin, UserID: 1})
	if err != nil {
		t.Fatal(err)
	}
	var first string
	for i := range maxAnonCeremonies + 50 {
		tok, err := w.put(&Ceremony{Kind: CeremonyPasskey})
		if err != nil {
			t.Fatalf("passkey ceremony %d: %v", i, err)
		}
		if i == 0 {
			first = tok
		}
	}
	if len(w.anon) != maxAnonCeremonies {
		t.Fatalf("%d passkey ceremonies kept, want %d", len(w.anon), maxAnonCeremonies)
	}
	if _, err := w.Take(first, CeremonyPasskey); err == nil {
		t.Error("the oldest passkey ceremony should have been dropped")
	}
	if _, err := w.put(&Ceremony{Kind: CeremonyRegister, UserID: 1}); err != nil {
		t.Fatalf("registration blocked: %v", err)
	}
	if c, err := w.Take(login, CeremonyLogin); err != nil || c.UserID != 1 {
		t.Fatalf("second-factor ceremony lost: %v", err)
	}
}

func TestOIDCUsedMFA(t *testing.T) {
	claims := map[string]any{"amr": []any{"pwd", "otp"}, "acr": "urn:silver"}
	for _, tc := range []struct {
		cfg  config.OIDC
		want bool
	}{
		{config.OIDC{}, false},
		{config.OIDC{MFAAmr: []string{"otp"}}, false}, // not trusted at all
		{config.OIDC{TrustMFA: true}, true},
		{config.OIDC{TrustMFA: true, MFAAmr: []string{"mfa", "otp"}}, true},
		{config.OIDC{TrustMFA: true, MFAAmr: []string{"hwk"}}, false},
		{config.OIDC{TrustMFA: true, MFAAcr: []string{"urn:gold"}}, false},
		{config.OIDC{TrustMFA: true, MFAAmr: []string{"hwk"}, MFAAcr: []string{"urn:silver"}}, true},
	} {
		if got := usedMFA(tc.cfg, claims); got != tc.want {
			t.Errorf("usedMFA(%+v) = %v", tc.cfg, got)
		}
	}
	if usedMFA(config.OIDC{TrustMFA: true, MFAAmr: []string{"otp"}}, map[string]any{"amr": "otp"}) {
		t.Error("a malformed amr claim was accepted")
	}
}

func TestPasswordPolicy(t *testing.T) {
	ctx := context.Background()
	offline := &PasswordPolicy{}
	for _, pw := range []string{"short", "aaaaaaaaaaaa", "abababababab", "1234567890", "abcdefghijkl", "9876543210", "Password123",
		"qwertyuiop", "my-alice-password", "ALICE-2026-xyz"} {
		if offline.Check(ctx, pw, "alice") == nil {
			t.Errorf("%q accepted", pw)
		}
	}
	for _, pw := range []string{"correct horse battery", "Tr0ub4dor&3-xkcd", "vier-Wörter-für-heute", "al-is-fine-here"} {
		if err := offline.Check(ctx, pw, "alice"); err != nil {
			t.Errorf("%q: %v", pw, err)
		}
	}
	// "password" has SHA-1 5BAA61E4C9B93F3F0682250B6CF8331B7EE68FD8.
	var asked string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		asked = r.URL.Path
		if r.Header.Get("Add-Padding") != "true" {
			t.Error("no padding asked for")
		}
		w.Write([]byte("0018A45C4D1DEF81644B54AB7F969B88D65:1\r\n1E4C9B93F3F0682250B6CF8331B7EE68FD8:3861493\r\nFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFF:0\r\n"))
	}))
	defer srv.Close()
	p := &PasswordPolicy{Pwned: true, RangeURL: srv.URL + "/range/"}
	if n := p.pwned(ctx, "password"); n != 3861493 || asked != "/range/5BAA6" {
		t.Fatalf("pwned = %d, asked %q", n, asked)
	}
	if n := p.pwned(ctx, "a password nobody has used before"); n != 0 {
		t.Fatalf("unbreached password: %d", n)
	}
	// Unreachable: the password is accepted.
	down := &PasswordPolicy{Pwned: true, RangeURL: "http://127.0.0.1:1/range/"}
	if err := down.Check(ctx, "correct horse battery", ""); err != nil {
		t.Fatalf("with the service down: %v", err)
	}
}
