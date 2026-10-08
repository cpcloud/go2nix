package compile

import (
	"fmt"
	"go/build"
	"io/fs"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/numtide/go2nix/pkg/gofiles"
)

func compileCgo(opts Options, files gofiles.PkgFiles, embedFlag string) error {
	uid := strings.ReplaceAll(opts.ImportPath, "/", "_")

	cgowork, err := os.MkdirTemp(opts.TrimPath, "cgo_work_"+uid+"_")
	if err != nil {
		return err
	}
	defer os.RemoveAll(cgowork) //nolint:errcheck

	// cgowork carries a random MkdirTemp suffix; prepend it to the
	// -trimpath rewrite (first-match wins, cmd/internal/objabi/line.go:72)
	// so it's stripped from go tool compile output before the broader
	// TrimPath rule can leave the suffix behind. Mirrors cmd/go's
	// `rewrite += objdir + "=>"` (cmd/go/internal/work/gc.go:323).
	opts.trimRewrite = cgowork + "=>;" + opts.trimRewrite

	cc := envOrDefault("CC", "gcc")
	cxx := envOrDefault("CXX", "g++")

	// go tool cgo discards every #cgo line (cmd/cgo/gcc.go
	// ProcessCgoDirectives): cmd/go is what turns the directives, the
	// CGO_* environment and pkg-config into compiler and linker arguments.
	pkg, err := cgoDirectives(opts, files.CgoFiles)
	if err != nil {
		return fmt.Errorf("#cgo directives: %w", err)
	}
	pcCflags, pcLdflags, err := pkgConfigFlags(pkg.CgoPkgConfig)
	if err != nil {
		return fmt.Errorf("pkg-config: %w", err)
	}
	flags := newCgoFlags(pkg, pcCflags, pcLdflags)

	// Support gfortran out of the box for Fortran files, matching cmd/go behavior.
	if len(files.FFiles) > 0 {
		fc := envOrDefault("FC", "gfortran")
		if strings.Contains(fc, "gfortran") {
			flags.ld = append(flags.ld, "-lgfortran")
		}
	}

	// Keep absolute build paths out of gcc/g++/gfortran object files. Mirrors
	// cmd/go's compilerCmd (cmd/go/internal/work/exec.go:2484-2500): map
	// the work dir to /tmp/go-build, and -gno-record-gcc-switches so the
	// flag itself (which contains cgowork's random suffix) isn't recorded.
	// Also map SrcDir so source-file debug info is store-path-free.
	ccReproFlags := []string{
		"-ffile-prefix-map=" + cgowork + "=/tmp/go-build",
		"-ffile-prefix-map=" + opts.SrcDir + "=.",
		"-gno-record-gcc-switches",
	}
	// CPPFLAGS go ahead of each language's own flags, and to every
	// compiler (cmd/go/internal/work/exec.go:3053-3056).
	cgoCflags := slices.Concat(ccReproFlags, flags.cpp, flags.c)
	cgoCxxflags := slices.Concat(ccReproFlags, flags.cpp, flags.cxx)
	cgoFflags := slices.Concat(ccReproFlags, flags.cpp, flags.f)

	// Copy headers.
	for _, h := range files.HFiles {
		data, err := os.ReadFile(filepath.Join(opts.SrcDir, h))
		if err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(cgowork, h), data, 0o644); err != nil {
			return err
		}
	}

	// Step 1: go tool cgo.
	// The C flags after "--" are for cgo's own runs of the C compiler. The
	// linker flags go in -ldflags, which cgo writes to _cgo_gotypes.go as
	// //go:cgo_ldflag directives for the final link; cgo would record
	// $CGO_LDFLAGS a second time, so it is blanked
	// (cmd/go/internal/work/exec.go:2990-3011).
	cgoArgs := []string{
		"tool", "cgo",
		"-objdir", cgowork,
		"-importpath", opts.PFlag,
	}
	if len(flags.ld) > 0 {
		quoted := make([]string, len(flags.ld))
		for i, f := range flags.ld {
			quoted[i] = strconv.Quote(f)
		}
		cgoArgs = append(cgoArgs, "-ldflags="+strings.Join(quoted, " "))
	}
	cgoArgs = append(cgoArgs, "--", "-I", cgowork)
	cgoArgs = append(cgoArgs, cgoCflags...)
	cgoArgs = append(cgoArgs, files.CgoFiles...)
	if err := runInEnv(opts.SrcDir, []string{"CGO_LDFLAGS="}, "go", cgoArgs...); err != nil {
		return fmt.Errorf("cgo: %w", err)
	}

	// go tool cgo writes _cgo_flags only under -gccgo (cmd/cgo/out.go), so
	// for the gc toolchain there is nothing to read here.
	var cgoFlagsLDFLAGS []string
	cgoFlagsFile := filepath.Join(cgowork, "_cgo_flags")
	if data, err := os.ReadFile(cgoFlagsFile); err == nil {
		for line := range strings.SplitSeq(string(data), "\n") {
			if after, ok := strings.CutPrefix(line, "_CGO_LDFLAGS="); ok {
				cgoFlagsLDFLAGS = strings.Fields(after)
			}
		}
	}

	// Kept for now: the pkg-config LDFLAGS are written to _cgo_flags for the
	// archive member below. They reach the final link through -ldflags above.
	if len(pcLdflags) > 0 {
		cgoFlagsLDFLAGS = append(cgoFlagsLDFLAGS, pcLdflags...)
		allFlags := strings.Join(cgoFlagsLDFLAGS, " ")
		if err := os.WriteFile(cgoFlagsFile, []byte("_CGO_LDFLAGS="+allFlags+"\n"), 0o644); err != nil {
			return fmt.Errorf("writing _cgo_flags: %w", err)
		}
	}

	// Split assembly files: .s (lowercase) → go tool asm (Plan 9 assembly),
	// .S/.sx (uppercase) → gcc (C preprocessor assembly).
	// In CGO packages, .S/.sx files are added to SFiles by go/build only when
	// CgoFiles > 0 (see go/build/build.go:1065-1074). They must be compiled
	// with gcc, not go tool asm. See cmd/go/internal/work/exec.go:720-753.
	var goAsmFiles, gccAsmFiles []string
	for _, f := range files.SFiles {
		ext := filepath.Ext(f)
		if ext == ".S" || ext == ".sx" {
			gccAsmFiles = append(gccAsmFiles, f)
		} else {
			goAsmFiles = append(goAsmFiles, f)
		}
	}

	// Step 2: compile C/C++ files and .S/.sx assembly files with gcc.
	compiledOFiles, err := compileCFiles(cc, cxx, cgowork, opts.SrcDir, uid, files, cgoCflags, cgoCxxflags, cgoFflags, gccAsmFiles)
	if err != nil {
		return err
	}

	// Step 3: test link + dynimport. The test link uses CXX when the
	// package has C++ sources, matching cmd/go's gccld
	// (cmd/go/internal/work/exec.go:2383).
	linker := cc
	if len(files.CXXFiles) > 0 || len(files.SwigCXXFiles) > 0 {
		linker = cxx
	}
	dynFailObj, err := cgoTestLinkAndDynimport(cc, linker, cgowork, opts, uid, compiledOFiles, cgoFlagsLDFLAGS, flags.ld, cgoCflags, len(files.SysoFiles) > 0)
	if err != nil {
		return err
	}
	if dynFailObj != "" {
		compiledOFiles = append(compiledOFiles, dynFailObj)
	}

	// Step 4a: if .s (Plan 9 assembly) files exist, generate symabis.
	hasGoAsm := len(goAsmFiles) > 0
	var symabisPath, asmhdr string
	if hasGoAsm {
		var err error
		asmhdr, symabisPath, err = generateSymabis(opts, uid, goAsmFiles)
		if err != nil {
			return err
		}
	}

	// Step 4b: compile Go + cgo-generated Go files.
	var cgoGoFiles []string
	gotypes := filepath.Join(cgowork, "_cgo_gotypes.go")
	if _, err := os.Stat(gotypes); err == nil {
		cgoGoFiles = append(cgoGoFiles, gotypes)
	}
	cgo1Files, _ := filepath.Glob(filepath.Join(cgowork, "*.cgo1.go"))
	cgoGoFiles = append(cgoGoFiles, cgo1Files...)
	dynImport := filepath.Join(cgowork, "_cgo_import_"+uid+".go")
	if _, err := os.Stat(dynImport); err == nil {
		cgoGoFiles = append(cgoGoFiles, dynImport)
	}

	compileArgs := append(baseCompileArgs(opts), opts.outputFlags()...)
	if opts.GoVersion != "" {
		compileArgs = append(compileArgs, "-lang=go"+opts.GoVersion)
	}
	if hasGoAsm {
		compileArgs = append(compileArgs, "-symabis", symabisPath, "-asmhdr", asmhdr)
	}
	if opts.concurrency > 1 {
		compileArgs = append(compileArgs, fmt.Sprintf("-c=%d", opts.concurrency))
	}
	if opts.pgoPreprofile != "" {
		compileArgs = append(compileArgs, "-pgoprofile="+opts.pgoPreprofile)
	}
	compileArgs = append(compileArgs, extraGCFlags(opts)...)
	if embedFlag != "" {
		compileArgs = append(compileArgs, embedFlag)
	}
	compileArgs = append(compileArgs, files.GoFiles...)
	compileArgs = append(compileArgs, cgoGoFiles...)
	if err := runIn(opts.SrcDir, "go", compileArgs...); err != nil {
		return fmt.Errorf("compile: %w", err)
	}

	// Step 4c: assemble .s (Plan 9) files with go tool asm (using the
	// real go_asm.h generated by the compile step above).
	var asmOFiles []string
	if hasGoAsm {
		for _, sf := range goAsmFiles {
			base := strings.TrimSuffix(sf, ".s")
			objFile := filepath.Join(opts.TrimPath, base+"_"+uid+".o")
			asmFileArgs := append(asmBaseArgs(opts), "-o", objFile, sf)
			if err := runIn(opts.SrcDir, "go", asmFileArgs...); err != nil {
				return fmt.Errorf("asm %s: %w", sf, err)
			}
			asmOFiles = append(asmOFiles, objFile)
		}
	}

	// Step 5: pack C/C++ objects, assembly objects, and .syso files.
	allOFiles := make([]string, 0, len(compiledOFiles)+len(asmOFiles))
	allOFiles = append(allOFiles, compiledOFiles...)
	allOFiles = append(allOFiles, asmOFiles...)
	for _, s := range files.SysoFiles {
		allOFiles = append(allOFiles, filepath.Join(opts.SrcDir, s))
	}
	if err := packAppend(opts.Output, allOFiles); err != nil {
		return fmt.Errorf("pack: %w", err)
	}

	// Kept for now: cmd/link skips the _cgo_flags member
	// (cmd/link/internal/ld/lib.go: a name under 16 bytes that does not end
	// in .o or .syso).
	if _, err := os.Stat(cgoFlagsFile); err == nil {
		if err := packAppend(opts.Output, []string{cgoFlagsFile}); err != nil {
			return fmt.Errorf("pack _cgo_flags: %w", err)
		}
	}

	return nil
}

// compileCFiles compiles C, C++, and .S/.sx assembly files, returning object file paths.
// Go .s files (Plan 9 assembly) are NOT handled here — they use go tool asm.
func compileCFiles(cc, cxx, cgowork, srcDir, uid string, files gofiles.PkgFiles, cgoCflags, cgoCxxflags, cgoFflags []string, gccAsmFiles []string) ([]string, error) {
	var compiledOFiles []string

	// Collect cgo-generated C files.
	var ccFiles []string
	cgoExport := filepath.Join(cgowork, "_cgo_export.c")
	if _, err := os.Stat(cgoExport); err == nil {
		ccFiles = append(ccFiles, cgoExport)
	}
	cgo2Files, _ := filepath.Glob(filepath.Join(cgowork, "*.cgo2.c"))
	ccFiles = append(ccFiles, cgo2Files...)
	// User C files.
	for _, f := range files.CFiles {
		ccFiles = append(ccFiles, filepath.Join(srcDir, f))
	}

	for _, f := range ccFiles {
		base := strings.TrimSuffix(filepath.Base(f), ".c")
		oFile := filepath.Join(cgowork, base+"_"+uid+".o")
		ccArgs := []string{"-c", "-I", cgowork, "-I", srcDir, "-fPIC", "-pthread"}
		ccArgs = append(ccArgs, cgoCflags...)
		ccArgs = append(ccArgs, f, "-o", oFile)
		if err := runIn("", cc, ccArgs...); err != nil {
			return nil, fmt.Errorf("cc %s: %w", f, err)
		}
		compiledOFiles = append(compiledOFiles, oFile)
	}

	// Compile .S/.sx assembly files with gcc (C preprocessor assembly).
	// These are GCC-style assembly files that need the C preprocessor,
	// unlike .s files which are Plan 9 assembly for go tool asm.
	for _, f := range gccAsmFiles {
		base := filepath.Base(f)
		base = strings.TrimSuffix(base, filepath.Ext(f))
		oFile := filepath.Join(cgowork, base+"_asm_"+uid+".o")
		asmArgs := []string{"-c", "-I", cgowork, "-I", srcDir, "-fPIC", "-pthread"}
		asmArgs = append(asmArgs, cgoCflags...)
		asmArgs = append(asmArgs, filepath.Join(srcDir, f), "-o", oFile)
		if err := runIn("", cc, asmArgs...); err != nil {
			return nil, fmt.Errorf("cc %s: %w", f, err)
		}
		compiledOFiles = append(compiledOFiles, oFile)
	}

	// Compile C++ files.
	for _, f := range files.CXXFiles {
		base := trimFileExt(filepath.Base(f))
		oFile := filepath.Join(cgowork, base+"_cxx_"+uid+".o")
		cxxArgs := []string{"-c", "-I", cgowork, "-I", srcDir, "-fPIC", "-pthread"}
		cxxArgs = append(cxxArgs, cgoCxxflags...)
		cxxArgs = append(cxxArgs, filepath.Join(srcDir, f), "-o", oFile)
		if err := runIn("", cxx, cxxArgs...); err != nil {
			return nil, fmt.Errorf("cxx %s: %w", f, err)
		}
		compiledOFiles = append(compiledOFiles, oFile)
	}

	// Compile Fortran files with FC (default: gfortran).
	if len(files.FFiles) > 0 {
		fc := envOrDefault("FC", "gfortran")
		for _, f := range files.FFiles {
			base := trimFileExt(filepath.Base(f))
			oFile := filepath.Join(cgowork, base+"_f_"+uid+".o")
			fcArgs := []string{"-c", "-I", cgowork, "-I", srcDir, "-fPIC", "-pthread"}
			fcArgs = append(fcArgs, cgoFflags...)
			fcArgs = append(fcArgs, filepath.Join(srcDir, f), "-o", oFile)
			if err := runIn("", fc, fcArgs...); err != nil {
				return nil, fmt.Errorf("fc %s: %w", f, err)
			}
			compiledOFiles = append(compiledOFiles, oFile)
		}
	}

	return compiledOFiles, nil
}

// cgoTestLinkAndDynimport performs the cgo test link and extracts dynamic
// imports. _cgo_main.c is always compiled with cc; the link itself uses
// linker (cxx when the package has C++ sources, cc otherwise) — see
// cmd/go's gccld (cmd/go/internal/work/exec.go:2383).
//
// Failure of the test link is an expected outcome (e.g. .syso files with
// unexpected dependencies) and is handled by writing an empty
// "dynimportfail" marker that gets packed into the archive; cmd/link
// recognises that member name and forces external linking (golang/go#52863;
// cmd/go/internal/work/exec.go:3284, cmd/link/internal/ld/lib.go:1139).
// cmd/go never shows the link's output. Here it is shown unless the
// package has .syso files: without them the usual cause is a missing
// library or flag, on which the application's link fails too, in another
// derivation.
func cgoTestLinkAndDynimport(cc, linker, cgowork string, opts Options, uid string, compiledOFiles, cgoFlagsLDFLAGS, cgoLdflags, cgoCflags []string, hasSyso bool) (string, error) {
	cgoMainC := filepath.Join(cgowork, "_cgo_main.c")
	if _, err := os.Stat(cgoMainC); err != nil {
		return "", nil //nolint:nilerr // missing _cgo_main.c is expected, not an error
	}

	mainO := filepath.Join(cgowork, "_cgo_main_"+uid+".o")
	mainArgs := []string{"-c", "-I", cgowork, "-I", opts.SrcDir, "-fPIC", "-pthread"}
	mainArgs = append(mainArgs, cgoCflags...)
	mainArgs = append(mainArgs, cgoMainC, "-o", mainO)
	if err := runIn("", cc, mainArgs...); err != nil {
		return "", fmt.Errorf("cc _cgo_main.c: %w", err)
	}

	testLinkO := filepath.Join(cgowork, "_cgo__"+uid+".o")
	linkArgs := append([]string{"-o", testLinkO, mainO}, compiledOFiles...)
	linkArgs = append(linkArgs, "-lpthread")
	linkArgs = append(linkArgs, cgoFlagsLDFLAGS...)
	linkArgs = append(linkArgs, cgoLdflags...)
	cmd := exec.Command(linker, linkArgs...)
	if out, err := cmd.CombinedOutput(); err != nil {
		if hasSyso {
			slog.Debug("cgo test link failed; emitting dynimportfail marker", "err", err, "out", string(out))
		} else {
			slog.Warn("cgo test link failed; the package will need external linking", "pkg", opts.ImportPath, "err", err)
			os.Stderr.Write(out) //nolint:errcheck
		}
		fail := filepath.Join(cgowork, "dynimportfail")
		if werr := os.WriteFile(fail, nil, 0o644); werr != nil {
			return "", werr
		}
		return fail, nil
	}

	// cmd/go passes -dynlinker only for runtime/cgo
	// (cmd/go/internal/work/exec.go:3294-3296); user packages don't need
	// the //go:cgo_dynamic_linker directive it emits.
	pkgName := extractPackageName(filepath.Join(cgowork, "_cgo_gotypes.go"))
	dynOut := filepath.Join(cgowork, "_cgo_import_"+uid+".go")
	if err := runIn(opts.SrcDir, "go", "tool", "cgo",
		"-dynimport", testLinkO,
		"-dynout", dynOut,
		"-dynpackage", pkgName); err != nil {
		return "", fmt.Errorf("cgo dynimport: %w", err)
	}
	return "", nil
}

// cgoFlags holds a cgo package's CPPFLAGS, CFLAGS, CXXFLAGS, FFLAGS and
// LDFLAGS.
type cgoFlags struct {
	cpp, c, cxx, f, ld []string
}

// defaultCFlags is cmd/go's work.DefaultCFlags: what CGO_CFLAGS,
// CGO_CXXFLAGS, CGO_FFLAGS and CGO_LDFLAGS stand for when unset or empty.
const defaultCFlags = "-O2 -g"

// newCgoFlags mirrors Builder.CFlags and the flag handling of runCgo
// (cmd/go/internal/work/exec.go): the CGO_* environment or its default
// first, then the package's #cgo directives, then pkg-config, whose
// --cflags go to CPPFLAGS and whose --libs go to LDFLAGS.
func newCgoFlags(pkg *build.Package, pcCflags, pcLdflags []string) cgoFlags {
	env := func(name, def string) []string {
		v := os.Getenv("CGO_" + name)
		if v == "" {
			v = def
		}
		return strings.Fields(v)
	}
	return cgoFlags{
		cpp: slices.Concat(env("CPPFLAGS", ""), pkg.CgoCPPFLAGS, pcCflags),
		c:   slices.Concat(env("CFLAGS", defaultCFlags), pkg.CgoCFLAGS),
		cxx: slices.Concat(env("CXXFLAGS", defaultCFlags), pkg.CgoCXXFLAGS),
		f:   slices.Concat(env("FFLAGS", defaultCFlags), pkg.CgoFFLAGS),
		ld:  slices.Concat(env("LDFLAGS", defaultCFlags), pkg.CgoLDFLAGS, pcLdflags),
	}
}

// cgoDirectives returns the #cgo directives of cgoFiles (the Cgo* fields of
// the result) as go/build parses them for cmd/go: conditions evaluated,
// ${SRCDIR} expanded to opts.SrcDir, relative -I and -L paths made absolute.
// The context is the one the files were selected with.
func cgoDirectives(opts Options, cgoFiles []string) (*build.Package, error) {
	ctx := gofiles.BuildContext(strings.Join(opts.Tags, ","), ToolchainVersion())
	ctx.GOOS, ctx.GOARCH = opts.goos, opts.goarch
	// CgoFiles are only listed with cgo enabled.
	ctx.CgoEnabled = true
	// File names and //go:build lines were evaluated when the files were
	// listed; go/build sees those files and no others.
	ctx.UseAllFiles = true
	ctx.ReadDir = func(dir string) ([]fs.FileInfo, error) {
		infos := make([]fs.FileInfo, 0, len(cgoFiles))
		for _, f := range cgoFiles {
			fi, err := os.Stat(filepath.Join(dir, f))
			if err != nil {
				return nil, err
			}
			infos = append(infos, fi)
		}
		// The order of os.ReadDir, in which cmd/go concatenates the flags.
		slices.SortFunc(infos, func(a, b fs.FileInfo) int { return strings.Compare(a.Name(), b.Name()) })
		return infos, nil
	}
	return ctx.ImportDir(opts.SrcDir, 0)
}

// pkgConfigFlags runs pkg-config on the arguments of the package's
// #cgo pkg-config: directives and returns the resulting CFLAGS and LDFLAGS.
func pkgConfigFlags(pkgNames []string) (cflags, ldflags []string, err error) {
	if len(pkgNames) == 0 {
		return nil, nil, nil
	}

	slog.Debug("pkg-config", "packages", pkgNames)

	// Run pkg-config --cflags.
	cflagsOut, err := exec.Command("pkg-config", append([]string{"--cflags"}, pkgNames...)...).Output()
	if err != nil {
		return nil, nil, fmt.Errorf("pkg-config --cflags %v: %w", pkgNames, err)
	}
	cflags = strings.Fields(strings.TrimSpace(string(cflagsOut)))

	// Run pkg-config --libs.
	ldflagsOut, err := exec.Command("pkg-config", append([]string{"--libs"}, pkgNames...)...).Output()
	if err != nil {
		return nil, nil, fmt.Errorf("pkg-config --libs %v: %w", pkgNames, err)
	}
	ldflags = strings.Fields(strings.TrimSpace(string(ldflagsOut)))

	return cflags, ldflags, nil
}

// runInEnv is runIn with env added to the inherited environment.
func runInEnv(dir string, env []string, name string, args ...string) error {
	slog.Debug("exec", "cmd", name, "args", args, "dir", dir, "env", env)
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), env...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// trimFileExt removes the file extension from a filename.
func trimFileExt(name string) string {
	if ext := filepath.Ext(name); ext != "" {
		return strings.TrimSuffix(name, ext)
	}
	return name
}
