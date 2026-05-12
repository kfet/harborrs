package auth

import (
	"crypto/rand"
	"encoding/json"
	"os"
)

// Hookable operations — overridden in tests to exercise error paths that
// cannot be reliably triggered by real OS/stdlib calls.
var (
	randRead          = rand.Read
	jsonMarshalIndent = json.MarshalIndent
	osMkdirAll        = os.MkdirAll
)
