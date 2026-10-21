package loop

import (
	"reflect"
	"testing"
)

var want = []int{3, 3, 3} // go1.21 shared-loopvar semantics

func TestOverlay(t *testing.T) {
	if Banner != "generated" {
		t.Fatalf("Banner = %q, want the srcOverlay's", Banner)
	}
}

// loop.go is compiled again, with this file, into the internal test archive.
func TestLibraryLoopvar(t *testing.T) {
	if got := Capture(); !reflect.DeepEqual(got, want) {
		t.Fatalf("Capture() = %v, want %v (internal test archive compiled without -lang=go1.21?)", got, want)
	}
}

func TestTestFileLoopvar(t *testing.T) {
	var fns []func() int
	for i := 0; i < 3; i++ {
		fns = append(fns, func() int { return i })
	}
	var got []int
	for _, f := range fns {
		got = append(got, f())
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v (internal test archive compiled without -lang=go1.21?)", got, want)
	}
}
