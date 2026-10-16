package buildinfo

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// writeGoroot makes a directory that looks like a GOROOT to toolchainTable:
// only src/internal/godebugs/table.go, with the given text.
func writeGoroot(t *testing.T, table string) string {
	t.Helper()
	goroot := t.TempDir()
	dir := filepath.Join(goroot, "src", "internal", "godebugs")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "table.go"), []byte(table), 0o644); err != nil {
		t.Fatal(err)
	}
	return goroot
}

func writeModule(t *testing.T, goLine string, extra ...string) string {
	t.Helper()
	dir := t.TempDir()
	gomod := "module example.com/test\n\ngo " + goLine + "\n" + strings.Join(extra, "\n")
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte(gomod), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

const syntheticTable = `package godebugs

type Info struct {
	Name    string
	Package string
	Changed int
	Old     string
	Opaque  bool
}

var All = []Info{
	{Name: "alpha", Package: "p", Changed: 26, Old: "1"},
	{Name: "beta", Package: "p"},
	{Name: "gamma", Package: "p", Changed: 21, Old: "0", Opaque: true},
}

type RemovedInfo struct {
	Name    string
	Removed int
}

var Removed = []RemovedInfo{
	{Name: "delta", Removed: 27},
}
`

// The defaults come from the table under goroot and from nothing else: the
// built-in copy's settings do not appear, whatever toolchainVersion says.
func TestDefaultGODEBUG_ReadsTheToolchainsTable(t *testing.T) {
	goroot := writeGoroot(t, syntheticTable)
	for _, tc := range []struct {
		goLine, toolchain, want string
	}{
		{"1.20", "go1.27.2", "alpha=1,gamma=0"},
		{"1.25", "go1.27.2", "alpha=1"},
		{"1.25", "go1.20", "alpha=1"},
		{"1.25", "", "alpha=1"},
		{"1.26", "go1.27.2", ""},
	} {
		if got := DefaultGODEBUG(writeModule(t, tc.goLine), nil, "", tc.toolchain, goroot); got != tc.want {
			t.Errorf("go %s, toolchain %q: got %q, want %q", tc.goLine, tc.toolchain, got, tc.want)
		}
	}
}

// A goroot without a readable table falls back on the built-in copy and its
// rules by toolchain version.
func TestDefaultGODEBUG_FallsBackOnTheCopy(t *testing.T) {
	dir := writeModule(t, "1.21")
	want := DefaultGODEBUG(dir, nil, "", "go1.26.3", "")
	if !contains(want, "asynctimerchan=1") || contains(want, "tracebacklabels=") {
		t.Fatalf("the copy under go1.26.3 gives %q", want)
	}
	for name, goroot := range map[string]string{
		"no such directory":    filepath.Join(t.TempDir(), "absent"),
		"no All":               writeGoroot(t, "package godebugs\n\nvar Other = 1\n"),
		"not Go":               writeGoroot(t, "{Name: \"alpha\", Changed: 26}\n"),
		"nothing with Changed": writeGoroot(t, "package godebugs\n\ntype Info struct{ Name string }\n\nvar All = []Info{{Name: \"beta\"}}\n"),
	} {
		if got := DefaultGODEBUG(dir, nil, "", "go1.26.3", goroot); got != want {
			t.Errorf("%s: got %q, want the copy's %q", name, got, want)
		}
	}
}

// The copy has httpservecontentmaxranges (Go 1.27.2 and 1.26.9), for a
// toolchain whose table cannot be read.
func TestDefaultGODEBUG_CopyHasMaxRanges(t *testing.T) {
	const want = "httpservecontentmaxranges=1"
	if got := DefaultGODEBUG(writeModule(t, "1.25"), nil, "", "go1.27.2", ""); !contains(got, want) {
		t.Errorf("go 1.25: %q lacks %s", got, want)
	}
	if got := DefaultGODEBUG(writeModule(t, "1.26"), nil, "", "go1.27.2", ""); contains(got, want) {
		t.Errorf("go 1.26: %q has %s", got, want)
	}
}

// With the toolchain on PATH, the result is what its own cmd/go reports for
// the same module.
func TestDefaultGODEBUG_MatchesGoList(t *testing.T) {
	gobin, err := exec.LookPath("go")
	if err != nil {
		t.Skip("no go on PATH")
	}
	scratch := t.TempDir()
	env := append(os.Environ(),
		"GOTOOLCHAIN=local", "GOFLAGS=-mod=mod", "GOWORK=off", "GOPROXY=off",
		"GOENV=off", "GOFIPS140=off", "GOROOT=",
		"GOCACHE="+filepath.Join(scratch, "cache"), "GOPATH="+filepath.Join(scratch, "gopath"), "GODEBUG=")
	goEnv := func(key string) string {
		cmd := exec.Command(gobin, "env", key)
		cmd.Env = env
		out, err := cmd.Output()
		if err != nil {
			t.Fatalf("go env %s: %v", key, err)
		}
		return strings.TrimSpace(string(out))
	}
	version, goroot := goEnv("GOVERSION"), goEnv("GOROOT")
	if _, ok := toolchainTable(goroot); !ok {
		t.Fatalf("the table of %s under %s cannot be read", version, goroot)
	}
	own := parseGoMinor(strings.TrimPrefix(version, "go"))

	for _, tc := range []struct {
		goLine string
		extra  []string
	}{
		{"1.16", nil},
		{"1.20", nil},
		{"1.21", nil},
		{"1.22", nil},
		{"1.23", nil},
		{"1.24", nil},
		{"1.25", nil},
		{"1.26", nil},
		{"1.27", nil},
		{"1.25", []string{"godebug default=go1.21"}},
		{"1.21", []string{"godebug (", "\tdefault=go1.23", "\thttpmuxgo121=1", ")"}},
	} {
		if parseGoMinor(tc.goLine) > own {
			continue
		}
		dir := writeModule(t, tc.goLine, tc.extra...)
		if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\n\nfunc main() {}\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		cmd := exec.Command(gobin, "list", "-f", "{{.DefaultGODEBUG}}", ".")
		cmd.Dir, cmd.Env = dir, env
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("go list for go %s %v: %v\n%s", tc.goLine, tc.extra, err, out)
		}
		want := strings.TrimSpace(string(out))
		if got := DefaultGODEBUG(dir, nil, "", version, goroot); got != want {
			t.Errorf("go %s %v with %s:\n go2nix %q\n go list %q", tc.goLine, tc.extra, version, got, want)
		}
	}
}
