// Package safedial provides an SSRF-hardened HTTP client/transport for
// fetching user-supplied feed URLs.
//
// harborrs fetches arbitrary URLs the user (or a feed's redirect) points
// it at — during polling and in the add-feed preview. Without a guard, a
// URL like http://169.254.169.254/ (cloud metadata) or http://10.0.0.5/
// (an internal service) would let a feed reach into the host's private
// network: a classic server-side request forgery (SSRF) vector.
//
// The guard is installed as the dialer's Control hook, which runs after
// DNS resolution but before the socket connects. Because it inspects the
// *resolved* IP, it also defeats DNS-rebinding (a hostname that resolves
// to a public IP on the first lookup and a private one on the next) and
// is re-evaluated on every redirect hop — each hop opens a fresh
// connection and re-dials.
//
// It is opt-out-able via the HARBORRS_ALLOW_PRIVATE_FETCH environment
// variable, for self-hosters who legitimately poll feeds on localhost or
// a private LAN.
package safedial

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"strings"
	"syscall"
	"time"
)

// ErrBlocked is returned (wrapped) when a dial targets a non-public
// address while the guard is active.
var ErrBlocked = errors.New("safedial: refusing to connect to non-public address (set HARBORRS_ALLOW_PRIVATE_FETCH=1 to allow)")

// Blocked reports whether ip falls in a range harborrs refuses to fetch
// from: loopback (127/8, ::1), private (RFC1918, ULA fc00::/7),
// carrier-grade NAT (100.64/10), link-local (169.254/16 incl. the
// 169.254.169.254 cloud-metadata endpoint, fe80::/10), the unspecified
// address, and multicast. A nil ip is treated as blocked.
func Blocked(ip net.IP) bool {
	if ip == nil {
		return true
	}
	if ip.IsLoopback() || ip.IsPrivate() || ip.IsUnspecified() ||
		ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() ||
		ip.IsInterfaceLocalMulticast() || ip.IsMulticast() {
		return true
	}
	// Carrier-grade NAT 100.64.0.0/10 — not covered by IsPrivate but a
	// shared/internal range that has no business being a feed origin.
	if v4 := ip.To4(); v4 != nil && v4[0] == 100 && v4[1] >= 64 && v4[1] <= 127 {
		return true
	}
	return false
}

// AllowPrivate reports whether the SSRF guard is disabled via the
// HARBORRS_ALLOW_PRIVATE_FETCH environment variable.
func AllowPrivate() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("HARBORRS_ALLOW_PRIVATE_FETCH"))) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}

// control is the net.Dialer Control hook. address is the resolved
// "ip:port" about to be dialed.
func control(network, address string, _ syscall.RawConn) error {
	if AllowPrivate() {
		return nil
	}
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return err
	}
	ip := net.ParseIP(host)
	if Blocked(ip) {
		return fmt.Errorf("%w: %s", ErrBlocked, host)
	}
	return nil
}

// NewTransport clones http.DefaultTransport and installs the SSRF-
// guarding dialer.
func NewTransport() *http.Transport {
	t := http.DefaultTransport.(*http.Transport).Clone()
	d := &net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second, Control: control}
	t.DialContext = d.DialContext
	return t
}

// NewClient returns an *http.Client whose transport refuses non-public
// destinations and whose overall deadline is timeout.
func NewClient(timeout time.Duration) *http.Client {
	return &http.Client{Timeout: timeout, Transport: NewTransport()}
}
