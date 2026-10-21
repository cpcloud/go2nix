// Package mid depends on loop and captures a loop variable itself. Only
// loop's external test imports it.
package mid

import (
	_ "embed"

	"example.com/langloopvar/internal/loop"
)

// Tag is "placeholder" in the source tree; dag.nix replaces tag.txt with a
// srcOverlay.
//
//go:embed tag.txt
var Tag string

func Capture() []int {
	var fns []func() int
	for i := 0; i < len(loop.Capture()); i++ {
		fns = append(fns, func() int { return i })
	}
	out := make([]int, 0, len(fns))
	for _, f := range fns {
		out = append(out, f())
	}
	return out
}
