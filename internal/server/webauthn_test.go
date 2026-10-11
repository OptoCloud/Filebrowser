package server

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/fxamacker/cbor/v2"

	"filebrowser/internal/store"
)

// softKey is a minimal software authenticator (ES256, "none" attestation),
// enough to drive the real WebAuthn ceremonies through the handlers.
type softKey struct {
	t       *testing.T
	priv    *ecdsa.PrivateKey
	id      []byte
	handle  []byte // user handle stored for discoverable use
	counter uint32
}

var b64 = base64.RawURLEncoding

func newSoftKey(t *testing.T) *softKey {
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	id := make([]byte, 16)
	rand.Read(id)
	return &softKey{t: t, priv: priv, id: id}
}

const rpID = "files.test"

func (k *softKey) authData(flags byte, attested []byte) []byte {
	rp := sha256.Sum256([]byte(rpID))
	k.counter++
	out := append(rp[:], flags)
	out = binary.BigEndian.AppendUint32(out, k.counter)
	return append(out, attested...)
}

func clientData(typ, challenge string) []byte {
	raw, _ := json.Marshal(map[string]any{"type": typ, "challenge": challenge, "origin": appOrigin, "crossOrigin": false})
	return raw
}

// register answers a creation options object.
func (k *softKey) register(options map[string]any) json.RawMessage {
	pk := options["publicKey"].(map[string]any)
	k.handle, _ = b64.DecodeString(pk["user"].(map[string]any)["id"].(string))
	point, _ := k.priv.PublicKey.Bytes() // 0x04 || X || Y
	x, y := point[1:33], point[33:65]
	cose, _ := cbor.Marshal(map[int]any{1: 2, 3: -7, -1: 1, -2: x, -3: y})
	attested := make([]byte, 16) // AAGUID
	attested = binary.BigEndian.AppendUint16(attested, uint16(len(k.id)))
	attested = append(append(attested, k.id...), cose...)
	auth := k.authData(0x01|0x04|0x40, attested) // UP, UV, AT
	att, _ := cbor.Marshal(map[string]any{"fmt": "none", "attStmt": map[string]any{}, "authData": auth})
	raw, _ := json.Marshal(map[string]any{
		"id": b64.EncodeToString(k.id), "rawId": b64.EncodeToString(k.id), "type": "public-key",
		"response": map[string]any{
			"clientDataJSON":    b64.EncodeToString(clientData("webauthn.create", pk["challenge"].(string))),
			"attestationObject": b64.EncodeToString(att),
		},
		"clientExtensionResults": map[string]any{"credProps": map[string]any{"rk": true}},
	})
	return raw
}

// assert answers a request options object.
func (k *softKey) assert(options map[string]any, uv bool) json.RawMessage {
	pk := options["publicKey"].(map[string]any)
	flags := byte(0x01)
	if uv {
		flags |= 0x04
	}
	auth := k.authData(flags, nil)
	cd := clientData("webauthn.get", pk["challenge"].(string))
	cdHash := sha256.Sum256(cd)
	digest := sha256.Sum256(append(append([]byte{}, auth...), cdHash[:]...))
	sig, err := ecdsa.SignASN1(rand.Reader, k.priv, digest[:])
	if err != nil {
		k.t.Fatal(err)
	}
	raw, _ := json.Marshal(map[string]any{
		"id": b64.EncodeToString(k.id), "rawId": b64.EncodeToString(k.id), "type": "public-key",
		"response": map[string]any{
			"clientDataJSON":    b64.EncodeToString(cd),
			"authenticatorData": b64.EncodeToString(auth),
			"signature":         b64.EncodeToString(sig),
			"userHandle":        b64.EncodeToString(k.handle),
		},
		"clientExtensionResults": map[string]any{},
	})
	return raw
}

type ceremonyJSON struct {
	Ceremony string         `json:"ceremony"`
	Options  map[string]any `json:"options"`
}

func TestWebAuthnEndToEnd(t *testing.T) {
	h := newHarness(t)
	h.user("kim", "user", store.Grant{Path: "kim", Permission: "write"})
	write(t, h.root, "kim/notes.txt", "n")
	key := newSoftKey(t)

	// A writer must enroll a second factor: a security key does it.
	c := h.app()
	expect(t, c.do("POST", "/api/auth/login", map[string]string{"username": "kim", "password": testPassword}), 200)
	if me := decode[map[string]any](t, c.do("GET", "/api/me", nil)); me["restricted"] != true {
		t.Fatalf("expected a restricted session, got %v", me)
	}
	begin := decode[ceremonyJSON](t, c.do("POST", "/api/me/webauthn/register/begin", struct{}{}))
	expect(t, c.do("POST", "/api/me/webauthn/register/finish", map[string]any{
		"ceremony": begin.Ceremony, "name": "YubiKey 5", "credential": key.register(begin.Options)}), 200)
	me := decode[map[string]any](t, c.do("GET", "/api/me", nil))
	if me["restricted"] != false || me["mfa_enabled"] != true || me["webauthn_count"] != float64(1) {
		t.Fatalf("after registering: %v", me)
	}
	keys := decode[[]map[string]any](t, c.do("GET", "/api/me/webauthn", nil))
	if len(keys) != 1 || keys[0]["name"] != "YubiKey 5" || keys[0]["passkey"] != true {
		t.Fatalf("keys = %v", keys)
	}
	// The same key cannot be registered twice; a used ceremony cannot be replayed.
	again := decode[ceremonyJSON](t, c.do("POST", "/api/me/webauthn/register/begin", struct{}{}))
	if ex, _ := again.Options["publicKey"].(map[string]any)["excludeCredentials"].([]any); len(ex) != 1 {
		t.Errorf("excludeCredentials = %v", ex)
	}
	expect(t, c.do("POST", "/api/me/webauthn/register/finish", map[string]any{
		"ceremony": begin.Ceremony, "name": "replay", "credential": key.register(begin.Options)}), 400)

	// Password login now asks for the key.
	c2 := h.app()
	login := decode[map[string]any](t, c2.do("POST", "/api/auth/login", map[string]string{"username": "kim", "password": testPassword}))
	if login["mfa_required"] != true || login["webauthn"] != true || login["totp"] != false {
		t.Fatalf("login = %v", login)
	}
	// A key that is not registered is refused.
	stranger := newSoftKey(t)
	b := decode[ceremonyJSON](t, c2.do("POST", "/api/auth/login/webauthn/begin", map[string]string{"token": login["token"].(string)}))
	expect(t, c2.do("POST", "/api/auth/login/webauthn/finish", map[string]any{"ceremony": b.Ceremony, "credential": stranger.assert(b.Options, false)}), 401)
	b = decode[ceremonyJSON](t, c2.do("POST", "/api/auth/login/webauthn/begin", map[string]string{"token": login["token"].(string)}))
	expect(t, c2.do("POST", "/api/auth/login/webauthn/finish", map[string]any{"ceremony": b.Ceremony, "credential": key.assert(b.Options, false)}), 200)
	expect(t, c2.do("GET", "/api/files?path=kim", nil), 200)

	// Passkey login without a password; user verification is required.
	c3 := h.app()
	p := decode[ceremonyJSON](t, c3.do("POST", "/api/auth/passkey/begin", struct{}{}))
	expect(t, c3.do("POST", "/api/auth/passkey/finish", map[string]any{"ceremony": p.Ceremony, "credential": key.assert(p.Options, false)}), 401)
	p = decode[ceremonyJSON](t, c3.do("POST", "/api/auth/passkey/begin", struct{}{}))
	expect(t, c3.do("POST", "/api/auth/passkey/finish", map[string]any{"ceremony": p.Ceremony, "credential": key.assert(p.Options, true)}), 200)
	if me := decode[map[string]any](t, c3.do("GET", "/api/me", nil)); me["restricted"] != false {
		t.Fatalf("passkey session restricted: %v", me)
	}

	// A counter that goes backwards (a cloned key) is refused.
	key.counter = 0
	p = decode[ceremonyJSON](t, c3.do("POST", "/api/auth/passkey/begin", struct{}{}))
	expect(t, h.app().do("POST", "/api/auth/passkey/finish", map[string]any{"ceremony": p.Ceremony, "credential": key.assert(p.Options, true)}), 401)

	// The last second factor of a writer cannot be removed.
	rec := c3.do("DELETE", "/api/me/webauthn/"+keys[0]["id"].(string), nil)
	expect(t, rec, http.StatusConflict)
	if decode[map[string]string](t, rec)["code"] != "last_second_factor" {
		t.Fatalf("delete body %s", rec.Body.String())
	}
}
