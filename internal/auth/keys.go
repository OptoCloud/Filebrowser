package auth

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// Keys holds subkeys derived from the server secret.
type Keys struct {
	aead     cipher.AEAD
	signKey  []byte
	tokenKey []byte
}

// LoadKeys reads (or creates) dataDir/secret.key.
func LoadKeys(dataDir string) (*Keys, error) {
	path := filepath.Join(dataDir, "secret.key")
	secret, err := os.ReadFile(path) // #nosec G304 -- path from server config
	if errors.Is(err, os.ErrNotExist) {
		if err := os.MkdirAll(dataDir, 0o700); err != nil {
			return nil, err
		}
		secret = make([]byte, 32)
		if _, err := rand.Read(secret); err != nil {
			return nil, err
		}
		if err := os.WriteFile(path, secret, 0o600); err != nil {
			return nil, err
		}
	} else if err != nil {
		return nil, err
	}
	if len(secret) < 32 {
		return nil, fmt.Errorf("%s: secret too short", path)
	}
	return NewKeys(secret)
}

func NewKeys(secret []byte) (*Keys, error) {
	sub := func(label string) []byte {
		m := hmac.New(sha256.New, secret)
		m.Write([]byte(label))
		return m.Sum(nil)
	}
	block, err := aes.NewCipher(sub("filebrowser encrypt v1"))
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &Keys{aead: aead, signKey: sub("filebrowser sign v1"), tokenKey: sub("filebrowser token v1")}, nil
}

func (k *Keys) Encrypt(plain, ad []byte) ([]byte, error) {
	nonce := make([]byte, k.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	return k.aead.Seal(nonce, nonce, plain, ad), nil
}

func (k *Keys) Decrypt(ct, ad []byte) ([]byte, error) {
	n := k.aead.NonceSize()
	if len(ct) < n {
		return nil, errors.New("ciphertext too short")
	}
	return k.aead.Open(nil, ct[:n], ct[n:], ad)
}

// Sign returns an HMAC-SHA256 of msg under the signing key, scoped by purpose.
func (k *Keys) Sign(purpose string, msg []byte) []byte {
	m := hmac.New(sha256.New, k.signKey)
	m.Write([]byte(purpose))
	m.Write([]byte{0})
	m.Write(msg)
	return m.Sum(nil)
}

func (k *Keys) Verify(purpose string, msg, sig []byte) bool {
	return hmac.Equal(k.Sign(purpose, msg), sig)
}

// HashToken hashes a bearer token (session id, share token, invite token) for
// storage. Tokens carry >=128 bits of entropy, so a keyed fast hash suffices.
func (k *Keys) HashToken(token string) []byte {
	m := hmac.New(sha256.New, k.tokenKey)
	m.Write([]byte(token))
	return m.Sum(nil)
}

// NewToken returns a URL-safe random token with n bytes of entropy.
func NewToken(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

func ConstantTimeEqual(a, b []byte) bool { return hmac.Equal(a, b) }
