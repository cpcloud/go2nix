package loop

import _ "embed"

// Banner is "placeholder" in the source tree; dag.nix replaces banner.txt
// with a srcOverlay, so tests that see "generated" were compiled from the
// overlaid copy of this directory, which has no go.mod above it.
//
//go:embed banner.txt
var Banner string
