package auth

import (
	"strings"
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
