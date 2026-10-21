// Package scale is a cgo package whose C side includes a header from a
// sub-directory of the package.
package scale

// #include "include/factor.h"
// static int triple(int v) { return v * FACTOR; }
import "C"

func Triple(v int) int {
	return int(C.triple(C.int(v)))
}
