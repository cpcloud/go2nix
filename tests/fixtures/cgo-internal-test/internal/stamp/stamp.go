// Package stamp is a non-cgo package with a //go:embed target so
// packageOverrides.srcOverlay can be exercised on the rawGoCompile path
// (cgo packages take the stdenv hook path instead). The overlay replaces
// VERSION and adds COMMIT, which the source tree lacks.
package stamp

import _ "embed"

//go:embed VERSION
var Version string

//go:embed COMMIT
var Commit string
