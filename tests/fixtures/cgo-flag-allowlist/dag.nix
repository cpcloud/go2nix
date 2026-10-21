# Test: default mode with cgo packages whose `#cgo CFLAGS` have a flag that is
# not on cmd/go's lists of permitted flags. compile-package refuses such a flag
# as `go build` does, unless CGO_CFLAGS_ALLOW in the environment of the
# derivation that compiles the package permits it.
#
#   allowed     internal/mathflag asks for -ffinite-math-only. Its derivation
#               gets CGO_CFLAGS_ALLOW from packageOverrides; the application's
#               gets it from env, because the test runner compiles the package
#               again there, with its _test.go file. It builds, the test
#               passes and the binary reports that the flag was in effect.
#   notAllowed  the same with neither: internal/mathflag fails.
#   refused     cmd/refused imports internal/plugin, whose
#               -fplugin=/nonexistent.so nothing permits: it fails before the
#               C compiler has seen the flag.
let
  pkgs = import <nixpkgs> { };
  inherit (pkgs) go;
  go2nix = import ../../../packages/go2nix { inherit pkgs; };
  goEnv = import ../../../nix/mk-go-env.nix {
    inherit go go2nix;
    inherit (pkgs) callPackage;
  };
  build =
    args:
    goEnv.buildGoApplication (
      {
        pname = "cgo-flag-allowlist";
        version = "0.0.1";
        src = ./.;
        goLock = ./go2nix.toml;
      }
      // args
    );
  allow = {
    "example.com/cgo-flag-allowlist/internal/mathflag".env = {
      CGO_CFLAGS_ALLOW = "-ffinite-math-only";
    };
  };
in
{
  allowed = build {
    packageOverrides = allow;
    env.CGO_CFLAGS_ALLOW = "-ffinite-math-only";
    doCheck = true;
  };
  notAllowed = build { };
  refused = build { subPackages = [ "./cmd/refused" ]; };
}
