# Test: a test binary is linked the way `go test` links it.
#
# go.mod says `go 1.21`, so the application gets the 1.21 default GODEBUG
# (httpmuxgo121=1 among others) and its tests have to run with the same
# one, plus the //go:debug lines of the _test.go files, and with
# testing.Testing() reporting true. internal/probe's tests assert all of
# it; with -test.v the check phase can see that they ran.
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
  pname = "test-link-flags";
  version = "0.0.1";
  src = ./.;
  goLock = ./go2nix.toml;
  doCheck = true;
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
    for t in TestTesting TestGoModGodebug TestTestFileGodebug TestXTestFileGodebug; do
      grep -q -- "--- PASS: $t" "$NIX_BUILD_TOP/check.done" \
        || { echo "FAIL: $t did not run and pass"; exit 1; }
    done
  '';
}
