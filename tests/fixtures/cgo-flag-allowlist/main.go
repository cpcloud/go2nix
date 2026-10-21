package main

import (
	"fmt"

	"example.com/cgo-flag-allowlist/internal/mathflag"
)

func main() {
	fmt.Printf("finite-math-only: %d\n", mathflag.FiniteMathOnly())
}
