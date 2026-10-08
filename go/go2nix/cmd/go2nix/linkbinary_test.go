package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/numtide/go2nix/pkg/compile"
)

func TestExpandLDFlags(t *testing.T) {
	tests := []struct {
		name    string
		flags   []string
		want    []string
		wantErr bool
	}{
		{
			name:  "nil input",
			flags: nil,
			want:  nil,
		},
		{
			name:  "empty list",
			flags: []string{},
			want:  nil,
		},
		{
			name:  "single-token flags pass through",
			flags: []string{"-s", "-w"},
			want:  []string{"-s", "-w"},
		},
		{
			name:  "multi-token flag is split",
			flags: []string{"-X main.Version=1.6"},
			want:  []string{"-X", "main.Version=1.6"},
		},
		{
			name:  "equals form is not split",
			flags: []string{"-X=main.Version=1.6"},
			want:  []string{"-X=main.Version=1.6"},
		},
		{
			name:  "mixed flags",
			flags: []string{"-s", "-X main.Version=1.6", "-w", "-X main.Commit=abc"},
			want:  []string{"-s", "-X", "main.Version=1.6", "-w", "-X", "main.Commit=abc"},
		},
		{
			name:  "quoted extldflags are preserved as one value",
			flags: []string{"-extldflags '-static -L/foo/lib '"},
			want:  []string{"-extldflags", "-static -L/foo/lib "},
		},
		{
			name:  "double-quoted values are preserved",
			flags: []string{`-X "main.Message=hello world"`},
			want:  []string{"-X", "main.Message=hello world"},
		},
		{
			name:  "extra whitespace is collapsed",
			flags: []string{"  -s  ", "  -X   main.Version=1.6  "},
			want:  []string{"-s", "-X", "main.Version=1.6"},
		},
		{
			name:  "empty string element is skipped",
			flags: []string{"", "-s", ""},
			want:  []string{"-s"},
		},
		{
			name:    "unterminated quoted string returns error",
			flags:   []string{"-extldflags '-static -L/foo/lib"},
			wantErr: true,
		},
		{
			name:  "literal backslash preserved",
			flags: []string{"-X main.Path=C:\\Users\\me"},
			want:  []string{"-X", "main.Path=C:\\Users\\me"},
		},
		{
			name:  "mid-token quote is literal",
			flags: []string{`a"b c"d`},
			want:  []string{`a"b`, `c"d`},
		},
		{
			name:  "adjacent quoted fields",
			flags: []string{`"hello" "world"`},
			want:  []string{"hello", "world"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := expandLDFlags(tt.flags)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expandLDFlags(%v) returned nil error, want error", tt.flags)
				}
				return
			}
			if err != nil {
				t.Fatalf("expandLDFlags(%v) returned unexpected error: %v", tt.flags, err)
			}
			if !slicesEqual(got, tt.want) {
				t.Errorf("expandLDFlags(%v) = %v, want %v", tt.flags, got, tt.want)
			}
		})
	}
}

func TestHasExtld(t *testing.T) {
	tests := []struct {
		name    string
		ldflags []string
		want    bool
	}{
		{"absent", []string{"-s", "-w"}, false},
		{"separate form", []string{"-s", "-extld", "/custom/ld"}, true},
		{"equals form", []string{"-extld=/custom/ld"}, true},
		{"extldflags is not extld", []string{"-extldflags", "-static"}, false},
		{"extldflags equals form is not extld", []string{"-extldflags=-static"}, false},
		{"nil", nil, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := hasExtld(tt.ldflags); got != tt.want {
				t.Errorf("hasExtld(%v) = %v, want %v", tt.ldflags, got, tt.want)
			}
		})
	}
}

func slicesEqual(a, b []string) bool {
	if len(a) == 0 && len(b) == 0 {
		return true
	}
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// fakeToolchainEnv names the directory in which the test binary, started as
// "go" or "link", records its calls instead of compiling or linking.
const fakeToolchainEnv = "GO2NIX_TEST_FAKE_TOOLCHAIN"

// fakeLinksEnv is how many fake linkers must be running at the same time
// before any of them goes on.
const fakeLinksEnv = "GO2NIX_TEST_FAKE_LINKS"

func TestMain(m *testing.M) {
	if dir := os.Getenv(fakeToolchainEnv); dir != "" {
		switch filepath.Base(os.Args[0]) {
		case "go":
			os.Exit(fakeGo(dir, os.Args[1:]))
		case "link":
			os.Exit(fakeLink(dir, os.Args[1:]))
		}
	}
	os.Exit(m.Run())
}

// recordCall writes the arguments of one tool run to its own file.
func recordCall(dir, tool string, args []string) error {
	data, err := json.Marshal(args)
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, fmt.Sprintf("%s-%d.json", tool, os.Getpid())), data, 0o644)
}

// flagValue returns what follows "-name" or "-name=" in args.
func flagValue(args []string, name string) string {
	for i, a := range args {
		if a == "-"+name && i+1 < len(args) {
			return args[i+1]
		}
		if v, ok := strings.CutPrefix(a, "-"+name+"="); ok {
			return v
		}
	}
	return ""
}

func fakeGo(dir string, args []string) int {
	switch {
	case len(args) == 2 && args[0] == "env":
		fmt.Println(map[string]string{
			"GOOS":        "linux",
			"GOARCH":      "amd64",
			"GOAMD64":     "v1",
			"GOROOT":      dir,
			"GOTOOLDIR":   filepath.Join(dir, "tool"),
			"GOVERSION":   "go1.26.0",
			"CGO_ENABLED": "0",
		}[args[1]])
	case len(args) > 2 && args[0] == "tool" && args[1] == "compile":
		if err := recordCall(dir, "compile", args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		if err := os.WriteFile(flagValue(args, "o"), nil, 0o644); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
	}
	return 0
}

// fakeLink writes the path of the main archive it was given as the "binary".
// It reports in dir/overlap when another fake linker ran at the same time, and
// with fakeLinksEnv=N it fails unless N of them do. A main package in a
// directory called slow, fail or hang gets a linker that does that.
func fakeLink(dir string, args []string) int {
	fail := func(err error) int {
		fmt.Fprintln(os.Stderr, "fake link:", err)
		return 1
	}
	if err := recordCall(dir, "link", args); err != nil {
		return fail(err)
	}
	archive := args[len(args)-1]

	started := filepath.Join(dir, "started")
	running := filepath.Join(dir, "running")
	me := fmt.Sprint(os.Getpid())
	for _, d := range []string{started, running} {
		if err := os.WriteFile(filepath.Join(d, me), nil, 0o644); err != nil {
			return fail(err)
		}
	}
	defer os.Remove(filepath.Join(running, me)) //nolint:errcheck

	// A linker leaves only after `want` have started, so the last one to
	// start finds the others still running.
	var want int
	_, _ = fmt.Sscan(os.Getenv(fakeLinksEnv), &want)
	for deadline := time.Now().Add(30 * time.Second); ; time.Sleep(5 * time.Millisecond) {
		entries, err := os.ReadDir(started)
		if err != nil {
			return fail(err)
		}
		if len(entries) >= want {
			break
		}
		if time.Now().After(deadline) {
			return fail(fmt.Errorf("%d of %d linkers started; the others never ran alongside", len(entries), want))
		}
	}
	// Long enough for a linker that should not be running to show up.
	time.Sleep(50 * time.Millisecond)
	switch {
	case strings.Contains(archive, "/slow/"):
		time.Sleep(300 * time.Millisecond)
	case strings.Contains(archive, "/fail."):
		return fail(errors.New("asked to fail"))
	case strings.Contains(archive, "/hang."):
		time.Sleep(time.Minute)
	}
	if entries, err := os.ReadDir(running); err != nil {
		return fail(err)
	} else if len(entries) > 1 {
		_ = os.WriteFile(filepath.Join(dir, "overlap"), nil, 0o644)
	}

	fmt.Println("linked", archive)
	if err := os.WriteFile(flagValue(args, "o"), []byte(archive+"\n"), 0o755); err != nil {
		return fail(err)
	}
	return 0
}

// fakeToolchain puts the test binary on PATH as "go" and in GOTOOLDIR as
// "link", and returns the directory they record their calls in.
func fakeToolchain(t *testing.T) string {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	for _, d := range []string{"bin", "tool", "started", "running"} {
		if err := os.Mkdir(filepath.Join(dir, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for _, link := range []string{"bin/go", "tool/link"} {
		if err := os.Symlink(exe, filepath.Join(dir, link)); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv(fakeToolchainEnv, dir)
	t.Setenv("PATH", filepath.Join(dir, "bin"))
	// linkBinary clears GOROOT for the process; t.Setenv restores it.
	t.Setenv("GOROOT", "")
	// Variables linkBinary reads, so that the developer's own do not leak in.
	for key, value := range map[string]string{
		"GOOS": "linux", "GOARCH": "amd64", "GOAMD64": "v1", "CGO_ENABLED": "0",
		"CC": "", "CXX": "", "GOFIPS140": "", "NIX_BUILD_CORES": "",
	} {
		t.Setenv(key, value)
	}
	t.Setenv("NIX_BUILD_TOP", t.TempDir())
	// Under -race a process sleeps for a second before it exits; the fake
	// tools have nothing to wait for.
	t.Setenv("GORACE", "atexit_sleep_ms=0")
	return dir
}

// fakeCalls returns the argument lists the fake tool was run with.
func fakeCalls(t *testing.T, dir, tool string) [][]string {
	t.Helper()
	files, err := filepath.Glob(filepath.Join(dir, tool+"-*.json"))
	if err != nil {
		t.Fatal(err)
	}
	var calls [][]string
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		var args []string
		if err := json.Unmarshal(data, &args); err != nil {
			t.Fatal(err)
		}
		calls = append(calls, args)
	}
	return calls
}

// writeLinkFixture writes a module with one main package per sub-package and
// the link manifest for them, and returns the manifest path.
func writeLinkFixture(t *testing.T, subPackages ...string) string {
	t.Helper()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module example.com/m\n\ngo 1.26\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	m := compile.LinkManifest{
		Version:    compile.ManifestVersion,
		Kind:       compile.ManifestKindLink,
		ModuleRoot: root,
		Pname:      "m",
	}
	for _, sp := range subPackages {
		dir := filepath.Join(root, sp)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\n\nfunc main() {}\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		m.SubPackages = append(m.SubPackages, compile.LinkSubPackage{
			Path:  "./" + sp,
			Files: &compile.ManifestFiles{GoFiles: []string{"main.go"}},
		})
	}
	data, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	manifest := filepath.Join(t.TempDir(), "link-manifest.json")
	if err := os.WriteFile(manifest, data, 0o644); err != nil {
		t.Fatal(err)
	}
	return manifest
}

func setGOMAXPROCS(t *testing.T, n int) {
	t.Helper()
	prev := runtime.GOMAXPROCS(n)
	t.Cleanup(func() { runtime.GOMAXPROCS(prev) })
}

// distinct fails the test unless the values are n different ones.
func distinct(t *testing.T, what string, values []string, n int) {
	t.Helper()
	seen := map[string]bool{}
	for _, v := range values {
		if v != "" {
			seen[v] = true
		}
	}
	if len(values) != n || len(seen) != n {
		t.Errorf("%s: want %d different values, one per binary, got %q", what, n, values)
	}
}

func TestLinkBinaryLinksSubPackagesTogether(t *testing.T) {
	subPackages := []string{"cmd/a", "cmd/b", "cmd/c", "cmd/d"}
	dir := fakeToolchain(t)
	manifest := writeLinkFixture(t, subPackages...)
	out := t.TempDir()

	setGOMAXPROCS(t, len(subPackages))
	t.Setenv("NIX_BUILD_CORES", fmt.Sprint(len(subPackages)))
	t.Setenv(fakeLinksEnv, fmt.Sprint(len(subPackages)))
	t.Setenv("GOFIPS140", "latest")

	if err := linkBinary(manifest, out); err != nil {
		t.Fatal(err)
	}

	for _, sp := range subPackages {
		name := filepath.Base(sp)
		got, err := os.ReadFile(filepath.Join(out, "bin", name))
		if err != nil {
			t.Fatal(err)
		}
		if want := "example.com/m/" + sp + ".a\n"; !strings.HasSuffix(string(got), want) {
			t.Errorf("bin/%s was linked from %q, want the archive %q", name, got, want)
		}
	}

	var trimPaths []string
	for _, args := range fakeCalls(t, dir, "compile") {
		_, scratch, _ := strings.Cut(flagValue(args, "trimpath"), ";")
		trimPaths = append(trimPaths, scratch)
	}
	distinct(t, "compile scratch directory in -trimpath", trimPaths, len(subPackages))

	var fipsObjects, importcfgs []string
	for _, args := range fakeCalls(t, dir, "link") {
		fipsObjects = append(fipsObjects, flagValue(args, "fipso"))
		importcfgs = append(importcfgs, flagValue(args, "importcfg"))

		cfg, err := os.ReadFile(flagValue(args, "importcfg"))
		if err != nil {
			t.Fatal(err)
		}
		_, mainPath, _ := strings.Cut(strings.TrimSuffix(args[len(args)-1], ".a"), "/main-pkgs/")
		if want := `path\t` + mainPath + `\n`; !strings.Contains(string(cfg), want) {
			t.Errorf("importcfg of %s has no modinfo line with %q:\n%s", mainPath, want, cfg)
		}
	}
	distinct(t, "-fipso", fipsObjects, len(subPackages))
	distinct(t, "-importcfg", importcfgs, len(subPackages))
}

func TestLinkBinaryOneCoreLinksOneAtATime(t *testing.T) {
	dir := fakeToolchain(t)
	manifest := writeLinkFixture(t, "cmd/a", "cmd/b", "cmd/c")
	setGOMAXPROCS(t, 4)
	t.Setenv("NIX_BUILD_CORES", "1")

	if err := linkBinary(manifest, t.TempDir()); err != nil {
		t.Fatal(err)
	}
	if got := len(fakeCalls(t, dir, "link")); got != 3 {
		t.Errorf("linker ran %d times, want 3", got)
	}
	if _, err := os.Stat(filepath.Join(dir, "overlap")); err == nil {
		t.Error("two linkers ran at the same time with NIX_BUILD_CORES=1")
	}
}

func TestLinkBinarySameNameLastListedWins(t *testing.T) {
	dir := fakeToolchain(t)
	manifest := writeLinkFixture(t, "cmd/slow/tool", "cmd/also/slow/tool", "cmd/fast/tool")
	out := t.TempDir()
	setGOMAXPROCS(t, 4)
	t.Setenv("NIX_BUILD_CORES", "4")

	if err := linkBinary(manifest, out); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(out, "bin", "tool"))
	if err != nil {
		t.Fatal(err)
	}
	if want := "example.com/m/cmd/fast/tool.a\n"; !strings.HasSuffix(string(got), want) {
		t.Errorf("bin/tool was linked from %q, want the one listed last, %q", got, want)
	}
	if _, err := os.Stat(filepath.Join(dir, "overlap")); err == nil {
		t.Error("two of the linkers writing bin/tool ran at the same time")
	}
}

func TestLinkBinaryFailureKillsRunningLinkers(t *testing.T) {
	fakeToolchain(t)
	manifest := writeLinkFixture(t, "cmd/hang", "cmd/fail")
	setGOMAXPROCS(t, 4)
	t.Setenv("NIX_BUILD_CORES", "4")
	// The linker of cmd/fail fails once the one of cmd/hang runs.
	t.Setenv(fakeLinksEnv, "2")

	start := time.Now()
	err := linkBinary(manifest, t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "linking example.com/m/cmd/fail") {
		t.Errorf("err = %v, want the failure of cmd/fail", err)
	}
	if elapsed := time.Since(start); elapsed > 30*time.Second {
		t.Errorf("linkBinary returned after %v: it waited for the linker of cmd/hang", elapsed)
	}
}

func TestLinkBinaryBackendConcurrencyFollowsCores(t *testing.T) {
	const megabyte = 1 << 20
	tests := []struct {
		name          string
		nixBuildCores string
		switchEnv     string
		sourceBytes   int
		want          string
	}{
		{"small package", "8", "", 0, "-c=4"},
		{"small package, two cores granted", "2", "", 0, "-c=2"},
		{"large package", "8", "", megabyte, "-c=8"},
		{"large package, all cores", "", "", megabyte, "-c=16"},
		{"one core granted", "1", "", megabyte, "-c=1"},
		{"switched off", "8", "0", megabyte, "-c=1"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := fakeToolchain(t)
			manifest := writeLinkFixture(t, "cmd/a")
			setGOMAXPROCS(t, 32)
			t.Setenv("NIX_BUILD_CORES", tt.nixBuildCores)
			t.Setenv("GO19CONCURRENTCOMPILATION", tt.switchEnv)

			var m compile.LinkManifest
			data, err := os.ReadFile(manifest)
			if err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal(data, &m); err != nil {
				t.Fatal(err)
			}
			padding := "\n// " + strings.Repeat("x", tt.sourceBytes) + "\n"
			f, err := os.OpenFile(filepath.Join(m.ModuleRoot, "cmd/a/main.go"), os.O_APPEND|os.O_WRONLY, 0)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := f.WriteString(padding); err != nil {
				t.Fatal(err)
			}
			if err := f.Close(); err != nil {
				t.Fatal(err)
			}

			if err := linkBinary(manifest, t.TempDir()); err != nil {
				t.Fatal(err)
			}
			calls := fakeCalls(t, dir, "compile")
			if len(calls) != 1 {
				t.Fatalf("compiler ran %d times, want 1", len(calls))
			}
			var got string
			for _, arg := range calls[0] {
				if strings.HasPrefix(arg, "-c=") {
					got = arg
				}
			}
			if got != tt.want {
				t.Errorf("compiler was passed %q, want %q (%q)", got, tt.want, calls[0])
			}
		})
	}
}

func noOrder(n int) []int {
	after := make([]int, n)
	for i := range after {
		after[i] = -1
	}
	return after
}

// lockedBuffer is a bytes.Buffer that jobs and the test may use at once. The
// first write closes wrote.
type lockedBuffer struct {
	mu    sync.Mutex
	buf   bytes.Buffer
	once  sync.Once
	wrote chan struct{}
}

func newLockedBuffer() *lockedBuffer {
	return &lockedBuffer{wrote: make(chan struct{})}
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.once.Do(func() { close(b.wrote) })
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func TestLinkBinaryLogIsInListOrder(t *testing.T) {
	subPackages := []string{"cmd/a", "cmd/b", "cmd/c", "cmd/d"}
	fakeToolchain(t)
	manifest := writeLinkFixture(t, subPackages...)
	setGOMAXPROCS(t, len(subPackages))
	t.Setenv("NIX_BUILD_CORES", fmt.Sprint(len(subPackages)))
	t.Setenv(fakeLinksEnv, fmt.Sprint(len(subPackages)))

	w := newLockedBuffer()
	if err := linkBinaryTo(w, manifest, t.TempDir()); err != nil {
		t.Fatal(err)
	}
	var got []string
	for line := range strings.Lines(w.String()) {
		for _, sp := range subPackages {
			switch {
			case strings.Contains(line, `msg="compiling main" pkg=example.com/m/`+sp+"\n"):
				got = append(got, "compile "+sp)
			case strings.Contains(line, "msg=linking pkg=example.com/m/"+sp+" "):
				got = append(got, "link "+sp)
			case strings.HasPrefix(line, "linked ") && strings.HasSuffix(line, "/example.com/m/"+sp+".a\n"):
				got = append(got, "linker output "+sp)
			}
		}
	}
	var want []string
	for _, sp := range subPackages {
		want = append(want, "compile "+sp, "link "+sp, "linker output "+sp)
	}
	if !slicesEqual(got, want) {
		t.Errorf("log has %q, want %q\n%s", got, want, w.String())
	}
}

func TestRunLinkJobsLogsInIndexOrder(t *testing.T) {
	const n = 4
	finished := make([]chan struct{}, n+1)
	for i := range finished {
		finished[i] = make(chan struct{})
	}
	close(finished[n])

	var w bytes.Buffer
	err := runLinkJobs(context.Background(), n, n, t.TempDir(), noOrder(n), &w, func(_ context.Context, job linkJob) error {
		// Finish in reverse order: each job waits for the next one.
		<-finished[job.index+1]
		_, _ = fmt.Fprintf(job.log, "job %d\n", job.index)
		close(finished[job.index])
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := w.String(), "job 0\njob 1\njob 2\njob 3\n"; got != want {
		t.Errorf("log = %q, want %q", got, want)
	}
}

func TestRunLinkJobsScratchDirsAreSeparate(t *testing.T) {
	const n = 8
	var written sync.WaitGroup
	written.Add(n)
	err := runLinkJobs(context.Background(), n, n, t.TempDir(), noOrder(n), &bytes.Buffer{}, func(_ context.Context, job linkJob) error {
		name := filepath.Join(job.scratch, "go_asm.h")
		mine := fmt.Sprint(job.index)
		if err := os.WriteFile(name, []byte(mine), 0o644); err != nil {
			return err
		}
		written.Done()
		written.Wait()
		got, err := os.ReadFile(name)
		if err != nil {
			return err
		}
		if string(got) != mine {
			return fmt.Errorf("job %d reads back %q from %s", job.index, got, name)
		}
		return nil
	})
	if err != nil {
		t.Error(err)
	}
}

func TestRunLinkJobsFirstFailureStopsTheRest(t *testing.T) {
	errBoom := errors.New("boom")
	var ran [4]atomic.Bool
	zeroStarted := make(chan struct{})
	var w bytes.Buffer
	err := runLinkJobs(context.Background(), 4, 2, t.TempDir(), noOrder(4), &w, func(ctx context.Context, job linkJob) error {
		ran[job.index].Store(true)
		_, _ = fmt.Fprintf(job.log, "job %d\n", job.index)
		if job.index == 1 {
			<-zeroStarted
			return errBoom
		}
		if job.index == 0 {
			close(zeroStarted)
		}
		<-ctx.Done()
		return ctx.Err()
	})
	if !errors.Is(err, errBoom) {
		t.Errorf("err = %v, want %v", err, errBoom)
	}
	if ran[2].Load() || ran[3].Load() {
		t.Errorf("jobs 2 and 3 ran after job 1 failed: %v %v", ran[2].Load(), ran[3].Load())
	}
	if got, want := w.String(), "job 0\njob 1\n"; got != want {
		t.Errorf("log = %q, want %q", got, want)
	}
}

func TestRunLinkJobsReportsLowestFailure(t *testing.T) {
	errFirst, errLater := errors.New("first listed"), errors.New("listed later")
	zeroStarted := make(chan struct{})
	w := newLockedBuffer()
	err := runLinkJobs(context.Background(), 3, 3, t.TempDir(), noOrder(3), w, func(ctx context.Context, job linkJob) error {
		_, _ = fmt.Fprintf(job.log, "job %d\n", job.index)
		switch job.index {
		case 0:
			close(zeroStarted)
			<-ctx.Done()
			return errFirst
		case 2:
			<-zeroStarted
			return errLater
		}
		return nil
	})
	if !errors.Is(err, errFirst) {
		t.Errorf("err = %v, want %v", err, errFirst)
	}
	if got, want := w.String(), "job 0\n"; got != want {
		t.Errorf("log = %q, want %q: nothing is printed past the failure that is reported", got, want)
	}
}

func TestRunLinkJobsAfter(t *testing.T) {
	var zeroDone atomic.Bool
	oneDone := make(chan struct{})
	err := runLinkJobs(context.Background(), 3, 3, t.TempDir(), []int{-1, -1, 0}, &bytes.Buffer{}, func(_ context.Context, job linkJob) error {
		switch job.index {
		case 0:
			// Job 1 runs alongside; job 2 must not.
			<-oneDone
			time.Sleep(10 * time.Millisecond)
			zeroDone.Store(true)
		case 1:
			close(oneDone)
		case 2:
			if !zeroDone.Load() {
				return errors.New("job 2 started before job 0 was done")
			}
		}
		return nil
	})
	if err != nil {
		t.Error(err)
	}
}

func TestRunLinkJobsLimit(t *testing.T) {
	var now, peak atomic.Int32
	err := runLinkJobs(context.Background(), 12, 3, t.TempDir(), noOrder(12), &bytes.Buffer{}, func(context.Context, linkJob) error {
		n := now.Add(1)
		for p := peak.Load(); n > p && !peak.CompareAndSwap(p, n); p = peak.Load() {
		}
		time.Sleep(5 * time.Millisecond)
		now.Add(-1)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := peak.Load(); got > 3 {
		t.Errorf("%d jobs ran at once, limit is 3", got)
	}
}

func TestRunLinkJobsOneAtATimeIsNotBuffered(t *testing.T) {
	var w bytes.Buffer
	err := runLinkJobs(context.Background(), 3, 1, t.TempDir(), noOrder(3), &w, func(_ context.Context, job linkJob) error {
		_, _ = fmt.Fprintf(job.log, "job %d\n", job.index)
		if want := fmt.Sprintf("job %d\n", job.index); !strings.HasSuffix(w.String(), want) {
			return fmt.Errorf("after job %d wrote its log, w ends with %q", job.index, w.String())
		}
		return nil
	})
	if err != nil {
		t.Error(err)
	}
	if got, want := w.String(), "job 0\njob 1\njob 2\n"; got != want {
		t.Errorf("log = %q, want %q", got, want)
	}
}

func TestRunLinkJobsLogAppearsOnceEarlierJobsAreDone(t *testing.T) {
	w := newLockedBuffer()
	err := runLinkJobs(context.Background(), 3, 3, t.TempDir(), noOrder(3), w, func(_ context.Context, job linkJob) error {
		_, _ = fmt.Fprintf(job.log, "job %d\n", job.index)
		if job.index == 0 {
			return nil
		}
		select {
		case <-w.wrote:
			return nil
		case <-time.After(30 * time.Second):
			return fmt.Errorf("job %d still runs and the log of job 0 has not been written", job.index)
		}
	})
	if err != nil {
		t.Error(err)
	}
	if got, want := w.String(), "job 0\njob 1\njob 2\n"; got != want {
		t.Errorf("log = %q, want %q", got, want)
	}
}

func TestRunLinkJobsSuccessorOfAFailureDoesNotStart(t *testing.T) {
	errBoom := errors.New("boom")
	scratch := t.TempDir()
	for round := range 2000 {
		var ran atomic.Bool
		err := runLinkJobs(context.Background(), 2, 2, scratch, []int{-1, 0}, &bytes.Buffer{}, func(_ context.Context, job linkJob) error {
			if job.index == 0 {
				return errBoom
			}
			ran.Store(true)
			return nil
		})
		if !errors.Is(err, errBoom) {
			t.Fatalf("round %d: err = %v, want %v", round, err, errBoom)
		}
		if ran.Load() {
			t.Fatalf("round %d: job 1 ran although job 0, which it waits for, had failed", round)
		}
	}
}
