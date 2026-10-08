// Package libm calls into libm, which only #cgo LDFLAGS: -lm links.
package libm

// #cgo LDFLAGS: -lm
// #include <math.h>
// static double gamma_(double x) { return tgamma(x); }
import "C"

func Gamma(x float64) float64 { return float64(C.gamma_(C.double(x))) }
