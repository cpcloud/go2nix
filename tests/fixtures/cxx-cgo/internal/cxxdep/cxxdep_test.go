package cxxdep

import "testing"

// The test binary holds concat.cc's object too, so its link needs CXX as
// -extld just like the application's.
func TestConcat(t *testing.T) {
	if got := Concat("hello-", "cxx"); got != "hello-cxx" {
		t.Fatalf("Concat = %q, want hello-cxx", got)
	}
}
