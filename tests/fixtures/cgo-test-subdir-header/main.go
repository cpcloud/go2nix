package main

import (
	"fmt"

	"example.com/cgo-test-subdir-header/internal/scale"
)

func main() {
	fmt.Println(scale.Triple(14))
}
