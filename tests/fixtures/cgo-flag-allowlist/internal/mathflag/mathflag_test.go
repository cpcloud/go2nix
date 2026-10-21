package mathflag

import "testing"

func TestFiniteMathOnly(t *testing.T) {
	if FiniteMathOnly() != 1 {
		t.Fatal("-ffinite-math-only is not in effect")
	}
}
