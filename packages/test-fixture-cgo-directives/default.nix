# default mode fixture test: cgo packages configured through `#cgo` lines.
#
# Each line of the binary's output comes from one package of the fixture and
# one kind of directive: CFLAGS and CPPFLAGS macros (preamble and a separate
# .c file), -I${SRCDIR}/inc, LDFLAGS: -lm, pkg-config Cflags and CXXFLAGS
# reaching a .cc file, GOOS conditions, and CGO_CPPFLAGS and CGO_LDFLAGS from
# packageOverrides.<pkg>.env. A dropped flag shows as a 0, as "goos: none",
# or as a failed compile or link.
{
  flake,
  pkgs,
  system,
  ...
}:
if !(flake.packages.${system} ? go2nix-nix-plugin) then
  pkgs.runCommand "test-dag-fixture-cgo-directives-unsupported"
    { meta.platforms = pkgs.lib.platforms.linux; }
    ''
      echo "test-dag-fixture-cgo-directives requires go2nix-nix-plugin (Linux only)" >&2
      exit 1
    ''
else
  let
    plugin = flake.packages.${system}.go2nix-nix-plugin;
    nix = pkgs.nixVersions.nix_2_34;

    nixpkgsPath = pkgs.path;
    go2nixSrc = flake;

    expected = pkgs.writeText "cgo-directives-expected" ''
      define: preamble=7 cppflags=11 cfile=7
      srcdir: 3
      libm: 24
      pcxx: header=9 pc=5 cxxflags=13
      goos: ${pkgs.stdenv.hostPlatform.parsed.kernel.name}
      envflags: cppflags=17 cbrt=3
    '';
  in
  pkgs.runCommand "test-dag-fixture-cgo-directives"
    {
      nativeBuildInputs = [ nix ];
      requiredSystemFeatures = [ "recursive-nix" ];
    }
    ''
      export HOME=$(mktemp -d)
      export NIX_CONFIG="extra-experimental-features = nix-command recursive-nix"

      echo "=== Building cgo-directives fixture (default mode, #cgo lines) ==="
      result=$(nix-build ${go2nixSrc}/tests/fixtures/cgo-directives/dag.nix \
        -I nixpkgs=${nixpkgsPath} \
        --option plugin-files "${plugin}/lib/nix/plugins/libgo2nix_plugin.so" \
        --no-out-link 2>build.log) || { cat build.log >&2; exit 1; }
      cat build.log >&2

      # With every LDFLAGS line in place the dynimport test link succeeds.
      ! grep -q 'cgo test link failed' build.log \
        || { echo "FAIL: a cgo test link failed"; exit 1; }

      $result/bin/cgo-directives | tee got.txt
      diff -u ${expected} got.txt \
        || { echo "FAIL: output differs from what the #cgo lines ask for"; exit 1; }

      echo "=== Asserting each linker flag is recorded once ==="
      drv=$(nix-instantiate ${go2nixSrc}/tests/fixtures/cgo-directives/dag.nix \
        -I nixpkgs=${nixpkgsPath} \
        --option plugin-files "${plugin}/lib/nix/plugins/libgo2nix_plugin.so" 2>/dev/null)
      for p in libm envflags; do
        d=$(nix-store -q --references "$drv" | grep -- "-golocal-.*-internal-$p\.drv\$")
        a=$(find "$(nix-store -q --outputs "$d")" -name '*.a')
        n=$(grep -ao 'cgo_ldflag","-lm"' "$a" | wc -l || true)
        [ "$n" = 1 ] || { echo "FAIL: $a records -lm $n times, want 1"; exit 1; }
      done

      echo "PASS: cgo-directives" > $out
    ''
