package main

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"go/build"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"

	"github.com/numtide/go2nix/pkg/buildinfo"
	"github.com/numtide/go2nix/pkg/compile"
	"github.com/numtide/go2nix/pkg/localpkgs"
	"github.com/numtide/go2nix/pkg/mvscheck"
	"golang.org/x/sync/errgroup"
)

func runLinkBinaryCmd(args []string) {
	fs := flag.NewFlagSet("link-binary", flag.ExitOnError)
	manifestPath := fs.String("manifest", "", "path to link-manifest.json (required)")
	output := fs.String("output", "", "output directory (binaries written to <output>/bin/)")
	_ = fs.Parse(args)

	if *manifestPath == "" || *output == "" {
		slog.Error("usage: go2nix link-binary --manifest FILE --output DIR")
		os.Exit(1)
	}

	if err := linkBinary(*manifestPath, *output); err != nil {
		slog.Error("link-binary failed", "err", err)
		os.Exit(1)
	}
}

func linkBinary(manifestPath, output string) error {
	return linkBinaryTo(os.Stderr, manifestPath, output)
}

// linkBinaryTo is linkBinary with the log of the binaries written to w.
func linkBinaryTo(w io.Writer, manifestPath, output string) error {
	m, err := compile.LoadLinkManifest(manifestPath)
	if err != nil {
		return err
	}

	tmpDir := os.Getenv("NIX_BUILD_TOP")
	if tmpDir == "" {
		tmpDir = os.TempDir()
	}

	// Step 1: Validate lockfile consistency (skipped in lockfile-free mode).
	if m.Lockfile != nil && *m.Lockfile != "" {
		if err := mvscheck.CheckLockfile(m.ModuleRoot, *m.Lockfile); err != nil {
			return fmt.Errorf("lockfile check: %w", err)
		}
	}

	// Step 2: Extract module path from go.mod.
	modulePath, err := localpkgs.ModulePath(m.ModuleRoot)
	if err != nil {
		return err
	}
	if modulePath == "" {
		return fmt.Errorf("no module directive in %s/go.mod", m.ModuleRoot)
	}

	// Step 3: Merge importcfg parts.
	buildCfg := func(name string, parts []string, locals map[string]string) (string, error) {
		dir := filepath.Join(tmpDir, name)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return "", err
		}
		cfg, err := compile.MergeImportcfg(parts, dir)
		if err != nil {
			return "", fmt.Errorf("merging %s: %w", name, err)
		}
		f, err := os.OpenFile(cfg, os.O_APPEND|os.O_WRONLY, 0o644)
		if err != nil {
			return "", fmt.Errorf("opening %s: %w", name, err)
		}
		for importPath, p := range locals {
			if _, err := fmt.Fprintf(f, "packagefile %s=%s\n", importPath, p); err != nil {
				return "", fmt.Errorf("writing %s entry: %w", name, err)
			}
		}
		if err := f.Close(); err != nil {
			return "", fmt.Errorf("closing %s: %w", name, err)
		}
		return cfg, nil
	}

	mergedCfg, err := buildCfg("link-cfg", m.ImportcfgParts, m.LocalArchives)
	if err != nil {
		return err
	}

	// In interface-split mode, the main-package compile reads export-data
	// (.x) files so private-symbol changes upstream don't invalidate it.
	// The link step always reads mergedCfg (.a link objects).
	compileCfg := mergedCfg
	if len(m.CompileImportcfgParts) > 0 {
		compileCfg, err = buildCfg("compile-cfg", m.CompileImportcfgParts, m.LocalIfaces)
		if err != nil {
			return err
		}
	}

	// Step 4: Determine target platform and build mode (needed for modinfo
	// build settings as well as the link step).
	goos := ""
	goarch := ""
	if m.GOOS != nil {
		goos = *m.GOOS
	}
	if m.GOARCH != nil {
		goarch = *m.GOARCH
	}
	if goos == "" {
		goos = compile.GoEnvVar("GOOS")
	}
	if goarch == "" {
		goarch = compile.GoEnvVar("GOARCH")
	}
	buildMode := compile.DefaultBuildMode(goos, goarch)

	// Step 5: Compute modinfo.
	goVersion, goroot, err := goToolchain("go")
	if err != nil {
		return fmt.Errorf("asking go env for the toolchain: %w", err)
	}

	// GOFIPS140 reaches this drv via the scope-level goEnv merge into env
	// (nix/dag), the same way it reaches stdlib's `go install std`. Read
	// directly: when unset os.Getenv returns "" (Fips140Enabled = false),
	// and the link drv's hash already varies with goEnv.
	goFips140 := os.Getenv("GOFIPS140")

	// DefaultGODEBUG depends on per-main-package //go:debug source
	// directives, so it (and the BuildSettings that carry it) is computed
	// per binary. Everything else is invariant.
	settings := buildinfo.BuildSettings{
		BuildMode:  buildMode,
		Tags:       strings.Join(m.Tags, ","),
		CGOEnabled: compile.GoEnvVar("CGO_ENABLED"),
		GOARCH:     goarch,
		GOFIPS140:  goFips140,
		GOOS:       goos,
	}
	if key := buildinfo.ArchLevelVar(goarch); key != "" {
		settings.GOARCHLevel = compile.GoEnvVar(key)
	}

	// build.Context for ParseSourceGodebugs — directive collection respects
	// build constraints, so use the same GOOS/GOARCH/tags as the compile.
	bctx := build.Default
	bctx.GOOS = goos
	bctx.GOARCH = goarch
	bctx.BuildTags = m.Tags

	// Step 6: Read merged importcfg once; the per-binary modinfo line (which
	// carries BuildInfo.Path = main package import path) is appended per
	// binary so each one records its own path.
	mergedCfgData, err := os.ReadFile(mergedCfg)
	if err != nil {
		return err
	}

	// Build gcflags: PIE requires -shared, then append manifest gcflags.
	var gcflagsList []string
	if buildMode == "pie" {
		gcflagsList = append(gcflagsList, "-shared")
	}
	gcflagsList = append(gcflagsList, m.GCFlags...)

	var pgoProfile string
	if m.PGOProfile != nil {
		pgoProfile = *m.PGOProfile
	}

	// Resolve the linker binary before clearing GOROOT.
	goToolDir, err := exec.Command("go", "env", "GOTOOLDIR").Output()
	if err != nil {
		return fmt.Errorf("go env GOTOOLDIR: %w", err)
	}
	goLinkTool := filepath.Join(strings.TrimSpace(string(goToolDir)), "link")

	// Do not set GOROOT: the linker reads it from os.Getenv and embeds it
	// as runtime.defaultGOROOT. Invoking the linker directly matches what
	// `go build -trimpath` does internally.
	_ = os.Setenv("GOROOT", "")

	// Step 7: Compile main packages and link.
	mainDir := filepath.Join(tmpDir, "main-pkgs")
	binDir := filepath.Join(output, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		return err
	}

	ldflags, err := expandLDFlags(m.LDFlags)
	if err != nil {
		return fmt.Errorf("parsing ldflags: %w", err)
	}
	userExtld := hasExtld(ldflags)

	type mainTarget struct{ importpath, srcdir, binname string }
	targets := make([]mainTarget, len(m.SubPackages))
	// Binaries with the same name share an output file: each waits for the
	// one listed before it, so the one listed last is what bin/ is left with.
	after := make([]int, len(m.SubPackages))
	lastNamed := map[string]int{}
	for i, sp := range m.SubPackages {
		var importpath, srcdir, binname string

		clean := strings.TrimPrefix(sp.Path, "./")
		if sp.Path == "." || clean == "" {
			importpath = modulePath
			srcdir = filepath.Clean(m.ModuleRoot)
			binname = m.Pname
			if binname == "" {
				binname = filepath.Base(modulePath)
			}
		} else {
			importpath = modulePath + "/" + clean
			srcdir = filepath.Join(m.ModuleRoot, clean)
			binname = filepath.Base(clean)
		}

		targets[i] = mainTarget{importpath, srcdir, binname}
		after[i] = -1
		if prev, ok := lastNamed[binname]; ok {
			after[i] = prev
		}
		lastNamed[binname] = i
	}

	linkOne := func(ctx context.Context, job linkJob) error {
		sp, target := m.SubPackages[job.index], targets[job.index]
		importpath, srcdir, binname := target.importpath, target.srcdir, target.binname
		// The handler main() installs, writing to this binary's log.
		logger := slog.New(slog.NewTextHandler(job.log, nil))

		deps := make([]buildinfo.ModDep, 0, len(sp.Modules))
		for _, mm := range sp.Modules {
			dep := buildinfo.ModDep{Path: mm.Path, Version: mm.Version}
			if mm.ReplacePath != "" {
				dep.Replace = &buildinfo.ModDep{Path: mm.ReplacePath, Version: mm.ReplaceVersion}
			}
			deps = append(deps, dep)
		}

		srcDirectives := buildinfo.ParseSourceGodebugs(&bctx, srcdir)
		godebugDefault := buildinfo.DefaultGODEBUG(m.ModuleRoot, srcDirectives, goFips140, goVersion, goroot)
		binSettings := settings
		binSettings.DefaultGODEBUG = godebugDefault

		logger.Info("compiling main", "pkg", importpath)

		mainArchive := filepath.Join(mainDir, importpath+".a")
		if err := os.MkdirAll(filepath.Dir(mainArchive), 0o755); err != nil {
			return err
		}

		if sp.Files == nil {
			return fmt.Errorf("link manifest: subPackage %q is missing files; rebuild the nix-plugin", sp.Path)
		}
		pf, err := sp.Files.ToPkgFiles(srcdir)
		if err != nil {
			return fmt.Errorf("resolving files for %s: %w", importpath, err)
		}

		if err := compile.CompileGoPackage(compile.Options{
			ImportPath:  "main",
			SrcDir:      srcdir,
			Output:      mainArchive,
			ImportCfg:   compileCfg,
			TrimPath:    job.scratch,
			GCFlagsList: gcflagsList,
			Tags:        m.Tags,
			PGOProfile:  pgoProfile,
			Files:       pf,
		}); err != nil {
			return fmt.Errorf("compiling main %s: %w", importpath, err)
		}

		// Compute link flags. cmd/go always passes -extld (CXX if any
		// transitive package has C++ sources, else CC) and lets cmd/link
		// pick the link mode itself (cmd/link/internal/ld/lib.go:576 sets
		// iscgo = LibraryByPkg["runtime/cgo"] != nil; config.go:208 picks
		// external when iscgo && externalobj). Mirror that: pass -extld
		// unconditionally, never force -linkmode. setextld (gc.go:538-550)
		// skips this when user ldflags already contain -extld.
		var linkFlags []string
		if !userExtld {
			extld := os.Getenv("CC")
			if sp.CXX {
				extld = os.Getenv("CXX")
			}
			if extld != "" {
				linkFlags = append(linkFlags, "-extld", extld)
			}
		}

		// Propagate sanitizer flags from gcflags to linker.
		for _, flag := range m.GCFlags {
			switch flag {
			case "-race", "-msan", "-asan":
				linkFlags = append(linkFlags, flag)
			}
		}

		// GODEBUG default.
		if godebugDefault != "" {
			linkFlags = append(linkFlags, "-X=runtime.godebugDefault="+godebugDefault)
		}

		// cmd/go passes -fipso so the linker writes a copy of the FIPS
		// object file (cmd/go/internal/work/gc.go:607-609). The file is
		// only used for `go build -work` inspection; we discard it.
		if buildinfo.Fips140Enabled(goFips140) {
			linkFlags = append(linkFlags, "-fipso", filepath.Join(job.scratch, "fips.o"))
		}

		// Per-binary modinfo: BuildInfo.Path must be the main package's
		// import path (matching `go build`), so each subPackage gets its
		// own importcfg.link.
		modinfo, err := buildinfo.GenerateModinfo(m.ModuleRoot, importpath, goVersion, deps, binSettings)
		if err != nil {
			return fmt.Errorf("generating modinfo for %s: %w", importpath, err)
		}
		linkCfg := filepath.Join(job.scratch, "importcfg.link")
		if err := os.WriteFile(linkCfg, slices.Concat(mergedCfgData, []byte(modinfo+"\n")), 0o644); err != nil {
			return err
		}

		// Assemble linker command.
		linkArgs := []string{
			"-buildid=redacted",
			"-buildmode=" + buildMode,
			"-importcfg", linkCfg,
		}
		linkArgs = append(linkArgs, ldflags...)
		linkArgs = append(linkArgs, linkFlags...)
		linkArgs = append(linkArgs, "-o", filepath.Join(binDir, binname))
		linkArgs = append(linkArgs, mainArchive)

		logger.Info("linking", "pkg", importpath, "bin", binname)
		cmd := exec.CommandContext(ctx, goLinkTool, linkArgs...)
		cmd.Stdout = job.log
		cmd.Stderr = job.log
		if err := cmd.Run(); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return fmt.Errorf("linking %s: %w", importpath, err)
		}
		return nil
	}

	return runLinkJobs(context.Background(), len(m.SubPackages), compile.BuildCores(), filepath.Join(tmpDir, "link-scratch"), after, w, linkOne)
}

// linkJob is what runLinkJobs hands to the function that builds one binary.
type linkJob struct {
	index   int
	scratch string    // directory of its own for temporary files
	log     io.Writer // receives the binary's log lines and tool output
}

// runLinkJobs calls run for binaries 0 to n-1 on at most limit goroutines and
// gives each a scratch directory of its own under scratchRoot. after[i] is -1
// or the index of an earlier binary that must finish before binary i starts.
// The logs reach w in index order whatever order the binaries finish in, each
// as soon as every binary before it is done. The first failure cancels ctx
// for the others; the error returned is that of the lowest-numbered binary
// that failed for a reason of its own, and no log is printed past it.
func runLinkJobs(ctx context.Context, n, limit int, scratchRoot string, after []int, w io.Writer, run func(context.Context, linkJob) error) error {
	logs := make([]bytes.Buffer, n)
	errs := make([]error, n)
	done := make([]chan struct{}, n)
	for i := range done {
		done[i] = make(chan struct{})
	}
	ownFailure := func(i int) bool {
		return errs[i] != nil && !errors.Is(errs[i], context.Canceled)
	}
	// One binary at a time runs in index order, so its log can go straight to w.
	unbuffered := n == 1 || limit == 1

	var mu sync.Mutex // guards finished, next, stopped and the writes to w
	finished := make([]bool, n)
	next, stopped := 0, false
	flush := func(i int) {
		mu.Lock()
		defer mu.Unlock()
		finished[i] = true
		for !stopped && next < n && finished[next] {
			_, _ = w.Write(logs[next].Bytes())
			stopped = ownFailure(next)
			next++
		}
	}

	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(limit)
	for i := range n {
		g.Go(func() error {
			defer close(done[i])
			defer flush(i)
			if after[i] >= 0 {
				select {
				case <-done[after[i]]:
					// done is closed before the group cancels gctx.
					if errs[after[i]] != nil {
						errs[i] = context.Canceled
						return errs[i]
					}
				case <-gctx.Done():
				}
			}
			errs[i] = gctx.Err()
			if errs[i] != nil {
				return errs[i]
			}
			job := linkJob{index: i, scratch: filepath.Join(scratchRoot, strconv.Itoa(i)), log: &logs[i]}
			if unbuffered {
				job.log = w
			}
			if errs[i] = os.MkdirAll(job.scratch, 0o755); errs[i] == nil {
				errs[i] = run(gctx, job)
			}
			return errs[i]
		})
	}
	waitErr := g.Wait()

	for i := range n {
		if ownFailure(i) {
			return errs[i]
		}
	}
	return waitErr
}

// goToolchain reports the version and the GOROOT of the toolchain gobin
// belongs to.
func goToolchain(gobin string) (version, goroot string, err error) {
	out, err := exec.Command(gobin, "env", "GOVERSION", "GOROOT").Output()
	if err != nil {
		return "", "", err
	}
	version, goroot, _ = strings.Cut(strings.TrimSpace(string(out)), "\n")
	version, goroot = strings.TrimSpace(version), strings.TrimSpace(goroot)
	if goroot == "" {
		slog.Warn("go env GOROOT is empty: default GODEBUG falls back on the built-in table", "go", gobin)
	}
	return version, goroot, nil
}

// hasExtld reports whether ldflags already specify -extld, mirroring
// cmd/go's setextld check (cmd/go/internal/work/gc.go:538-550).
func hasExtld(ldflags []string) bool {
	for _, f := range ldflags {
		if f == "-extld" || strings.HasPrefix(f, "-extld=") {
			return true
		}
	}
	return false
}

// expandLDFlags splits each ldflag element into separate exec arguments using
// the same quoting rules as `go build -ldflags` (cmd/internal/quoted.Split).
//
// Nix users write ldflags like ["-X main.Version=1.6"] or
// ["-extldflags '-static -L/foo/lib'"] and expect the quoted value to stay
// intact when invoking the linker directly.
func expandLDFlags(flags []string) ([]string, error) {
	var out []string
	for _, f := range flags {
		parts, err := quotedSplit(f)
		if err != nil {
			return nil, err
		}
		out = append(out, parts...)
	}
	return out, nil
}

// quotedSplit is copied from src/cmd/internal/quoted/quoted.go (Go 1.26.1,
// BSD-3-Clause) so go2nix's ldflags parsing matches go build -ldflags exactly.
// Upstream ships an identical copy in cmd/dist/quoted.go for the same reason.
//
// Split splits s into a list of fields, allowing single or double quotes
// around elements. There is no unescaping or other processing within
// quoted fields.
func quotedSplit(s string) ([]string, error) {
	// Split fields allowing '' or "" around elements.
	// Quotes further inside the string do not count.
	var f []string
	for len(s) > 0 {
		for len(s) > 0 && isSpaceByte(s[0]) {
			s = s[1:]
		}
		if len(s) == 0 {
			break
		}
		// Accepted quoted string. No unescaping inside.
		if s[0] == '"' || s[0] == '\'' {
			quote := s[0]
			s = s[1:]
			i := 0
			for i < len(s) && s[i] != quote {
				i++
			}
			if i >= len(s) {
				return nil, fmt.Errorf("unterminated %c string", quote)
			}
			f = append(f, s[:i])
			s = s[i+1:]
			continue
		}
		i := 0
		for i < len(s) && !isSpaceByte(s[i]) {
			i++
		}
		f = append(f, s[:i])
		s = s[i:]
	}
	return f, nil
}

func isSpaceByte(c byte) bool {
	return c == ' ' || c == '\t' || c == '\n' || c == '\r'
}
