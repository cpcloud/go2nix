// Package envflags has no #cgo line: its macro comes from CGO_CPPFLAGS and
// libm from CGO_LDFLAGS, both set on its derivation by packageOverrides.
package envflags

/*
#include <math.h>

#ifndef FROM_ENV_CPPFLAGS
#define FROM_ENV_CPPFLAGS 0
#endif

static int cppflags(void) { return FROM_ENV_CPPFLAGS; }
static double cuberoot(double x) { return cbrt(x); }
*/
import "C"

func CPPFlags() int              { return int(C.cppflags()) }
func CubeRoot(x float64) float64 { return float64(C.cuberoot(C.double(x))) }
