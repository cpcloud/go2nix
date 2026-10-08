package scale

import "testing"

func TestTriple(t *testing.T) {
	if got := Triple(14); got != 42 {
		t.Fatalf("Triple(14) = %d, want 42", got)
	}
}
