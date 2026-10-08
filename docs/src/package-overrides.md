# Package Overrides

`packageOverrides` lets you customize the per-package derivation for
specific Go packages — typically to give a cgo package the C toolchain
inputs it needs.

This page covers the lookup rules, the supported keys, and worked examples.
For the one-line summary, see the [Builder API](builder-api.md) table.

## Lookup order

`packageOverrides` is keyed by **import path or module path**. When
compiling a package, the builder looks up an override in this order:

1. The package's exact import path
   (e.g. `"github.com/diamondburned/gotk4/pkg/core/glib"`).
1. The package's module path (e.g. `"github.com/diamondburned/gotk4/pkg"`).
1. Otherwise, no override.

In default mode only the first match is used; entries are **not** merged.
(The experimental builder applies both: the `nativeBuildInputs` of the
import-path entry and of the module-path entry are added together.)
Module-path keys are convenient when one module ships many cgo packages that
all need the same system libraries.

> In default mode the module-path fallback applies to local packages too: a
> key equal to your main module's path, or to the path of a module you
> `replace` with a directory, is applied to every local package of that
> module. In experimental mode local packages are matched by exact import
> path only.

## Supported keys

| Key | Type | Default mode | Experimental mode | Notes |
|-----|------|--------------|-------------------|-------|
| `nativeBuildInputs` | list of derivations | cgo packages only | yes | Default mode: added to the per-package derivation's `nativeBuildInputs`. Experimental mode: their `bin`, `lib/pkgconfig` and `include` directories are put on `PATH`, `PKG_CONFIG_PATH` and `CGO_CFLAGS`, and a derivation's `dev` output is added automatically |
| `env` | attrset | yes | no | Extra environment variables on the per-package derivation |
| `srcOverlay` | derivation or path | yes | no | Contents are layered onto the package's source directory at build time |

In default mode, `nativeBuildInputs` from every entry in `packageOverrides`
(regardless of whether the key matched a package in the graph) are also
collected and added to the **final** application derivation, so headers and
libraries are available at link time as well.

### `nativeBuildInputs` is cgo-only

Non-cgo packages are compiled with a raw builder (`rawGoCompile`) that
bypasses stdenv entirely and hardcodes `PATH` — `nativeBuildInputs` would
silently do nothing. When the key is that package's exact import path the
builder rejects it instead; when the entry matched through the module path,
which legitimately covers cgo and non-cgo packages alike, it is dropped for
the non-cgo ones without an error. For the error message and fix list, see
[Troubleshooting](troubleshooting.md#packageoverridespath-unknown-attributes-nativebuildinputs).

## Example: single cgo package

`dotool` has one local cgo package wrapping libxkbcommon via pkg-config:

```nix
goEnv.buildGoApplication {
  pname = "dotool";
  version = "1.6";
  src = ./.;
  goLock = ./go2nix.toml;

  packageOverrides = {
    "git.sr.ht/~geb/dotool/xkb" = {
      nativeBuildInputs = [
        pkgs.pkg-config
        pkgs.libxkbcommon
      ];
    };
  };
}
```

## Example: many cgo packages from one module

`gotk4` ships dozens of cgo packages under one module. Key the override by
the **module path** so it applies to every package in that module:

```nix
let
  gtkDeps = {
    nativeBuildInputs = [
      pkgs.pkg-config
      pkgs.glib
      pkgs.cairo
      pkgs.gobject-introspection
      pkgs.gdk-pixbuf
      pkgs.pango
      pkgs.gtk3
      pkgs.at-spi2-core
      pkgs.gtk-layer-shell
    ];
  };
in
goEnv.buildGoApplication {
  pname = "nwg-drawer";
  version = "0.7.4";
  src = ./.;
  goLock = ./go2nix.toml;

  packageOverrides = {
    "github.com/diamondburned/gotk4/pkg" = gtkDeps;
    "github.com/diamondburned/gotk4-layer-shell/pkg" = gtkDeps;
  };
}
```

## Example: `env`

```nix
packageOverrides = {
  "github.com/example/pkg" = {
    env = {
      CGO_CPPFLAGS = "-I${libfoo.dev}/include";
    };
  };
};
```

`CGO_CPPFLAGS`, `CGO_CFLAGS`, `CGO_CXXFLAGS`, `CGO_FFLAGS` and `CGO_LDFLAGS`
mean what they mean to `go build`: they go ahead of the flags from the
package's own `#cgo` lines and from `pkg-config`, and one that is unset or
empty counts as `-O2 -g` (`CGO_CPPFLAGS` as empty). So a `CGO_CFLAGS` of your
own replaces `-O2 -g` for that package's C code: put include paths and macros
in `CGO_CPPFLAGS`, which also reaches C++ and Fortran files, and set
`CGO_CFLAGS = "-O2"` to drop `-g`.

The `#cgo CFLAGS`, `CPPFLAGS`, `CXXFLAGS`, `FFLAGS` and `LDFLAGS` lines of the
sources need no override; their conditions (`#cgo linux`, a build tag) and
`${SRCDIR}` work as they do under `go build`, with two exceptions: the
experimental builder passes no tags to the compile step, and a local package's
source holds its own directory only, so `-I${SRCDIR}/../include` finds nothing.

`go build` refuses `#cgo` flags and `pkg-config` output that could make the
compiler or the linker load or run something (`-fplugin=`, `-specs=`, `-B`,
`-wrapper`, `-fuse-ld=`, `-Wa,…`, `-Xlinker`, `-Wl,--plugin`, `@file`, …)
unless `CGO_CFLAGS_ALLOW`, `CGO_LDFLAGS_ALLOW` and friends permit them.
`compile-package` passes on every flag go/build accepts, including those in a
dependency's sources, and does not read the `CGO_*_ALLOW` and `CGO_*_DISALLOW`
variables. What confines such a flag is the Nix sandbox: the build user,
without network, writing the package's own output. With `sandbox = false`
(the default on macOS) or `relaxed` it runs with the build user's access to
the machine.

`env` is default-mode only. The experimental builder synthesizes
derivations at build time inside `go2nix resolve` and can only forward
store paths, so it rejects `env` (and any other unknown key) at eval time.

## Example: `srcOverlay` for build-time-generated `//go:embed` content

When a package embeds files that are themselves build outputs — a bundled
SPA under `//go:embed all:dist`, generated protobuf descriptors, etc. —
`srcOverlay` lets you supply them as a derivation that is layered onto the
package's source directory at build time:

```nix
packageOverrides = {
  "example.com/app/ui" = {
    srcOverlay = pkgs.runCommand "ui-dist" { } ''
      mkdir -p $out/dist
      cp -r ${uiBundle}/* $out/dist/
    '';
  };
};
```

The overlay is `cp -rL`'d onto a writable copy of the package's source
before `go2nix compile-package` runs, so `ResolveEmbedCfg` sees the
generated files. This is a build-time input of that one compile derivation
only — it does **not** flow into `src` at evaluation time, so it does not
trigger IFD and does not invalidate sibling packages.

The source tree needs no placeholder for the generated files. The
eval-time `go list` reports a `//go:embed` pattern that matches nothing
(`pattern all:dist: no matching files found`) as a package error; the
plugin ignores that error for a package whose override sets `srcOverlay`
and keeps the package's files and imports. `compile-package` resolves the
patterns again over the overlaid source, so an overlay that lacks the files
still fails the build. In any other package the error stays fatal at eval
time.

Once one pattern fails, `go list` reports none of the package's embedded
files, not even checked-in matches of its other patterns. The plugin then
adds every file under each pattern's leading glob-free directories to the
application's link and test source, or the whole package directory when the
first path element has a glob. A change to any of those files re-links the
application and, under `doCheck`, re-runs its tests, even when no pattern
matches the file.

The `doCheck` testrunner applies the same overlay before resolving
`//go:embed` patterns and recompiling for tests, so tests see the
overlaid content too.
