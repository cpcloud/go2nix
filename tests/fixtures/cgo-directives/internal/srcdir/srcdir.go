// Package srcdir includes a header from a sub-directory that only
// -I${SRCDIR}/inc puts on the search path.
package srcdir

// #cgo CFLAGS: -I${SRCDIR}/inc
// #include "hdr.h"
import "C"

func Header() int { return int(C.HDR_VALUE) }
