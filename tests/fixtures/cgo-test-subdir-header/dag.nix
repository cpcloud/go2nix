# Test: checkPhase for a local cgo package that includes a header from a
# sub-directory and has an internal test.
#
# The internal test archive is the package compiled again with its _test.go
# files, so that compile has to find include/factor.h the way the package's
# own derivation does: relative to the package's directory. `go list` reports
# only the headers next to the Go files, hence extraMainSrcFiles for the
# sub-directory. With -test.v the check phase can see that the test ran.
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
  pname = "cgo-test-subdir-header";
  version = "0.0.1";
  src = ./.;
  goLock = ./go2nix.toml;
  doCheck = true;
  checkFlags = [ "-test.v" ];
  extraMainSrcFiles = [ "internal/scale/include" ];
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
    for t in TestTriple; do
      grep -q -- "--- PASS: $t" "$NIX_BUILD_TOP/check.done" \
        || { echo "FAIL: $t did not run and pass"; exit 1; }
    done
  '';
}
