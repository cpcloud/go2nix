# Regression: -lang must be threaded to non-root local packages.
# go.mod says `go 1.21`; internal/loop relies on pre-1.22 shared-loopvar
# semantics. The per-package src filter excludes go.mod, so without an
# explicit --go-version the subpackage would compile under the toolchain's
# default language version and the binary would print [0 1 2].
#
# The tests of internal/loop ask the same of the test runner, which compiles
# the package again with its _test.go files, its external test package, and
# internal/mid, a dependent that the external test imports. Both packages
# have a srcOverlay: the runner compiles them from a copy of their
# directory that has no go.mod above it, so it has to know the language
# version beforehand. With -test.v the check phase can see that the tests
# ran.
let
  pkgs = import <nixpkgs> { };
  inherit (pkgs) go;
  go2nix = import ../../../packages/go2nix { inherit pkgs; };
  goEnv = import ../../../nix/mk-go-env.nix {
    inherit go go2nix;
    inherit (pkgs) callPackage;
  };
in
goEnv.buildGoApplication {
  pname = "lang-loopvar";
  version = "0.0.1";
  src = ./.;
  goLock = ./go2nix.toml;
  doCheck = true;
  checkFlags = [ "-test.v" ];
  packageOverrides = {
    "example.com/langloopvar/internal/loop" = {
      srcOverlay = pkgs.runCommand "loop-overlay" { } ''
        mkdir -p $out
        echo -n "generated" > $out/banner.txt
      '';
    };
    "example.com/langloopvar/internal/mid" = {
      srcOverlay = pkgs.runCommand "mid-overlay" { } ''
        mkdir -p $out
        echo -n "generated" > $out/tag.txt
      '';
    };
  };
  # The check phase asserts on its own output: a wrapper that runs inside a
  # sandboxed build cannot read this derivation's log.
  preCheck = ''
    exec 3>&1 4>&2 >"$NIX_BUILD_TOP/check.log" 2>&1
  '';
  failureHook = ''
    [ -e "$NIX_BUILD_TOP/check.log" ] && cat "$NIX_BUILD_TOP/check.log" >&4
  '';
  postCheck = ''
    exec >&3 2>&4
    mv "$NIX_BUILD_TOP/check.log" "$NIX_BUILD_TOP/check.done"
    cat "$NIX_BUILD_TOP/check.done"
    for t in TestOverlay TestLibraryLoopvar TestTestFileLoopvar TestXTestFileLoopvar TestRecompiledDependentLoopvar; do
      grep -q -- "--- PASS: $t" "$NIX_BUILD_TOP/check.done" \
        || { echo "FAIL: $t did not run and pass"; exit 1; }
    done
  '';
}
