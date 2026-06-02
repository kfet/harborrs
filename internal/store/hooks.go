package store

import (
	"encoding/json"
	"encoding/xml"
	"io"
	"os"

	"github.com/kfet/harb/internal/atomic"
)

// Hookable operations — overridden in tests to exercise error paths that
// cannot be reliably triggered by real filesystem operations (json.Marshal
// of well-typed values, atomic write inner failures, etc.).
var (
	jsonMarshal       = json.Marshal
	jsonMarshalIndent = json.MarshalIndent
	xmlMarshalIndent  = xml.MarshalIndent
	atomicWriteFile   = atomic.WriteFile
	osOpenAppend      = func(path string) (io.WriteCloser, error) {
		return os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	}
)
