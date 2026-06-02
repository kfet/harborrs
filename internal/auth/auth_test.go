package auth

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func makeCfg(t *testing.T, user, pass string) Config {
	t.Helper()
	h, err := HashPassword(pass)
	if err != nil {
		t.Fatal(err)
	}
	return Config{Username: user, PasswordHash: h}
}

func TestVerifyAndIssue(t *testing.T) {
	dir := t.TempDir()
	cfg := makeCfg(t, "alice", "hunter2")
	s, err := OpenStore(filepath.Join(dir, "tokens.json"), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := cfg.Verify("alice", "hunter2"); err != nil {
		t.Fatal(err)
	}
	if err := cfg.Verify("alice", "nope"); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("want invalid, got %v", err)
	}
	if err := cfg.Verify("eve", "hunter2"); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("want invalid, got %v", err)
	}
	tok, err := s.IssueAPIToken("alice", "hunter2")
	if err != nil || tok == "" {
		t.Fatalf("issue: %v / %q", err, tok)
	}
	if !s.CheckAPIToken(tok) {
		t.Fatal("CheckAPIToken false")
	}
	if s.CheckAPIToken("bogus") || s.CheckAPIToken("") {
		t.Fatal("CheckAPIToken should be false")
	}
	// Sessions
	sess, err := s.IssueSession("alice", "hunter2")
	if err != nil {
		t.Fatal(err)
	}
	if !s.CheckSession(sess) || s.CheckSession("nope") || s.CheckSession("") {
		t.Fatal("session check")
	}
	// Revoke
	if err := s.RevokeSession(sess); err != nil {
		t.Fatal(err)
	}
	if s.CheckSession(sess) {
		t.Fatal("still valid after revoke")
	}
	// Issue fails on bad creds
	if _, err := s.IssueAPIToken("alice", "x"); err == nil {
		t.Fatal("expected creds error")
	}
	if _, err := s.IssueSession("alice", "x"); err == nil {
		t.Fatal("expected creds error")
	}
}

func TestVerifyMalformedHash(t *testing.T) {
	cases := []string{"", "no-dollars", "bad$x$y", "sha256$nothex$y", "sha256$00$nothex"}
	for _, h := range cases {
		c := Config{Username: "u", PasswordHash: h}
		if err := c.Verify("u", "p"); !errors.Is(err, ErrInvalidCredentials) {
			t.Fatalf("hash=%q got %v", h, err)
		}
	}
}

func TestStorePersistAndReopen(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "tokens.json")
	cfg := makeCfg(t, "u", "p")
	s, err := OpenStore(path, cfg)
	if err != nil {
		t.Fatal(err)
	}
	tok, _ := s.IssueAPIToken("u", "p")
	sess, _ := s.IssueSession("u", "p")
	s2, err := OpenStore(path, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if !s2.CheckAPIToken(tok) || !s2.CheckSession(sess) {
		t.Fatal("not restored")
	}
}

func TestOpenStoreBadJSON(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "tokens.json")
	os.WriteFile(path, []byte("{bad"), 0o644)
	if _, err := OpenStore(path, Config{}); err == nil {
		t.Fatal("expected error")
	}
}

func TestOpenStoreOtherError(t *testing.T) {
	dir := t.TempDir()
	// Make the path a directory → ReadFile returns EISDIR.
	p := filepath.Join(dir, "tokens.json")
	os.MkdirAll(p, 0o755)
	if _, err := OpenStore(p, Config{}); err == nil {
		t.Fatal("expected error")
	}
}

func TestOpenStoreMissingIsOK(t *testing.T) {
	dir := t.TempDir()
	s, err := OpenStore(filepath.Join(dir, "no.json"), Config{})
	if err != nil || s == nil {
		t.Fatalf("err=%v", err)
	}
}

func TestExtractAPIToken(t *testing.T) {
	r := httptest.NewRequest("GET", "/?T=fromform", nil)
	r.Header.Set("Authorization", `GoogleLogin auth="bearer"`)
	if got := ExtractAPIToken(r); got != "bearer" {
		t.Fatalf("got %q", got)
	}
	r2 := httptest.NewRequest("GET", "/?T=fromform", nil)
	if got := ExtractAPIToken(r2); got != "fromform" {
		t.Fatalf("form got %q", got)
	}
	r3 := httptest.NewRequest("GET", "/", nil)
	if got := ExtractAPIToken(r3); got != "" {
		t.Fatalf("expected empty, got %q", got)
	}
	// Authorization without prefix
	r4 := httptest.NewRequest("GET", "/", nil)
	r4.Header.Set("Authorization", "Basic abcdef")
	if got := ExtractAPIToken(r4); got != "" {
		t.Fatalf("got %q", got)
	}
	// Authorization with prefix but malformed pair
	r5 := httptest.NewRequest("GET", "/", nil)
	r5.Header.Set("Authorization", "GoogleLogin noequals")
	if got := ExtractAPIToken(r5); got != "" {
		t.Fatalf("got %q", got)
	}
	// Authorization with multi-param
	r6 := httptest.NewRequest("GET", "/", nil)
	r6.Header.Set("Authorization", "GoogleLogin SID=x, auth=tok")
	if got := ExtractAPIToken(r6); got != "tok" {
		t.Fatalf("got %q", got)
	}
}

func TestSessionCookieRoundtrip(t *testing.T) {
	w := httptest.NewRecorder()
	SetSessionCookie(w, "v1", true)
	res := w.Result()
	req := &http.Request{Header: http.Header{"Cookie": res.Header.Values("Set-Cookie")}}
	if got := SessionFromRequest(req); got != "v1" {
		t.Fatalf("got %q", got)
	}
	r2 := httptest.NewRequest("GET", "/", nil)
	if got := SessionFromRequest(r2); got != "" {
		t.Fatalf("got %q", got)
	}
	w2 := httptest.NewRecorder()
	ClearSessionCookie(w2)
	if !strings.Contains(w2.Header().Get("Set-Cookie"), "Max-Age=0") {
		t.Fatalf("clear cookie: %q", w2.Header().Get("Set-Cookie"))
	}
}

func TestHashPasswordReadError(t *testing.T) {
	orig := randRead
	t.Cleanup(func() { randRead = orig })
	randRead = func([]byte) (int, error) { return 0, errors.New("rand-boom") }
	if _, err := HashPassword("p"); err == nil {
		t.Fatal("expected rand error")
	}
}

func TestIssueRandError(t *testing.T) {
	dir := t.TempDir()
	cfg := makeCfg(t, "u", "p")
	s, _ := OpenStore(filepath.Join(dir, "tokens.json"), cfg)
	orig := randRead
	t.Cleanup(func() { randRead = orig })
	randRead = func([]byte) (int, error) { return 0, errors.New("rand-boom") }
	if _, err := s.IssueAPIToken("u", "p"); err == nil {
		t.Fatal("expected rand error api")
	}
	if _, err := s.IssueSession("u", "p"); err == nil {
		t.Fatal("expected rand error sess")
	}
}

func TestPersistMarshalAndMkdirFail(t *testing.T) {
	dir := t.TempDir()
	cfg := makeCfg(t, "u", "p")
	s, _ := OpenStore(filepath.Join(dir, "tokens.json"), cfg)
	// Marshal fail
	origJ := jsonMarshalIndent
	t.Cleanup(func() { jsonMarshalIndent = origJ })
	jsonMarshalIndent = func(any, string, string) ([]byte, error) { return nil, errors.New("m-boom") }
	if _, err := s.IssueAPIToken("u", "p"); err == nil {
		t.Fatal("expected marshal err")
	}
	jsonMarshalIndent = origJ
	// Mkdir fail
	origM := osMkdirAll
	t.Cleanup(func() { osMkdirAll = origM })
	osMkdirAll = func(string, os.FileMode) error { return errors.New("mk-boom") }
	if _, err := s.IssueAPIToken("u", "p"); err == nil {
		t.Fatal("expected mkdir err")
	}
}

// IssueAPIToken / IssueSession persist-locked failure: make tokens.json
// parent dir non-writable so atomic write fails.
func TestIssuePersistFail(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root bypass")
	}
	dir := t.TempDir()
	cfg := makeCfg(t, "u", "p")
	s, _ := OpenStore(filepath.Join(dir, "tokens.json"), cfg)
	os.Chmod(dir, 0o500)
	t.Cleanup(func() { os.Chmod(dir, 0o755) })
	if _, err := s.IssueAPIToken("u", "p"); err == nil {
		t.Fatal("expected persist err")
	}
	if _, err := s.IssueSession("u", "p"); err == nil {
		t.Fatal("expected persist err")
	}
	if err := s.RevokeSession("anything"); err == nil {
		t.Fatal("expected persist err")
	}
}

func TestStoreVerifyAndSetPasswordHash(t *testing.T) {
	hOrig, err := HashPassword("orig")
	if err != nil {
		t.Fatal(err)
	}
	s, _ := OpenStore(filepath.Join(t.TempDir(), "tokens.json"), Config{Username: "u", PasswordHash: hOrig})
	if err := s.Verify("u", "orig"); err != nil {
		t.Fatalf("verify orig: %v", err)
	}
	hNew, _ := HashPassword("new!!")
	s.SetPasswordHash(hNew)
	if err := s.Verify("u", "orig"); err == nil {
		t.Fatal("orig should no longer verify")
	}
	if err := s.Verify("u", "new!!"); err != nil {
		t.Fatalf("new should verify: %v", err)
	}
}

func TestRevokeAllSessions(t *testing.T) {
	h, _ := HashPassword("p")
	s, _ := OpenStore(filepath.Join(t.TempDir(), "tokens.json"), Config{Username: "u", PasswordHash: h})
	t1, _ := s.IssueSession("u", "p")
	t2, _ := s.IssueSession("u", "p")
	if !s.CheckSession(t1) || !s.CheckSession(t2) {
		t.Fatal("sessions should exist pre-revoke")
	}
	if err := s.RevokeAllSessions(); err != nil {
		t.Fatal(err)
	}
	if s.CheckSession(t1) || s.CheckSession(t2) {
		t.Fatal("sessions should be gone")
	}
}

// TestOpenStoreSweepsExpired verifies that tokens/sessions already past
// TokenLifetime on disk are evicted at open and the file rewritten.
func TestOpenStoreSweepsExpired(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "tokens.json")
	old := time.Now().UTC().Add(-2 * TokenLifetime)
	fresh := time.Now().UTC()
	disk := struct {
		API      map[string]time.Time `json:"api"`
		Sessions map[string]time.Time `json:"sessions"`
	}{
		API:      map[string]time.Time{"old-api": old, "fresh-api": fresh},
		Sessions: map[string]time.Time{"old-sess": old, "fresh-sess": fresh},
	}
	data, _ := json.MarshalIndent(disk, "", "  ")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := OpenStore(path, makeCfg(t, "a", "p"))
	if err != nil {
		t.Fatal(err)
	}
	if s.CheckAPIToken("old-api") || s.CheckSession("old-sess") {
		t.Fatal("expired entries should have been swept")
	}
	if !s.CheckAPIToken("fresh-api") || !s.CheckSession("fresh-sess") {
		t.Fatal("fresh entries should survive the sweep")
	}
	// The on-disk file must have been rewritten without the expired
	// entries (reopen and re-check).
	s2, err := OpenStore(path, makeCfg(t, "a", "p"))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := s2.api["old-api"]; ok {
		t.Fatal("expired api token still on disk")
	}
	if _, ok := s2.sessions["old-sess"]; ok {
		t.Fatal("expired session still on disk")
	}
}

// TestIssueSweepsExpired confirms the opportunistic sweep on issue
// drops a token that has since expired.
func TestIssueSweepsExpired(t *testing.T) {
	dir := t.TempDir()
	cfg := makeCfg(t, "a", "p")
	s, err := OpenStore(filepath.Join(dir, "tokens.json"), cfg)
	if err != nil {
		t.Fatal(err)
	}
	// Plant an already-expired token directly.
	s.api["stale"] = time.Now().UTC().Add(-2 * TokenLifetime)
	s.sessions["stale-sess"] = time.Now().UTC().Add(-2 * TokenLifetime)
	if _, err := s.IssueAPIToken("a", "p"); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.api["stale"]; ok {
		t.Fatal("issuing an API token should have swept the stale one")
	}
	if _, err := s.IssueSession("a", "p"); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.sessions["stale-sess"]; ok {
		t.Fatal("issuing a session should have swept the stale one")
	}
}

// TestOpenStoreSweepPersistError surfaces a persist failure from the
// open-time sweep.
func TestOpenStoreSweepPersistError(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "tokens.json")
	old := time.Now().UTC().Add(-2 * TokenLifetime)
	disk := struct {
		API map[string]time.Time `json:"api"`
	}{API: map[string]time.Time{"old": old}}
	data, _ := json.MarshalIndent(disk, "", "  ")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	origJ := jsonMarshalIndent
	t.Cleanup(func() { jsonMarshalIndent = origJ })
	jsonMarshalIndent = func(any, string, string) ([]byte, error) { return nil, errors.New("persist-boom") }
	if _, err := OpenStore(path, makeCfg(t, "a", "p")); err == nil {
		t.Fatal("expected persist error from open-time sweep")
	}
}
