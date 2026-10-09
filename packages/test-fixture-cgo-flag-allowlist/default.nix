# default mode fixture test: `#cgo` flags checked against cmd/go's lists.
#
# A flag the lists do not have fails its package with the message `go build`
# gives, before the C compiler runs; CGO_CFLAGS_ALLOW from
# packageOverrides.<pkg>.env lets it through in the package's derivation and
# from env in the application's, where the test runner compiles the package
# with its tests, and then it reaches the compiler in both.
{
  flake,
  pkgs,
  system,
  ...
}:
if !(flake.packages.${system} ? go2nix-nix-plugin) then
  pkgs.runCommand "test-dag-fixture-cgo-flag-allowlist-unsupported"
    { meta.platforms = pkgs.lib.platforms.linux; }
    ''
      echo "test-dag-fixture-cgo-flag-allowlist requires go2nix-nix-plugin (Linux only)" >&2
      exit 1
    ''
else
  let
    plugin = flake.packages.${system}.go2nix-nix-plugin;
    nix = pkgs.nixVersions.nix_2_34;

    nixpkgsPath = pkgs.path;
    go2nixSrc = flake;
  in
  pkgs.runCommand "test-dag-fixture-cgo-flag-allowlist"
    {
      nativeBuildInputs = [ nix ];
      requiredSystemFeatures = [ "recursive-nix" ];
    }
    ''
      export HOME=$(mktemp -d)
      export NIX_CONFIG="extra-experimental-features = nix-command recursive-nix"

      dag=${go2nixSrc}/tests/fixtures/cgo-flag-allowlist/dag.nix
      nixArgs=(
        -I nixpkgs=${nixpkgsPath}
        --option plugin-files "${plugin}/lib/nix/plugins/libgo2nix_plugin.so"
      )

      # The log of a package's derivation: under recursive-nix it goes to the
      # store, not to the stderr of the nix-build that failed on it.
      packageLog() {
        local drv pkgDrv
        drv=$(nix-instantiate "$dag" -A "$1" "''${nixArgs[@]}" 2>/dev/null)
        pkgDrv=$(nix-store -q --requisites "$drv" | grep -- "-golocal-.*-internal-$2\.drv\$")
        nix-store --read-log "$pkgDrv"
      }

      echo "=== allowed: CGO_CFLAGS_ALLOW from packageOverrides and env ==="
      result=$(nix-build "$dag" -A allowed "''${nixArgs[@]}" --no-out-link)
      got=$($result/bin/cgo-flag-allowlist)
      [ "$got" = "finite-math-only: 1" ] \
        || { echo "FAIL: -ffinite-math-only did not reach the C compiler: '$got'"; exit 1; }

      echo "=== notAllowed: the same flag without the override ==="
      if nix-build "$dag" -A notAllowed "''${nixArgs[@]}" --no-out-link; then
        echo "FAIL: built with a #cgo flag that is not on cmd/go's lists"
        exit 1
      fi
      packageLog notAllowed mathflag | tee notAllowed.log
      grep -qF 'example.com/cgo-flag-allowlist/internal/mathflag: invalid flag in #cgo CFLAGS: -ffinite-math-only' notAllowed.log \
        || { echo "FAIL: internal/mathflag did not fail on its flag"; exit 1; }

      echo "=== refused: -fplugin= ==="
      if nix-build "$dag" -A refused "''${nixArgs[@]}" --no-out-link; then
        echo "FAIL: built with #cgo CFLAGS: -fplugin="
        exit 1
      fi
      packageLog refused plugin | tee refused.log
      grep -qF 'example.com/cgo-flag-allowlist/internal/plugin: invalid flag in #cgo CFLAGS: -fplugin=/nonexistent.so' refused.log \
        || { echo "FAIL: internal/plugin did not fail on its flag"; exit 1; }
      ! grep -q 'cannot load plugin' refused.log \
        || { echo "FAIL: the C compiler was given -fplugin="; exit 1; }

      echo "PASS: cgo-flag-allowlist" > $out
    ''
