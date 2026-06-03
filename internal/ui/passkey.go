package ui

import (
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"time"

	"github.com/kfet/harb/internal/auth"
)

// b64Std encodes/decodes credential IDs for the settings remove-form.
var b64Std = base64.StdEncoding

// challengeCookie is the short-lived HttpOnly cookie that binds a browser
// to the pending WebAuthn challenge it was issued, so the finish step can
// look the challenge up server-side. It is single-use and expires with
// the challenge.
const challengeCookie = "harb_wa_chal"

// maxPasskeyBody caps the size of a WebAuthn response body. Real
// attestation objects are a few hundred bytes to low single-digit KB.
const maxPasskeyBody = 1 << 16 // 64 KiB

// setChallengeCookie writes the handle cookie, mirroring the session
// cookie's Secure flag so it is only sent over https when configured.
func (s *Server) setChallengeCookie(w http.ResponseWriter, handle string) {
	http.SetCookie(w, &http.Cookie{
		Name:     challengeCookie,
		Value:    handle,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   s.Secure,
		MaxAge:   int((5 * time.Minute) / time.Second),
	})
}

func (s *Server) clearChallengeCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{Name: challengeCookie, Value: "", Path: "/", MaxAge: -1})
}

// challengeHandle returns the handle from the request cookie, or "".
func challengeHandle(r *http.Request) string {
	c, err := r.Cookie(challengeCookie)
	if err != nil {
		return ""
	}
	return c.Value
}

// writeJSON emits a JSON body that is already marshalled.
func writeJSON(w http.ResponseWriter, raw []byte) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.Write(raw)
}

// jsonError emits a JSON {"error": msg} with the given status.
func jsonError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

func (s *Server) handlePasskeyRegisterBegin(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	handle, opts, err := s.Passkey.BeginRegistration()
	if err != nil {
		jsonError(w, http.StatusInternalServerError, "could not start registration")
		return
	}
	s.setChallengeCookie(w, handle)
	writeJSON(w, opts)
}

func (s *Server) handlePasskeyRegisterFinish(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	handle := challengeHandle(r)
	body, err := io.ReadAll(io.LimitReader(r.Body, maxPasskeyBody))
	if err != nil {
		jsonError(w, http.StatusBadRequest, "could not read body")
		return
	}
	label := r.URL.Query().Get("label")
	cred, err := s.Passkey.FinishRegistration(handle, body, label)
	s.clearChallengeCookie(w)
	if err != nil {
		jsonError(w, http.StatusBadRequest, "registration failed")
		return
	}
	writeJSON(w, mustJSON(map[string]string{"label": cred.Label}))
}

func (s *Server) handlePasskeyLoginBegin(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	handle, opts, err := s.Passkey.BeginLogin()
	if err != nil {
		jsonError(w, http.StatusInternalServerError, "could not start login")
		return
	}
	s.setChallengeCookie(w, handle)
	writeJSON(w, opts)
}

func (s *Server) handlePasskeyLoginFinish(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	handle := challengeHandle(r)
	body, err := io.ReadAll(io.LimitReader(r.Body, maxPasskeyBody))
	if err != nil {
		jsonError(w, http.StatusBadRequest, "could not read body")
		return
	}
	_, err = s.Passkey.FinishLogin(handle, body)
	s.clearChallengeCookie(w)
	if err != nil {
		jsonError(w, http.StatusUnauthorized, "authentication failed")
		return
	}
	// Mint a session for the single user, exactly like a password login.
	tok, err := s.Auth.NewSession()
	if err != nil {
		jsonError(w, http.StatusInternalServerError, "could not create session")
		return
	}
	auth.SetSessionCookie(w, tok, s.Secure)
	writeJSON(w, mustJSON(map[string]bool{"ok": true}))
}

func (s *Server) handlePasskeyRemove(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	id, err := b64Std.DecodeString(r.FormValue("id"))
	if err != nil {
		http.Error(w, "bad credential id", http.StatusBadRequest)
		return
	}
	_ = s.Passkey.Store().Delete(id)
	RelRedirect(w, r, "../settings?ok=1", http.StatusSeeOther)
}

// mustJSON marshals v, panicking on error. Used only for fixed
// server-controlled maps that always marshal.
func mustJSON(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		panic("ui: mustJSON: " + err.Error())
	}
	return b
}
