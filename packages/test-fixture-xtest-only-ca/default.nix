# default mode fixture test: contentAddressed = true and a package with
# external tests only, whose test imports a package that imports it back.
#
# This relies on the inner recursive-nix store accepting CA derivations.
{
  flake,
  pkgs,
  system,
  ...
}:
if !(flake.packages.${system} ? go2nix-nix-plugin) then
  pkgs.runCommand "test-dag-fixture-xtest-only-ca-unsupported"
    { meta.platforms = pkgs.lib.platforms.linux; }
    ''
      echo "test-dag-fixture-xtest-only-ca requires go2nix-nix-plugin (Linux only)" >&2
      exit 1
    ''
else
  let
    plugin = flake.packages.${system}.go2nix-nix-plugin;
    nix = pkgs.nixVersions.nix_2_34;

    nixpkgsPath = pkgs.path;
    go2nixSrc = flake;
  in
  pkgs.runCommand "test-dag-fixture-xtest-only-ca"
    {
      nativeBuildInputs = [ nix ];
      requiredSystemFeatures = [
        "recursive-nix"
        "ca-derivations"
      ];
    }
    ''
      export HOME=$(mktemp -d)
      export NIX_CONFIG="extra-experimental-features = nix-command recursive-nix ca-derivations"

      echo "=== Building xtest-only-ca (contentAddressed, doCheck=true) ==="
      result=$(nix-build ${go2nixSrc}/tests/fixtures/xtest-only-ca/dag.nix \
        -I nixpkgs=${nixpkgsPath} \
        --option extra-experimental-features 'nix-command ca-derivations' \
        --option plugin-files "${plugin}/lib/nix/plugins/libgo2nix_plugin.so" \
        --no-out-link)

      got=$($result/bin/xtest-only-ca)
      [ "$got" = "2" ] || { echo "FAIL: got '$got', want 2"; exit 1; }

      echo "PASS: xtest-only-ca" > $out
    ''
