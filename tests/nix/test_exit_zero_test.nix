# Negative integration check: a test that reaches os.Exit(0) must fail the
# test run, as it fails `go test`, instead of ending the test binary with the
# status of a passing run and skipping the tests after it.
#
# Copies the test-helper-pkg fixture and adds a test to the in-scope
# internal/app package that calls os.Exit(0). The copy's check phase asserts
# that test-packages failed with package testing's `unexpected call to
# os.Exit(0) during test`.
{
  flake,
  pkgs,
  system,
  ...
}:
if !(flake.packages.${system} ? go2nix-nix-plugin) then
  pkgs.runCommand "test-exit-zero-test-unsupported" { meta.platforms = pkgs.lib.platforms.linux; } ''
    echo "test-exit-zero requires go2nix-nix-plugin (Linux only)" >&2
    exit 1
  ''
else
  let
    plugin = flake.packages.${system}.go2nix-nix-plugin;
    nix = pkgs.nixVersions.nix_2_34;
    nixpkgsPath = pkgs.path;
    go2nixSrc = flake;

    dag = pkgs.writeText "dag.nix" ''
      let
        pkgs = import <nixpkgs> { };
        inherit (pkgs) go;
        go2nix = import ${go2nixSrc}/packages/go2nix { inherit pkgs; };
        goEnv = import ${go2nixSrc}/nix/mk-go-env.nix {
          inherit go go2nix;
          inherit (pkgs) callPackage;
        };
      in
      goEnv.buildGoApplication {
        pname = "test-helper-pkg-exit-zero";
        version = "0.0.1";
        src = ./.;
        goLock = ./go2nix.toml;
        doCheck = true;
        # The test run is expected to fail. set +e keeps the check phase going
        # past it, and postCheck asserts on what it printed: a wrapper that
        # runs inside a sandboxed build cannot read the log of a failed one.
        preCheck = '''
          exec 3>&1 4>&2 >"$NIX_BUILD_TOP/check.log" 2>&1
          set +e
        ''';
        postCheck = '''
          set -e
          exec >&3 2>&4
          cat "$NIX_BUILD_TOP/check.log"
          grep -qF "test-packages failed" "$NIX_BUILD_TOP/check.log" \
            || { echo "FAIL: os.Exit(0) in a test passed for a green run"; exit 1; }
          grep -qF "unexpected call to os.Exit(0) during test" "$NIX_BUILD_TOP/check.log" \
            || { echo "FAIL: the run failed without 'unexpected call to os.Exit(0) during test'"; exit 1; }
        ''';
      }
    '';
  in
  pkgs.runCommand "test-exit-zero-test"
    {
      nativeBuildInputs = [ nix ];
      requiredSystemFeatures = [ "recursive-nix" ];
    }
    ''
      export HOME=$(mktemp -d)
      export NIX_CONFIG="extra-experimental-features = nix-command recursive-nix"

      cp -r ${go2nixSrc}/tests/fixtures/test-helper-pkg $TMPDIR/fixture
      chmod -R u+w $TMPDIR/fixture
      cp ${dag} $TMPDIR/fixture/dag.nix

      cat > $TMPDIR/fixture/internal/app/exit_test.go <<'EOF'
      package app

      import "os"
      import "testing"

      func TestExitZero(t *testing.T) { os.Exit(0) }
      EOF

      nix-build $TMPDIR/fixture/dag.nix \
        -I nixpkgs=${nixpkgsPath} \
        --option plugin-files "${plugin}/lib/nix/plugins/libgo2nix_plugin.so" \
        --no-out-link

      echo "PASS: os.Exit(0) in a test failed the test run" > $out
    ''
