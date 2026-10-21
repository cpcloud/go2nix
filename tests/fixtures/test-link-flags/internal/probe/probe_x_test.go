//go:debug randautoseed=0
package probe_test

import (
	"math/rand"
	"testing"
)

// With randautoseed=0 the global source is seeded as if by rand.Seed(1), so
// its next value is one of the first of that sequence, however many were
// drawn before this test.
func TestXTestFileGodebug(t *testing.T) {
	got := rand.Int63()
	seeded := rand.New(rand.NewSource(1))
	for i := 0; i < 1000; i++ {
		if seeded.Int63() == got {
			return
		}
	}
	t.Fatalf("rand.Int63() = %d is not from the sequence of seed 1: //go:debug randautoseed=0 is set in this file", got)
}
