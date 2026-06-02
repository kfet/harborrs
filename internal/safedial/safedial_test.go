package safedial

import (
	"net"
	"strings"
	"testing"
	"time"
)

func TestBlocked(t *testing.T) {
	cases := []struct {
		ip   string
		want bool
	}{
		{"127.0.0.1", true},             // loopback
		{"::1", true},                   // loopback v6
		{"10.0.0.5", true},              // RFC1918
		{"192.168.1.1", true},           // RFC1918
		{"172.16.0.1", true},            // RFC1918
		{"169.254.169.254", true},       // link-local / cloud metadata
		{"fe80::1", true},               // link-local v6
		{"fc00::1", true},               // ULA
		{"0.0.0.0", true},               // unspecified
		{"::", true},                    // unspecified v6
		{"224.0.0.1", true},             // multicast
		{"100.64.0.1", true},            // CGNAT
		{"100.127.255.255", true},       // CGNAT upper
		{"8.8.8.8", false},              // public
		{"1.1.1.1", false},              // public
		{"100.63.255.255", false},       // just below CGNAT
		{"100.128.0.1", false},          // just above CGNAT
		{"2606:4700:4700::1111", false}, // public v6
	}
	for _, c := range cases {
		ip := net.ParseIP(c.ip)
		if got := Blocked(ip); got != c.want {
			t.Errorf("Blocked(%s) = %v, want %v", c.ip, got, c.want)
		}
	}
	if !Blocked(nil) {
		t.Error("Blocked(nil) must be true")
	}
}

func TestAllowPrivate(t *testing.T) {
	for _, v := range []string{"1", "true", "TRUE", "yes", "on", " on "} {
		t.Setenv("HARBORRS_ALLOW_PRIVATE_FETCH", v)
		if !AllowPrivate() {
			t.Errorf("AllowPrivate() should be true for %q", v)
		}
	}
	for _, v := range []string{"", "0", "false", "no", "nope"} {
		t.Setenv("HARBORRS_ALLOW_PRIVATE_FETCH", v)
		if AllowPrivate() {
			t.Errorf("AllowPrivate() should be false for %q", v)
		}
	}
}

func TestControl(t *testing.T) {
	t.Setenv("HARBORRS_ALLOW_PRIVATE_FETCH", "")
	// Blocked addresses → error.
	for _, addr := range []string{"127.0.0.1:80", "169.254.169.254:80", "10.0.0.1:443"} {
		if err := control("tcp", addr, nil); err == nil {
			t.Errorf("control(%s) should be blocked", addr)
		} else if !strings.Contains(err.Error(), "non-public") {
			t.Errorf("control(%s) wrong error: %v", addr, err)
		}
	}
	// Public address → allowed.
	if err := control("tcp", "8.8.8.8:80", nil); err != nil {
		t.Errorf("control(public) unexpected error: %v", err)
	}
	// Malformed address (no port) → SplitHostPort error surfaces.
	if err := control("tcp", "noport", nil); err == nil {
		t.Error("control(noport) should error")
	}
	// Opt-out → always allowed, even loopback.
	t.Setenv("HARBORRS_ALLOW_PRIVATE_FETCH", "1")
	if err := control("tcp", "127.0.0.1:80", nil); err != nil {
		t.Errorf("control with opt-out should allow loopback: %v", err)
	}
}

func TestNewClientAndTransport(t *testing.T) {
	if tr := NewTransport(); tr == nil || tr.DialContext == nil {
		t.Fatal("NewTransport must return a transport with a DialContext")
	}
	c := NewClient(5 * time.Second)
	if c == nil || c.Timeout != 5*time.Second || c.Transport == nil {
		t.Fatal("NewClient must return a configured client")
	}
}
