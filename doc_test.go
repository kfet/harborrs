package harborrs

import "testing"

func TestVersionNonEmpty(t *testing.T) {
	if Version == "" {
		t.Fatal("Version must not be empty")
	}
}
