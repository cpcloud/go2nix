// Package testrunner compiles and runs tests for local Go packages.
// It is invoked during the checkPhase of default mode builds.
package testrunner

import (
	"bufio"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/numtide/go2nix/pkg/buildinfo"
	"github.com/numtide/go2nix/pkg/compile"
	"github.com/numtide/go2nix/pkg/gofiles"
	"github.com/numtide/go2nix/pkg/localpkgs"
	"github.com/numtide/go2nix/pkg/testmain"
	"github.com/numtide/go2nix/pkg/toposort"
)

// Options configures the test runner.
type Options struct {
	ModuleRoot string // path to module root (containing go.mod)
	ImportCfg  string // path to link importcfg (.a paths for local packages)
	// CompileImportCfg is an optional alternate importcfg used for
	// test-package compilation in interface-split mode (.x paths for
	// local packages so test compiles cut off when no exported API
	// changes). Empty means compile and link share ImportCfg.
	CompileImportCfg string
	TrimPath         string   // path prefix to trim
	Tags             string   // comma-separated build tags
	GCFlagsList      []string // extra compiler flags
	CheckFlagsList   []string // flags to pass to test binaries
	// LocalArchives bounds the test scope: only packages with an entry
	// here are tested. ListLocalPackages walks the whole module tree
	// (and after #75, sibling-replace targets too), but the eval-time
	// resolver only computed importcfg entries for packages reachable
	// from the subPackages closure (build + test-only locals). Packages
	// outside that closure can't link their tests anyway, so testing
	// them just produces a confusing "could not import" failure.
	// Nil means no filter (test everything ListLocalPackages finds).
	LocalArchives map[string]string
	// SrcOverlays maps importPath → packageOverrides.<pkg>.srcOverlay
	// store path. The per-package compile drv layered this onto its
	// pkgSrc before ResolveEmbedCfg (compile-go-pkg.sh:24-28); the
	// testrunner walks mainSrc instead, so without re-applying the
	// overlay any in-closure srcOverlay package fails embed resolution
	// (or, with placeholders present, observes source-tree content).
	SrcOverlays map[string]string
	// CXX selects $CXX instead of $CC as the external linker of the test
	// binaries (TestManifest.CXX).
	CXX bool
}

func (o Options) checkFlagsArgs() []string {
	return o.CheckFlagsList
}

func (o Options) tagsList() []string {
	if o.Tags == "" {
		return nil
	}
	return strings.Split(o.Tags, ",")
}

// Run discovers testable packages and runs their tests.
func Run(opts Options) error {
	modulePath, err := localpkgs.ModulePath(opts.ModuleRoot)
	if err != nil {
		return err
	}

	pkgs, err := localpkgs.ListLocalPackages(opts.ModuleRoot, opts.Tags, compile.ToolchainVersion())
	if err != nil {
		return fmt.Errorf("listing local packages: %w", err)
	}

	// Build a lookup map for all local packages (needed for recompilation).
	pkgMap := make(map[string]*localpkgs.LocalPkg, len(pkgs))
	for _, p := range pkgs {
		pkgMap[p.ImportPath] = p
	}

	inScope := func(ip string) bool {
		if opts.LocalArchives == nil {
			return true
		}
		_, ok := opts.LocalArchives[ip]
		return ok
	}

	// Filter to packages with test files that are inside the build's
	// resolved scope (see Options.LocalArchives).
	var testable []*localpkgs.LocalPkg
	skipped := 0
	for _, p := range pkgs {
		if len(p.TestGoFiles) == 0 && len(p.XTestGoFiles) == 0 {
			continue
		}
		if !inScope(p.ImportPath) {
			slog.Debug("skipping testable package outside subPackages closure", "pkg", p.ImportPath)
			skipped++
			continue
		}
		testable = append(testable, p)
	}

	if len(testable) == 0 {
		slog.Info("no testable packages found", "skipped", skipped)
		return nil
	}
	slog.Info("testable packages", "count", len(testable), "skipped", skipped)

	// Before the overlays: one moves SrcDir to a copy outside the module.
	goVersions := langVersions(pkgs, inScope)

	// Resolve //go:embed patterns now, only for packages in scope. Doing this
	// after the LocalArchives filter means an out-of-scope package whose
	// embed targets are absent from mainSrc (e.g. supplied at build time
	// via srcOverlay, or filtered from the source closure) cannot fail the
	// run. The recompileForTest set is the intersection of an in-scope
	// package's reverse-dep closure and an xtest's forward-dep closure, so
	// it is itself a subset of LocalArchives.
	for _, p := range pkgs {
		if !inScope(p.ImportPath) {
			continue
		}
		if overlay, ok := opts.SrcOverlays[p.ImportPath]; ok {
			if err := applySrcOverlay(p, overlay); err != nil {
				return fmt.Errorf("applying srcOverlay for %s: %w", p.ImportPath, err)
			}
		}
		if err := p.ResolveEmbeds(); err != nil {
			return fmt.Errorf("listing local packages: %w", err)
		}
	}

	for _, p := range testable {
		if err := runPackageTests(opts, modulePath, p, pkgMap, goVersions); err != nil {
			return fmt.Errorf("testing %s: %w", p.ImportPath, err)
		}
	}
	return nil
}

// langVersions returns, per in-scope import path, the go line of the go.mod
// above the package's directory, which is the package's own module also for
// a sibling behind a filesystem replace: mainSrc carries go.mod at modRoot
// and at every replace target.
func langVersions(pkgs []*localpkgs.LocalPkg, inScope func(string) bool) map[string]string {
	versions := make(map[string]string)
	for _, p := range pkgs {
		if inScope(p.ImportPath) {
			versions[p.ImportPath] = compile.FindGoVersion(p.SrcDir)
		}
	}
	return versions
}

// applySrcOverlay layers the packageOverrides.<pkg>.srcOverlay derivation
// onto a writable copy of the package's source dir and repoints p.SrcDir
// at it, mirroring compile-go-pkg.sh's overlay step. Subsequent
// ResolveEmbeds and any internal/xtest recompile then see overlay content.
func applySrcOverlay(p *localpkgs.LocalPkg, overlay string) error {
	dst, err := os.MkdirTemp("", "srcoverlay-*")
	if err != nil {
		return err
	}
	for _, src := range []string{p.SrcDir, overlay} {
		cmd := exec.Command("cp", "-rL", "--no-preserve=mode", src+"/.", dst+"/")
		if out, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("cp -rL %s: %w: %s", src, err, out)
		}
	}
	slog.Debug("applied srcOverlay", "pkg", p.ImportPath, "overlay", overlay)
	p.SrcDir = dst
	return nil
}

// affectedLocalDeps returns local packages that need recompilation for an xtest,
// in topological order (leaves first). This is Go's "recompileForTest" logic:
// after the internal test archive replaces the original package, local dependents
// reachable from the xtest must be recompiled so the graph is consistent.
//
// The set is the intersection of:
//   - reverse-dep closure of targetPkg (packages affected by the replacement)
//   - forward-dep closure of the xtest's local imports (packages the xtest can reach)
//
// This avoids recompiling unrelated packages that happen to import targetPkg
// but are not part of the current xtest's dependency graph.
func affectedLocalDeps(targetPkg string, xtestLocalImports []string, pkgMap map[string]*localpkgs.LocalPkg) ([]string, error) {
	// 1. Forward closure: local packages reachable from the xtest.
	xtestReachable := make(map[string]bool)
	var walkForward func(string)
	walkForward = func(ip string) {
		if xtestReachable[ip] {
			return
		}
		xtestReachable[ip] = true
		if p, ok := pkgMap[ip]; ok {
			for _, dep := range p.LocalDeps {
				walkForward(dep)
			}
		}
	}
	for _, imp := range xtestLocalImports {
		walkForward(imp)
	}

	// 2. Reverse-dep closure of targetPkg, intersected with xtest reachable set.
	revDeps := make(map[string][]string)
	for ip, p := range pkgMap {
		for _, dep := range p.LocalDeps {
			revDeps[dep] = append(revDeps[dep], ip)
		}
	}

	affected := make(map[string]string) // identity map for toposort
	queue := []string{targetPkg}
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		for _, rdep := range revDeps[cur] {
			if rdep == targetPkg {
				continue
			}
			if _, ok := affected[rdep]; ok {
				continue
			}
			if !xtestReachable[rdep] {
				continue // not reachable from xtest, skip
			}
			affected[rdep] = rdep
			queue = append(queue, rdep)
		}
	}

	if len(affected) == 0 {
		return nil, nil
	}

	// Topo-sort affected packages. Deps include local deps that are either
	// in the affected set or the target package itself (already recompiled).
	return toposort.Sort(affected, func(key string) []string {
		p, ok := pkgMap[key]
		if !ok {
			return nil
		}
		var deps []string
		for _, d := range p.LocalDeps {
			if _, isAffected := affected[d]; isAffected || d == targetPkg {
				deps = append(deps, d)
			}
		}
		return deps
	})
}

// runPackageTests builds and runs the test binary of pkg. goVersions holds,
// per import path, the go line of the go.mod above the package: cmd/go
// compiles ptest, pxtest and pmain with the -lang of the module of the
// package under test, and a recompiled dependent with its own
// (cmd/go/internal/work/gc.go:68-80; load.TestPackagesAndErrors keeps
// Module: p.Module).
func runPackageTests(opts Options, modulePath string, pkg *localpkgs.LocalPkg, pkgMap map[string]*localpkgs.LocalPkg, goVersions map[string]string) error {
	slog.Info("testing", "pkg", pkg.ImportPath)

	testDir := filepath.Join(opts.TrimPath, "test-"+sanitize(pkg.ImportPath))
	if err := os.MkdirAll(testDir, 0o755); err != nil {
		return err
	}

	// We need a separate importcfg for test compilation that overrides
	// the package under test with the internal test archive.
	testImportCfg := filepath.Join(testDir, "importcfg")
	if err := copyFile(opts.ImportCfg, testImportCfg); err != nil {
		return fmt.Errorf("copying importcfg: %w", err)
	}

	// In interface-split mode the test compiles read .x (export-data)
	// for local packages while the link still reads .a. Maintain a
	// parallel compile-cfg and apply every override to both. When
	// CompileImportCfg is empty, compile and link share testImportCfg.
	testCompileImportCfg := testImportCfg
	if opts.CompileImportCfg != "" {
		testCompileImportCfg = filepath.Join(testDir, "importcfg.compile")
		if err := copyFile(opts.CompileImportCfg, testCompileImportCfg); err != nil {
			return fmt.Errorf("copying compile importcfg: %w", err)
		}
	}
	overrideBoth := func(importPath, archivePath string) error {
		if err := overrideImportCfgEntry(testImportCfg, importPath, archivePath); err != nil {
			return err
		}
		if testCompileImportCfg != testImportCfg {
			if err := overrideImportCfgEntry(testCompileImportCfg, importPath, archivePath); err != nil {
				return err
			}
		}
		return nil
	}

	// Step 1: Compile internal test archive (replaces library archive)
	// This includes the library's GoFiles + internal _test.go files,
	// compiled with the same import path as the original package, in the
	// package's own directory: a cgo file may include a header from a
	// sub-directory, and the embed configs name files below SrcDir.
	if len(pkg.TestGoFiles) > 0 {
		// Merge production + test embed configs for the compiler.
		mergedEmbedCfg, err := gofiles.MergeEmbedCfg(pkg.EmbedCfg, pkg.TestEmbedCfg)
		if err != nil {
			return fmt.Errorf("merging embed configs for %s: %w", pkg.ImportPath, err)
		}

		// Explicit file list: go/build.ImportDir would classify _test.go
		// files into TestGoFiles, not GoFiles. We must compile them all
		// together as a single package. Preserve the package's cgo/asm/
		// syso file sets so compile.CompileGoPackage takes the cgo or
		// asm path instead of the pure-Go -complete path.
		internalFiles := internalTestPkgFiles(pkg, mergedEmbedCfg)
		internalArchive := filepath.Join(testDir, "internal.a")
		if err := compile.CompileGoPackage(compile.Options{
			ImportPath:  pkg.ImportPath,
			SrcDir:      pkg.SrcDir,
			Output:      internalArchive,
			ImportCfg:   testCompileImportCfg,
			GoVersion:   goVersions[pkg.ImportPath],
			TrimPath:    opts.TrimPath,
			GCFlagsList: opts.GCFlagsList,
			Tags:        opts.tagsList(),
			Files:       &internalFiles,
		}); err != nil {
			return fmt.Errorf("compiling internal test: %w", err)
		}

		// Override the package entry in importcfg to point to the test archive.
		// We rewrite the importcfg filtering out any existing entry for this
		// package to avoid duplicate packagefile directives.
		if err := overrideBoth(pkg.ImportPath, internalArchive); err != nil {
			return fmt.Errorf("overriding importcfg for %s: %w", pkg.ImportPath, err)
		}
	}

	// Step 1b: Recompile local packages that transitively depend on the
	// package under test ("recompileForTest"). After replacing the package
	// archive with the internal test version, dependents must be recompiled
	// so xtest compilation and linking see consistent archives. Without
	// internal tests nothing was replaced: both importcfgs keep the entries
	// of the package's own derivation, and cmd/go recompiles nothing either
	// (load.recompileForTest replaces an import only where its test copy
	// differs from it, and ptest is p then).
	if len(pkg.TestGoFiles) > 0 && len(pkg.XTestGoFiles) > 0 {
		// Filter xtest imports to local-only for the forward closure.
		var xtestLocalImports []string
		for _, imp := range pkg.XTestImports {
			if _, ok := pkgMap[imp]; ok {
				xtestLocalImports = append(xtestLocalImports, imp)
			}
		}
		affected, err := affectedLocalDeps(pkg.ImportPath, xtestLocalImports, pkgMap)
		if err != nil {
			return fmt.Errorf("computing recompilation set for %s: %w", pkg.ImportPath, err)
		}
		for _, depIP := range affected {
			depPkg, ok := pkgMap[depIP]
			if !ok {
				continue
			}
			recompArchive := filepath.Join(testDir, "recomp-"+sanitize(depIP)+".a")
			slog.Info("recompiling for test", "pkg", depIP, "because", pkg.ImportPath)
			if err := compile.CompileGoPackage(compile.Options{
				ImportPath:  depIP,
				SrcDir:      depPkg.SrcDir,
				Output:      recompArchive,
				ImportCfg:   testCompileImportCfg,
				GoVersion:   goVersions[depIP],
				TrimPath:    opts.TrimPath,
				GCFlagsList: opts.GCFlagsList,
				Tags:        opts.tagsList(),
				Files:       &depPkg.PkgFiles,
			}); err != nil {
				return fmt.Errorf("recompiling %s for test: %w", depIP, err)
			}
			if err := overrideBoth(depIP, recompArchive); err != nil {
				return fmt.Errorf("overriding importcfg for recompiled %s: %w", depIP, err)
			}
		}
	}

	// Step 2: Compile external test archive
	externalArchive := filepath.Join(testDir, "external.a")
	if len(pkg.XTestGoFiles) > 0 {
		// Explicit file list: _test.go files would be classified as
		// TestGoFiles/XTestGoFiles by ImportDir, not GoFiles.
		if err := compile.CompileGoPackage(compile.Options{
			ImportPath:  pkg.ImportPath + "_test",
			SrcDir:      pkg.SrcDir,
			Output:      externalArchive,
			ImportCfg:   testCompileImportCfg,
			GoVersion:   goVersions[pkg.ImportPath],
			TrimPath:    opts.TrimPath,
			GCFlagsList: opts.GCFlagsList,
			Tags:        opts.tagsList(),
			Files:       &gofiles.PkgFiles{GoFiles: pkg.XTestGoFiles, EmbedCfg: pkg.XTestEmbedCfg},
		}); err != nil {
			return fmt.Errorf("compiling external test: %w", err)
		}

		// Add external test to both importcfgs (compile reads it for
		// _testmain, link reads it for the binary).
		if err := overrideBoth(pkg.ImportPath+"_test", externalArchive); err != nil {
			return fmt.Errorf("adding external test entry: %w", err)
		}
	}

	// Step 3: Generate test main
	testMainFile := filepath.Join(testDir, "_testmain.go")
	var testFiles, xtestFiles []string
	for _, f := range pkg.TestGoFiles {
		testFiles = append(testFiles, filepath.Join(pkg.SrcDir, f))
	}
	for _, f := range pkg.XTestGoFiles {
		xtestFiles = append(xtestFiles, filepath.Join(pkg.SrcDir, f))
	}
	src, err := testmain.Generate(testmain.Options{
		ImportPath:   pkg.ImportPath,
		ModulePath:   modulePath,
		TestGoFiles:  testFiles,
		XTestGoFiles: xtestFiles,
	})
	if err != nil {
		return fmt.Errorf("generating test main: %w", err)
	}
	if err := os.WriteFile(testMainFile, src, 0o644); err != nil {
		return err
	}

	// Step 4: Compile test main
	// Explicit file: _testmain.go starts with _ and would be ignored by ImportDir.
	testMainArchive := filepath.Join(testDir, "testmain.a")
	if err := compile.CompileGoPackage(compile.Options{
		ImportPath:  pkg.ImportPath + ".test",
		PFlag:       "main",
		SrcDir:      testDir,
		Output:      testMainArchive,
		ImportCfg:   testCompileImportCfg,
		GoVersion:   goVersions[pkg.ImportPath],
		TrimPath:    opts.TrimPath,
		GCFlagsList: opts.GCFlagsList,
		Tags:        opts.tagsList(),
		Files:       &gofiles.PkgFiles{GoFiles: []string{"_testmain.go"}},
	}); err != nil {
		return fmt.Errorf("compiling test main: %w", err)
	}

	// Step 5: Link test binary
	testBin := filepath.Join(testDir, "test.exe")

	// Use `go tool link` rather than invoking the linker binary directly.
	// The build phase invokes the linker directly to prevent GOROOT embedding
	// in production binaries, but test binaries are ephemeral so this doesn't
	// matter. Using `go tool link` matches how compile uses `go tool compile`.
	linkArgs := []string{
		"tool", "link",
		"-extld", externalLinker(opts.CXX),
		"-buildid=redacted",
		"-importcfg", testImportCfg,
		"-o", testBin,
	}
	linkArgs = append(linkArgs, testLinkFlags(opts, pkg)...)
	linkArgs = append(linkArgs, testMainArchive)
	cmd := exec.Command("go", linkArgs...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("linking test binary: %w", err)
	}

	// Step 6: Run tests
	testArgs := opts.checkFlagsArgs()
	testCmd := exec.Command(testBin, testArgs...)
	testCmd.Dir = pkg.SrcDir
	testCmd.Stdout = os.Stdout
	testCmd.Stderr = os.Stderr
	if err := testCmd.Run(); err != nil {
		return fmt.Errorf("tests failed: %w", err)
	}

	slog.Info("tests passed", "pkg", pkg.ImportPath)
	return nil
}

// internalTestPkgFiles returns the explicit file list for compiling the
// internal test archive: the package's production PkgFiles with TestGoFiles
// folded into GoFiles. CgoFiles/SFiles/CFiles/CXXFiles/HFiles/FFiles/
// SysoFiles are carried through verbatim so compile.CompileGoPackage routes
// to the cgo / asm paths exactly as it does for the non-test build.
func internalTestPkgFiles(pkg *localpkgs.LocalPkg, embedCfg *gofiles.EmbedCfg) gofiles.PkgFiles {
	goFiles := append(append([]string{}, pkg.GoFiles...), pkg.TestGoFiles...)
	return gofiles.PkgFiles{
		GoFiles:   goFiles,
		CgoFiles:  pkg.CgoFiles,
		SFiles:    pkg.SFiles,
		CFiles:    pkg.CFiles,
		CXXFiles:  pkg.CXXFiles,
		HFiles:    pkg.HFiles,
		FFiles:    pkg.FFiles,
		SysoFiles: pkg.SysoFiles,
		EmbedCfg:  embedCfg,
	}
}

// testLinkFlags returns the flags cmd/go adds to the link of pkg's test
// binary: -X testing.testBinary=1, which is what testing.Testing reads, and
// the default GODEBUG (cmd/go/internal/load.TestPackagesAndErrors,
// work.gcToolchain.ld). The latter comes from the main module's go.mod, like
// the application's in link-binary and also for a package of a sibling
// module (defaultGODEBUG reads MainModules), and from the //go:debug lines
// of the package and of its tests.
func testLinkFlags(opts Options, pkg *localpkgs.LocalPkg) []string {
	flags := []string{"-X", "testing.testBinary=1"}
	bctx := gofiles.BuildContext(opts.Tags, compile.ToolchainVersion())
	godebugDefault := buildinfo.DefaultGODEBUG(opts.ModuleRoot,
		buildinfo.ParseTestGodebugs(&bctx, pkg.SrcDir), os.Getenv("GOFIPS140"), compile.ToolchainVersion(), compile.GoEnvVar("GOROOT"))
	if godebugDefault != "" {
		flags = append(flags, "-X=runtime.godebugDefault="+godebugDefault)
	}
	return flags
}

// overrideImportCfgEntry rewrites the importcfg file, replacing (or adding)
// the packagefile entry for importPath with the given archive path.
// This avoids duplicate packagefile directives which have undefined behavior.
func overrideImportCfgEntry(importCfgPath, importPath, archivePath string) error {
	data, err := os.ReadFile(importCfgPath)
	if err != nil {
		return err
	}

	prefix := "packagefile " + importPath + "="
	var lines []string
	scanner := bufio.NewScanner(strings.NewReader(string(data)))
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, prefix) {
			lines = append(lines, line)
		}
	}
	lines = append(lines, fmt.Sprintf("packagefile %s=%s", importPath, archivePath))

	return os.WriteFile(importCfgPath, []byte(strings.Join(lines, "\n")+"\n"), 0o644)
}

// externalLinker is cmd/go's choice of -extld, which it always passes: $CXX
// when a linked package has C++ sources and $CC otherwise, each with the
// toolchain's own default when unset (cmd/go/internal/work/gc.go:591-595,
// 632-644; link-binary chooses the same way from LinkSubPackage.CXX). Left
// alone, cmd/link runs its platform default ("gcc", "clang" on darwin), which
// links no C++ runtime and need not be on PATH.
func externalLinker(cxx bool) string {
	if cxx {
		return compile.GoEnvVar("CXX")
	}
	return compile.GoEnvVar("CC")
}

func sanitize(s string) string {
	return strings.NewReplacer("/", "_", ".", "_").Replace(s)
}

func copyFile(src, dst string) error {
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	return os.WriteFile(dst, data, 0o644)
}
