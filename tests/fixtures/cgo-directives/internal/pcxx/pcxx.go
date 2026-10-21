// Package pcxx has a C++ file that needs what bar.pc's Cflags add: an
// include directory below include/ and a macro. A macro from #cgo CXXFLAGS
// reaches the same file.
package pcxx

// #cgo pkg-config: bar
// #cgo CXXFLAGS: -DFROM_CXXFLAGS=13
// #include "shim.h"
import "C"

func Header() int   { return int(C.shim_header()) }
func FromPC() int   { return int(C.shim_from_pc()) }
func CXXFlags() int { return int(C.shim_cxxflags()) }
