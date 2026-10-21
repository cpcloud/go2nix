package core_test

import (
	"testing"

	"example.com/xtest-only-ca/internal/core"
	"example.com/xtest-only-ca/internal/user"
)

func TestNext(t *testing.T) {
	if got, want := user.Next(), core.Value()+1; got != want {
		t.Fatalf("user.Next() = %d, want %d", got, want)
	}
}
