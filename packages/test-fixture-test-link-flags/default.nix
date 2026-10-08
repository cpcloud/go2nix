# default mode fixture test: test binaries get testing.Testing() and the
# application's default GODEBUG.
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
  pkgs.runCommand "test-dag-fixture-test-link-flags-unsupported"
    { meta.platforms = pkgs.lib.platforms.linux; }
    ''
      echo "test-dag-fixture-test-link-flags requires go2nix-nix-plugin (Linux only)" >&2
      exit 1
    ''
else
  let
    plugin = flake.packages.${system}.go2nix-nix-plugin;
    nix = pkgs.nixVersions.nix_2_34;

    nixpkgsPath = pkgs.path;
    go2nixSrc = flake;
  in
  pkgs.runCommand "test-dag-fixture-test-link-flags"
    {
      nativeBuildInputs = [ nix ];
      requiredSystemFeatures = [ "recursive-nix" ];
    }
    ''
      export HOME=$(mktemp -d)
      export NIX_CONFIG="extra-experimental-features = nix-command recursive-nix"

      echo "=== Building test-link-flags fixture (default mode, go 1.21, doCheck=true) ==="
      result=$(nix-build ${go2nixSrc}/tests/fixtures/test-link-flags/dag.nix \
        -I nixpkgs=${nixpkgsPath} \
        --option plugin-files "${plugin}/lib/nix/plugins/libgo2nix_plugin.so" \
        --no-out-link)

      echo "=== Asserting the binary runs with the go 1.21 mux ==="
      got=$($result/bin/test-link-flags)
      [ "$got" = "404" ] || { echo "FAIL: binary printed '$got', want 404"; exit 1; }

      echo "PASS: test-link-flags" > $out
    ''
