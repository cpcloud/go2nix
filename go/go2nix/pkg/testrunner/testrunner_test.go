package testrunner

import (
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/numtide/go2nix/pkg/gofiles"
	"github.com/numtide/go2nix/pkg/localpkgs"
)

func TestOverrideImportCfgEntry(t *testing.T) {
	tests := []struct {
		name       string
		initial    string
		importPath string
		archive    string
		want       string
	}{
		{
			name:       "add new entry",
			initial:    "packagefile fmt=/nix/store/xxx/fmt.a\n",
			importPath: "example.com/foo",
			archive:    "/tmp/test/foo.a",
			want:       "packagefile fmt=/nix/store/xxx/fmt.a\npackagefile example.com/foo=/tmp/test/foo.a\n",
		},
		{
			name:       "replace existing entry",
			initial:    "packagefile example.com/foo=/old/path.a\npackagefile fmt=/nix/store/xxx/fmt.a\n",
			importPath: "example.com/foo",
			archive:    "/new/path.a",
			want:       "packagefile fmt=/nix/store/xxx/fmt.a\npackagefile example.com/foo=/new/path.a\n",
		},
		{
			name:       "no false match on prefix",
			initial:    "packagefile example.com/foobar=/keep.a\npackagefile example.com/foo=/old.a\n",
			importPath: "example.com/foo",
			archive:    "/new.a",
			want:       "packagefile example.com/foobar=/keep.a\npackagefile example.com/foo=/new.a\n",
		},
		{
			name:       "remove duplicates",
			initial:    "packagefile example.com/foo=/first.a\npackagefile example.com/foo=/second.a\n",
			importPath: "example.com/foo",
			archive:    "/override.a",
			want:       "packagefile example.com/foo=/override.a\n",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := filepath.Join(t.TempDir(), "importcfg")
			if err := os.WriteFile(f, []byte(tt.initial), 0o644); err != nil {
				t.Fatal(err)
			}
			if err := overrideImportCfgEntry(f, tt.importPath, tt.archive); err != nil {
				t.Fatal(err)
			}
			got, err := os.ReadFile(f)
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != tt.want {
				t.Errorf("got:\n%s\nwant:\n%s", got, tt.want)
			}
		})
	}
}

func TestTestLinkFlags(t *testing.T) {
	root := t.TempDir()
	for name, src := range map[string]string{
		"old/go.mod":      "module example.com/old\n\ngo 1.21\n",
		"old/p/p.go":      "package p\n",
		"old/p/p_test.go": "//go:debug panicnil=1\npackage p\n",
		"new/go.mod":      "module example.com/new\n\ngo 1.99\n",
		"new/p/p.go":      "package p\n",
	} {
		path := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(src), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("GOFIPS140", "")

	// go 1.21: httpmuxgo121=1 is a default with every toolchain from 1.22 on;
	// panicnil=1 comes from the _test.go file only.
	got := testLinkFlags(Options{ModuleRoot: filepath.Join(root, "old")},
		&localpkgs.LocalPkg{SrcDir: filepath.Join(root, "old/p")})
	if len(got) != 3 || got[0] != "-X" || got[1] != "testing.testBinary=1" {
		t.Fatalf("testLinkFlags = %v, want -X testing.testBinary=1 and one more flag", got)
	}
	godebug, ok := strings.CutPrefix(got[2], "-X=runtime.godebugDefault=")
	if !ok {
		t.Fatalf("testLinkFlags[2] = %q, want -X=runtime.godebugDefault=...", got[2])
	}
	for _, want := range []string{"httpmuxgo121=1", "panicnil=1"} {
		if !slices.Contains(strings.Split(godebug, ","), want) {
			t.Errorf("default GODEBUG %q lacks %s", godebug, want)
		}
	}

	// Nothing to default for a module newer than every table entry.
	got = testLinkFlags(Options{ModuleRoot: filepath.Join(root, "new")},
		&localpkgs.LocalPkg{SrcDir: filepath.Join(root, "new/p")})
	if want := []string{"-X", "testing.testBinary=1"}; !reflect.DeepEqual(got, want) {
		t.Errorf("testLinkFlags = %v, want %v", got, want)
	}
}

func TestInternalTestPkgFiles(t *testing.T) {
	// Regression for finding #3: the internal-test compile must carry the
	// package's CgoFiles/SFiles/SysoFiles/etc. through so CompileGoPackage
	// routes to the cgo/asm paths instead of the pure-Go -complete path.
	pkg := &localpkgs.LocalPkg{
		TestGoFiles: []string{"a_test.go"},
		PkgFiles: gofiles.PkgFiles{
			GoFiles:   []string{"a.go", "b.go"},
			CgoFiles:  []string{"cgo.go"},
			SFiles:    []string{"asm_amd64.s"},
			CFiles:    []string{"impl.c"},
			CXXFiles:  []string{"impl.cc"},
			HFiles:    []string{"impl.h"},
			FFiles:    []string{"impl.f90"},
			SysoFiles: []string{"blob.syso"},
		},
	}
	got := internalTestPkgFiles(pkg, nil)

	wantGo := []string{"a.go", "b.go", "a_test.go"}
	if !reflect.DeepEqual(got.GoFiles, wantGo) {
		t.Errorf("GoFiles = %v, want %v", got.GoFiles, wantGo)
	}
	if !reflect.DeepEqual(got.CgoFiles, []string{"cgo.go"}) {
		t.Errorf("CgoFiles = %v, want [cgo.go]", got.CgoFiles)
	}
	if !reflect.DeepEqual(got.SFiles, []string{"asm_amd64.s"}) {
		t.Errorf("SFiles = %v, want [asm_amd64.s]", got.SFiles)
	}
	if !reflect.DeepEqual(got.CFiles, []string{"impl.c"}) {
		t.Errorf("CFiles = %v, want [impl.c]", got.CFiles)
	}
	if !reflect.DeepEqual(got.CXXFiles, []string{"impl.cc"}) {
		t.Errorf("CXXFiles = %v, want [impl.cc]", got.CXXFiles)
	}
	if !reflect.DeepEqual(got.HFiles, []string{"impl.h"}) {
		t.Errorf("HFiles = %v, want [impl.h]", got.HFiles)
	}
	if !reflect.DeepEqual(got.FFiles, []string{"impl.f90"}) {
		t.Errorf("FFiles = %v, want [impl.f90]", got.FFiles)
	}
	if !reflect.DeepEqual(got.SysoFiles, []string{"blob.syso"}) {
		t.Errorf("SysoFiles = %v, want [blob.syso]", got.SysoFiles)
	}
}

func TestLangVersions(t *testing.T) {
	root := t.TempDir()
	write := func(rel, content string) {
		t.Helper()
		path := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("app/go.mod", "module example.com/app\n\ngo 1.21\n")
	write("app/internal/a/a.go", "package a\n")
	write("app/internal/skipped/s.go", "package skipped\n")
	write("sib/go.mod", "module example.com/sib\n\ngo 1.20.3\n")
	write("sib/util/u.go", "package util\n")

	pkgs := []*localpkgs.LocalPkg{
		{ImportPath: "example.com/app/internal/a", SrcDir: filepath.Join(root, "app/internal/a")},
		{ImportPath: "example.com/app/internal/skipped", SrcDir: filepath.Join(root, "app/internal/skipped")},
		{ImportPath: "example.com/sib/util", SrcDir: filepath.Join(root, "sib/util")},
	}
	got := langVersions(pkgs, func(ip string) bool { return ip != "example.com/app/internal/skipped" })
	want := map[string]string{
		"example.com/app/internal/a": "1.21",
		"example.com/sib/util":       "1.20",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("langVersions = %v, want %v", got, want)
	}
}

func TestApplySrcOverlay(t *testing.T) {
	srcDir := t.TempDir()
	overlay := t.TempDir()

	if err := os.WriteFile(filepath.Join(srcDir, "a.txt"), []byte("from-src"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(srcDir, "shared.txt"), []byte("from-src"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(overlay, "dist"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(overlay, "dist", "b.txt"), []byte("from-overlay"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(overlay, "shared.txt"), []byte("from-overlay"), 0o644); err != nil {
		t.Fatal(err)
	}

	p := &localpkgs.LocalPkg{ImportPath: "example.com/test", SrcDir: srcDir}
	if err := applySrcOverlay(p, overlay); err != nil {
		t.Fatal(err)
	}

	if p.SrcDir == srcDir {
		t.Fatal("SrcDir not repointed")
	}
	t.Cleanup(func() { _ = os.RemoveAll(p.SrcDir) })

	mustRead := func(rel string) string {
		t.Helper()
		b, err := os.ReadFile(filepath.Join(p.SrcDir, rel))
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	if got := mustRead("a.txt"); got != "from-src" {
		t.Errorf("a.txt = %q, want from-src (srcDir content preserved)", got)
	}
	if got := mustRead("dist/b.txt"); got != "from-overlay" {
		t.Errorf("dist/b.txt = %q, want from-overlay (overlay content layered in)", got)
	}
	if got := mustRead("shared.txt"); got != "from-overlay" {
		t.Errorf("shared.txt = %q, want from-overlay (overlay wins on conflict)", got)
	}

	// Result must be writable so the testrunner can drop generated files
	// (xtest internal-test recompile writes alongside the source).
	if err := os.WriteFile(filepath.Join(p.SrcDir, "a.txt"), []byte("mutated"), 0o644); err != nil {
		t.Errorf("overlaid SrcDir not writable: %v", err)
	}
}

func TestExternalLinker(t *testing.T) {
	t.Setenv("CC", "the-cc")
	t.Setenv("CXX", "the-cxx")
	if got := externalLinker(false); got != "the-cc" {
		t.Errorf("externalLinker(false) = %q, want $CC", got)
	}
	if got := externalLinker(true); got != "the-cxx" {
		t.Errorf("externalLinker(true) = %q, want $CXX", got)
	}
}

func TestSanitize(t *testing.T) {
	tests := []struct {
		input, want string
	}{
		{"example.com/foo/bar", "example_com_foo_bar"},
		{"simple", "simple"},
		{"a.b/c.d", "a_b_c_d"},
	}
	for _, tt := range tests {
		if got := sanitize(tt.input); got != tt.want {
			t.Errorf("sanitize(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestTestBinaryArgs(t *testing.T) {
	if got, want := testBinaryArgs(nil), []string{"-test.paniconexit0"}; !reflect.DeepEqual(got, want) {
		t.Errorf("testBinaryArgs(nil) = %v, want %v", got, want)
	}
	checkFlags := []string{"-test.v", "-test.run", "X"}
	got := testBinaryArgs(checkFlags)
	want := []string{"-test.paniconexit0", "-test.v", "-test.run", "X"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("testBinaryArgs = %v, want %v", got, want)
	}
	if !reflect.DeepEqual(checkFlags, []string{"-test.v", "-test.run", "X"}) {
		t.Errorf("checkFlags modified: %v", checkFlags)
	}
}
