package passkey

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/kfet/pinopass"
)

// Config configures the relying party. Passkeys are enabled only when
// both RPID and Origin are non-empty (see Enabled).
type Config struct {
	// RPID is the WebAuthn Relying Party ID — the effective domain the
	// credential is scoped to, e.g. "rss.example.com". It must be the
	// origin's host or a registrable parent of it.
	RPID string `json:"rp_id,omitempty"`
	// Origin is the exact browser origin, e.g. "https://rss.example.com".
	Origin string `json:"origin,omitempty"`
	// RPName is the human-facing relying-party name shown by some
	// authenticators during registration. Defaults to "harb".
	RPName string `json:"rp_name,omitempty"`
}

// Enabled reports whether passkey support is configured.
func (c Config) Enabled() bool { return c.RPID != "" && c.Origin != "" }

// challengeTTL bounds how long a begin→finish ceremony may take.
const challengeTTL = 5 * time.Minute

// ceremony distinguishes the two pending-challenge kinds so a challenge
// minted for registration can't be redeemed at the login endpoint.
type ceremony string

const (
	ceremonyRegister ceremony = "register"
	ceremonyLogin    ceremony = "login"
)

type pending struct {
	challenge []byte
	kind      ceremony
	created   time.Time
}

// Manager ties the pinopass verifier to a credential store and the
// short-lived challenge bookkeeping needed between the two halves of a
// ceremony. It is safe for concurrent use.
type Manager struct {
	rp       pinopass.RelyingParty
	rpName   string
	store    *CredentialStore
	userID   []byte // opaque, stable WebAuthn user handle (single user)
	userName string

	mu      sync.Mutex
	pending map[string]pending

	// Injectable for tests.
	now          func() time.Time
	newChallenge func() ([]byte, error)
	newHandle    func() (string, error)
}

// New builds a Manager for the given config, login username, and
// credential store. The username seeds a stable opaque user handle.
func New(cfg Config, username string, store *CredentialStore) *Manager {
	name := cfg.RPName
	if name == "" {
		name = "harb"
	}
	// A stable, opaque per-user handle. Single-user, so any stable value
	// works; deriving it from the username keeps it deterministic without
	// storing extra state.
	sum := sha256.Sum256([]byte("harb-webauthn-user:" + username))
	return &Manager{
		rp:           pinopass.RelyingParty{ID: cfg.RPID, Origin: cfg.Origin},
		rpName:       name,
		store:        store,
		userID:       sum[:16],
		userName:     username,
		pending:      map[string]pending{},
		now:          time.Now,
		newChallenge: pinopass.NewChallenge,
		newHandle:    newHandle,
	}
}

// Store exposes the underlying credential store (for listing/removal by
// the settings UI).
func (m *Manager) Store() *CredentialStore { return m.store }

// b64 is base64url without padding — the encoding WebAuthn uses for
// binary fields in JSON.
var b64 = base64.RawURLEncoding

func newHandle() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return b64.EncodeToString(b), nil
}

// stash records a pending challenge, sweeps expired ones, and returns a
// fresh handle to bind it to (delivered to the browser as a cookie).
func (m *Manager) stash(kind ceremony, challenge []byte) (string, error) {
	h, err := m.newHandle()
	if err != nil {
		return "", err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sweepLocked()
	m.pending[h] = pending{challenge: challenge, kind: kind, created: m.now()}
	return h, nil
}

// redeem consumes the challenge bound to handle for the given ceremony.
// It is single-use: the entry is deleted whether or not it validates.
func (m *Manager) redeem(handle string, kind ceremony) ([]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sweepLocked()
	p, ok := m.pending[handle]
	if !ok {
		return nil, errors.New("passkey: no pending challenge (expired or already used)")
	}
	delete(m.pending, handle)
	if p.kind != kind {
		return nil, errors.New("passkey: challenge ceremony mismatch")
	}
	return p.challenge, nil
}

// sweepLocked drops expired pending challenges. Caller holds m.mu.
func (m *Manager) sweepLocked() {
	now := m.now()
	for h, p := range m.pending {
		if now.Sub(p.created) >= challengeTTL {
			delete(m.pending, h)
		}
	}
}

// --- registration ---

// BeginRegistration mints a creation challenge and returns the handle to
// bind it to plus the PublicKeyCredentialCreationOptions JSON the
// browser passes to navigator.credentials.create({publicKey: ...}).
func (m *Manager) BeginRegistration() (handle string, optionsJSON []byte, err error) {
	ch, err := m.newChallenge()
	if err != nil {
		return "", nil, err
	}
	handle, err = m.stash(ceremonyRegister, ch)
	if err != nil {
		return "", nil, err
	}
	exclude := make([]map[string]any, 0, m.store.Len())
	for _, c := range m.store.List() {
		exclude = append(exclude, map[string]any{
			"type": "public-key",
			"id":   b64.EncodeToString(c.ID),
		})
	}
	opts := map[string]any{
		"challenge": b64.EncodeToString(ch),
		"rp":        map[string]any{"id": m.rp.ID, "name": m.rpName},
		"user": map[string]any{
			"id":          b64.EncodeToString(m.userID),
			"name":        m.userName,
			"displayName": m.userName,
		},
		"pubKeyCredParams": []map[string]any{
			{"type": "public-key", "alg": -7}, // ES256
		},
		"timeout":            300000,
		"attestation":        "none",
		"excludeCredentials": exclude,
		"authenticatorSelection": map[string]any{
			"userVerification": "required",
			"residentKey":      "preferred",
		},
	}
	optionsJSON, err = json.Marshal(map[string]any{"publicKey": opts})
	if err != nil {
		return "", nil, err
	}
	return handle, optionsJSON, nil
}

// registrationResponse is the browser's attestation response, with binary
// fields base64url-encoded by passkey.js.
type registrationResponse struct {
	RawID    string `json:"rawId"`
	Response struct {
		ClientDataJSON    string `json:"clientDataJSON"`
		AttestationObject string `json:"attestationObject"`
	} `json:"response"`
}

// FinishRegistration verifies a create() response against the challenge
// bound to handle and, on success, persists the new credential under
// label. The returned Credential is the stored value.
func (m *Manager) FinishRegistration(handle string, body []byte, label string) (Credential, error) {
	challenge, err := m.redeem(handle, ceremonyRegister)
	if err != nil {
		return Credential{}, err
	}
	var resp registrationResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return Credential{}, fmt.Errorf("passkey: decode response: %w", err)
	}
	clientData, err := b64.DecodeString(resp.Response.ClientDataJSON)
	if err != nil {
		return Credential{}, fmt.Errorf("passkey: clientDataJSON: %w", err)
	}
	att, err := b64.DecodeString(resp.Response.AttestationObject)
	if err != nil {
		return Credential{}, fmt.Errorf("passkey: attestationObject: %w", err)
	}
	cred, err := m.rp.VerifyRegistration(challenge, clientData, att)
	if err != nil {
		return Credential{}, err
	}
	if label == "" {
		label = "passkey"
	}
	stored := Credential{Credential: cred, Label: label, AddedAt: m.now().UTC()}
	if err := m.store.Add(stored); err != nil {
		return Credential{}, err
	}
	return stored, nil
}

// --- login ---

// BeginLogin mints an assertion challenge and returns the handle plus the
// PublicKeyCredentialRequestOptions JSON for navigator.credentials.get.
// It is callable without a session (it is the login path); it lists the
// registered credential IDs in allowCredentials, which are not secrets.
func (m *Manager) BeginLogin() (handle string, optionsJSON []byte, err error) {
	ch, err := m.newChallenge()
	if err != nil {
		return "", nil, err
	}
	handle, err = m.stash(ceremonyLogin, ch)
	if err != nil {
		return "", nil, err
	}
	allow := make([]map[string]any, 0, m.store.Len())
	for _, c := range m.store.List() {
		allow = append(allow, map[string]any{
			"type": "public-key",
			"id":   b64.EncodeToString(c.ID),
		})
	}
	opts := map[string]any{
		"challenge":        b64.EncodeToString(ch),
		"rpId":             m.rp.ID,
		"timeout":          300000,
		"userVerification": "required",
		"allowCredentials": allow,
	}
	optionsJSON, err = json.Marshal(map[string]any{"publicKey": opts})
	if err != nil {
		return "", nil, err
	}
	return handle, optionsJSON, nil
}

// assertionResponse is the browser's get() response.
type assertionResponse struct {
	RawID    string `json:"rawId"`
	Response struct {
		ClientDataJSON    string `json:"clientDataJSON"`
		AuthenticatorData string `json:"authenticatorData"`
		Signature         string `json:"signature"`
	} `json:"response"`
}

// FinishLogin verifies a get() response against the challenge bound to
// handle. On success it updates the credential's signature counter and
// returns the label of the credential that authenticated. A failure
// (unknown credential, bad signature, counter regression) is an error.
func (m *Manager) FinishLogin(handle string, body []byte) (string, error) {
	challenge, err := m.redeem(handle, ceremonyLogin)
	if err != nil {
		return "", err
	}
	var resp assertionResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return "", fmt.Errorf("passkey: decode response: %w", err)
	}
	rawID, err := b64.DecodeString(resp.RawID)
	if err != nil {
		return "", fmt.Errorf("passkey: rawId: %w", err)
	}
	cred, ok := m.store.GetByID(rawID)
	if !ok {
		return "", errors.New("passkey: unknown credential")
	}
	clientData, err := b64.DecodeString(resp.Response.ClientDataJSON)
	if err != nil {
		return "", fmt.Errorf("passkey: clientDataJSON: %w", err)
	}
	authData, err := b64.DecodeString(resp.Response.AuthenticatorData)
	if err != nil {
		return "", fmt.Errorf("passkey: authenticatorData: %w", err)
	}
	sig, err := b64.DecodeString(resp.Response.Signature)
	if err != nil {
		return "", fmt.Errorf("passkey: signature: %w", err)
	}
	newCount, err := m.rp.VerifyAssertion(challenge, cred.Credential, clientData, authData, sig)
	if err != nil {
		return "", err
	}
	if err := m.store.UpdateSignCount(rawID, newCount); err != nil {
		return "", err
	}
	return cred.Label, nil
}
