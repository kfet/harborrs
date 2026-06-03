package passkey

import (
	"crypto/ecdsa"
	"encoding/base64"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func newManager(t *testing.T) *Manager {
	t.Helper()
	p := filepath.Join(t.TempDir(), "credentials.json")
	cs, err := OpenCredentialStore(p)
	if err != nil {
		t.Fatal(err)
	}
	return New(testConfig(), "admin", cs)
}

// optChallenge extracts and decodes the challenge from a begin-options
// JSON blob, asserting the publicKey envelope shape along the way.
func optChallenge(t *testing.T, optionsJSON []byte) []byte {
	t.Helper()
	var env struct {
		PublicKey struct {
			Challenge string `json:"challenge"`
		} `json:"publicKey"`
	}
	if err := json.Unmarshal(optionsJSON, &env); err != nil {
		t.Fatalf("options json: %v", err)
	}
	ch, err := base64.RawURLEncoding.DecodeString(env.PublicKey.Challenge)
	if err != nil {
		t.Fatalf("challenge b64: %v", err)
	}
	return ch
}

// register drives a full successful registration and returns the cred ID.
func register(t *testing.T, m *Manager, key *ecdsa.PrivateKey, credID []byte, label string) {
	t.Helper()
	handle, opts, err := m.BeginRegistration()
	if err != nil {
		t.Fatalf("begin reg: %v", err)
	}
	ch := optChallenge(t, opts)
	cose := coseKeyES256(&key.PublicKey)
	ad := buildAuthData(testRPID, flagUP|flagUV, 0, credID, cose)
	att := attestationNone(ad)
	cd := clientDataJSON(t, "webauthn.create", testOrigin, ch)
	body := regResponseJSON(t, credID, cd, att)
	if _, err := m.FinishRegistration(handle, body, label); err != nil {
		t.Fatalf("finish reg: %v", err)
	}
}

func TestEnabled(t *testing.T) {
	if !testConfig().Enabled() {
		t.Fatal("want enabled")
	}
	if (Config{RPID: "x"}).Enabled() {
		t.Fatal("origin missing should be disabled")
	}
	if (Config{Origin: "x"}).Enabled() {
		t.Fatal("rpid missing should be disabled")
	}
}

func TestNewDefaultName(t *testing.T) {
	m := New(Config{RPID: "a", Origin: "b"}, "admin", nil)
	if m.rpName != "harb" {
		t.Fatalf("default name = %q", m.rpName)
	}
	m2 := New(testConfig(), "admin", nil)
	if m2.rpName != "Harbour RSS" {
		t.Fatalf("name = %q", m2.rpName)
	}
}

func TestRegisterThenLogin(t *testing.T) {
	m := newManager(t)
	key := newKey(t)
	credID := []byte("cred-id-1")
	register(t, m, key, credID, "laptop")

	if m.Store().Len() != 1 {
		t.Fatalf("want 1 cred, got %d", m.Store().Len())
	}

	// Login with a higher sign counter than stored (0) for clone-check.
	handle, opts, err := m.BeginLogin()
	if err != nil {
		t.Fatalf("begin login: %v", err)
	}
	ch := optChallenge(t, opts)
	ad := buildAuthData(testRPID, flagUP|flagUV, 5, nil, nil)
	cd := clientDataJSON(t, "webauthn.get", testOrigin, ch)
	sig := signAssertion(t, key, ad, cd)
	body := asrResponseJSON(t, credID, cd, ad, sig)
	label, err := m.FinishLogin(handle, body)
	if err != nil {
		t.Fatalf("finish login: %v", err)
	}
	if label != "laptop" {
		t.Fatalf("label = %q", label)
	}
	// Sign counter persisted.
	c, _ := m.Store().GetByID(credID)
	if c.SignCount != 5 {
		t.Fatalf("sign count = %d, want 5", c.SignCount)
	}
}

func TestBeginRegistrationExcludesExisting(t *testing.T) {
	m := newManager(t)
	register(t, m, newKey(t), []byte("c1"), "a")
	_, opts, err := m.BeginRegistration()
	if err != nil {
		t.Fatal(err)
	}
	var env struct {
		PublicKey struct {
			ExcludeCredentials []map[string]any `json:"excludeCredentials"`
			User               struct {
				ID string `json:"id"`
			} `json:"user"`
		} `json:"publicKey"`
	}
	if err := json.Unmarshal(opts, &env); err != nil {
		t.Fatal(err)
	}
	if len(env.PublicKey.ExcludeCredentials) != 1 {
		t.Fatalf("want 1 excluded, got %d", len(env.PublicKey.ExcludeCredentials))
	}
	if env.PublicKey.User.ID == "" {
		t.Fatal("user id empty")
	}
}

func TestBeginLoginListsAllowCredentials(t *testing.T) {
	m := newManager(t)
	register(t, m, newKey(t), []byte("c1"), "a")
	_, opts, err := m.BeginLogin()
	if err != nil {
		t.Fatal(err)
	}
	var env struct {
		PublicKey struct {
			AllowCredentials []map[string]any `json:"allowCredentials"`
			RPID             string           `json:"rpId"`
		} `json:"publicKey"`
	}
	_ = json.Unmarshal(opts, &env)
	if len(env.PublicKey.AllowCredentials) != 1 {
		t.Fatalf("want 1 allowed, got %d", len(env.PublicKey.AllowCredentials))
	}
	if env.PublicKey.RPID != testRPID {
		t.Fatalf("rpId = %q", env.PublicKey.RPID)
	}
}

func TestChallengeErrors(t *testing.T) {
	boom := errors.New("boom")
	m := newManager(t)
	m.newChallenge = func() ([]byte, error) { return nil, boom }
	if _, _, err := m.BeginRegistration(); !errors.Is(err, boom) {
		t.Fatalf("reg challenge err = %v", err)
	}
	if _, _, err := m.BeginLogin(); !errors.Is(err, boom) {
		t.Fatalf("login challenge err = %v", err)
	}
}

func TestHandleErrors(t *testing.T) {
	boom := errors.New("handle-boom")
	m := newManager(t)
	m.newHandle = func() (string, error) { return "", boom }
	if _, _, err := m.BeginRegistration(); !errors.Is(err, boom) {
		t.Fatalf("reg handle err = %v", err)
	}
	if _, _, err := m.BeginLogin(); !errors.Is(err, boom) {
		t.Fatalf("login handle err = %v", err)
	}
}

func TestFinishRegistrationBadHandle(t *testing.T) {
	m := newManager(t)
	if _, err := m.FinishRegistration("nope", []byte("{}"), "x"); err == nil {
		t.Fatal("want redeem error")
	}
}

func TestFinishRegistrationBadJSON(t *testing.T) {
	m := newManager(t)
	handle, _, _ := m.BeginRegistration()
	if _, err := m.FinishRegistration(handle, []byte("{not json"), "x"); err == nil {
		t.Fatal("want json error")
	}
}

func TestFinishRegistrationBadBase64(t *testing.T) {
	m := newManager(t)

	// Bad clientDataJSON base64.
	handle, _, _ := m.BeginRegistration()
	body, _ := json.Marshal(map[string]any{
		"rawId":    "AA",
		"response": map[string]string{"clientDataJSON": "!!!", "attestationObject": "AA"},
	})
	if _, err := m.FinishRegistration(handle, body, "x"); err == nil {
		t.Fatal("want clientData b64 error")
	}

	// Bad attestationObject base64.
	handle, _, _ = m.BeginRegistration()
	body, _ = json.Marshal(map[string]any{
		"rawId":    "AA",
		"response": map[string]string{"clientDataJSON": "AA", "attestationObject": "!!!"},
	})
	if _, err := m.FinishRegistration(handle, body, "x"); err == nil {
		t.Fatal("want attestation b64 error")
	}
}

func TestFinishRegistrationVerifyFails(t *testing.T) {
	m := newManager(t)
	key := newKey(t)
	credID := []byte("c1")
	handle, _, _ := m.BeginRegistration()
	// Use a WRONG challenge in clientData so verification fails.
	cose := coseKeyES256(&key.PublicKey)
	ad := buildAuthData(testRPID, flagUP|flagUV, 0, credID, cose)
	att := attestationNone(ad)
	cd := clientDataJSON(t, "webauthn.create", testOrigin, []byte("wrong-challenge"))
	body := regResponseJSON(t, credID, cd, att)
	if _, err := m.FinishRegistration(handle, body, "x"); err == nil {
		t.Fatal("want verification error")
	}
}

func TestFinishRegistrationDefaultLabelAndDuplicate(t *testing.T) {
	m := newManager(t)
	key := newKey(t)
	credID := []byte("c1")

	// First registration with empty label → defaults to "passkey".
	handle, opts, _ := m.BeginRegistration()
	ch := optChallenge(t, opts)
	cose := coseKeyES256(&key.PublicKey)
	ad := buildAuthData(testRPID, flagUP|flagUV, 0, credID, cose)
	att := attestationNone(ad)
	cd := clientDataJSON(t, "webauthn.create", testOrigin, ch)
	stored, err := m.FinishRegistration(handle, regResponseJSON(t, credID, cd, att), "")
	if err != nil {
		t.Fatalf("finish: %v", err)
	}
	if stored.Label != "passkey" {
		t.Fatalf("default label = %q", stored.Label)
	}

	// Second registration with the SAME credID → store.Add duplicate error.
	handle2, opts2, _ := m.BeginRegistration()
	ch2 := optChallenge(t, opts2)
	cd2 := clientDataJSON(t, "webauthn.create", testOrigin, ch2)
	if _, err := m.FinishRegistration(handle2, regResponseJSON(t, credID, cd2, att), "dup"); err == nil {
		t.Fatal("want duplicate store error")
	}
}

func TestRedeemCeremonyMismatch(t *testing.T) {
	m := newManager(t)
	// A login handle redeemed at the registration finish must fail.
	handle, _, _ := m.BeginLogin()
	if _, err := m.FinishRegistration(handle, []byte("{}"), "x"); err == nil {
		t.Fatal("want ceremony mismatch")
	}
}

func TestChallengeExpiry(t *testing.T) {
	m := newManager(t)
	clock := time.Unix(1000, 0)
	m.now = func() time.Time { return clock }
	handle, _, err := m.BeginRegistration()
	if err != nil {
		t.Fatal(err)
	}
	// Advance past the TTL; sweepLocked should drop it on redeem.
	clock = clock.Add(challengeTTL + time.Second)
	if _, err := m.FinishRegistration(handle, []byte("{}"), "x"); err == nil {
		t.Fatal("want expired-challenge error")
	}
}

func TestFinishLoginErrors(t *testing.T) {
	m := newManager(t)
	key := newKey(t)
	credID := []byte("c1")
	register(t, m, key, credID, "laptop")

	// bad handle
	if _, err := m.FinishLogin("nope", []byte("{}")); err == nil {
		t.Fatal("want redeem error")
	}

	// bad json
	h, _, _ := m.BeginLogin()
	if _, err := m.FinishLogin(h, []byte("{nope")); err == nil {
		t.Fatal("want json error")
	}

	// bad rawId base64
	h, _, _ = m.BeginLogin()
	body, _ := json.Marshal(map[string]any{"rawId": "!!!", "response": map[string]string{}})
	if _, err := m.FinishLogin(h, body); err == nil {
		t.Fatal("want rawId b64 error")
	}

	// unknown credential
	h, _, _ = m.BeginLogin()
	body, _ = json.Marshal(map[string]any{
		"rawId":    base64.RawURLEncoding.EncodeToString([]byte("unknown")),
		"response": map[string]string{"clientDataJSON": "AA", "authenticatorData": "AA", "signature": "AA"},
	})
	if _, err := m.FinishLogin(h, body); err == nil {
		t.Fatal("want unknown-credential error")
	}
}

func TestFinishLoginBadFieldBase64(t *testing.T) {
	m := newManager(t)
	key := newKey(t)
	credID := []byte("c1")
	register(t, m, key, credID, "laptop")
	rid := base64.RawURLEncoding.EncodeToString(credID)

	cases := []struct {
		name string
		resp map[string]string
	}{
		{"clientData", map[string]string{"clientDataJSON": "!!!", "authenticatorData": "AA", "signature": "AA"}},
		{"authData", map[string]string{"clientDataJSON": "AA", "authenticatorData": "!!!", "signature": "AA"}},
		{"signature", map[string]string{"clientDataJSON": "AA", "authenticatorData": "AA", "signature": "!!!"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h, _, _ := m.BeginLogin()
			body, _ := json.Marshal(map[string]any{"rawId": rid, "response": c.resp})
			if _, err := m.FinishLogin(h, body); err == nil {
				t.Fatalf("want %s b64 error", c.name)
			}
		})
	}
}

func TestFinishLoginVerifyFails(t *testing.T) {
	m := newManager(t)
	key := newKey(t)
	credID := []byte("c1")
	register(t, m, key, credID, "laptop")

	h, opts, _ := m.BeginLogin()
	ch := optChallenge(t, opts)
	// Sign with a DIFFERENT key so the signature doesn't verify.
	wrong := newKey(t)
	ad := buildAuthData(testRPID, flagUP|flagUV, 1, nil, nil)
	cd := clientDataJSON(t, "webauthn.get", testOrigin, ch)
	sig := signAssertion(t, wrong, ad, cd)
	body := asrResponseJSON(t, credID, cd, ad, sig)
	if _, err := m.FinishLogin(h, body); err == nil {
		t.Fatal("want verification error")
	}
}

func TestFinishLoginUpdateSignCountError(t *testing.T) {
	m := newManager(t)
	key := newKey(t)
	credID := []byte("c1")
	register(t, m, key, credID, "laptop")

	// Point the store at a bad path so UpdateSignCount's persist fails
	// after a valid assertion.
	bad := badPathStore(t)
	m.store.Path = bad.Path

	h, opts, _ := m.BeginLogin()
	ch := optChallenge(t, opts)
	ad := buildAuthData(testRPID, flagUP|flagUV, 9, nil, nil)
	cd := clientDataJSON(t, "webauthn.get", testOrigin, ch)
	sig := signAssertion(t, key, ad, cd)
	body := asrResponseJSON(t, credID, cd, ad, sig)
	if _, err := m.FinishLogin(h, body); err == nil {
		t.Fatal("want update-sign-count persist error")
	}
}
