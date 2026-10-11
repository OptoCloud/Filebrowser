// Package auth implements password hashing, TOTP, sessions, rate limiting and OIDC.
package auth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"golang.org/x/crypto/argon2"
)

// Argon2id parameters (RFC 9106 second recommended option).
const (
	argonTime    = 3
	argonMemory  = 64 * 1024 // KiB
	argonThreads = 4
	argonKeyLen  = 32
	argonSaltLen = 16
)

const (
	MinPasswordLen = 10
	MaxPasswordLen = 256
)

// hashSem bounds concurrent argon2 computations (64 MiB each).
var hashSem = make(chan struct{}, 4)

func ValidatePassword(pw string) error {
	if !utf8.ValidString(pw) {
		return errors.New("password must be valid UTF-8")
	}
	n := utf8.RuneCountInString(pw)
	if n < MinPasswordLen {
		return fmt.Errorf("password must be at least %d characters", MinPasswordLen)
	}
	if len(pw) > MaxPasswordLen {
		return fmt.Errorf("password must be at most %d bytes", MaxPasswordLen)
	}
	return nil
}

func HashPassword(pw string) (string, error) {
	salt := make([]byte, argonSaltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	key := derive([]byte(pw), salt, argonTime, argonMemory, argonThreads, argonKeyLen)
	enc := base64.RawStdEncoding
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, argonMemory, argonTime, argonThreads, enc.EncodeToString(salt), enc.EncodeToString(key)), nil
}

func derive(pw, salt []byte, t, m uint32, p uint8, n uint32) []byte {
	hashSem <- struct{}{}
	defer func() { <-hashSem }()
	return argon2.IDKey(pw, salt, t, m, p, n)
}

// dummyHash is verified against when the user does not exist, so that unknown
// usernames take as long as wrong passwords.
var dummyHash, _ = HashPassword("dummy password for timing equalization")

// VerifyPassword reports whether pw matches the encoded hash. An empty hash
// (e.g. OIDC-only accounts) never matches but still costs one hash.
func VerifyPassword(encoded, pw string) bool {
	if encoded == "" {
		encoded = dummyHash
		pw = "\x00" + pw
	}
	var version int
	var m, t uint32
	var p uint8
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[1] != "argon2id" {
		return false
	}
	if _, err := fmt.Sscanf(parts[2], "v=%d", &version); err != nil || version != argon2.Version {
		return false
	}
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &m, &t, &p); err != nil {
		return false
	}
	if m > 1<<20 || t > 16 || p == 0 {
		return false
	}
	enc := base64.RawStdEncoding
	salt, err := enc.DecodeString(parts[4])
	if err != nil {
		return false
	}
	want, err := enc.DecodeString(parts[5])
	if err != nil || len(want) == 0 || len(want) > 64 {
		return false
	}
	got := derive([]byte(pw), salt, t, m, p, uint32(len(want))) // #nosec G115 -- len(want) is bounded to 64 above
	return subtle.ConstantTimeCompare(got, want) == 1
}

// VerifyDummy burns the same time as a real verification.
func VerifyDummy(pw string) { VerifyPassword(dummyHash, "\x00"+pw) }
