package compile

import (
	"encoding/json"
	"errors"
	"go/build"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/numtide/go2nix/pkg/gofiles"
)

// cgoDirectiveSet is the part of `go list -json` (and of build.Package)
// that carries the #cgo directives.
type cgoDirectiveSet struct {
	CgoFiles     []string
	CgoCFLAGS    []string
	CgoCPPFLAGS  []string
	CgoCXXFLAGS  []string
	CgoFFLAGS    []string
	CgoLDFLAGS   []string
	CgoPkgConfig []string
}

// goListDirectives asks cmd/go for the directives of the package in dir.
func goListDirectives(t *testing.T, dir, goos, goarch string, tags []string) cgoDirectiveSet {
	t.Helper()
	cmd := exec.Command("go", "list", "-json=CgoFiles,CgoCFLAGS,CgoCPPFLAGS,CgoCXXFLAGS,CgoFFLAGS,CgoLDFLAGS,CgoPkgConfig", ".")
	cmd.Dir = dir
	cmd.Env = append(
		os.Environ(),
		"GOOS="+goos, "GOARCH="+goarch, "CGO_ENABLED=1",
		"GOFLAGS=-tags="+strings.Join(tags, ","),
		"GOWORK=off", "GOENV=off", "GOTOOLCHAIN=local",
		"GOCACHE="+t.TempDir(),
	)
	out, err := cmd.Output()
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			t.Fatalf("go list: %v\n%s", err, ee.Stderr)
		}
		t.Fatalf("go list: %v", err)
	}
	var got cgoDirectiveSet
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("go list output: %v\n%s", err, out)
	}
	return got
}

// Every case is checked twice: against the flags written in the table
// ($DIR stands for the package directory), and against what `go list`
// reports for the same directory, GOOS, GOARCH and tags.
func TestCgoDirectives(t *testing.T) {
	_, goErr := exec.LookPath("go")

	const conditions = `package p

/*
#cgo linux CFLAGS: -DON_LINUX
#cgo !linux CFLAGS: -DNOT_LINUX
#cgo darwin LDFLAGS: -framework CoreFoundation
#cgo linux,arm64 darwin LDFLAGS: -lother
#cgo linux,amd64 LDFLAGS: -lthis
#cgo unix CPPFLAGS: -DUNIX
*/
import "C"
`
	const tagged = `package p

// #cgo mytag pkg-config: libfoo
// #cgo !mytag CFLAGS: -DNO_TAG
import "C"
`

	tests := []struct {
		name string
		// files of the package directory, by name.
		files map[string]string
		// cgoFiles is the manifest's list.
		cgoFiles     []string
		goos, goarch string
		tags         []string
		// listTags are the tags `go list` needs to select cgoFiles, when
		// they are not tags.
		listTags []string
		want     cgoDirectiveSet
	}{
		{
			name: "every verb",
			files: map[string]string{"a.go": `package p

/*
#cgo CFLAGS: -DC=1
#cgo CPPFLAGS: -DCPP=1
#cgo CXXFLAGS: -std=c++17
#cgo FFLAGS: -ffree-form
#cgo LDFLAGS: -lm
#cgo pkg-config: foo --static
*/
import "C"
`},
			cgoFiles: []string{"a.go"},
			goos:     "linux",
			goarch:   "amd64",
			want: cgoDirectiveSet{
				CgoCFLAGS:    []string{"-DC=1"},
				CgoCPPFLAGS:  []string{"-DCPP=1"},
				CgoCXXFLAGS:  []string{"-std=c++17"},
				CgoFFLAGS:    []string{"-ffree-form"},
				CgoLDFLAGS:   []string{"-lm"},
				CgoPkgConfig: []string{"foo", "--static"},
			},
		},
		{
			name: "SRCDIR, relative paths and quoting",
			files: map[string]string{"a.go": `package p

// #cgo CFLAGS: -I${SRCDIR}/inc -Irel "-DMSG=a b"
// #cgo LDFLAGS: -L${SRCDIR}/lib -lfoo
import "C"
`},
			cgoFiles: []string{"a.go"},
			goos:     "linux",
			goarch:   "amd64",
			want: cgoDirectiveSet{
				CgoCFLAGS:  []string{"-I$DIR/inc", "-I$DIR/rel", "-DMSG=a b"},
				CgoLDFLAGS: []string{"-L$DIR/lib", "-lfoo"},
			},
		},
		{
			name:     "GOOS and GOARCH conditions",
			files:    map[string]string{"a.go": conditions},
			cgoFiles: []string{"a.go"},
			goos:     "linux",
			goarch:   "amd64",
			want: cgoDirectiveSet{
				CgoCFLAGS:   []string{"-DON_LINUX"},
				CgoCPPFLAGS: []string{"-DUNIX"},
				CgoLDFLAGS:  []string{"-lthis"},
			},
		},
		{
			name:     "the same file for another GOOS",
			files:    map[string]string{"a.go": conditions},
			cgoFiles: []string{"a.go"},
			goos:     "darwin",
			goarch:   "arm64",
			want: cgoDirectiveSet{
				CgoCFLAGS:   []string{"-DNOT_LINUX"},
				CgoCPPFLAGS: []string{"-DUNIX"},
				CgoLDFLAGS:  []string{"-framework", "CoreFoundation", "-lother"},
			},
		},
		{
			name: "android implies linux",
			files: map[string]string{"a.go": `package p

// #cgo linux LDFLAGS: -llog
import "C"
`},
			cgoFiles: []string{"a.go"},
			goos:     "android",
			goarch:   "arm64",
			want:     cgoDirectiveSet{CgoLDFLAGS: []string{"-llog"}},
		},
		{
			name:     "build tag unset",
			files:    map[string]string{"a.go": tagged},
			cgoFiles: []string{"a.go"},
			goos:     "linux",
			goarch:   "amd64",
			want:     cgoDirectiveSet{CgoCFLAGS: []string{"-DNO_TAG"}},
		},
		{
			name:     "build tag set",
			files:    map[string]string{"a.go": tagged},
			cgoFiles: []string{"a.go"},
			goos:     "linux",
			goarch:   "amd64",
			tags:     []string{"mytag"},
			want:     cgoDirectiveSet{CgoPkgConfig: []string{"libfoo"}},
		},
		{
			name: "release tags, cgo and gc",
			files: map[string]string{"a.go": `package p

/*
#cgo go1.20 CFLAGS: -DHAS_GO120
#cgo go1.9999 CFLAGS: -DHAS_FUTURE
#cgo cgo,gc CFLAGS: -DCGO_GC
*/
import "C"
`},
			cgoFiles: []string{"a.go"},
			goos:     "linux",
			goarch:   "amd64",
			want:     cgoDirectiveSet{CgoCFLAGS: []string{"-DHAS_GO120", "-DCGO_GC"}},
		},
		{
			name: "files in name order, grouped import, two import C",
			files: map[string]string{
				"b.go": `package p

import (
	// #cgo CFLAGS: -DB1
	"C"
)

// #cgo CFLAGS: -DB2
import "C"
`,
				"a.go": `package p

// #cgo CFLAGS: -DA
import "C"
`,
			},
			cgoFiles: []string{"b.go", "a.go"},
			goos:     "linux",
			goarch:   "amd64",
			want:     cgoDirectiveSet{CgoCFLAGS: []string{"-DA", "-DB1", "-DB2"}},
		},
		{
			name: "files outside the manifest are not read",
			files: map[string]string{
				"a.go": `package p

// #cgo CFLAGS: -DA
import "C"
`,
				"other_windows.go": `package p

// #cgo LDFLAGS: -lws2_32
import "C"
`,
				"opt.go": `//go:build opt

package p

// #cgo CFLAGS: -DOPT
import "C"
`,
				"plain.go": "package p\n",
			},
			cgoFiles: []string{"a.go"},
			goos:     "linux",
			goarch:   "amd64",
			want:     cgoDirectiveSet{CgoCFLAGS: []string{"-DA"}},
		},
		{
			// The experimental builder's manifest has the files that
			// `go list -tags=opt` selected, and no tags.
			name: "file constraints are not evaluated again",
			files: map[string]string{
				"a_darwin.go": `package p

// #cgo CFLAGS: -DNAME
import "C"
`,
				"opt.go": `//go:build opt

package p

// #cgo CFLAGS: -DOPT
import "C"
`,
			},
			cgoFiles: []string{"a_darwin.go", "opt.go"},
			goos:     "darwin",
			goarch:   "arm64",
			listTags: []string{"opt"},
			want:     cgoDirectiveSet{CgoCFLAGS: []string{"-DNAME", "-DOPT"}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.com/p\n\ngo 1.22\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			for name, src := range tt.files {
				if err := os.WriteFile(filepath.Join(dir, name), []byte(src), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			expand := func(flags []string) []string {
				var out []string
				for _, f := range flags {
					out = append(out, strings.ReplaceAll(f, "$DIR", dir))
				}
				return out
			}

			pkg, err := cgoDirectives(Options{SrcDir: dir, Tags: tt.tags, goos: tt.goos, goarch: tt.goarch}, tt.cgoFiles)
			if err != nil {
				t.Fatal(err)
			}
			got := cgoDirectiveSet{
				CgoCFLAGS: pkg.CgoCFLAGS, CgoCPPFLAGS: pkg.CgoCPPFLAGS, CgoCXXFLAGS: pkg.CgoCXXFLAGS,
				CgoFFLAGS: pkg.CgoFFLAGS, CgoLDFLAGS: pkg.CgoLDFLAGS, CgoPkgConfig: pkg.CgoPkgConfig,
			}
			want := tt.want
			want.CgoCFLAGS, want.CgoLDFLAGS = expand(want.CgoCFLAGS), expand(want.CgoLDFLAGS)
			if !reflect.DeepEqual(got, want) {
				t.Errorf("cgoDirectives:\n got %+v\nwant %+v", got, want)
			}

			if goErr != nil {
				t.Skip("go not found: not compared with go list")
			}
			listTags := tt.listTags
			if listTags == nil {
				listTags = tt.tags
			}
			list := goListDirectives(t, dir, tt.goos, tt.goarch, listTags)
			if want := slices.Sorted(slices.Values(tt.cgoFiles)); !slices.Equal(list.CgoFiles, want) {
				t.Fatalf("go list selects %v, the case lists %v", list.CgoFiles, want)
			}
			list.CgoFiles = nil
			if !reflect.DeepEqual(got, list) {
				t.Errorf("differs from go list:\n     got %+v\ngo list %+v", got, list)
			}
		})
	}
}

// build.Default has cgo off under CGO_ENABLED=0 and in a cross build that
// does not set CGO_ENABLED; a manifest with CgoFiles still means cgo is on.
func TestCgoDirectives_CgoOffInBuildDefault(t *testing.T) {
	old := build.Default.CgoEnabled
	build.Default.CgoEnabled = false
	t.Cleanup(func() { build.Default.CgoEnabled = old })

	dir := t.TempDir()
	src := "package p\n\n// #cgo cgo CFLAGS: -DCGO\nimport \"C\"\n"
	if err := os.WriteFile(filepath.Join(dir, "a.go"), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	pkg, err := cgoDirectives(Options{SrcDir: dir, goos: "linux", goarch: "amd64"}, []string{"a.go"})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(pkg.CgoCFLAGS, []string{"-DCGO"}) {
		t.Errorf("CgoCFLAGS = %q, want [-DCGO]", pkg.CgoCFLAGS)
	}
}

func TestCgoDirectives_Errors(t *testing.T) {
	tests := []struct {
		name string
		src  string
		want string
	}{
		{"unknown verb", "package p\n\n// #cgo CFLAG: -DX\nimport \"C\"\n", "invalid #cgo verb"},
		{"no colon", "package p\n\n// #cgo CFLAGS -DX\nimport \"C\"\n", "invalid #cgo line"},
		{"unsafe argument", "package p\n\n// #cgo CFLAGS: -DX=`id`\nimport \"C\"\n", "malformed #cgo argument"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, "a.go"), []byte(tt.src), 0o644); err != nil {
				t.Fatal(err)
			}
			_, err := cgoDirectives(Options{SrcDir: dir, goos: "linux", goarch: "amd64"}, []string{"a.go"})
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %v, want one mentioning %q", err, tt.want)
			}
		})
	}
}

// The order within each set is cmd/go's (Builder.CFlags, then runCgo):
// environment or default, the package's directives, pkg-config.
func TestNewCgoFlags(t *testing.T) {
	dir := t.TempDir()
	src := `package p

/*
#cgo CFLAGS: -DC
#cgo CPPFLAGS: -DCPP
#cgo CXXFLAGS: -DCXX
#cgo FFLAGS: -DF
#cgo LDFLAGS: -lpkg
*/
import "C"
`
	if err := os.WriteFile(filepath.Join(dir, "a.go"), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	pkg, err := cgoDirectives(Options{SrcDir: dir, goos: "linux", goarch: "amd64"}, []string{"a.go"})
	if err != nil {
		t.Fatal(err)
	}
	pcCflags := []string{"-I/pc/include", "-DPC"}
	pcLdflags := []string{"-L/pc/lib", "-lpc"}

	tests := []struct {
		name string
		env  map[string]string
		want cgoFlags
	}{
		{
			name: "defaults",
			want: cgoFlags{
				cpp: []string{"-DCPP", "-I/pc/include", "-DPC"},
				c:   []string{"-O2", "-g", "-DC"},
				cxx: []string{"-O2", "-g", "-DCXX"},
				f:   []string{"-O2", "-g", "-DF"},
				ld:  []string{"-O2", "-g", "-lpkg", "-L/pc/lib", "-lpc"},
			},
		},
		{
			name: "the environment replaces the defaults",
			env: map[string]string{
				"CGO_CPPFLAGS": "-DENV_CPP",
				"CGO_CFLAGS":   "-O0  -DENV_C",
				"CGO_CXXFLAGS": "-DENV_CXX",
				"CGO_FFLAGS":   "-DENV_F",
				"CGO_LDFLAGS":  "-lenv -lstdc++",
			},
			want: cgoFlags{
				cpp: []string{"-DENV_CPP", "-DCPP", "-I/pc/include", "-DPC"},
				c:   []string{"-O0", "-DENV_C", "-DC"},
				cxx: []string{"-DENV_CXX", "-DCXX"},
				f:   []string{"-DENV_F", "-DF"},
				ld:  []string{"-lenv", "-lstdc++", "-lpkg", "-L/pc/lib", "-lpc"},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for _, k := range []string{"CGO_CPPFLAGS", "CGO_CFLAGS", "CGO_CXXFLAGS", "CGO_FFLAGS", "CGO_LDFLAGS"} {
				t.Setenv(k, tt.env[k])
			}
			got := newCgoFlags(pkg, pcCflags, pcLdflags)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("newCgoFlags:\n got %+v\nwant %+v", got, tt.want)
			}
		})
	}
}

// A // #cgo <tag> pkg-config: line reaches pkg-config only when the
// manifest's tags satisfy it.
func TestPkgConfigFlagsCustomTag(t *testing.T) {
	srcDir := t.TempDir()
	src := "package foo\n\n// #cgo mytag pkg-config: libfoo\nimport \"C\"\n"
	if err := os.WriteFile(filepath.Join(srcDir, "a.go"), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}

	// Stub pkg-config so the test doesn't depend on the real binary or libfoo.
	binDir := t.TempDir()
	stub := "#!/bin/sh\n[ \"$2\" = libfoo ] || exit 1\ncase \"$1\" in --cflags) echo -I/stub;; --libs) echo -lstub;; esac\n"
	if err := os.WriteFile(filepath.Join(binDir, "pkg-config"), []byte(stub), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	resolve := func(tags []string) (cflags, ldflags []string) {
		t.Helper()
		pkg, err := cgoDirectives(Options{SrcDir: srcDir, Tags: tags, goos: "linux", goarch: "amd64"}, []string{"a.go"})
		if err != nil {
			t.Fatal(err)
		}
		cflags, ldflags, err = pkgConfigFlags(pkg.CgoPkgConfig)
		if err != nil {
			t.Fatalf("tags=%v: %v", tags, err)
		}
		return cflags, ldflags
	}

	// Without the tag, the constraint fails and pkg-config is not invoked.
	if cflags, ldflags := resolve(nil); cflags != nil || ldflags != nil {
		t.Fatalf("tags=nil: want (nil,nil), got (%v,%v)", cflags, ldflags)
	}

	// With the tag, the directive matches and pkg-config is invoked.
	cflags, ldflags := resolve([]string{"mytag"})
	if got, want := strings.Join(cflags, " "), "-I/stub"; got != want {
		t.Errorf("cflags = %q, want %q", got, want)
	}
	if got, want := strings.Join(ldflags, " "), "-lstub"; got != want {
		t.Errorf("ldflags = %q, want %q", got, want)
	}
}

// stdImportcfg returns an importcfg with the export data of pkgs and of
// what they import, or skips the test when the toolchain cannot build it.
func stdImportcfg(t *testing.T, pkgs ...string) string {
	t.Helper()
	args := append([]string{"list", "-deps", "-export", "-f", "{{if .Export}}packagefile {{.ImportPath}}={{.Export}}{{end}}"}, pkgs...)
	cmd := exec.Command("go", args...)
	cmd.Env = append(os.Environ(), "CGO_ENABLED=1", "GOFLAGS=", "GOWORK=off")
	out, err := cmd.Output()
	if err != nil {
		t.Skipf("go list -export %v: %v", pkgs, err)
	}
	path := filepath.Join(t.TempDir(), "importcfg")
	if err := os.WriteFile(path, out, 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// writeTree writes files, by slash-separated name, below a new directory.
func writeTree(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, data := range files {
		path := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// compileToArchive runs CompileGoPackage on the package in src and returns
// the archive it wrote.
func compileToArchive(t *testing.T, src, importcfg string, files gofiles.PkgFiles) string {
	t.Helper()
	work := t.TempDir()
	output := filepath.Join(work, "out", "p.a")
	err := CompileGoPackage(Options{
		ImportPath: "example.com/p",
		SrcDir:     src,
		Output:     output,
		ImportCfg:  importcfg,
		TrimPath:   work,
		GoVersion:  "1.22",
		Files:      &files,
	})
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(output)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func cgoToolsOrSkip(t *testing.T, tools ...string) {
	t.Helper()
	for _, tool := range tools {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s not found", tool)
		}
	}
}

// The C, C++ and cgo-generated sources carry #error unless each gets the
// flags of the #cgo lines and of the CGO_* environment that are meant for
// it, CPPFLAGS first. The Fortran compiler is a script that records its
// arguments.
func TestCompileGoPackage_CgoFlagsReachEveryCompiler(t *testing.T) {
	cc := envOrDefault("CC", "gcc")
	cgoToolsOrSkip(t, "go", "sh", cc, envOrDefault("CXX", "g++"))
	importcfg := stdImportcfg(t, "runtime/cgo", "syscall")

	const check = `
#if !defined(FROM_CFLAGS) || !defined(FROM_CPPFLAGS) || !defined(FROM_ENV) || defined(ORDER)
#error "wrong flags for C"
#endif
`
	src := writeTree(t, map[string]string{
		"a.go": `package p

/*
#cgo CFLAGS: -DFROM_CFLAGS -UORDER -I${SRCDIR}/inc
#cgo CPPFLAGS: -DFROM_CPPFLAGS -DORDER
#cgo CXXFLAGS: -DFROM_CXXFLAGS -UORDER
#cgo FFLAGS: -DFROM_FFLAGS
#include "hdr.h"
` + check + `
int in_c(void);
int in_cxx(void);
*/
import "C"

func F() int { return int(C.in_c() + C.in_cxx() + C.HDR) }
`,
		"inc/hdr.h": "#define HDR 1\n",
		"c.c":       check + "int in_c(void) { return 1; }\n",
		"x.cc": `#if !defined(FROM_CXXFLAGS) || !defined(FROM_CPPFLAGS) || !defined(FROM_ENV) || defined(FROM_CFLAGS) || defined(ORDER)
#error "wrong flags for C++"
#endif
extern "C" int in_cxx(void) { return 1; }
`,
		"f.f": "      end\n",
		"fc": `#!/bin/sh
printf '%s\n' "$@" > "$FC_ARGS"
while [ $# -gt 1 ]; do
	[ "$1" = -o ] && out=$2
	shift
done
exec "$FC_CC" -c -x c /dev/null -o "$out"
`,
	})
	if err := os.Chmod(filepath.Join(src, "fc"), 0o755); err != nil {
		t.Fatal(err)
	}
	fcArgs := filepath.Join(t.TempDir(), "args")
	t.Setenv("FC", filepath.Join(src, "fc"))
	t.Setenv("FC_ARGS", fcArgs)
	t.Setenv("FC_CC", cc)
	t.Setenv("CGO_CPPFLAGS", "-DFROM_ENV")
	for _, k := range []string{"CGO_CFLAGS", "CGO_CXXFLAGS", "CGO_FFLAGS", "CGO_LDFLAGS"} {
		t.Setenv(k, "")
	}

	compileToArchive(t, src, importcfg, gofiles.PkgFiles{
		CgoFiles: []string{"a.go"}, CFiles: []string{"c.c"}, CXXFiles: []string{"x.cc"}, FFiles: []string{"f.f"},
	})

	data, err := os.ReadFile(fcArgs)
	if err != nil {
		t.Fatal(err)
	}
	args := strings.Split(strings.TrimSpace(string(data)), "\n")
	i := slices.Index(args, "-pthread")
	if i < 0 || len(args) < i+4 {
		t.Fatalf("Fortran compiler arguments: %q", args)
	}
	if !strings.HasPrefix(args[i+1], "-ffile-prefix-map=") || args[i+2] != "-ffile-prefix-map="+src+"=." || args[i+3] != "-gno-record-gcc-switches" {
		t.Errorf("Fortran compiler does not get the path-mapping flags first: %q", args[i+1:])
	}
	got := args[i+4:]
	want := []string{"-DFROM_ENV", "-DFROM_CPPFLAGS", "-DORDER", "-O2", "-g", "-DFROM_FFLAGS", filepath.Join(src, "f.f"), "-o"}
	if len(got) != len(want)+1 || !slices.Equal(got[:len(want)], want) {
		t.Errorf("Fortran compiler arguments after the path-mapping flags:\n got %q\nwant %q and the object", got, want)
	}
}

// A C-only package, so that the test link runs $CC, which does not add
// libm by itself as $CXX does: it succeeds only with #cgo LDFLAGS: -lm.
func TestCompileGoPackage_CgoLdflags(t *testing.T) {
	cgoToolsOrSkip(t, "go", envOrDefault("CC", "gcc"))
	importcfg := stdImportcfg(t, "runtime/cgo", "syscall")

	src := writeTree(t, map[string]string{
		"a.go": `package p

/*
#cgo LDFLAGS: -lm
#cgo !linux,!darwin,!freebsd LDFLAGS: -lno_such_library
#include <math.h>
static double gamma_(double x) { return tgamma(x); }
*/
import "C"

func F(x float64) float64 { return float64(C.gamma_(C.double(x))) }
`,
	})
	t.Setenv("CGO_LDFLAGS", "-lpthread")

	archive := compileToArchive(t, src, importcfg, gofiles.PkgFiles{CgoFiles: []string{"a.go"}})
	if strings.Contains(archive, "dynimportfail") {
		t.Error("the test link failed: archive has a dynimportfail member")
	}
	for _, flag := range []string{"-lpthread", "-lm"} {
		if n := strings.Count(archive, `"cgo_ldflag","`+flag+`"`); n != 1 {
			t.Errorf("archive records linker flag %s %d times, want 1", flag, n)
		}
	}
	if strings.Contains(archive, "-lno_such_library") {
		t.Error("archive records a linker flag whose GOOS condition is false")
	}
}

// Without -lm the test link fails and the package gets a dynimportfail
// member either way; the linker's complaint is shown unless the package
// has .syso files.
func TestCompileGoPackage_CgoTestLinkOutput(t *testing.T) {
	cgoToolsOrSkip(t, "go", envOrDefault("CC", "gcc"))
	importcfg := stdImportcfg(t, "runtime/cgo", "syscall")

	src := writeTree(t, map[string]string{
		"a.go": `package p

/*
#include <math.h>
static double gamma_(double x) { return tgamma(x); }
*/
import "C"

func F(x float64) float64 { return float64(C.gamma_(C.double(x))) }
`,
		"blob.syso": "xxx",
	})
	t.Setenv("CGO_LDFLAGS", "")

	for _, tt := range []struct {
		name  string
		syso  []string
		shown bool
	}{
		{"no syso", nil, true},
		{"syso", []string{"blob.syso"}, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			stderr, err := os.CreateTemp(t.TempDir(), "stderr")
			if err != nil {
				t.Fatal(err)
			}
			old := os.Stderr
			os.Stderr = stderr
			t.Cleanup(func() { os.Stderr = old })
			archive := compileToArchive(t, src, importcfg, gofiles.PkgFiles{CgoFiles: []string{"a.go"}, SysoFiles: tt.syso})
			os.Stderr = old

			if !strings.Contains(archive, "dynimportfail") {
				t.Fatal("archive has no dynimportfail member: the test link did not fail")
			}
			out, err := os.ReadFile(stderr.Name())
			if err != nil {
				t.Fatal(err)
			}
			if shown := strings.Contains(string(out), "tgamma"); shown != tt.shown {
				t.Errorf("linker output on stderr: %v, want %v\n%s", shown, tt.shown, out)
			}
		})
	}
}
