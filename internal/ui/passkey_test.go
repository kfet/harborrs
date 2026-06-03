package ui

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kfet/harb/internal/auth"
	"github.com/kfet/harb/internal/passkey"
	"github.com/kfet/harb/internal/store"
)

// --- compact WebAuthn fixture builders (test-only) ------------------------

const (
	pkRPID   = "rss.example.com"
	pkOrigin = "https://rss.example.com"
)

func cborHead(major byte, n uint64) []byte {
	mb := major << 5
	switch {
	case n < 24:
		return []byte{mb | byte(n)}
	case n < 1<<8:
		return []byte{mb | 24, byte(n)}
	case n < 1<<16:
		b := []byte{mb | 25, 0, 0}
		binary.BigEndian.PutUint16(b[1:], uint16(n))
		return b
	default:
		b := []byte{mb | 26, 0, 0, 0, 0}
		binary.BigEndian.PutUint32(b[1:], uint32(n))
		return b
	}
}
func cUint(n uint64) []byte  { return cborHead(0, n) }
func cNeg(v int64) []byte    { return cborHead(1, uint64(-1-v)) }
func cBytes(b []byte) []byte { return append(cborHead(2, uint64(len(b))), b...) }
func cText(s string) []byte  { return append(cborHead(3, uint64(len(s))), []byte(s)...) }
func cInt(v int64) []byte {
	if v < 0 {
		return cNeg(v)
	}
	return cUint(uint64(v))
}
func cMap(pairs [][2][]byte) []byte {
	out := cborHead(5, uint64(len(pairs)))
	for _, kv := range pairs {
		out = append(out, kv[0]...)
		out = append(out, kv[1]...)
	}
	return out
}

func coseKey(pub *ecdsa.PublicKey) []byte {
	x := pub.X.FillBytes(make([]byte, 32))
	y := pub.Y.FillBytes(make([]byte, 32))
	return cMap([][2][]byte{
		{cInt(1), cInt(2)},  // kty EC2
		{cInt(3), cInt(-7)}, // alg ES256
		{cInt(-1), cInt(1)}, // crv P-256
		{cInt(-2), cBytes(x)},
		{cInt(-3), cBytes(y)},
	})
}

func pkAuthData(rpID string, signCount uint32, credID, cose []byte) []byte {
	var flags byte = 0x01 | 0x04 // UP|UV
	if cose != nil {
		flags |= 0x40 // AT
	}
	h := sha256.Sum256([]byte(rpID))
	out := append([]byte{}, h[:]...)
	out = append(out, flags)
	var sc [4]byte
	binary.BigEndian.PutUint32(sc[:], signCount)
	out = append(out, sc[:]...)
	if cose != nil {
		out = append(out, make([]byte, 16)...)
		var cl [2]byte
		binary.BigEndian.PutUint16(cl[:], uint16(len(credID)))
		out = append(out, cl[:]...)
		out = append(out, credID...)
		out = append(out, cose...)
	}
	return out
}

func attNone(authData []byte) []byte {
	return cMap([][2][]byte{
		{cText("fmt"), cText("none")},
		{cText("attStmt"), cMap(nil)},
		{cText("authData"), cBytes(authData)},
	})
}

func cdJSON(t *testing.T, typ string, challenge []byte) []byte {
	t.Helper()
	b, _ := json.Marshal(map[string]string{
		"type":      typ,
		"challenge": base64.RawURLEncoding.EncodeToString(challenge),
		"origin":    pkOrigin,
	})
	return b
}

// --- harness --------------------------------------------------------------

func pkFixture(t *testing.T) (*Server, *http.ServeMux, *passkey.Manager, string) {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	as, _ := auth.OpenStore(filepath.Join(dir, "tokens.json"), auth.Config{Username: "u", PasswordHash: testPwHash})
	srv, err := New(st, as, &memOPML{}, "dark", filepath.Join(dir, "cfg"))
	if err != nil {
		t.Fatal(err)
	}
	srv.ConfigPath = filepath.Join(dir, "config.json")
	cs, _ := passkey.OpenCredentialStore(filepath.Join(dir, "credentials.json"))
	mgr := passkey.New(passkey.Config{RPID: pkRPID, Origin: pkOrigin}, "u", cs)
	srv.Passkey = mgr
	mux := http.NewServeMux()
	srv.Routes(mux)
	tok, _ := as.IssueSession("u", "p")
	return srv, mux, mgr, tok
}

// beginChallenge POSTs to a begin endpoint and returns (handle, challenge).
func beginChallenge(t *testing.T, mux *http.ServeMux, path, tok string) (string, []byte) {
	t.Helper()
	w := do(mux, req("POST", path, tok, nil))
	if w.Code != 200 {
		t.Fatalf("%s: code %d body %s", path, w.Code, w.Body.String())
	}
	var handle string
	for _, c := range w.Result().Cookies() {
		if c.Name == challengeCookie {
			handle = c.Value
		}
	}
	if handle == "" {
		t.Fatalf("%s: no challenge cookie", path)
	}
	var env struct {
		PublicKey struct {
			Challenge string `json:"challenge"`
		} `json:"publicKey"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
		t.Fatalf("%s: json %v", path, err)
	}
	ch, _ := base64.RawURLEncoding.DecodeString(env.PublicKey.Challenge)
	return handle, ch
}

// postFinish sends a finish request with the handle cookie + JSON body.
func postFinish(mux *http.ServeMux, path, handle, tok string, body []byte) *httptest.ResponseRecorder {
	r := httptest.NewRequest("POST", path, strings.NewReader(string(body)))
	r.Header.Set("Content-Type", "application/json")
	r.AddCookie(&http.Cookie{Name: challengeCookie, Value: handle})
	if tok != "" {
		r.AddCookie(&http.Cookie{Name: auth.CookieName, Value: tok})
	}
	return do(mux, r)
}

func newPKKey(t *testing.T) *ecdsa.PrivateKey {
	t.Helper()
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return k
}

// registerPasskey drives the full register flow over HTTP.
func registerPasskey(t *testing.T, mux *http.ServeMux, tok string, key *ecdsa.PrivateKey, credID []byte) {
	t.Helper()
	handle, ch := beginChallenge(t, mux, "/ui/webauthn/register/begin", tok)
	ad := pkAuthData(pkRPID, 0, credID, coseKey(&key.PublicKey))
	att := attNone(ad)
	cd := cdJSON(t, "webauthn.create", ch)
	body, _ := json.Marshal(map[string]any{
		"rawId": base64.RawURLEncoding.EncodeToString(credID),
		"response": map[string]string{
			"clientDataJSON":    base64.RawURLEncoding.EncodeToString(cd),
			"attestationObject": base64.RawURLEncoding.EncodeToString(att),
		},
	})
	w := postFinish(mux, "/ui/webauthn/register/finish?label=laptop", handle, tok, body)
	if w.Code != 200 {
		t.Fatalf("register finish: %d %s", w.Code, w.Body.String())
	}
}

func TestPasskeyRoutesDisabledWhenNil(t *testing.T) {
	srv, mux, _, _ := pkFixture(t)
	srv.Passkey = nil
	// Re-mount routes without passkey.
	mux2 := http.NewServeMux()
	srv.Routes(mux2)
	w := do(mux2, req("POST", "/ui/webauthn/login/begin", "", nil))
	// With passkey disabled the route isn't registered; the request
	// falls through to the /ui/ session guard, which redirects to login.
	if w.Code != http.StatusSeeOther {
		t.Fatalf("want 303 fallthrough when disabled, got %d", w.Code)
	}
	_ = mux
}

func TestPasskeyRegisterAndLogin(t *testing.T) {
	_, mux, mgr, tok := pkFixture(t)
	key := newPKKey(t)
	credID := []byte("cred-1")
	registerPasskey(t, mux, tok, key, credID)
	if mgr.Store().Len() != 1 {
		t.Fatalf("want 1 cred, got %d", mgr.Store().Len())
	}

	// Login (no session cookie).
	handle, ch := beginChallenge(t, mux, "/ui/webauthn/login/begin", "")
	ad := pkAuthData(pkRPID, 7, nil, nil)
	cd := cdJSON(t, "webauthn.get", ch)
	cdHash := sha256.Sum256(cd)
	digest := sha256.Sum256(append(append([]byte{}, ad...), cdHash[:]...))
	sig, _ := ecdsa.SignASN1(rand.Reader, key, digest[:])
	body, _ := json.Marshal(map[string]any{
		"rawId": base64.RawURLEncoding.EncodeToString(credID),
		"response": map[string]string{
			"clientDataJSON":    base64.RawURLEncoding.EncodeToString(cd),
			"authenticatorData": base64.RawURLEncoding.EncodeToString(ad),
			"signature":         base64.RawURLEncoding.EncodeToString(sig),
		},
	})
	w := postFinish(mux, "/ui/webauthn/login/finish", handle, "", body)
	if w.Code != 200 {
		t.Fatalf("login finish: %d %s", w.Code, w.Body.String())
	}
	// A session cookie must have been set.
	var sess string
	for _, c := range w.Result().Cookies() {
		if c.Name == auth.CookieName {
			sess = c.Value
		}
	}
	if sess == "" {
		t.Fatal("login finish did not set a session cookie")
	}
}

func TestPasskeyFinishFailures(t *testing.T) {
	_, mux, _, tok := pkFixture(t)

	// register finish with bad handle → 400
	w := postFinish(mux, "/ui/webauthn/register/finish", "bad", tok, []byte("{}"))
	if w.Code != 400 {
		t.Fatalf("reg bad handle: %d", w.Code)
	}
	// login finish with bad handle → 401
	w = postFinish(mux, "/ui/webauthn/login/finish", "bad", "", []byte("{}"))
	if w.Code != 401 {
		t.Fatalf("login bad handle: %d", w.Code)
	}
}

func TestPasskeyMethodNotAllowed(t *testing.T) {
	_, mux, _, tok := pkFixture(t)
	for _, p := range []string{
		"/ui/webauthn/register/begin",
		"/ui/webauthn/register/finish",
		"/ui/webauthn/login/begin",
		"/ui/webauthn/login/finish",
		"/ui/settings/passkey/remove",
	} {
		w := do(mux, req("GET", p, tok, nil))
		if w.Code != http.StatusMethodNotAllowed {
			t.Fatalf("%s GET: want 405, got %d", p, w.Code)
		}
	}
}

func TestPasskeyRemove(t *testing.T) {
	_, mux, mgr, tok := pkFixture(t)
	key := newPKKey(t)
	credID := []byte("cred-x")
	registerPasskey(t, mux, tok, key, credID)

	// Bad form (unparseable) → 400 via ParseForm? Use a bad id instead.
	w := do(mux, req("POST", "/ui/settings/passkey/remove", tok, url.Values{"id": {"!!!notb64"}}))
	if w.Code != 400 {
		t.Fatalf("bad id: %d", w.Code)
	}
	// Good remove → 303 and credential gone.
	idB64 := base64.StdEncoding.EncodeToString(credID)
	w = do(mux, req("POST", "/ui/settings/passkey/remove", tok, url.Values{"id": {idB64}}))
	if w.Code != 303 {
		t.Fatalf("remove: %d", w.Code)
	}
	if mgr.Store().Len() != 0 {
		t.Fatalf("cred not removed: %d", mgr.Store().Len())
	}
}

func TestSettingsShowsPasskeys(t *testing.T) {
	_, mux, _, tok := pkFixture(t)
	key := newPKKey(t)
	registerPasskey(t, mux, tok, key, []byte("cred-z"))
	w := do(mux, req("GET", "/ui/settings", tok, nil))
	if w.Code != 200 {
		t.Fatalf("settings: %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "passkeys") || !strings.Contains(w.Body.String(), "laptop") {
		t.Fatalf("settings missing passkey section: %s", w.Body.String())
	}
}

func TestLoginPageShowsPasskeyButton(t *testing.T) {
	_, mux, _, _ := pkFixture(t)
	w := do(mux, req("GET", "/ui/login", "", nil))
	if !strings.Contains(w.Body.String(), "passkey-login-btn") {
		t.Fatalf("login page missing passkey button: %s", w.Body.String())
	}
}

// errBody is a request body whose Read always fails.
type errBody struct{}

func (errBody) Read([]byte) (int, error) { return 0, errReadFail }

var errReadFail = errReadFailT("read fail")

type errReadFailT string

func (e errReadFailT) Error() string { return string(e) }

func TestPasskeyFinishNoCookie(t *testing.T) {
	_, mux, _, tok := pkFixture(t)
	// register finish with no challenge cookie → handle "" → 400.
	r := httptest.NewRequest("POST", "/ui/webauthn/register/finish", strings.NewReader("{}"))
	r.Header.Set("Content-Type", "application/json")
	r.AddCookie(&http.Cookie{Name: auth.CookieName, Value: tok})
	w := do(mux, r)
	if w.Code != 400 {
		t.Fatalf("no-cookie finish: %d", w.Code)
	}
}

func TestPasskeyFinishBodyReadError(t *testing.T) {
	_, mux, _, tok := pkFixture(t)

	// register finish body read error → 400
	r := httptest.NewRequest("POST", "/ui/webauthn/register/finish", errBody{})
	r.Header.Set("Content-Type", "application/json")
	r.AddCookie(&http.Cookie{Name: challengeCookie, Value: "x"})
	r.AddCookie(&http.Cookie{Name: auth.CookieName, Value: tok})
	if w := do(mux, r); w.Code != 400 {
		t.Fatalf("reg body err: %d", w.Code)
	}

	// login finish body read error → 400
	r = httptest.NewRequest("POST", "/ui/webauthn/login/finish", errBody{})
	r.Header.Set("Content-Type", "application/json")
	r.AddCookie(&http.Cookie{Name: challengeCookie, Value: "x"})
	if w := do(mux, r); w.Code != 400 {
		t.Fatalf("login body err: %d", w.Code)
	}
}

func TestPasskeyRemoveParseFormError(t *testing.T) {
	_, mux, _, tok := pkFixture(t)
	r := httptest.NewRequest("POST", "/ui/settings/passkey/remove", strings.NewReader("%ZZ"))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.AddCookie(&http.Cookie{Name: auth.CookieName, Value: tok})
	if w := do(mux, r); w.Code != 400 {
		t.Fatalf("parseform err: %d", w.Code)
	}
}

func TestPasskeyLoginNewSessionError(t *testing.T) {
	srv, mux, _, tok := pkFixture(t)
	key := newPKKey(t)
	credID := []byte("cred-ns")
	registerPasskey(t, mux, tok, key, credID)

	// Break the auth token store so NewSession's persist fails after a
	// valid assertion.
	f := filepath.Join(t.TempDir(), "afile")
	if err := writeFileForTest(f); err != nil {
		t.Fatal(err)
	}
	srv.Auth.Path = filepath.Join(f, "sub", "tokens.json")

	handle, ch := beginChallenge(t, mux, "/ui/webauthn/login/begin", "")
	ad := pkAuthData(pkRPID, 11, nil, nil)
	cd := cdJSON(t, "webauthn.get", ch)
	cdHash := sha256.Sum256(cd)
	digest := sha256.Sum256(append(append([]byte{}, ad...), cdHash[:]...))
	sig, _ := ecdsa.SignASN1(rand.Reader, key, digest[:])
	body, _ := json.Marshal(map[string]any{
		"rawId": base64.RawURLEncoding.EncodeToString(credID),
		"response": map[string]string{
			"clientDataJSON":    base64.RawURLEncoding.EncodeToString(cd),
			"authenticatorData": base64.RawURLEncoding.EncodeToString(ad),
			"signature":         base64.RawURLEncoding.EncodeToString(sig),
		},
	})
	w := postFinish(mux, "/ui/webauthn/login/finish", handle, "", body)
	if w.Code != 500 {
		t.Fatalf("new-session err: want 500, got %d %s", w.Code, w.Body.String())
	}
}

func writeFileForTest(p string) error { return os.WriteFile(p, []byte("x"), 0o600) }
