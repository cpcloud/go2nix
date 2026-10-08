//go:debug panicnil=1
package probe

import (
	"net/http"
	"testing"
)

func TestTesting(t *testing.T) {
	if !testing.Testing() {
		t.Fatal("testing.Testing() = false in a test binary")
	}
}

func TestGoModGodebug(t *testing.T) {
	if got := WildcardStatus(); got != http.StatusNotFound {
		t.Fatalf("WildcardStatus() = %d, want 404: go.mod says go 1.21, so httpmuxgo121=1 is the default", got)
	}
}

// panicnil=1 comes from the //go:debug line of this file only: `go 1.21` is
// not old enough to make it a default.
func TestTestFileGodebug(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("recover() = %v, want nil: //go:debug panicnil=1 is set in this file", r)
		}
	}()
	panic(nil)
}
