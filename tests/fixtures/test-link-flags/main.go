// Binary test-link-flags prints what the tests of internal/probe assert, so
// the wrapper can check that the binary and its tests agree.
package main

import (
	"fmt"

	"example.com/test-link-flags/internal/probe"
)

func main() {
	fmt.Println(probe.WildcardStatus())
}
