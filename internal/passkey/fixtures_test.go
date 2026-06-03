package passkey

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"testing"
)

// --- minimal CBOR encoder (test-only) -------------------------------------
// Mirrors the subset pinopass decodes: definite-length ints, byte/text
// strings, and maps. Used to synthesise COSE keys and attestation objects.

func encHead(major byte, n uint64) []byte {
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
	case n < 1<<32:
		b := []byte{mb | 26, 0, 0, 0, 0}
		binary.BigEndian.PutUint32(b[1:], uint32(n))
		return b
	default:
		b := []byte{mb | 27, 0, 0, 0, 0, 0, 0, 0, 0}
		binary.BigEndian.PutUint64(b[1:], n)
		return b
	}
}

func encUint(n uint64) []byte  { return encHead(0, n) }
func encNeg(v int64) []byte    { return encHead(1, uint64(-1-v)) }
func encBytes(b []byte) []byte { return append(encHead(2, uint64(len(b))), b...) }
func encText(s string) []byte  { return append(encHead(3, uint64(len(s))), []byte(s)...) }

func encInt(v int64) []byte {
	if v < 0 {
		return encNeg(v)
	}
	return encUint(uint64(v))
}

func encMap(pairs [][2][]byte) []byte {
	out := encHead(5, uint64(len(pairs)))
	for _, kv := range pairs {
		out = append(out, kv[0]...)
		out = append(out, kv[1]...)
	}
	return out
}

// COSE labels/values (RFC 8152) for ES256 / P-256.
const (
	coseKeyKty   = 1
	coseKeyAlg   = 3
	coseKeyCrvEC = -1
	coseKeyXEC   = -2
	coseKeyYEC   = -3
	coseKtyEC2   = 2
	coseAlgES256 = -7
	coseCrvP256  = 1

	flagUP byte = 0x01
	flagUV byte = 0x04
	flagAT byte = 0x40
)

const (
	testRPID   = "rss.example.com"
	testOrigin = "https://rss.example.com"
)

func testConfig() Config { return Config{RPID: testRPID, Origin: testOrigin, RPName: "Harbour RSS"} }

func newKey(t *testing.T) *ecdsa.PrivateKey {
	t.Helper()
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	return k
}

func coseKeyES256(pub *ecdsa.PublicKey) []byte {
	x := pub.X.FillBytes(make([]byte, 32))
	y := pub.Y.FillBytes(make([]byte, 32))
	return encMap([][2][]byte{
		{encInt(coseKeyKty), encInt(coseKtyEC2)},
		{encInt(coseKeyAlg), encInt(coseAlgES256)},
		{encInt(coseKeyCrvEC), encInt(coseCrvP256)},
		{encInt(coseKeyXEC), encBytes(x)},
		{encInt(coseKeyYEC), encBytes(y)},
	})
}

func buildAuthData(rpID string, flags byte, signCount uint32, credID, coseKey []byte) []byte {
	if coseKey != nil {
		flags |= flagAT
	}
	h := sha256.Sum256([]byte(rpID))
	out := make([]byte, 0, 64)
	out = append(out, h[:]...)
	out = append(out, flags)
	var sc [4]byte
	binary.BigEndian.PutUint32(sc[:], signCount)
	out = append(out, sc[:]...)
	if coseKey != nil {
		out = append(out, make([]byte, 16)...) // aaguid
		var cl [2]byte
		binary.BigEndian.PutUint16(cl[:], uint16(len(credID)))
		out = append(out, cl[:]...)
		out = append(out, credID...)
		out = append(out, coseKey...)
	}
	return out
}

func attestationNone(authData []byte) []byte {
	return encMap([][2][]byte{
		{encText("fmt"), encText("none")},
		{encText("attStmt"), encMap(nil)},
		{encText("authData"), encBytes(authData)},
	})
}

func clientDataJSON(t *testing.T, typ, origin string, challenge []byte) []byte {
	t.Helper()
	b, err := json.Marshal(map[string]string{
		"type":      typ,
		"challenge": base64.RawURLEncoding.EncodeToString(challenge),
		"origin":    origin,
	})
	if err != nil {
		t.Fatalf("marshal clientData: %v", err)
	}
	return b
}

func signAssertion(t *testing.T, priv *ecdsa.PrivateKey, authData, cdJSON []byte) []byte {
	t.Helper()
	cdHash := sha256.Sum256(cdJSON)
	signed := append(append([]byte{}, authData...), cdHash[:]...)
	digest := sha256.Sum256(signed)
	sig, err := ecdsa.SignASN1(rand.Reader, priv, digest[:])
	if err != nil {
		t.Fatalf("SignASN1: %v", err)
	}
	return sig
}

// regResponseJSON builds the browser-style registration response body
// that FinishRegistration consumes (binary fields base64url-encoded).
func regResponseJSON(t *testing.T, credID, clientData, att []byte) []byte {
	t.Helper()
	b, err := json.Marshal(map[string]any{
		"rawId": base64.RawURLEncoding.EncodeToString(credID),
		"response": map[string]string{
			"clientDataJSON":    base64.RawURLEncoding.EncodeToString(clientData),
			"attestationObject": base64.RawURLEncoding.EncodeToString(att),
		},
	})
	if err != nil {
		t.Fatalf("marshal regResponse: %v", err)
	}
	return b
}

// asrResponseJSON builds the browser-style assertion response body.
func asrResponseJSON(t *testing.T, credID, clientData, authData, sig []byte) []byte {
	t.Helper()
	b, err := json.Marshal(map[string]any{
		"rawId": base64.RawURLEncoding.EncodeToString(credID),
		"response": map[string]string{
			"clientDataJSON":    base64.RawURLEncoding.EncodeToString(clientData),
			"authenticatorData": base64.RawURLEncoding.EncodeToString(authData),
			"signature":         base64.RawURLEncoding.EncodeToString(sig),
		},
	})
	if err != nil {
		t.Fatalf("marshal asrResponse: %v", err)
	}
	return b
}
