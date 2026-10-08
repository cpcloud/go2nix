package loop_test

import (
	"reflect"
	"testing"

	"example.com/langloopvar/internal/mid"
)

var want = []int{3, 3, 3} // go1.21 shared-loopvar semantics

func TestXTestFileLoopvar(t *testing.T) {
	var fns []func() int
	for i := 0; i < 3; i++ {
		fns = append(fns, func() int { return i })
	}
	var got []int
	for _, f := range fns {
		got = append(got, f())
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v (external test archive compiled without -lang=go1.21?)", got, want)
	}
}

// mid imports loop, and loop has internal tests, so mid is compiled again
// for this test binary, from the overlaid copy of its directory.
func TestRecompiledDependentLoopvar(t *testing.T) {
	if mid.Tag != "generated" {
		t.Fatalf("mid.Tag = %q, want the srcOverlay's", mid.Tag)
	}
	if got := mid.Capture(); !reflect.DeepEqual(got, want) {
		t.Fatalf("mid.Capture() = %v, want %v (mid recompiled without -lang=go1.21?)", got, want)
	}
}
