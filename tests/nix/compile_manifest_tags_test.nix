# tests/nix/compile_manifest_tags_test.nix: build tags reach a package's
# compile derivation only when the package has cgo files.
#
# File selection happens at eval time, so compile-package reads the tags
# only to evaluate `#cgo` lines in cgo files. A pure-Go package must get
# the same compile derivation whatever tags the application sets, so that
# two applications with different tags share it; a cgo package must not.
#
# tests/fixtures/cgo-internal-test has one of each: internal/stamp is pure
# Go and internal/adder imports "C". The fixture is evaluated twice, with
# no tags and with a tag that selects no file, and the compile .drv paths
# are compared.
#
# Evaluation only, like test-drv-canary: drvPath in read-only mode with a
# dummy store needs neither a daemon nor recursive-nix. A stand-in for the
# go2nix CLI keeps the paths independent of go/go2nix/.
{
  flake,
  pkgs,
  system,
  ...
}:
let
  plugin = flake.packages.${system}.go2nix-nix-plugin;
  nix = pkgs.nixVersions.nix_2_34;
  nixpkgsPath = pkgs.path;
  go2nixSrc = flake;

  expr = pkgs.writeText "compile-manifest-tags.nix" ''
    let
      pkgs = import <nixpkgs> { };
      goEnv = import ${go2nixSrc}/nix/mk-go-env.nix {
        inherit (pkgs) go callPackage;
        go2nix = derivation {
          name = "go2nix-stand-in";
          system = "${system}";
          builder = "/bin/sh";
        };
      };
      app =
        tags:
        goEnv.buildGoApplication {
          pname = "cgo-internal-test";
          version = "0.0.1";
          src = ${go2nixSrc}/tests/fixtures/cgo-internal-test;
          goLock = ${go2nixSrc}/tests/fixtures/cgo-internal-test/go2nix.toml;
          inherit tags;
        };
      compileDrv =
        tags: pkg: (app tags).passthru.localPackages."example.com/cgo-internal-test/internal/''${pkg}".drvPath;
    in
    {
      stampNoTags = compileDrv [ ] "stamp";
      stampTagged = compileDrv [ "go2nix_unused_tag" ] "stamp";
      adderNoTags = compileDrv [ ] "adder";
      adderTagged = compileDrv [ "go2nix_unused_tag" ] "adder";
    }
  '';
in
pkgs.runCommand "compile-manifest-tags-test"
  {
    nativeBuildInputs = [
      nix
      pkgs.jq
    ];
  }
  ''
    export HOME=$(mktemp -d)
    mkdir "$TMPDIR/empty-gmc"

    paths=$(GOMODCACHE="$TMPDIR/empty-gmc" nix-instantiate \
      --eval --strict --json --readonly-mode --store dummy:// \
      -I nixpkgs=${nixpkgsPath} \
      --option plugin-files "${plugin}/lib/nix/plugins/libgo2nix_plugin.so" \
      ${expr})
    echo "$paths" | jq .

    field() { echo "$paths" | jq -r ".$1"; }

    fail=0
    if [ "$(field stampNoTags)" != "$(field stampTagged)" ]; then
      echo "FAIL: pure-Go internal/stamp got a different compile drv when the tags changed"
      fail=1
    fi
    if [ "$(field adderNoTags)" = "$(field adderTagged)" ]; then
      echo "FAIL: cgo internal/adder got the same compile drv when the tags changed"
      fail=1
    fi

    [ "$fail" -eq 0 ] || exit 1
    echo "compile-manifest-tags-test: 2 assertions passed" > $out
  ''
