# Test: contentAddressed = true with a package that has external tests only.
#
# internal/core has no internal test, and its external test imports
# internal/user, which imports internal/core back. Nothing replaces core's
# archive, so the compile-side importcfg has to keep core's interface file:
# pointing it at the link object, which carries no export data, breaks any
# compile that imports core. With -test.v the check phase can see that the
# test ran.
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
  pname = "xtest-only-ca";
  version = "0.0.1";
  src = ./.;
  goLock = ./go2nix.toml;
  doCheck = true;
  contentAddressed = true;
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
    for t in TestNext; do
      grep -q -- "--- PASS: $t" "$NIX_BUILD_TOP/check.done" \
        || { echo "FAIL: $t did not run and pass"; exit 1; }
    done
  '';
}
