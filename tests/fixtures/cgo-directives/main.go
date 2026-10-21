package main

import (
	"fmt"

	"example.com/cgo-directives/internal/define"
	"example.com/cgo-directives/internal/envflags"
	"example.com/cgo-directives/internal/goos"
	"example.com/cgo-directives/internal/libm"
	"example.com/cgo-directives/internal/pcxx"
	"example.com/cgo-directives/internal/srcdir"
)

func main() {
	fmt.Printf("define: preamble=%d cppflags=%d cfile=%d\n", define.Preamble(), define.CPPFlags(), define.CFile())
	fmt.Printf("srcdir: %d\n", srcdir.Header())
	fmt.Printf("libm: %.0f\n", libm.Gamma(5))
	fmt.Printf("pcxx: header=%d pc=%d cxxflags=%d\n", pcxx.Header(), pcxx.FromPC(), pcxx.CXXFlags())
	fmt.Printf("goos: %s\n", goos.Name())
	fmt.Printf("envflags: cppflags=%d cbrt=%.0f\n", envflags.CPPFlags(), envflags.CubeRoot(27))
}
