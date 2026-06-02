// Package auth provides the single-user credential, token store, and cookie
// session machinery shared by the Reader API and the htmx web UI.
//
// Two front doors share one credential:
//
//   - Reader API (Google ClientLogin): clients POST username+password,
//     receive an opaque Auth=<token>; subsequent requests send
//     `Authorization: GoogleLogin auth=<token>` (Reeder also sends
//     T=<token> as a write-token after a `/reader/api/0/token` call).
//   - Web UI: a standard cookie session set on POST /ui/login.
//
// Tokens are random opaque strings (32 bytes, hex-encoded). They are
// persisted to tokens.json so they survive restarts.
package auth

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/kfet/harb/internal/atomic"
)

// ErrInvalidCredentials is returned on a bad username/password pair.
var ErrInvalidCredentials = errors.New("invalid credentials")

// Config is the single-user credential configuration. The on-disk format
// for PasswordHash is "sha256$<hex-salt>$<hex-hash>" where hash is computed
// over (salt || password) repeatedly (HashIterations rounds). This is not
// bcrypt-grade; for v0.1 single-user with a strong password it is
// acceptable, and it keeps the dep surface stdlib-only.
type Config struct {
	Username     string `json:"username"`
	PasswordHash string `json:"password_hash"`
}

// HashIterations is the number of SHA-256 rounds applied to the salted
// password. Tuned to ~50ms on a recent laptop.
const HashIterations = 100_000

// HashPassword returns a salted-and-stretched SHA-256 hash for password.
func HashPassword(plain string) (string, error) {
	salt := make([]byte, 16)
	if _, err := randRead(salt); err != nil {
		return "", err
	}
	h := stretch(salt, []byte(plain))
	return fmt.Sprintf("sha256$%s$%s", hex.EncodeToString(salt), hex.EncodeToString(h)), nil
}

// Verify checks a plaintext password against this config. Returns
// ErrInvalidCredentials on mismatch.
func (c Config) Verify(username, password string) error {
	if subtle.ConstantTimeCompare([]byte(username), []byte(c.Username)) != 1 {
		return ErrInvalidCredentials
	}
	parts := strings.SplitN(c.PasswordHash, "$", 3)
	if len(parts) != 3 || parts[0] != "sha256" {
		return ErrInvalidCredentials
	}
	salt, err := hex.DecodeString(parts[1])
	if err != nil {
		return ErrInvalidCredentials
	}
	want, err := hex.DecodeString(parts[2])
	if err != nil {
		return ErrInvalidCredentials
	}
	got := stretch(salt, []byte(password))
	if subtle.ConstantTimeCompare(got, want) != 1 {
		return ErrInvalidCredentials
	}
	return nil
}

func stretch(salt, password []byte) []byte {
	h := sha256.New()
	h.Write(salt)
	h.Write(password)
	out := h.Sum(nil)
	for i := 1; i < HashIterations; i++ {
		h.Reset()
		h.Write(salt)
		h.Write(out)
		out = h.Sum(nil)
	}
	return out
}

// Store holds API tokens and cookie sessions.
type Store struct {
	Path string // tokens.json
	Cfg  Config

	mu       sync.RWMutex
	api      map[string]time.Time // api-token -> issued-at
	sessions map[string]time.Time // session-cookie -> issued-at

	now func() time.Time
}

// OpenStore loads (and lazily creates) a token store at path.
func OpenStore(path string, cfg Config) (*Store, error) {
	s := &Store{
		Path:     path,
		Cfg:      cfg,
		api:      map[string]time.Time{},
		sessions: map[string]time.Time{},
		now:      time.Now,
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return s, nil
		}
		return nil, err
	}
	var disk struct {
		API      map[string]time.Time `json:"api"`
		Sessions map[string]time.Time `json:"sessions"`
	}
	if err := json.Unmarshal(data, &disk); err != nil {
		return nil, err
	}
	if disk.API != nil {
		s.api = disk.API
	}
	if disk.Sessions != nil {
		s.sessions = disk.Sessions
	}
	// Evict any tokens/sessions that were already past their lifetime
	// when we loaded them, and rewrite the file if we dropped anything.
	// Without this the token store grows without bound: every
	// ClientLogin persists a fresh API token and nothing ever removed
	// the expired ones.
	if s.sweepLocked() > 0 {
		if err := s.persistLocked(); err != nil {
			return nil, err
		}
	}
	return s, nil
}

// sweepLocked deletes every API token and session whose age has reached
// TokenLifetime. Caller must hold s.mu. Returns the number of entries
// removed. Cheap (a single pass over two small maps) so it is safe to
// run on every issue as well as at open.
func (s *Store) sweepLocked() int {
	now := s.now()
	removed := 0
	for tok, issued := range s.api {
		if now.Sub(issued) >= TokenLifetime {
			delete(s.api, tok)
			removed++
		}
	}
	for tok, issued := range s.sessions {
		if now.Sub(issued) >= TokenLifetime {
			delete(s.sessions, tok)
			removed++
		}
	}
	return removed
}

// CookieName is the HTTP cookie name for the UI session.
const CookieName = "harb_session"

// TokenLifetime governs how long tokens are valid. v0.1: 30 days.
const TokenLifetime = 30 * 24 * time.Hour

// IssueAPIToken authenticates and returns a new opaque token. The token
// is also persisted to disk so it survives restarts.
func (s *Store) IssueAPIToken(username, password string) (string, error) {
	if err := s.Verify(username, password); err != nil {
		return "", err
	}
	tok, err := newToken()
	if err != nil {
		return "", err
	}
	s.mu.Lock()
	s.sweepLocked()
	s.api[tok] = s.now().UTC()
	err = s.persistLocked()
	s.mu.Unlock()
	if err != nil {
		return "", err
	}
	return tok, nil
}

// IssueSession authenticates and returns a new opaque session cookie value.
func (s *Store) IssueSession(username, password string) (string, error) {
	if err := s.Verify(username, password); err != nil {
		return "", err
	}
	tok, err := newToken()
	if err != nil {
		return "", err
	}
	s.mu.Lock()
	s.sweepLocked()
	s.sessions[tok] = s.now().UTC()
	err = s.persistLocked()
	s.mu.Unlock()
	if err != nil {
		return "", err
	}
	return tok, nil
}

// CheckAPIToken returns true if token is valid (exists + not expired).
func (s *Store) CheckAPIToken(tok string) bool {
	if tok == "" {
		return false
	}
	s.mu.RLock()
	issued, ok := s.api[tok]
	s.mu.RUnlock()
	if !ok {
		return false
	}
	return s.now().Sub(issued) < TokenLifetime
}

// CheckSession returns true if a session cookie value is valid.
func (s *Store) CheckSession(tok string) bool {
	if tok == "" {
		return false
	}
	s.mu.RLock()
	issued, ok := s.sessions[tok]
	s.mu.RUnlock()
	if !ok {
		return false
	}
	return s.now().Sub(issued) < TokenLifetime
}

// RevokeSession deletes a session token (logout).
func (s *Store) RevokeSession(tok string) error {
	s.mu.Lock()
	delete(s.sessions, tok)
	err := s.persistLocked()
	s.mu.Unlock()
	return err
}

// ExtractAPIToken pulls a Reader-API token from an http.Request:
// either `Authorization: GoogleLogin auth=<token>` or a `T=<token>` form value.
func ExtractAPIToken(r *http.Request) string {
	if h := r.Header.Get("Authorization"); h != "" {
		// Forms seen in the wild:
		//   "GoogleLogin auth=TOKEN"
		//   "GoogleLogin auth=\"TOKEN\""
		const prefix = "GoogleLogin "
		if strings.HasPrefix(h, prefix) {
			for _, part := range strings.Split(h[len(prefix):], ",") {
				kv := strings.SplitN(strings.TrimSpace(part), "=", 2)
				if len(kv) == 2 && kv[0] == "auth" {
					return strings.Trim(kv[1], `"`)
				}
			}
		}
	}
	if t := r.FormValue("T"); t != "" {
		return t
	}
	return ""
}

// SetSessionCookie writes a session cookie to w.
func SetSessionCookie(w http.ResponseWriter, tok string, secure bool) {
	http.SetCookie(w, &http.Cookie{
		Name:     CookieName,
		Value:    tok,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   secure,
		MaxAge:   int(TokenLifetime / time.Second),
	})
}

// ClearSessionCookie writes an expired session cookie to w.
func ClearSessionCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:   CookieName,
		Value:  "",
		Path:   "/",
		MaxAge: -1,
	})
}

// SessionFromRequest returns the session value from the cookie, or "".
func SessionFromRequest(r *http.Request) string {
	c, err := r.Cookie(CookieName)
	if err != nil {
		return ""
	}
	return c.Value
}

// --- internals ---

func (s *Store) persistLocked() error {
	type disk struct {
		API      map[string]time.Time `json:"api"`
		Sessions map[string]time.Time `json:"sessions"`
	}
	data, err := jsonMarshalIndent(disk{API: s.api, Sessions: s.sessions}, "", "  ")
	if err != nil {
		return err
	}
	if err := osMkdirAll(filepath.Dir(s.Path), 0o755); err != nil {
		return err
	}
	return atomic.WriteFileMode(s.Path, data, 0o600)
}

func newToken() (string, error) {
	b := make([]byte, 32)
	if _, err := randRead(b); err != nil {
		return "", fmt.Errorf("auth: read random: %w", err)
	}
	return hex.EncodeToString(b), nil
}

// Verify checks (username, password) against the current Config under
// the store's lock. Use this instead of s.Cfg.Verify directly so that
// concurrent calls to SetPasswordHash don't race the read.
func (s *Store) Verify(username, password string) error {
	s.mu.RLock()
	cfg := s.Cfg
	s.mu.RUnlock()
	return cfg.Verify(username, password)
}

// SetPasswordHash atomically replaces the stored password hash. Callers
// are responsible for also persisting the new hash to config.json — the
// auth store has no knowledge of where the Config lives on disk.
func (s *Store) SetPasswordHash(h string) {
	s.mu.Lock()
	s.Cfg.PasswordHash = h
	s.mu.Unlock()
}

// RevokeAllSessions drops every session cookie, forcing all browsers
// to re-authenticate. Used after a password change. API tokens are
// kept (clients re-authenticate with the new password lazily).
func (s *Store) RevokeAllSessions() error {
	s.mu.Lock()
	s.sessions = map[string]time.Time{}
	err := s.persistLocked()
	s.mu.Unlock()
	return err
}
