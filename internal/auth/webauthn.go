package auth

import (
	"errors"
	"net/url"
	"sync"
	"time"

	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/webauthn"
)

// Ceremony kinds.
const (
	CeremonyRegister = "register" // adding a key to a logged-in account
	CeremonyLogin    = "login"    // a key as the second factor after a password or OIDC login
	CeremonyPasskey  = "passkey"  // passwordless login with a discoverable credential
)

var (
	ErrCeremony = errors.New("webauthn ceremony expired or invalid")
	// ErrCloned reports a sign counter that went backwards: the key may have
	// been cloned, so the login is refused.
	ErrCloned = errors.New("security key counter went backwards")
)

const (
	ceremonyTTL   = 5 * time.Minute
	maxCeremonies = 10000
)

// WebAuthn handles security keys (YubiKey and other FIDO2 keys, platform
// authenticators) and passkeys. Challenges live in memory and are single use.
type WebAuthn struct {
	lib *webauthn.WebAuthn

	mu         sync.Mutex
	ceremonies map[string]*Ceremony
}

// Ceremony is the server side of one registration or login attempt.
type Ceremony struct {
	Kind   string
	UserID int64 // the account, for register and login
	// Pending is the password (or OIDC) login this second factor completes.
	Pending string

	session webauthn.SessionData
	expires time.Time
}

// NewWebAuthn configures the relying party from the app origin: credentials
// are bound to its host name and only that origin may use them.
func NewWebAuthn(appOrigin string) (*WebAuthn, error) {
	u, err := url.Parse(appOrigin)
	if err != nil {
		return nil, err
	}
	lib, err := webauthn.New(&webauthn.Config{
		RPID:          u.Hostname(),
		RPDisplayName: u.Hostname(),
		RPOrigins:     []string{appOrigin},
	})
	if err != nil {
		return nil, err
	}
	return &WebAuthn{lib: lib, ceremonies: map[string]*Ceremony{}}, nil
}

// WebAuthnUser adapts an account for the webauthn library.
type WebAuthnUser struct {
	UserID int64
	Handle []byte // random user handle, see store.WebAuthnHandle
	Name   string
	Creds  []webauthn.Credential
}

func (u *WebAuthnUser) WebAuthnID() []byte                         { return u.Handle }
func (u *WebAuthnUser) WebAuthnName() string                       { return u.Name }
func (u *WebAuthnUser) WebAuthnDisplayName() string                { return u.Name }
func (u *WebAuthnUser) WebAuthnCredentials() []webauthn.Credential { return u.Creds }

// IsPasskey reports whether the authenticator stored the credential as
// discoverable (from the credProps extension), so it can log in without a
// user name.
func IsPasskey(c *webauthn.Credential) bool { return c.Extensions.RK != nil && *c.Extensions.RK }

func descriptors(creds []webauthn.Credential) []protocol.CredentialDescriptor {
	out := make([]protocol.CredentialDescriptor, len(creds))
	for i, c := range creds {
		out[i] = c.Descriptor()
	}
	return out
}

// BeginRegistration starts adding a key to u. Discoverable credentials are
// preferred so that the key also works as a passkey.
func (w *WebAuthn) BeginRegistration(u *WebAuthnUser) (string, *protocol.CredentialCreation, error) {
	creation, session, err := w.lib.BeginRegistration(u,
		webauthn.WithExclusions(descriptors(u.Creds)),
		webauthn.WithAuthenticatorSelection(protocol.AuthenticatorSelection{
			ResidentKey:      protocol.ResidentKeyRequirementPreferred,
			UserVerification: protocol.VerificationPreferred,
		}),
		webauthn.WithResidentKeyRequirement(protocol.ResidentKeyRequirementPreferred),
		// credProps tells whether the key stored a discoverable credential (a passkey).
		webauthn.WithExtensions(webauthn.WithExtensionCredProps()))
	if err != nil {
		return "", nil, err
	}
	tok, err := w.put(&Ceremony{Kind: CeremonyRegister, UserID: u.UserID, session: *session})
	return tok, creation, err
}

// CreateCredential verifies a registration response.
func (w *WebAuthn) CreateCredential(u *WebAuthnUser, c *Ceremony, response []byte) (*webauthn.Credential, error) {
	parsed, err := protocol.ParseCredentialCreationResponseBytes(response)
	if err != nil {
		return nil, err
	}
	return w.lib.CreateCredential(u, c.session, parsed)
}

// BeginLogin asks for one of u's keys as the second factor of the pending
// login. Touching the key is enough; its PIN is not required on top of the
// password.
func (w *WebAuthn) BeginLogin(u *WebAuthnUser, pending string) (string, *protocol.CredentialAssertion, error) {
	assertion, session, err := w.lib.BeginLogin(u, webauthn.WithUserVerification(protocol.VerificationDiscouraged))
	if err != nil {
		return "", nil, err
	}
	tok, err := w.put(&Ceremony{Kind: CeremonyLogin, UserID: u.UserID, Pending: pending, session: *session})
	return tok, assertion, err
}

// ValidateLogin verifies a second-factor response and returns the credential
// with its updated sign count and flags, to be stored.
func (w *WebAuthn) ValidateLogin(u *WebAuthnUser, c *Ceremony, response []byte) (*webauthn.Credential, error) {
	parsed, err := protocol.ParseCredentialRequestResponseBytes(response)
	if err != nil {
		return nil, err
	}
	cred, err := w.lib.ValidateLogin(u, c.session, parsed)
	if err != nil {
		return nil, err
	}
	if cred.Authenticator.CloneWarning {
		return nil, ErrCloned
	}
	return cred, nil
}

// BeginPasskey starts a passwordless login. The authenticator must verify the
// user (PIN or biometric), so a passkey counts as two factors.
func (w *WebAuthn) BeginPasskey() (string, *protocol.CredentialAssertion, error) {
	assertion, session, err := w.lib.BeginDiscoverableLogin(webauthn.WithUserVerification(protocol.VerificationRequired))
	if err != nil {
		return "", nil, err
	}
	tok, err := w.put(&Ceremony{Kind: CeremonyPasskey, session: *session})
	return tok, assertion, err
}

// ValidatePasskey verifies a passkey response. lookup finds the account by
// the user handle the authenticator returned.
func (w *WebAuthn) ValidatePasskey(c *Ceremony, response []byte, lookup func(handle []byte) (*WebAuthnUser, error)) (*WebAuthnUser, *webauthn.Credential, error) {
	parsed, err := protocol.ParseCredentialRequestResponseBytes(response)
	if err != nil {
		return nil, nil, err
	}
	var found *WebAuthnUser
	_, cred, err := w.lib.ValidatePasskeyLogin(func(_, handle []byte) (webauthn.User, error) {
		u, err := lookup(handle)
		found = u
		return u, err
	}, c.session, parsed)
	if err != nil {
		return nil, nil, err
	}
	if cred.Authenticator.CloneWarning {
		return nil, nil, ErrCloned
	}
	if !cred.Flags.UserVerified {
		return nil, nil, errors.New("passkey did not verify the user")
	}
	return found, cred, nil
}

// Take removes and returns the ceremony for token if it is of the given kind
// and still valid. Each challenge can be answered once.
func (w *WebAuthn) Take(token, kind string) (*Ceremony, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	c := w.ceremonies[token]
	delete(w.ceremonies, token)
	if c == nil || c.Kind != kind || time.Now().After(c.expires) {
		return nil, ErrCeremony
	}
	return c, nil
}

func (w *WebAuthn) put(c *Ceremony) (string, error) {
	c.expires = time.Now().Add(ceremonyTTL)
	w.mu.Lock()
	defer w.mu.Unlock()
	if len(w.ceremonies) >= maxCeremonies {
		w.gcLocked()
		if len(w.ceremonies) >= maxCeremonies {
			return "", errors.New("too many webauthn ceremonies in progress")
		}
	}
	tok := NewToken(24)
	w.ceremonies[tok] = c
	return tok, nil
}

// GC drops expired ceremonies.
func (w *WebAuthn) GC() {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.gcLocked()
}

func (w *WebAuthn) gcLocked() {
	now := time.Now()
	for k, c := range w.ceremonies {
		if now.After(c.expires) {
			delete(w.ceremonies, k)
		}
	}
}
