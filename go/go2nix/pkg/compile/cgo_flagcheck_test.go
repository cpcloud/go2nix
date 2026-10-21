package compile

import (
	"fmt"
	"go/build"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/numtide/go2nix/pkg/gofiles"
)

var cgoFlagVerbs = []string{"CPPFLAGS", "CFLAGS", "CXXFLAGS", "FFLAGS", "LDFLAGS"}

// clearCgoEnv empties every variable newCgoFlags and the flag checks read.
func clearCgoEnv(t *testing.T) {
	t.Helper()
	for _, verb := range cgoFlagVerbs {
		t.Setenv("CGO_"+verb, "")
		t.Setenv("CGO_"+verb+"_ALLOW", "")
		t.Setenv("CGO_"+verb+"_DISALLOW", "")
	}
}

func invalidFlag(source, flag string) string {
	return "invalid flag in " + source + ": " + flag + " (see https://go.dev/s/invalidflag)"
}

// directivesOf returns go/build's view of a one-file package whose preamble
// is the given #cgo lines.
func directivesOf(t *testing.T, lines ...string) *build.Package {
	t.Helper()
	dir := t.TempDir()
	src := "package p\n\n/*\n" + strings.Join(lines, "\n") + "\n*/\nimport \"C\"\n"
	if err := os.WriteFile(filepath.Join(dir, "a.go"), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	pkg, err := cgoDirectives(Options{SrcDir: dir, goos: "linux", goarch: "amd64"}, []string{"a.go"})
	if err != nil {
		t.Fatal(err)
	}
	return pkg
}

// Each kind of #cgo line is checked against cmd/go's lists under its own
// name, and CGO_<name>_ALLOW and CGO_<name>_DISALLOW act on that kind only.
func TestNewCgoFlags_ChecksDirectives(t *testing.T) {
	for _, verb := range cgoFlagVerbs {
		refused, listed := "-fplugin=/x.so", "-DLISTED"
		if verb == "LDFLAGS" {
			refused, listed = "-fuse-ld=/x", "-llisted"
		}
		other := "CFLAGS"
		if verb == "CFLAGS" {
			other = "CPPFLAGS"
		}
		set := func(f cgoFlags) []string {
			return map[string][]string{"CPPFLAGS": f.cpp, "CFLAGS": f.c, "CXXFLAGS": f.cxx, "FFLAGS": f.f, "LDFLAGS": f.ld}[verb]
		}

		for _, tt := range []struct {
			name    string
			flag    string
			env     map[string]string
			wantErr string
		}{
			{name: "refused", flag: refused, wantErr: invalidFlag("#cgo "+verb, refused)},
			{name: "listed", flag: listed},
			{name: "allowed", flag: refused, env: map[string]string{"CGO_" + verb + "_ALLOW": "-f(plugin|use-ld)=.*"}},
			{
				name: "allowed in part", flag: refused, env: map[string]string{"CGO_" + verb + "_ALLOW": "-f(plugin|use-ld)="},
				wantErr: invalidFlag("#cgo "+verb, refused),
			},
			{
				name: "allowed for another kind", flag: refused, env: map[string]string{"CGO_" + other + "_ALLOW": ".*"},
				wantErr: invalidFlag("#cgo "+verb, refused),
			},
			{
				name: "disallowed", flag: listed, env: map[string]string{"CGO_" + verb + "_DISALLOW": "-(D|l)(LISTED|listed)"},
				wantErr: invalidFlag("#cgo "+verb, listed),
			},
			{name: "disallowed for another kind", flag: listed, env: map[string]string{"CGO_" + other + "_DISALLOW": ".*"}},
			{
				name: "disallowed and allowed", flag: refused,
				env:     map[string]string{"CGO_" + verb + "_ALLOW": ".*", "CGO_" + verb + "_DISALLOW": "-f(plugin|use-ld)=.*"},
				wantErr: invalidFlag("#cgo "+verb, refused),
			},
			{
				name: "malformed", flag: listed, env: map[string]string{"CGO_" + verb + "_ALLOW": "("},
				wantErr: "parsing $CGO_" + verb + "_ALLOW: error parsing regexp: missing closing ): `(`",
			},
		} {
			t.Run(verb+"/"+tt.name, func(t *testing.T) {
				clearCgoEnv(t)
				for k, v := range tt.env {
					t.Setenv(k, v)
				}
				pkg := directivesOf(t, "#cgo "+verb+": "+tt.flag)
				got, err := newCgoFlags(pkg, nil, nil)
				if tt.wantErr != "" {
					if err == nil || err.Error() != tt.wantErr {
						t.Fatalf("newCgoFlags: error %v, want %s", err, tt.wantErr)
					}
					return
				}
				if err != nil {
					t.Fatal(err)
				}
				if !slices.Contains(set(got), tt.flag) {
					t.Errorf("%s is not among the %s: %q", tt.flag, verb, set(got))
				}
			})
		}
	}
}

// -lto_library makes the macOS linker load its argument; it would pass as
// -l<name> were it not on the list of invalid flags.
func TestNewCgoFlags_InvalidLinkerFlag(t *testing.T) {
	clearCgoEnv(t)
	pkg := directivesOf(t, "#cgo LDFLAGS: -lto_library /x.dylib")
	_, err := newCgoFlags(pkg, nil, nil)
	if want := invalidFlag("#cgo LDFLAGS", "-lto_library"); err == nil || err.Error() != want {
		t.Errorf("newCgoFlags: error %v, want %s", err, want)
	}
}

// cmd/go trusts the CGO_* environment: its values are neither checked
// against the lists nor against CGO_*_DISALLOW.
func TestNewCgoFlags_EnvironmentIsNotChecked(t *testing.T) {
	clearCgoEnv(t)
	pkg := directivesOf(t, "#cgo CFLAGS: -DFROM_PACKAGE")
	want := map[string][]string{}
	for _, verb := range cgoFlagVerbs {
		flags := []string{"-fplugin=/env.so", "-specs=/env.specs", "@/env.rsp", "-Wl,--plugin,/env.so", "-D" + verb}
		t.Setenv("CGO_"+verb, strings.Join(flags, " "))
		t.Setenv("CGO_"+verb+"_DISALLOW", "-D"+verb)
		want[verb] = flags
	}
	got, err := newCgoFlags(pkg, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	want["CFLAGS"] = append(want["CFLAGS"], "-DFROM_PACKAGE")
	for verb, set := range map[string][]string{"CPPFLAGS": got.cpp, "CFLAGS": got.c, "CXXFLAGS": got.cxx, "FFLAGS": got.f, "LDFLAGS": got.ld} {
		if !slices.Equal(set, want[verb]) {
			t.Errorf("%s:\n got %q\nwant %q", verb, set, want[verb])
		}
	}
}

// stubPkgConfig puts a pkg-config on PATH that appends its arguments to the
// returned file and prints $PC_CFLAGS or $PC_LIBS.
func stubPkgConfig(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	log := filepath.Join(dir, "calls")
	stub := `#!/bin/sh
echo "$*" >> "` + log + `"
case "$1" in --cflags) echo "$PC_CFLAGS";; --libs) echo "$PC_LIBS";; esac
`
	if err := os.WriteFile(filepath.Join(dir, "pkg-config"), []byte(stub), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return log
}

// What a #cgo pkg-config: line names is checked before pkg-config runs, and
// what pkg-config prints is checked like the package's own flags, --cflags
// as CFLAGS and --libs as LDFLAGS.
func TestPkgConfigFlags_Checks(t *testing.T) {
	for _, tt := range []struct {
		name          string
		args          []string
		cflags, libs  string
		env           map[string]string
		wantErr       string
		wantCalls     []string
		wantCflags    []string
		wantLibs      []string
		wantNotCalled bool
	}{
		{
			name: "options first, then --, then names", args: []string{"foo", "--static", "--", "bar"},
			cflags: "-I/inc -DX=1", libs: "-L/lib -lfoo",
			wantCalls:  []string{"--cflags --static -- foo bar", "--libs --static -- foo bar"},
			wantCflags: []string{"-I/inc", "-DX=1"}, wantLibs: []string{"-L/lib", "-lfoo"},
		},
		{
			name: "name that reads as an option", args: []string{"foo", "-bar"},
			wantErr: "invalid pkg-config package name: -bar", wantNotCalled: true,
		},
		{
			name: "name that reads as a response file", args: []string{"@foo"},
			wantErr: "invalid pkg-config package name: @foo", wantNotCalled: true,
		},
		{
			name: "option outside the list", args: []string{"--exec-prefix=/x", "foo"},
			wantErr: invalidFlag("pkg-config", "--exec-prefix=/x"), wantNotCalled: true,
		},
		{
			name: "option outside the list, allowed for CFLAGS", args: []string{"--exec-prefix=/x", "foo"},
			env:     map[string]string{"CGO_CFLAGS_ALLOW": ".*", "CGO_LDFLAGS_ALLOW": ".*"},
			wantErr: invalidFlag("pkg-config", "--exec-prefix=/x"), wantNotCalled: true,
		},
		{
			name: "refused --cflags", args: []string{"foo"}, cflags: "-I/inc -fplugin=/x.so", libs: "-lfoo",
			wantErr: invalidFlag("pkg-config --cflags", "-fplugin=/x.so"),
		},
		{
			name: "refused --cflags, allowed", args: []string{"foo"}, cflags: "-I/inc -fplugin=/x.so", libs: "-lfoo",
			env:        map[string]string{"CGO_CFLAGS_ALLOW": "-fplugin=.*"},
			wantCflags: []string{"-I/inc", "-fplugin=/x.so"}, wantLibs: []string{"-lfoo"},
		},
		{
			name: "refused --cflags, allowed as CPPFLAGS", args: []string{"foo"}, cflags: "-fplugin=/x.so",
			env:     map[string]string{"CGO_CPPFLAGS_ALLOW": ".*"},
			wantErr: invalidFlag("pkg-config --cflags", "-fplugin=/x.so"),
		},
		{
			name: "listed --cflags, disallowed", args: []string{"foo"}, cflags: "-DX=1",
			env:     map[string]string{"CGO_CFLAGS_DISALLOW": "-DX=.*"},
			wantErr: invalidFlag("pkg-config --cflags", "-DX=1"),
		},
		{
			name: "refused --libs", args: []string{"foo"}, cflags: "-I/inc", libs: "-lfoo -fuse-ld=/x",
			wantErr: invalidFlag("pkg-config --libs", "-fuse-ld=/x"),
		},
		{
			name: "refused --libs, allowed", args: []string{"foo"}, cflags: "-I/inc", libs: "-lfoo -fuse-ld=/x",
			env:        map[string]string{"CGO_LDFLAGS_ALLOW": "-fuse-ld=.*"},
			wantCflags: []string{"-I/inc"}, wantLibs: []string{"-lfoo", "-fuse-ld=/x"},
		},
		{
			name: "refused --libs, allowed as CFLAGS", args: []string{"foo"}, libs: "-fuse-ld=/x",
			env:     map[string]string{"CGO_CFLAGS_ALLOW": ".*"},
			wantErr: invalidFlag("pkg-config --libs", "-fuse-ld=/x"),
		},
		{
			name: "listed --libs, disallowed", args: []string{"foo"}, libs: "-lfoo",
			env:     map[string]string{"CGO_LDFLAGS_DISALLOW": "-lfoo"},
			wantErr: invalidFlag("pkg-config --libs", "-lfoo"),
		},
		{
			name: "--cflags that are linker flags only", args: []string{"foo"}, cflags: "-lfoo",
			wantErr: invalidFlag("pkg-config --cflags", "-lfoo"),
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			clearCgoEnv(t)
			for k, v := range tt.env {
				t.Setenv(k, v)
			}
			t.Setenv("PC_CFLAGS", tt.cflags)
			t.Setenv("PC_LIBS", tt.libs)
			log := stubPkgConfig(t)

			cflags, libs, err := pkgConfigFlags(tt.args)
			calls, _ := os.ReadFile(log)
			if tt.wantNotCalled && len(calls) > 0 {
				t.Errorf("pkg-config was run:\n%s", calls)
			}
			if tt.wantErr != "" {
				if err == nil || err.Error() != tt.wantErr {
					t.Fatalf("pkgConfigFlags: error %v, want %s", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(cflags, tt.wantCflags) || !slices.Equal(libs, tt.wantLibs) {
				t.Errorf("pkgConfigFlags: %q %q, want %q %q", cflags, libs, tt.wantCflags, tt.wantLibs)
			}
			if tt.wantCalls != nil {
				if got := strings.Split(strings.TrimSpace(string(calls)), "\n"); !slices.Equal(got, tt.wantCalls) {
					t.Errorf("pkg-config was run with\n%q\nwant\n%q", got, tt.wantCalls)
				}
			}
		})
	}
}

func TestCheckCgoLdflagPragmas(t *testing.T) {
	passed := []string{"-fuse-ld=/from-environment", "-lm"}
	gotypes := func(flags ...string) string {
		var b strings.Builder
		b.WriteString("package p\n\n")
		for _, f := range flags {
			fmt.Fprintf(&b, "//go:cgo_ldflag %q\n", f)
		}
		b.WriteString("\nvar x = 1 // not the first comment of its line: //go:cgo_ldflag \"-fplugin=/x.so\"\n")
		return b.String()
	}

	for _, tt := range []struct {
		name    string
		files   map[string]string
		passed  []string
		env     map[string]string
		wantErr string
	}{
		{name: "the flags cgo was given", passed: passed, files: map[string]string{"_cgo_gotypes.go": gotypes(passed...)}},
		{
			name: "one more", passed: passed,
			files:   map[string]string{"_cgo_gotypes.go": gotypes(append(slices.Clone(passed), "-fplugin=/x.so")...)},
			wantErr: invalidFlag("go:cgo_ldflag", "-fplugin=/x.so"),
		},
		{
			name: "one more, listed", passed: passed,
			files: map[string]string{"_cgo_gotypes.go": gotypes(append(slices.Clone(passed), "-lz")...)},
		},
		{
			name: "one more, in another generated file", passed: passed,
			files: map[string]string{
				"_cgo_gotypes.go":  gotypes(passed...),
				"_cgo_import_p.go": "package p\n//go:cgo_ldflag \"-fplugin=/x.so\"\n",
			},
			wantErr: invalidFlag("go:cgo_ldflag", "-fplugin=/x.so"),
		},
		{
			name: "one more, allowed", passed: passed, env: map[string]string{"CGO_LDFLAGS_ALLOW": "-fplugin=.*"},
			files: map[string]string{"_cgo_gotypes.go": gotypes(append(slices.Clone(passed), "-fplugin=/x.so")...)},
		},
		{
			name: "a file the compiler takes no cgo_ldflag from", passed: passed,
			files: map[string]string{
				"_cgo_gotypes.go": gotypes(passed...),
				"a.cgo1.go":       "package p\n//go:cgo_ldflag \"-fplugin=/x.so\"\n",
			},
		},
		{
			name: "not all of the flags cgo was given", passed: passed,
			files:   map[string]string{"_cgo_gotypes.go": gotypes(passed[0])},
			wantErr: invalidFlag("go:cgo_ldflag", passed[0]),
		},
		{name: "none given, none found", files: map[string]string{"_cgo_gotypes.go": gotypes()}},
		{
			name:    "none given, one found",
			files:   map[string]string{"_cgo_gotypes.go": gotypes("-fplugin=/x.so")},
			wantErr: invalidFlag("go:cgo_ldflag", "-fplugin=/x.so"),
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			clearCgoEnv(t)
			for k, v := range tt.env {
				t.Setenv(k, v)
			}
			dir := writeTree(t, tt.files)
			var files []string
			for name := range tt.files {
				files = append(files, filepath.Join(dir, name))
			}
			slices.Sort(files)
			err := checkCgoLdflagPragmas(files, tt.passed)
			if tt.wantErr == "" && err != nil || tt.wantErr != "" && (err == nil || err.Error() != tt.wantErr) {
				t.Errorf("checkCgoLdflagPragmas: error %v, want %q", err, tt.wantErr)
			}
		})
	}
}

// archiveMembers lists the member names of an ar archive.
func archiveMembers(t *testing.T, archive string) []string {
	t.Helper()
	var names []string
	for rest := strings.TrimPrefix(archive, "!<arch>\n"); len(rest) >= 60; {
		size, err := strconv.Atoi(strings.TrimSpace(rest[48:58]))
		if err != nil {
			t.Fatalf("archive member header %q: %v", rest[:60], err)
		}
		names = append(names, strings.TrimRight(rest[:16], " /"))
		rest = rest[min(len(rest), 60+size+size%2):]
	}
	return names
}

// compileErr runs CompileGoPackage on the package in src and returns its
// error and the archive, if one was written.
func compileErr(t *testing.T, src, importcfg string, files gofiles.PkgFiles) (string, error) {
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
	data, _ := os.ReadFile(output)
	return string(data), err
}

// A refused flag fails the package with cmd/go's message, before any tool
// has seen it.
func TestCompileGoPackage_CgoFlagRefused(t *testing.T) {
	cgoToolsOrSkip(t, "go")
	clearCgoEnv(t)
	log := stubPkgConfig(t)
	t.Setenv("PC_CFLAGS", "-I/inc -fplugin=/nonexistent.so")

	for _, tt := range []struct {
		name, line, want string
	}{
		{"directive", "#cgo CFLAGS: -fplugin=/nonexistent.so", invalidFlag("#cgo CFLAGS", "-fplugin=/nonexistent.so")},
		{"pkg-config output", "#cgo pkg-config: foo", invalidFlag("pkg-config --cflags", "-fplugin=/nonexistent.so")},
		{"pkg-config name", "#cgo pkg-config: -foo", "invalid pkg-config package name: -foo"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			src := writeTree(t, map[string]string{
				"a.go": "package p\n\n/*\n" + tt.line + "\nstatic int one(void) { return 1; }\n*/\nimport \"C\"\n\nfunc F() int { return int(C.one()) }\n",
			})
			archive, err := compileErr(t, src, "/dev/null", gofiles.PkgFiles{CgoFiles: []string{"a.go"}})
			if want := "example.com/p: " + tt.want; err == nil || err.Error() != want {
				t.Errorf("CompileGoPackage: error %v, want %s", err, want)
			}
			if archive != "" {
				t.Error("CompileGoPackage wrote an archive")
			}
		})
	}
	if calls, _ := os.ReadFile(log); strings.Count(string(calls), "\n") != 1 {
		t.Errorf("pkg-config runs:\n%swant one, for --cflags of foo", calls)
	}
}

// With CGO_CFLAGS_ALLOW a flag outside cmd/go's list reaches the compilers:
// the sources carry #error unless -ffinite-math-only is in effect.
func TestCompileGoPackage_CgoFlagAllowed(t *testing.T) {
	cgoToolsOrSkip(t, "go", envOrDefault("CC", "gcc"))
	importcfg := stdImportcfg(t, "runtime/cgo", "syscall")
	clearCgoEnv(t)

	const check = "#if !__FINITE_MATH_ONLY__\n#error \"-ffinite-math-only is not in effect\"\n#endif\n"
	src := writeTree(t, map[string]string{
		"a.go": "package p\n\n/*\n#cgo CFLAGS: -ffinite-math-only\n" + check + "int one(void);\n*/\nimport \"C\"\n\nfunc F() int { return int(C.one()) }\n",
		"c.c":  check + "int one(void) { return 1; }\n",
	})
	files := gofiles.PkgFiles{CgoFiles: []string{"a.go"}, CFiles: []string{"c.c"}}

	_, err := compileErr(t, src, importcfg, files)
	if want := "example.com/p: " + invalidFlag("#cgo CFLAGS", "-ffinite-math-only"); err == nil || err.Error() != want {
		t.Fatalf("without CGO_CFLAGS_ALLOW: error %v, want %s", err, want)
	}

	t.Setenv("CGO_CFLAGS_ALLOW", "-ffinite-math-only")
	archive, err := compileErr(t, src, importcfg, files)
	if err != nil {
		t.Fatalf("with CGO_CFLAGS_ALLOW: %v", err)
	}
	if members := archiveMembers(t, archive); !slices.Contains(members, "preferlinkext") {
		t.Errorf("archive members %q: no preferlinkext, which go build writes for a flag outside its list", members)
	}
}

// The archive gets a preferlinkext member where go build's does: for -flto
// or a flag outside cmd/go's list among the C, C++ or Fortran flags, also
// those of the environment, and not for CPPFLAGS. The environment's linker
// flags neither count nor are refused when they come back as cgo_ldflag.
func TestCompileGoPackage_PreferLinkExt(t *testing.T) {
	cgoToolsOrSkip(t, "go", envOrDefault("CC", "gcc"))
	importcfg := stdImportcfg(t, "runtime/cgo", "syscall")

	for _, tt := range []struct {
		name string
		line string
		env  map[string]string
		want bool
	}{
		{name: "listed flags only", line: "#cgo CFLAGS: -DX=1"},
		{name: "-flto", line: "#cgo CFLAGS: -flto -ffat-lto-objects", want: true},
		{name: "CGO_CFLAGS outside the list", env: map[string]string{"CGO_CFLAGS": "-O2 -ffunction-sections"}, want: true},
		{name: "CGO_CXXFLAGS outside the list", env: map[string]string{"CGO_CXXFLAGS": "-ffunction-sections"}, want: true},
		{name: "CGO_FFLAGS outside the list", env: map[string]string{"CGO_FFLAGS": "-ffunction-sections"}, want: true},
		{name: "CGO_CPPFLAGS outside the list", env: map[string]string{"CGO_CPPFLAGS": "-ffunction-sections"}},
		{name: "CGO_LDFLAGS outside the list", line: "#cgo LDFLAGS: -lm", env: map[string]string{"CGO_LDFLAGS": "-O2 -Wl,--compress-debug-sections=zlib"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			clearCgoEnv(t)
			for k, v := range tt.env {
				t.Setenv(k, v)
			}
			src := writeTree(t, map[string]string{
				"a.go": "package p\n\n/*\n" + tt.line + "\nstatic int one(void) { return 1; }\n*/\nimport \"C\"\n\nfunc F() int { return int(C.one()) }\n",
			})
			archive, err := compileErr(t, src, importcfg, gofiles.PkgFiles{CgoFiles: []string{"a.go"}})
			if err != nil {
				t.Fatal(err)
			}
			members := archiveMembers(t, archive)
			if got := slices.Contains(members, "preferlinkext"); got != tt.want {
				t.Errorf("archive members %q: preferlinkext %v, want %v", members, got, tt.want)
			}
		})
	}
}

// The //go:cgo_ldflag comments cgo wrote are read back and checked against
// the flags it was given. cgo quotes a backslash and, like cmd/go's, the
// check compares without unquoting: with one in CGO_LDFLAGS the flags are
// not recognised as the ones passed and the lists decide on each, as under
// go build.
func TestCompileGoPackage_CgoLdflagPragmasChecked(t *testing.T) {
	cgoToolsOrSkip(t, "go", envOrDefault("CC", "gcc"))
	clearCgoEnv(t)
	t.Setenv("CGO_LDFLAGS", `-Wl,--compress-debug-sections=zlib -L/a\b`)

	src := writeTree(t, map[string]string{
		"a.go": "package p\n\n/*\n#cgo LDFLAGS: -lm\nstatic int one(void) { return 1; }\n*/\nimport \"C\"\n\nfunc F() int { return int(C.one()) }\n",
	})
	archive, err := compileErr(t, src, "/dev/null", gofiles.PkgFiles{CgoFiles: []string{"a.go"}})
	if want := "example.com/p: " + invalidFlag("go:cgo_ldflag", "-Wl,--compress-debug-sections=zlib"); err == nil || err.Error() != want {
		t.Errorf("CompileGoPackage: error %v, want %s", err, want)
	}
	if archive != "" {
		t.Error("CompileGoPackage wrote an archive")
	}
}

// The compiler takes //go:cgo_ldflag from any file whose name starts with
// _cgo_, and cgo copies such a comment from x.go to x.cgo1.go. cmd/go refuses
// such a file as an input, and one whose name reads as a flag; go list never
// reports either, a manifest can.
func TestCheckInputFileNames(t *testing.T) {
	for _, tt := range []struct {
		name  string
		files gofiles.PkgFiles
		bad   string
	}{
		{name: "ordinary names", files: gofiles.PkgFiles{GoFiles: []string{"a.go", "_testmain.go", "a_cgo_.go"}, CgoFiles: []string{"cgo_b.go"}}},
		{name: "_cgo_ file among the cgo files", files: gofiles.PkgFiles{CgoFiles: []string{"_cgo_x.go", "a.go"}}, bad: "_cgo_x.go"},
		{name: "_cgo_ file among the Go files", files: gofiles.PkgFiles{CgoFiles: []string{"a.go"}, GoFiles: []string{"_cgo_x.go"}}, bad: "_cgo_x.go"},
		{name: "_cgo_ file in a directory", files: gofiles.PkgFiles{GoFiles: []string{"sub/_cgo_x.go"}}, bad: "sub/_cgo_x.go"},
		{name: "name that reads as a flag", files: gofiles.PkgFiles{GoFiles: []string{"a.go", "-S.go"}}, bad: "-S.go"},
		{name: "name that reads as a response file", files: gofiles.PkgFiles{CgoFiles: []string{"@a.go"}}, bad: "@a.go"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			err := checkInputFileNames(tt.files)
			if tt.bad == "" {
				if err != nil {
					t.Errorf("checkInputFileNames: %v", err)
				}
				return
			}
			if want := "invalid input file name " + strconv.Quote(tt.bad); err == nil || err.Error() != want {
				t.Errorf("checkInputFileNames: error %v, want %s", err, want)
			}
		})
	}
}

// A file named _cgo_x.go with a //go:cgo_ldflag comment fails the package
// before any tool runs, as a cgo file, as a Go file next to cgo files and in
// a package without cgo.
func TestCompileGoPackage_InvalidInputFileName(t *testing.T) {
	cgoToolsOrSkip(t, "go")
	clearCgoEnv(t)

	const cgoFile = "package p\n\n/*\n#cgo LDFLAGS: -lm\nstatic int one(void) { return 1; }\n*/\nimport \"C\"\n\nfunc F() int { return int(C.one()) }\n"
	const pragma = "//go:cgo_ldflag \"-fplugin=/nonexistent.so\"\nfunc g() {}\n"
	for _, tt := range []struct {
		name  string
		tree  map[string]string
		files gofiles.PkgFiles
	}{
		{
			name:  "among the cgo files",
			tree:  map[string]string{"a.go": cgoFile, "_cgo_x.go": "package p\n\nimport \"C\"\n\n" + pragma},
			files: gofiles.PkgFiles{CgoFiles: []string{"_cgo_x.go", "a.go"}},
		},
		{
			name:  "among the Go files of a cgo package",
			tree:  map[string]string{"a.go": cgoFile, "_cgo_x.go": "package p\n\n" + pragma},
			files: gofiles.PkgFiles{CgoFiles: []string{"a.go"}, GoFiles: []string{"_cgo_x.go"}},
		},
		{
			name:  "in a package without cgo",
			tree:  map[string]string{"a.go": "package p\n", "_cgo_x.go": "package p\n\n" + pragma},
			files: gofiles.PkgFiles{GoFiles: []string{"_cgo_x.go", "a.go"}},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			archive, err := compileErr(t, writeTree(t, tt.tree), "/dev/null", tt.files)
			if want := `example.com/p: invalid input file name "_cgo_x.go"`; err == nil || err.Error() != want {
				t.Errorf("CompileGoPackage: error %v, want %s", err, want)
			}
			if archive != "" {
				t.Error("CompileGoPackage wrote an archive")
			}
		})
	}
}
