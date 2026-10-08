# default mode fixture test: internal test of a cgo package that includes a
# header from a sub-directory.
#
# Spawns nix-build with --option plugin-files so the go2nix-nix-plugin is
# available during evaluation. Requires recursive-nix.
{
  flake,
  pkgs,
  system,
  ...
}:
if !(flake.packages.${system} ? go2nix-nix-plugin) then
  pkgs.runCommand "test-dag-fixture-cgo-test-subdir-header-unsupported"
    { meta.platforms = pkgs.lib.platforms.linux; }
    ''
      echo "test-dag-fixture-cgo-test-subdir-header requires go2nix-nix-plugin (Linux only)" >&2
      exit 1
    ''
else
  let
    plugin = flake.packages.${system}.go2nix-nix-plugin;
    nix = pkgs.nixVersions.nix_2_34;

    nixpkgsPath = pkgs.path;
    go2nixSrc = flake;
  in
  pkgs.runCommand "test-dag-fixture-cgo-test-subdir-header"
    {
      nativeBuildInputs = [ nix ];
      requiredSystemFeatures = [ "recursive-nix" ];
    }
    ''
      export HOME=$(mktemp -d)
      export NIX_CONFIG="extra-experimental-features = nix-command recursive-nix"

      echo "=== Building cgo-test-subdir-header fixture (default mode, doCheck=true) ==="
      result=$(nix-build ${go2nixSrc}/tests/fixtures/cgo-test-subdir-header/dag.nix \
        -I nixpkgs=${nixpkgsPath} \
        --option plugin-files "${plugin}/lib/nix/plugins/libgo2nix_plugin.so" \
        --no-out-link)

      got=$($result/bin/cgo-test-subdir-header)
      [ "$got" = "42" ] || { echo "FAIL: got '$got', want 42"; exit 1; }

      echo "PASS: cgo-test-subdir-header" > $out
    ''
