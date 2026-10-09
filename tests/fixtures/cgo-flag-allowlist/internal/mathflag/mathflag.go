// Package mathflag asks for -ffinite-math-only, which cmd/go's lists of
// permitted #cgo flags do not have and which does this code no harm. With
// the flag in effect the compiler defines __FINITE_MATH_ONLY__ to 1.
package mathflag

/*
#cgo CFLAGS: -ffinite-math-only

static int finite_math_only(void) { return __FINITE_MATH_ONLY__; }
*/
import "C"

func FiniteMathOnly() int { return int(C.finite_math_only()) }
