# Test: default mode with a *transitive* C++ cgo dependency.
#
# main.go is pure Go; internal/cxxdep has a .cc file that uses std::string.
# Exercises nix/dag's per-subPackage closure walk that sets cxx=true (so
# linkbinary picks $CXX, not $CC, for -extld), plus compileCgo's CXX path
# (compile/cgo.go compileCFiles for CXXFiles). Linking with CC would fail
# on undefined libstdc++ symbols, so the build itself is the regression.
#
# internal/cxxdep has a test: its binary links the same C++ object, so the
# test manifest's cxx bit has to select $CXX for the test link as well.
# With -test.v the check phase can see that the test ran.
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
  pname = "cxx-cgo";
  version = "0.0.1";
  src = ./.;
  goLock = ./go2nix.toml;
  checkFlags = [ "-test.v" ];
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
    for t in TestConcat; do
      grep -q -- "--- PASS: $t" "$NIX_BUILD_TOP/check.done" \
        || { echo "FAIL: $t did not run and pass"; exit 1; }
    done
  '';
}
