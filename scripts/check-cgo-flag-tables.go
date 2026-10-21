// check-cgo-flag-tables verifies that go/go2nix/pkg/compile/cgosecurity.go
// still is the Go toolchain's cmd/go/internal/work/security.go: the flag
// tables, checkFlags and its callers, and load.SafeArg, with the
// substitutions the port's header names. On a difference it prints a line
// diff; the port is then brought up to date by hand, and the callers in
// cmd/go/internal/work/exec.go (runCgo, getPkgConfigFlags,
// processCgoOutputs), which this script does not read, are compared with
// go/go2nix/pkg/compile/cgo.go.
//
// Usage:
//
//	go run scripts/check-cgo-flag-tables.go
//
//go:build ignore

package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

const (
	portPath = "go/go2nix/pkg/compile/cgosecurity.go"
	// The port and upstream agree from here on...
	firstLine = "var validCompilerFlags = "
	// ...up to this line of the port, which follows its copy of security.go.
	portEnd = "// safeArg is cmd/go/internal/load.SafeArg."
)

var substitutions = strings.NewReplacer(
	"*lazyregexp.Regexp", "*lazyRegexp",
	"cfg.Getenv(", "os.Getenv(",
	"load.SafeArg(", "safeArg(",
	`ALLOW: %v"`, `ALLOW: %w"`,
	"func SafeArg(", "func safeArg(",
)

func main() {
	goroot := os.Getenv("GOROOT")
	if goroot == "" {
		out, err := exec.Command("go", "env", "GOROOT").Output()
		if err != nil {
			fatal("cannot determine GOROOT: %v", err)
		}
		goroot = strings.TrimSpace(string(out))
	}
	data, _ := os.ReadFile(filepath.Join(goroot, "VERSION"))
	version, _, _ := strings.Cut(string(data), "\n")

	port := lines(portPath)
	security := lines(filepath.Join(goroot, "src", "cmd", "go", "internal", "work", "security.go"))
	load := lines(filepath.Join(goroot, "src", "cmd", "go", "internal", "load", "pkg.go"))

	stale := false
	for _, c := range []struct {
		what           string
		port, upstream []string
	}{
		{
			"security.go from its first table on",
			section(portPath, port, firstLine, portEnd, false),
			section("security.go", security, firstLine, "", false),
		},
		{
			"load.SafeArg",
			section(portPath, port, "func safeArg(", "}", true),
			section("load/pkg.go", load, "func SafeArg(", "}", true),
		},
	} {
		for i, l := range c.upstream {
			c.upstream[i] = substitutions.Replace(l)
		}
		if d := diff(c.port, c.upstream); d != "" {
			fmt.Fprintf(os.Stderr, "%s differs from %s (- %s, + %s):\n%s\n", c.what, version, portPath, version, d)
			stale = true
		}
	}
	if stale {
		fmt.Fprintf(os.Stderr, "Bring %s up to date with %s by hand, and compare the callers in cmd/go/internal/work/exec.go (runCgo, getPkgConfigFlags, processCgoOutputs) with go/go2nix/pkg/compile/cgo.go.\n", portPath, version)
		os.Exit(1)
	}
	fmt.Printf("OK: %s matches cmd/go/internal/work/security.go of %s\n", portPath, version)
}

func lines(path string) []string {
	data, err := os.ReadFile(path)
	if err != nil {
		fatal("%v", err)
	}
	return strings.Split(strings.TrimRight(string(data), "\n"), "\n")
}

// section returns the lines from the first one that starts with first up to
// the first later one that is end (all the rest when end is empty), which
// is included only when inclusive is set. Trailing blank lines are dropped.
func section(name string, all []string, first, end string, inclusive bool) []string {
	for i, l := range all {
		if !strings.HasPrefix(l, first) {
			continue
		}
		rest := all[i:]
		if end != "" {
			found := false
			for j, l := range rest {
				if l == end {
					if inclusive {
						j++
					}
					rest, found = rest[:j], true
					break
				}
			}
			if !found {
				fatal("%s: no line %q after %q", name, end, first)
			}
		}
		for len(rest) > 0 && strings.TrimSpace(rest[len(rest)-1]) == "" {
			rest = rest[:len(rest)-1]
		}
		return append([]string(nil), rest...)
	}
	fatal("%s: no line starts with %q", name, first)
	return nil
}

// diff returns the differing lines of a and b in order, by longest common
// subsequence, or "" when they are equal.
func diff(a, b []string) string {
	lcs := make([][]int, len(a)+1)
	for i := range lcs {
		lcs[i] = make([]int, len(b)+1)
	}
	for i := len(a) - 1; i >= 0; i-- {
		for j := len(b) - 1; j >= 0; j-- {
			if a[i] == b[j] {
				lcs[i][j] = lcs[i+1][j+1] + 1
			} else {
				lcs[i][j] = max(lcs[i+1][j], lcs[i][j+1])
			}
		}
	}
	var out strings.Builder
	i, j := 0, 0
	for i < len(a) || j < len(b) {
		switch {
		case i < len(a) && j < len(b) && a[i] == b[j]:
			i, j = i+1, j+1
		case j == len(b) || i < len(a) && lcs[i+1][j] >= lcs[i][j+1]:
			fmt.Fprintf(&out, "  - %s\n", a[i])
			i++
		default:
			fmt.Fprintf(&out, "  + %s\n", b[j])
			j++
		}
	}
	return out.String()
}

func fatal(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(1)
}
