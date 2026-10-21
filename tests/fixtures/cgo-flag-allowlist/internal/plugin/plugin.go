// Package plugin asks the C compiler to load a plugin, which go build
// refuses: the flag runs code of the package author's choosing.
package plugin

/*
#cgo CFLAGS: -fplugin=/nonexistent.so

static int one(void) { return 1; }
*/
import "C"

func One() int { return int(C.one()) }
